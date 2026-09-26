package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/sanat-19/schema-lens/internal/graph"
	"github.com/sanat-19/schema-lens/internal/schema"
)

// Run "go test ./internal/render -update" after an intended output change,
// then read the diff of the golden file before committing it.
var update = flag.Bool("update", false, "rewrite the golden file")

func TestMermaidGolden(t *testing.T) {
	var got bytes.Buffer
	if err := Mermaid(&got, sampleSchema()); err != nil {
		t.Fatal(err)
	}

	golden := filepath.Join("testdata", "sample.mermaid.golden")
	if *update {
		if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("Mermaid output changed.\n--- got ---\n%s\n--- want ---\n%s", got.String(), want)
	}
}

// sampleSchema has one of everything the renderer treats differently: a
// required FK, an optional one, a one-to-one, a self-reference, a guessed
// relation, a table outside public, and awkward type names.
func sampleSchema() *schema.Schema {
	users := &schema.Table{Schema: "public", Name: "users", PrimaryKey: []string{"id"},
		Uniques: [][]string{{"email"}},
		Columns: []*schema.Column{
			{Name: "id", Type: "bigint", IsPK: true},
			{Name: "email", Type: "character varying(255)", IsUnique: true, Comment: `Login "email"`},
			{Name: "created_at", Type: "timestamp with time zone"},
		}}
	profiles := &schema.Table{Schema: "public", Name: "user_profiles", PrimaryKey: []string{"id"},
		Uniques: [][]string{{"user_id"}},
		Columns: []*schema.Column{
			{Name: "id", Type: "bigint", IsPK: true},
			{Name: "user_id", Type: "bigint", IsFK: true, IsUnique: true},
		},
		ForeignKeys: []*schema.ForeignKey{{Name: "profiles_user_fkey", Columns: []string{"user_id"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"id"}}}}
	categories := &schema.Table{Schema: "public", Name: "categories", PrimaryKey: []string{"id"},
		Columns: []*schema.Column{
			{Name: "id", Type: "integer", IsPK: true},
			{Name: "parent_id", Type: "integer", IsFK: true, Nullable: true},
		},
		ForeignKeys: []*schema.ForeignKey{{Name: "categories_parent_fkey", Columns: []string{"parent_id"},
			RefSchema: "public", RefTable: "categories", RefColumns: []string{"id"}}}}
	products := &schema.Table{Schema: "public", Name: "products", PrimaryKey: []string{"id"},
		Columns: []*schema.Column{
			{Name: "id", Type: "bigint", IsPK: true},
			{Name: "price", Type: "numeric(10,2)"},
		}}
	reviews := &schema.Table{Schema: "public", Name: "reviews", PrimaryKey: []string{"id"},
		Columns: []*schema.Column{
			{Name: "id", Type: "bigint", IsPK: true},
			{Name: "product_id", Type: "bigint"},
		}}
	payments := &schema.Table{Schema: "billing", Name: "payments", PrimaryKey: []string{"id"},
		Columns: []*schema.Column{
			{Name: "id", Type: "bigint", IsPK: true},
			{Name: "user_id", Type: "bigint", IsFK: true},
		},
		ForeignKeys: []*schema.ForeignKey{{Name: "payments_user_fkey", Columns: []string{"user_id"},
			RefSchema: "public", RefTable: "users", RefColumns: []string{"id"}}}}

	s := &schema.Schema{Tables: []*schema.Table{payments, categories, products, reviews, profiles, users}}
	s.Relations = graph.Relations(s)
	return s
}
