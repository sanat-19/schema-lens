package analyze

import (
	"fmt"
	"strings"

	"github.com/sanat-19/schema-lens/backend/models"
	"github.com/sanat-19/schema-lens/backend/pkg/graph"
)

// noPrimaryKey flags tables without a primary key.
//
// Without one, nothing stops two identical rows, ORMs can't reliably update
// or delete a single row, and logical replication can't replicate UPDATEs
// and DELETEs until you set a replica identity.
func noPrimaryKey(t *models.Table) []models.Finding {
	if len(t.PrimaryKey) > 0 {
		return nil
	}
	return []models.Finding{{
		Kind:     KindNoPrimaryKey,
		Severity: models.SeverityMedium,
		Table:    t.ID(),
		Title:    fmt.Sprintf("%s has no primary key", t.Name),
		Detail: "Nothing stops duplicate rows, ORMs can't safely update or delete one row, " +
			"and logical replication can't replicate its UPDATEs and DELETEs.",
		Suggestion: addPrimaryKeySQL(t),
	}}
}

// addPrimaryKeySQL promotes an existing unique, NOT NULL key if there is one,
// since that's what the table already treats as its identity. Otherwise it
// adds a new identity column.
func addPrimaryKeySQL(t *models.Table) string {
	target := qualified(t.Schema, t.Name)
	for _, key := range t.Uniques {
		if !anyNullable(t, key) {
			return fmt.Sprintf("ALTER TABLE %s ADD PRIMARY KEY (%s);", target, columnList(key))
		}
	}
	column := "id"
	if t.Column("id") != nil {
		column = t.Name + "_id"
	}
	sql := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY;",
		target, ident(column))
	if t.Partitioned {
		sql = "-- On a partitioned table the primary key must include the partition key column(s).\n" + sql
	}
	return "-- Rewrites the table and locks it while it runs.\n" + sql
}

func anyNullable(t *models.Table, cols []string) bool {
	for _, name := range cols {
		if c := t.Column(name); c == nil || c.Nullable {
			return true
		}
	}
	return false
}

// fkTypeMismatches finds FK columns whose type differs from the column they
// point at, like an integer pointing at a bigint.
//
// Every join and FK check then compares across two types. For some pairs
// that stops Postgres from using an index; for integer → bigint the real
// trap is that the child column overflows once parent ids pass 2^31.
func fkTypeMismatches(s *models.Schema, t *models.Table) []models.Finding {
	var out []models.Finding
	for _, fk := range t.ForeignKeys {
		parent := s.Table(fk.RefID())
		if parent == nil {
			continue // not loaded, so we don't know its column types
		}
		for i, name := range fk.Columns {
			child, ref := t.Column(name), parent.Column(fk.RefColumns[i])
			if child == nil || ref == nil || graph.BaseType(child.Type) == graph.BaseType(ref.Type) {
				continue
			}
			out = append(out, models.Finding{
				Kind:     KindFKTypeMismatch,
				Severity: models.SeverityMedium,
				Table:    t.ID(),
				Title:    fmt.Sprintf("%s.%s is %s but %s.%s is %s", t.Name, name, child.Type, parent.Name, ref.Name, ref.Type),
				Detail:   mismatchDetail(child.Type, ref.Type),
				Suggestion: fmt.Sprintf("-- Rewrites %s and holds an exclusive lock while it runs.\n"+
					"ALTER TABLE %s ALTER COLUMN %s TYPE %s;",
					t.Name, qualified(t.Schema, t.Name), ident(name), ref.Type),
			})
		}
	}
	return out
}

func mismatchDetail(childType, refType string) string {
	if graph.TypeFamily(childType) == "integer" && graph.TypeFamily(refType) == "integer" {
		return fmt.Sprintf("Joins and FK checks have to compare %s with %s. Worse, once the parent's ids "+
			"grow past what %s can hold, inserts into this table start failing.", childType, refType, childType)
	}
	return fmt.Sprintf("Every join and FK check has to compare %s with %s. Implicit casts like this can "+
		"stop the planner from using an index on either side.", childType, refType)
}

// inferredRelations turns every guessed relation into a suggestion to make
// it real. The app clearly relies on the link; the database should enforce
// it, so orphaned rows can't creep in.
func inferredRelations(s *models.Schema) []models.Finding {
	var out []models.Finding
	for _, r := range s.Relations {
		if !r.Inferred {
			continue
		}
		child, parent := s.Table(r.From), s.Table(r.To)
		if child == nil || parent == nil {
			continue
		}
		name := objectName(child.Name, strings.Join(r.FromCols, "_"), "fkey")
		childTable := qualified(child.Schema, child.Name)

		out = append(out, models.Finding{
			Kind:     KindInferredRelation,
			Severity: models.SeverityLow,
			Table:    child.ID(),
			Title: fmt.Sprintf("%s.%s looks like it references %s, but has no foreign key",
				child.Name, strings.Join(r.FromCols, ", "), parent.Name),
			Detail: "The name and type say this column points at another table, but the database doesn't " +
				"enforce it, so rows can point at things that no longer exist. A real FK also tells the " +
				"planner and other developers about the link.",
			Suggestion: fmt.Sprintf(
				"-- First, check for rows that point at nothing (the constraint won't validate if there are any):\n"+
					"-- SELECT count(*) FROM %[1]s c LEFT JOIN %[2]s p ON p.%[3]s = c.%[4]s WHERE c.%[4]s IS NOT NULL AND p.%[3]s IS NULL;\n"+
					"-- NOT VALID adds the constraint without scanning existing rows; VALIDATE checks them\n"+
					"-- afterwards without blocking writes.\n"+
					"ALTER TABLE %[1]s ADD CONSTRAINT %[5]s FOREIGN KEY (%[6]s) REFERENCES %[2]s (%[7]s) NOT VALID;\n"+
					"ALTER TABLE %[1]s VALIDATE CONSTRAINT %[5]s;",
				childTable, qualified(parent.Schema, parent.Name), ident(r.ToCols[0]), ident(r.FromCols[0]),
				name, columnList(r.FromCols), columnList(r.ToCols)),
		})
	}
	return out
}
