// Package analyze looks at a schema and points out structural problems that
// cost performance or correctness, each with SQL the developer can copy.
//
// SchemaLens never runs that SQL. Suggestions are text for a human to read,
// adapt and put in a migration.
package analyze

import (
	"cmp"
	"slices"

	"github.com/sanat-19/schema-lens/internal/schema"
)

// The kinds of finding, as they appear in JSON and in the UI.
const (
	KindMissingFKIndex   = "missing_fk_index"
	KindNoPrimaryKey     = "no_primary_key"
	KindRedundantIndex   = "redundant_index"
	KindDuplicateIndex   = "duplicate_index"
	KindUnusedIndex      = "unused_index"
	KindInferredRelation = "inferred_relation"
	KindFKTypeMismatch   = "fk_type_mismatch"
)

// Thresholds. A child table above largeTable rows makes a missing FK index
// urgent; an unused index below unusedMinBytes isn't worth anyone's time.
const (
	largeTable     = 10_000
	unusedMinBytes = 1 << 20
)

// Findings runs every check. It expects s.Relations to be filled in already,
// because inferred relations are reported and they also protect indexes
// from being called unused.
func Findings(s *schema.Schema) []schema.Finding {
	var out []schema.Finding
	for _, t := range s.Tables {
		out = append(out, missingFKIndexes(t)...)
		out = append(out, noPrimaryKey(t)...)
		out = append(out, fkTypeMismatches(s, t)...)
		out = append(out, indexProblems(t, s.Relations)...)
	}
	out = append(out, inferredRelations(s)...)
	sortFindings(out)
	return out
}

// sortFindings puts the most serious problems first, then groups by table.
func sortFindings(fs []schema.Finding) {
	rank := map[string]int{schema.SeverityHigh: 0, schema.SeverityMedium: 1, schema.SeverityLow: 2}
	slices.SortStableFunc(fs, func(a, b schema.Finding) int {
		return cmp.Or(
			cmp.Compare(rank[a.Severity], rank[b.Severity]),
			cmp.Compare(a.Table, b.Table),
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.Title, b.Title),
		)
	})
}
