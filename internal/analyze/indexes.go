package analyze

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/sanat-19/schema-lens/internal/schema"
)

// missingFKIndexes finds foreign keys that no index can serve.
//
// Postgres indexes the parent side of an FK (it has to be a key) but not the
// child side. Without that index, joining child to parent and every DELETE
// or key UPDATE on the parent has to scan the whole child table.
func missingFKIndexes(t *schema.Table) []schema.Finding {
	var out []schema.Finding
	for _, fk := range t.ForeignKeys {
		if slices.ContainsFunc(t.Indexes, func(ix *schema.Index) bool { return servesColumns(ix, fk.Columns) }) {
			continue
		}

		severity := schema.SeverityMedium
		if t.RowEstimate > largeTable {
			severity = schema.SeverityHigh
		}

		out = append(out, schema.Finding{
			Kind:     KindMissingFKIndex,
			Severity: severity,
			Table:    t.ID(),
			Title:    fmt.Sprintf("Foreign key %s (%s) has no index", fk.Name, strings.Join(fk.Columns, ", ")),
			Detail: fmt.Sprintf("Joins from %s to %s, and every delete or key update on %s, "+
				"have to scan all of %s (%s) to find matching rows.",
				t.Name, fk.RefTable, fk.RefTable, t.Name, rowsText(t.RowEstimate)),
			Suggestion: createIndexSQL(t, fk.Columns),
		})
	}
	return out
}

// servesColumns reports whether a lookup on cols can use ix: a plain btree
// whose leading columns are exactly cols, in any order.
func servesColumns(ix *schema.Index, cols []string) bool {
	if ix.Method != "btree" || ix.Partial || len(ix.Columns) < len(cols) {
		return false
	}
	lead := ix.Columns[:len(cols)]
	for _, c := range cols {
		if !slices.Contains(lead, c) {
			return false
		}
	}
	return true
}

func createIndexSQL(t *schema.Table, cols []string) string {
	name := objectName(append([]string{"idx", t.Name}, cols...)...)
	target := qualified(t.Schema, t.Name)
	if t.Partitioned {
		// CONCURRENTLY doesn't work on a partitioned table, so say how to
		// avoid a long lock instead.
		return fmt.Sprintf("-- CONCURRENTLY is not supported on partitioned tables. To avoid a long lock,\n"+
			"-- create it ON ONLY %s, build it CONCURRENTLY on each partition, then ATTACH.\n"+
			"CREATE INDEX %s ON %s (%s);", target, name, target, columnList(cols))
	}
	return fmt.Sprintf("CREATE INDEX CONCURRENTLY %s ON %s (%s);", name, target, columnList(cols))
}

// indexProblems finds duplicate, redundant and unused indexes on one table.
// An index is only reported once, by the first check that catches it.
func indexProblems(t *schema.Table, rels []schema.Relation) []schema.Finding {
	reported := map[string]bool{}
	var out []schema.Finding

	for _, f := range duplicateIndexes(t) {
		out = append(out, f.finding)
		reported[f.index] = true
	}
	for _, f := range redundantIndexes(t) {
		if !reported[f.index] {
			out = append(out, f.finding)
			reported[f.index] = true
		}
	}
	for _, f := range unusedIndexes(t, rels) {
		if !reported[f.index] {
			out = append(out, f.finding)
		}
	}
	return out
}

// indexFinding remembers which index a finding is about, so we don't tell
// the developer about the same index twice.
type indexFinding struct {
	index   string
	finding schema.Finding
}

// duplicateIndexes finds indexes that are exactly the same as another one:
// same method, key columns, INCLUDE columns and WHERE clause. One of them
// does all the work; the rest only slow down writes and take up disk.
func duplicateIndexes(t *schema.Table) []indexFinding {
	groups := map[string][]*schema.Index{}
	var order []string
	for _, ix := range t.Indexes {
		key := strings.Join([]string{ix.Method, strings.Join(ix.Columns, ","),
			strings.Join(ix.Include, ","), ix.Predicate}, "|")
		if groups[key] == nil {
			order = append(order, key)
		}
		groups[key] = append(groups[key], ix)
	}

	var out []indexFinding
	for _, key := range order {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		keep := slices.MinFunc(group, keepFirst)
		for _, ix := range group {
			if ix == keep {
				continue
			}
			out = append(out, indexFinding{ix.Name, schema.Finding{
				Kind:     KindDuplicateIndex,
				Severity: schema.SeverityMedium,
				Table:    t.ID(),
				Title:    fmt.Sprintf("Index %s duplicates %s", ix.Name, keep.Name),
				Detail: fmt.Sprintf("Both index (%s) the same way. Every insert and update on %s "+
					"writes to both, and %s uses %s of disk for nothing.",
					strings.Join(ix.Columns, ", "), t.Name, ix.Name, humanBytes(ix.Bytes)),
				Suggestion: dropIndexSQL(t, ix),
			}})
		}
	}
	return out
}

// keepFirst orders a group of identical indexes by which one to keep: the
// primary key, then unique ones (they enforce something), then the most
// used, then by name so the answer is stable.
func keepFirst(a, b *schema.Index) int {
	return cmp.Or(
		boolFirst(a.Primary, b.Primary),
		boolFirst(a.Unique, b.Unique),
		cmp.Compare(b.Scans, a.Scans),
		cmp.Compare(a.Name, b.Name),
	)
}

func boolFirst(a, b bool) int {
	switch {
	case a && !b:
		return -1
	case b && !a:
		return 1
	}
	return 0
}

// redundantIndexes finds btree indexes whose columns are the start of a
// longer btree index. Anything that can use (a) can use (a, b) just as well,
// so (a) only costs writes and disk.
//
// We leave it alone if it's unique (it enforces something the longer one
// doesn't), partial, or has INCLUDE columns (it may be there for index-only
// scans the longer one can't do).
func redundantIndexes(t *schema.Table) []indexFinding {
	var out []indexFinding
	for _, short := range t.Indexes {
		if short.Method != "btree" || short.Unique || short.Partial || len(short.Include) > 0 {
			continue
		}
		for _, long := range t.Indexes {
			if long.Method != "btree" || long.Partial || len(long.Columns) <= len(short.Columns) ||
				!slices.Equal(long.Columns[:len(short.Columns)], short.Columns) {
				continue
			}
			out = append(out, indexFinding{short.Name, schema.Finding{
				Kind:     KindRedundantIndex,
				Severity: schema.SeverityLow,
				Table:    t.ID(),
				Title:    fmt.Sprintf("Index %s is covered by %s", short.Name, long.Name),
				Detail: fmt.Sprintf("%s (%s) starts with the same columns as %s (%s), so queries can use "+
					"the longer one instead. Dropping it saves %s and makes writes to %s cheaper.",
					short.Name, strings.Join(short.Columns, ", "), long.Name, strings.Join(long.Columns, ", "),
					humanBytes(short.Bytes), t.Name),
				Suggestion: dropIndexSQL(t, short),
			}})
			break
		}
	}
	return out
}

// unusedIndexes finds big indexes that have never been scanned.
//
// Unique and primary indexes are skipped: they enforce a rule even if no
// query reads them. So are indexes that serve a foreign key, real or
// inferred: the FK check on a parent delete may be rare, but dropping the
// index would bring back the full-table scan missingFKIndexes warns about.
func unusedIndexes(t *schema.Table, rels []schema.Relation) []indexFinding {
	var out []indexFinding
	for _, ix := range t.Indexes {
		if ix.Scans != 0 || ix.Unique || ix.Primary || ix.Bytes <= unusedMinBytes {
			continue
		}
		if servesAnyRelation(t, ix, rels) {
			continue
		}
		out = append(out, indexFinding{ix.Name, schema.Finding{
			Kind:     KindUnusedIndex,
			Severity: schema.SeverityLow,
			Table:    t.ID(),
			Title:    fmt.Sprintf("Index %s has never been used", ix.Name),
			Detail: fmt.Sprintf("No query has scanned %s since statistics were last reset, yet it takes %s "+
				"and every write to %s has to update it. The count starts again after a stats reset "+
				"or a restart, and doesn't include replicas, so check that it covers a full business "+
				"cycle (month-end jobs, reports) before dropping it.",
				ix.Name, humanBytes(ix.Bytes), t.Name),
			Suggestion: dropIndexSQL(t, ix),
		}})
	}
	return out
}

func servesAnyRelation(t *schema.Table, ix *schema.Index, rels []schema.Relation) bool {
	for _, r := range rels {
		if r.From == t.ID() && servesColumns(ix, r.FromCols) {
			return true
		}
	}
	return false
}

func dropIndexSQL(t *schema.Table, ix *schema.Index) string {
	target := qualified(t.Schema, ix.Name)
	var sql string
	if t.Partitioned {
		sql = fmt.Sprintf("-- CONCURRENTLY is not supported on a partitioned table's index.\nDROP INDEX %s;", target)
	} else {
		sql = fmt.Sprintf("DROP INDEX CONCURRENTLY %s;", target)
	}
	if ix.Unique {
		sql = "-- If this index backs a UNIQUE constraint, drop the constraint instead:\n" +
			fmt.Sprintf("-- ALTER TABLE %s DROP CONSTRAINT %s;\n", qualified(t.Schema, t.Name), ident(ix.Name)) + sql
	}
	return sql
}

func rowsText(n int64) string {
	if n < 0 {
		return "row count unknown, never analyzed"
	}
	return "about " + humanCount(n) + " rows"
}
