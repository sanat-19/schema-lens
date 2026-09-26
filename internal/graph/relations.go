// Package graph works out how tables relate: which table points at which,
// whether it's one-to-one or many-to-one, and whether the link is required.
//
// Real relations come from FOREIGN KEY constraints. Inferred ones are guessed
// from column names, for the many databases that don't declare FKs.
package graph

import (
	"slices"

	"github.com/sanat-19/schema-lens/internal/schema"
)

// Relations returns every edge of the graph: one per FK whose parent table
// was loaded, followed by the relations guessed from column names.
func Relations(s *schema.Schema) []schema.Relation {
	var rels []schema.Relation
	for _, child := range s.Tables {
		for _, fk := range child.ForeignKeys {
			// An FK into a schema we didn't load has nothing to draw an edge
			// to. It stays on the table and the UI lists it as external.
			if s.Table(fk.RefID()) == nil {
				continue
			}
			rels = append(rels, schema.Relation{
				ID:          "fk:" + child.ID() + "." + fk.Name,
				From:        child.ID(),
				FromCols:    fk.Columns,
				To:          fk.RefID(),
				ToCols:      fk.RefColumns,
				Cardinality: cardinality(child, fk.Columns),
				Optional:    anyNullable(child, fk.Columns),
				Name:        fk.Name,
			})
		}
	}
	return append(rels, inferRelations(s)...)
}

// cardinality says how many child rows one parent row can have.
//
// If the FK columns contain a whole unique key of the child (its primary key
// or a unique constraint), no two child rows can point at the same parent,
// so it's one-to-one. Otherwise many children can share a parent.
func cardinality(child *schema.Table, fkCols []string) string {
	keys := child.Uniques
	if len(child.PrimaryKey) > 0 {
		keys = append([][]string{child.PrimaryKey}, keys...)
	}
	for _, key := range keys {
		if containsAll(fkCols, key) {
			return schema.OneToOne
		}
	}
	return schema.ManyToOne
}

// anyNullable reports whether a child row can leave the link empty.
// With a composite FK, one NULL column is enough (MATCH SIMPLE).
func anyNullable(t *schema.Table, cols []string) bool {
	for _, name := range cols {
		if c := t.Column(name); c != nil && c.Nullable {
			return true
		}
	}
	return false
}

// containsAll reports whether every column of want is in have.
func containsAll(have, want []string) bool {
	if len(want) == 0 {
		return false
	}
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}
