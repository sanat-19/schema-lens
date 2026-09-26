package graph

import (
	"slices"
	"strings"
	"unicode"

	"github.com/sanat-19/schema-lens/internal/schema"
)

// inferRelations guesses links that the app relies on but the database
// doesn't enforce, like reviews.product_id → products.id with no FK.
//
// A guess needs three things to line up:
//  1. the column is named <thing>_id or <thing>Id and isn't already an FK,
//  2. a table named after <thing> exists (product, products, categories, ...),
//     preferring the column's own schema,
//  3. that table has a single-column primary key of a compatible type.
func inferRelations(s *schema.Schema) []schema.Relation {
	var rels []schema.Relation
	for _, child := range s.Tables {
		for _, col := range child.Columns {
			if inForeignKey(child, col.Name) {
				continue
			}
			thing, ok := referencedThing(col.Name)
			if !ok {
				continue
			}
			parent := findTableFor(s, child.Schema, thing)
			if parent == nil || len(parent.PrimaryKey) != 1 {
				continue
			}
			// users.user_id as the table's own key isn't a link to itself.
			if parent == child && col.IsPK {
				continue
			}
			pk := parent.Column(parent.PrimaryKey[0])
			if pk == nil || !typesCompatible(col.Type, pk.Type) {
				continue
			}
			rels = append(rels, schema.Relation{
				ID:          "inferred:" + child.ID() + "." + col.Name,
				From:        child.ID(),
				FromCols:    []string{col.Name},
				To:          parent.ID(),
				ToCols:      []string{pk.Name},
				Cardinality: cardinality(child, []string{col.Name}),
				Optional:    col.Nullable,
				Inferred:    true,
				Name:        child.Name + "." + col.Name + " → " + parent.Name,
			})
		}
	}
	return rels
}

// inForeignKey reports whether the column is already part of a declared FK.
// We check the FKs themselves rather than Column.IsFK, so this works on any
// schema, not only one whose reader set the flags.
func inForeignKey(t *schema.Table, column string) bool {
	for _, fk := range t.ForeignKeys {
		if slices.Contains(fk.Columns, column) {
			return true
		}
	}
	return false
}

// referencedThing pulls "product" out of "product_id" or "productId".
// A bare "id" refers to the table itself, so it isn't a link.
func referencedThing(column string) (string, bool) {
	if thing, ok := strings.CutSuffix(strings.ToLower(column), "_id"); ok && thing != "" {
		return thing, true
	}
	// camelCase: the "I" must be upper-case, so "paid" isn't "pa" + "id".
	if thing, ok := strings.CutSuffix(column, "Id"); ok && thing != "" {
		return camelToSnake(thing), true
	}
	return "", false
}

// findTableFor looks for a table named after thing, first in the child's own
// schema, then in the other loaded schemas. If several other schemas have a
// match we can't tell which one is meant, so we don't guess.
func findTableFor(s *schema.Schema, childSchema, thing string) *schema.Table {
	names := tableNamesFor(thing)

	var elsewhere []*schema.Table
	for _, t := range s.Tables {
		if !matchesAny(t.Name, names) {
			continue
		}
		if t.Schema == childSchema {
			return t
		}
		elsewhere = append(elsewhere, t)
	}
	if len(elsewhere) == 1 {
		return elsewhere[0]
	}
	return nil
}

// tableNamesFor lists the names a table about "thing" usually has:
// product → product, products; box → boxes; category → categories.
func tableNamesFor(thing string) []string {
	names := []string{thing, thing + "s", thing + "es"}
	if base, ok := strings.CutSuffix(thing, "y"); ok {
		names = append(names, base+"ies")
	}
	return names
}

func matchesAny(name string, candidates []string) bool {
	for _, c := range candidates {
		if strings.EqualFold(name, c) {
			return true
		}
	}
	return false
}

// camelToSnake turns "orderItem" into "order_item".
func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// typesCompatible decides whether a column could sensibly hold values of a
// key: integer → bigint is fine, integer → uuid is not.
func typesCompatible(a, b string) bool {
	return TypeFamily(a) == TypeFamily(b)
}

// TypeFamily groups Postgres types whose values compare to each other
// without surprises, e.g. all integer sizes, or text and varchar.
func TypeFamily(typ string) string {
	base := BaseType(typ)
	switch base {
	case "smallint", "integer", "bigint":
		return "integer"
	case "text", "character varying", "character":
		return "text"
	}
	return base
}

// BaseType strips the modifier, so "character varying(255)" becomes
// "character varying" and "numeric(10,2)" becomes "numeric".
func BaseType(typ string) string {
	if i := strings.IndexByte(typ, '('); i >= 0 {
		return strings.TrimSpace(typ[:i] + typ[strings.IndexByte(typ, ')')+1:])
	}
	return typ
}
