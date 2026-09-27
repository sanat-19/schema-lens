package graph

import (
	"testing"

	"github.com/sanat-19/schema-lens/backend/models"
)

// Small helpers so each test reads like the schema it describes.

func table(name string, cols ...*models.Column) *models.Table {
	return &models.Table{Schema: "public", Name: name, Columns: cols}
}

func col(name, typ string) *models.Column {
	return &models.Column{Name: name, Type: typ}
}

func nullable(c *models.Column) *models.Column {
	c.Nullable = true
	return c
}

func withPK(t *models.Table, cols ...string) *models.Table {
	t.PrimaryKey = cols
	for _, name := range cols {
		t.Column(name).IsPK = true
	}
	return t
}

func withFK(t *models.Table, name string, cols []string, ref string, refCols []string) *models.Table {
	t.ForeignKeys = append(t.ForeignKeys, &models.ForeignKey{
		Name: name, Columns: cols, RefSchema: "public", RefTable: ref, RefColumns: refCols,
	})
	for _, c := range cols {
		t.Column(c).IsFK = true
	}
	return t
}

func users() *models.Table {
	return withPK(table("users", col("id", "bigint"), col("email", "text")), "id")
}

func only(t *testing.T, rels []models.Relation) models.Relation {
	t.Helper()
	if len(rels) != 1 {
		t.Fatalf("want exactly 1 relation, got %d: %+v", len(rels), rels)
	}
	return rels[0]
}

func TestForeignKeyIsManyToOne(t *testing.T) {
	orders := withPK(table("orders", col("id", "bigint"), col("user_id", "bigint")), "id")
	withFK(orders, "orders_user_id_fkey", []string{"user_id"}, "users", []string{"id"})

	r := only(t, Relations(&models.Schema{Tables: []*models.Table{users(), orders}}))

	if r.From != "public.orders" || r.To != "public.users" {
		t.Errorf("edge should go child → parent, got %s → %s", r.From, r.To)
	}
	if r.Cardinality != models.ManyToOne {
		t.Errorf("many orders per user, got %s", r.Cardinality)
	}
	if r.Optional || r.Inferred {
		t.Errorf("NOT NULL real FK should be required and not inferred: %+v", r)
	}
}

func TestUniqueForeignKeyIsOneToOne(t *testing.T) {
	profiles := withPK(table("user_profiles", col("id", "bigint"), col("user_id", "bigint")), "id")
	profiles.Uniques = [][]string{{"user_id"}}
	withFK(profiles, "profiles_user_fkey", []string{"user_id"}, "users", []string{"id"})

	r := only(t, Relations(&models.Schema{Tables: []*models.Table{users(), profiles}}))

	if r.Cardinality != models.OneToOne {
		t.Errorf("unique FK column means one profile per user, got %s", r.Cardinality)
	}
}

func TestForeignKeyThatIsThePrimaryKeyIsOneToOne(t *testing.T) {
	settings := withPK(table("user_settings", col("user_id", "bigint")), "user_id")
	withFK(settings, "settings_user_fkey", []string{"user_id"}, "users", []string{"id"})

	r := only(t, Relations(&models.Schema{Tables: []*models.Table{users(), settings}}))

	if r.Cardinality != models.OneToOne {
		t.Errorf("FK = PK means one-to-one, got %s", r.Cardinality)
	}
}

func TestNullableForeignKeyIsOptional(t *testing.T) {
	carts := withPK(table("carts", col("id", "bigint"), nullable(col("user_id", "bigint"))), "id")
	withFK(carts, "carts_user_fkey", []string{"user_id"}, "users", []string{"id"})

	r := only(t, Relations(&models.Schema{Tables: []*models.Table{users(), carts}}))

	if !r.Optional {
		t.Error("guest carts have no user, so the relation should be optional")
	}
}

func TestSelfReference(t *testing.T) {
	cats := withPK(table("categories", col("id", "integer"), nullable(col("parent_id", "integer"))), "id")
	withFK(cats, "categories_parent_fkey", []string{"parent_id"}, "categories", []string{"id"})

	r := only(t, Relations(&models.Schema{Tables: []*models.Table{cats}}))

	if r.From != r.To || r.From != "public.categories" {
		t.Errorf("self-reference should be a loop on categories, got %s → %s", r.From, r.To)
	}
}

func TestCompositeForeignKeyIsOneRelation(t *testing.T) {
	variants := withPK(table("variants", col("product_id", "bigint"), col("size", "text")), "product_id", "size")
	stock := withPK(table("stock", col("id", "bigint"), col("product_id", "bigint"), col("size", "text")), "id")
	withFK(stock, "stock_variant_fkey", []string{"product_id", "size"}, "variants", []string{"product_id", "size"})

	r := only(t, Relations(&models.Schema{Tables: []*models.Table{variants, stock}}))

	if len(r.FromCols) != 2 || r.FromCols[0] != "product_id" || r.FromCols[1] != "size" {
		t.Errorf("composite FK should keep both columns in order, got %v", r.FromCols)
	}
}

func TestForeignKeyToUnloadedSchemaHasNoEdge(t *testing.T) {
	invoices := withPK(table("invoices", col("id", "bigint"), col("account_id", "bigint")), "id")
	invoices.ForeignKeys = []*models.ForeignKey{{
		Name: "invoices_account_fkey", Columns: []string{"account_id"},
		RefSchema: "crm", RefTable: "accounts", RefColumns: []string{"id"},
	}}
	invoices.Column("account_id").IsFK = true

	rels := Relations(&models.Schema{Tables: []*models.Table{invoices}})

	if len(rels) != 0 {
		t.Errorf("crm.accounts wasn't loaded, so no edge should be drawn: %+v", rels)
	}
}

// --- inferred relations -----------------------------------------------------

func TestInferFromSnakeCaseColumn(t *testing.T) {
	products := withPK(table("products", col("id", "bigint")), "id")
	reviews := withPK(table("reviews", col("id", "bigint"), col("product_id", "bigint")), "id")

	r := only(t, Relations(&models.Schema{Tables: []*models.Table{products, reviews}}))

	if !r.Inferred || r.From != "public.reviews" || r.To != "public.products" || r.ToCols[0] != "id" {
		t.Errorf("want inferred reviews.product_id → products.id, got %+v", r)
	}
}

func TestInferPluralForms(t *testing.T) {
	cases := map[string]string{
		"category_id": "categories",  // y → ies
		"box_id":      "boxes",       // + es
		"person_id":   "person",      // table named in the singular
		"orderItemId": "order_items", // camelCase column
	}
	for column, tableName := range cases {
		parent := withPK(table(tableName, col("id", "integer")), "id")
		child := withPK(table("child", col("id", "integer"), col(column, "integer")), "id")

		rels := Relations(&models.Schema{Tables: []*models.Table{parent, child}})

		if len(rels) != 1 || rels[0].To != "public."+tableName {
			t.Errorf("%s should point at %s, got %+v", column, tableName, rels)
		}
	}
}

func TestInferPrefersSameSchema(t *testing.T) {
	publicUsers := users()
	crmUsers := withPK(&models.Table{Schema: "crm", Name: "users", Columns: []*models.Column{col("id", "bigint")}}, "id")
	crmNotes := withPK(&models.Table{Schema: "crm", Name: "notes",
		Columns: []*models.Column{col("id", "bigint"), col("user_id", "bigint")}}, "id")

	r := only(t, Relations(&models.Schema{Tables: []*models.Table{crmNotes, crmUsers, publicUsers}}))

	if r.To != "crm.users" {
		t.Errorf("crm.notes.user_id should point at crm.users, got %s", r.To)
	}
}

func TestInferIntegerToBigintIsFine(t *testing.T) {
	products := withPK(table("products", col("id", "bigint")), "id")
	reviews := withPK(table("reviews", col("id", "bigint"), col("product_id", "integer")), "id")

	only(t, Relations(&models.Schema{Tables: []*models.Table{products, reviews}}))
}

func TestNoInference(t *testing.T) {
	cases := map[string][]*models.Table{
		"integer column can't point at a uuid key": {
			withPK(table("products", col("id", "uuid")), "id"),
			withPK(table("reviews", col("id", "bigint"), col("product_id", "integer")), "id"),
		},
		"no table named after the column": {
			withPK(table("audit_log", col("id", "bigint"), col("actor_id", "bigint")), "id"),
		},
		"parent has a composite primary key": {
			withPK(table("products", col("id", "bigint"), col("region", "text")), "id", "region"),
			withPK(table("reviews", col("id", "bigint"), col("product_id", "bigint")), "id"),
		},
		"parent has no primary key": {
			table("products", col("id", "bigint")),
			withPK(table("reviews", col("id", "bigint"), col("product_id", "bigint")), "id"),
		},
		"a table's own key named user_id is not a link to itself": {
			withPK(table("users", col("user_id", "bigint")), "user_id"),
		},
		"'paid' is not a camelCase Id column": {
			withPK(table("pa", col("id", "bigint")), "id"),
			withPK(table("orders", col("id", "bigint"), col("paid", "bigint")), "id"),
		},
	}
	for name, tables := range cases {
		if rels := Relations(&models.Schema{Tables: tables}); len(rels) != 0 {
			t.Errorf("%s: want no relation, got %+v", name, rels)
		}
	}
}

func TestNoInferenceWhenColumnAlreadyHasForeignKey(t *testing.T) {
	orders := withPK(table("orders", col("id", "bigint"), col("user_id", "bigint")), "id")
	withFK(orders, "orders_user_fkey", []string{"user_id"}, "users", []string{"id"})

	r := only(t, Relations(&models.Schema{Tables: []*models.Table{users(), orders}}))

	if r.Inferred {
		t.Error("user_id already has a real FK; it must not also get an inferred one")
	}
}

func TestBaseTypeAndFamily(t *testing.T) {
	cases := []struct{ typ, base, family string }{
		{"character varying(255)", "character varying", "text"},
		{"numeric(10,2)", "numeric", "numeric"},
		{"timestamp(3) with time zone", "timestamp with time zone", "timestamp with time zone"},
		{"integer", "integer", "integer"},
		{"bigint", "bigint", "integer"},
	}
	for _, c := range cases {
		if got := BaseType(c.typ); got != c.base {
			t.Errorf("BaseType(%q) = %q, want %q", c.typ, got, c.base)
		}
		if got := TypeFamily(c.typ); got != c.family {
			t.Errorf("TypeFamily(%q) = %q, want %q", c.typ, got, c.family)
		}
	}
}
