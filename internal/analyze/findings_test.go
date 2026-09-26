package analyze

import (
	"strings"
	"testing"

	"github.com/sanat-19/schema-lens/internal/graph"
	"github.com/sanat-19/schema-lens/internal/schema"
)

// --- helpers: build a schema by hand, no database needed --------------------

func table(name string, rows int64, cols ...*schema.Column) *schema.Table {
	return &schema.Table{Schema: "public", Name: name, RowEstimate: rows, Columns: cols}
}

func col(name, typ string) *schema.Column { return &schema.Column{Name: name, Type: typ} }

func btree(name string, cols ...string) *schema.Index {
	return &schema.Index{Name: name, Method: "btree", Columns: cols, Scans: 100}
}

// ordersSchema is users ← orders, with orders.user_id FK'd, and whatever
// indexes the test gives orders.
func ordersSchema(orderRows int64, indexes ...*schema.Index) *schema.Schema {
	users := table("users", 1000, col("id", "bigint"))
	users.PrimaryKey = []string{"id"}
	users.Indexes = []*schema.Index{{Name: "users_pkey", Method: "btree", Columns: []string{"id"}, Unique: true, Primary: true}}

	orders := table("orders", orderRows, col("id", "bigint"), col("user_id", "bigint"), col("created_at", "timestamptz"))
	orders.PrimaryKey = []string{"id"}
	orders.ForeignKeys = []*schema.ForeignKey{{
		Name: "orders_user_id_fkey", Columns: []string{"user_id"},
		RefSchema: "public", RefTable: "users", RefColumns: []string{"id"},
	}}
	orders.Indexes = append([]*schema.Index{
		{Name: "orders_pkey", Method: "btree", Columns: []string{"id"}, Unique: true, Primary: true, Scans: 5},
	}, indexes...)

	return analyzed(users, orders)
}

// analyzed builds a schema the way the app does: relations first.
func analyzed(tables ...*schema.Table) *schema.Schema {
	s := &schema.Schema{Tables: tables}
	s.Relations = graph.Relations(s)
	return s
}

func findingsOfKind(s *schema.Schema, kind string) []schema.Finding {
	var out []schema.Finding
	for _, f := range Findings(s) {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

func wantOne(t *testing.T, fs []schema.Finding) schema.Finding {
	t.Helper()
	if len(fs) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(fs), fs)
	}
	return fs[0]
}

func wantNone(t *testing.T, why string, fs []schema.Finding) {
	t.Helper()
	if len(fs) != 0 {
		t.Errorf("%s: want no finding, got %+v", why, fs)
	}
}

// --- missing_fk_index ---------------------------------------------------------

func TestMissingFKIndexOnLargeTableIsHigh(t *testing.T) {
	f := wantOne(t, findingsOfKind(ordersSchema(50_000), KindMissingFKIndex))

	if f.Severity != schema.SeverityHigh || f.Table != "public.orders" {
		t.Errorf("50k-row child without FK index should be high on public.orders, got %+v", f)
	}
	if f.Suggestion != "CREATE INDEX CONCURRENTLY idx_orders_user_id ON public.orders (user_id);" {
		t.Errorf("unexpected suggestion: %s", f.Suggestion)
	}
}

func TestMissingFKIndexOnSmallTableIsMedium(t *testing.T) {
	f := wantOne(t, findingsOfKind(ordersSchema(500), KindMissingFKIndex))

	if f.Severity != schema.SeverityMedium {
		t.Errorf("small child table should be medium, got %s", f.Severity)
	}
}

func TestFKIndexCountsWhenLeadingColumnsMatch(t *testing.T) {
	wantNone(t, "exact index",
		findingsOfKind(ordersSchema(50_000, btree("i", "user_id")), KindMissingFKIndex))
	wantNone(t, "FK column leads a composite index",
		findingsOfKind(ordersSchema(50_000, btree("i", "user_id", "created_at")), KindMissingFKIndex))
}

func TestFKIndexDoesNotCount(t *testing.T) {
	partial := btree("i", "user_id")
	partial.Partial = true
	hash := btree("i", "user_id")
	hash.Method = "hash"

	cases := map[string]*schema.Index{
		"FK column is second in the index": btree("i", "created_at", "user_id"),
		"index is partial":                 partial,
		"index is not a btree":             hash,
	}
	for why, ix := range cases {
		if len(findingsOfKind(ordersSchema(50_000, ix), KindMissingFKIndex)) != 1 {
			t.Errorf("%s: the FK should still be reported as unindexed", why)
		}
	}
}

func TestCompositeFKIndexInAnyOrder(t *testing.T) {
	variants := table("variants", 100, col("product_id", "bigint"), col("size", "text"))
	variants.PrimaryKey = []string{"product_id", "size"}
	stock := table("stock", 100, col("id", "bigint"), col("product_id", "bigint"), col("size", "text"))
	stock.PrimaryKey = []string{"id"}
	stock.ForeignKeys = []*schema.ForeignKey{{Name: "stock_variant_fkey", Columns: []string{"product_id", "size"},
		RefSchema: "public", RefTable: "variants", RefColumns: []string{"product_id", "size"}}}
	stock.Indexes = []*schema.Index{btree("stock_size_product", "size", "product_id")}

	wantNone(t, "(size, product_id) serves FK (product_id, size)",
		findingsOfKind(analyzed(variants, stock), KindMissingFKIndex))
}

func TestMissingFKIndexOnPartitionedTableAvoidsConcurrently(t *testing.T) {
	s := ordersSchema(50_000)
	s.Table("public.orders").Partitioned = true

	f := wantOne(t, findingsOfKind(s, KindMissingFKIndex))
	if strings.Contains(f.Suggestion, "CREATE INDEX CONCURRENTLY") {
		t.Errorf("CONCURRENTLY fails on partitioned tables: %s", f.Suggestion)
	}
}

// --- no_primary_key -----------------------------------------------------------

func TestNoPrimaryKey(t *testing.T) {
	audit := table("audit_log", 100, col("occurred_at", "timestamptz"), col("action", "text"))

	f := wantOne(t, findingsOfKind(analyzed(audit), KindNoPrimaryKey))

	if !strings.Contains(f.Suggestion, "ADD COLUMN id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY") {
		t.Errorf("should suggest a new identity key: %s", f.Suggestion)
	}
}

func TestNoPrimaryKeyPromotesExistingUniqueKey(t *testing.T) {
	tags := table("tags", 100, col("slug", "text"))
	tags.Uniques = [][]string{{"slug"}}

	f := wantOne(t, findingsOfKind(analyzed(tags), KindNoPrimaryKey))

	if !strings.Contains(f.Suggestion, "ALTER TABLE public.tags ADD PRIMARY KEY (slug);") {
		t.Errorf("NOT NULL unique slug should become the key: %s", f.Suggestion)
	}
}

func TestTableWithPrimaryKeyIsFine(t *testing.T) {
	wantNone(t, "both tables have a PK", findingsOfKind(ordersSchema(10), KindNoPrimaryKey))
}

// --- redundant_index ----------------------------------------------------------

func TestRedundantIndex(t *testing.T) {
	short := btree("idx_orders_user_id", "user_id")
	short.Bytes = 1 << 20

	f := wantOne(t, findingsOfKind(
		ordersSchema(50_000, short, btree("idx_orders_user_created", "user_id", "created_at")),
		KindRedundantIndex))

	if f.Severity != schema.SeverityLow || f.Suggestion != "DROP INDEX CONCURRENTLY public.idx_orders_user_id;" {
		t.Errorf("unexpected finding: %+v", f)
	}
	if !strings.Contains(f.Detail, "1.0 MB") {
		t.Errorf("detail should say how much space dropping it saves: %s", f.Detail)
	}
}

func TestNotRedundant(t *testing.T) {
	unique := btree("a", "user_id")
	unique.Unique = true
	partial := btree("a", "user_id")
	partial.Partial = true
	withInclude := btree("a", "user_id")
	withInclude.Include = []string{"created_at"}
	long := btree("b", "user_id", "created_at")

	cases := map[string][]*schema.Index{
		"columns in a different order": {btree("a", "created_at"), long},
		"short index is unique":        {unique, long},
		"short index is partial":       {partial, long},
		"short index has INCLUDE":      {withInclude, long},
	}
	for why, indexes := range cases {
		wantNone(t, why, findingsOfKind(ordersSchema(10, indexes...), KindRedundantIndex))
	}
}

// --- duplicate_index ----------------------------------------------------------

func TestDuplicateIndex(t *testing.T) {
	a := btree("idx_orders_created", "created_at")
	b := btree("orders_created_idx", "created_at")
	b.Scans = 0 // the one nobody uses is the one to drop

	f := wantOne(t, findingsOfKind(ordersSchema(10, a, b), KindDuplicateIndex))

	if f.Severity != schema.SeverityMedium || !strings.Contains(f.Suggestion, "orders_created_idx") {
		t.Errorf("should suggest dropping the unused copy: %+v", f)
	}
}

func TestDuplicateOfPrimaryKeyKeepsPrimaryKey(t *testing.T) {
	copyOfPK := btree("orders_id_idx", "id")

	f := wantOne(t, findingsOfKind(ordersSchema(10, copyOfPK), KindDuplicateIndex))

	if !strings.Contains(f.Suggestion, "orders_id_idx") || strings.Contains(f.Suggestion, "orders_pkey") {
		t.Errorf("must never suggest dropping the primary key: %s", f.Suggestion)
	}
}

func TestNotDuplicate(t *testing.T) {
	partial := btree("b", "created_at")
	partial.Partial = true
	partial.Predicate = "created_at > '2020-01-01'"

	wantNone(t, "different predicate",
		findingsOfKind(ordersSchema(10, btree("a", "created_at"), partial), KindDuplicateIndex))
	wantNone(t, "same columns, different order",
		findingsOfKind(ordersSchema(10, btree("a", "user_id", "created_at"), btree("b", "created_at", "user_id")),
			KindDuplicateIndex))
}

// --- unused_index -------------------------------------------------------------

func bigUnused(name string, cols ...string) *schema.Index {
	ix := btree(name, cols...)
	ix.Scans = 0
	ix.Bytes = 5 << 20
	return ix
}

func TestUnusedIndex(t *testing.T) {
	f := wantOne(t, findingsOfKind(ordersSchema(10, bigUnused("idx_orders_created", "created_at")), KindUnusedIndex))

	if f.Severity != schema.SeverityLow || !strings.Contains(f.Detail, "stats reset") {
		t.Errorf("should be low and carry the stats-reset caveat: %+v", f)
	}
}

func TestNotUnused(t *testing.T) {
	small := bigUnused("a", "created_at")
	small.Bytes = 64 << 10
	unique := bigUnused("a", "created_at")
	unique.Unique = true
	unknown := bigUnused("a", "created_at")
	unknown.Scans = -1

	cases := map[string]*schema.Index{
		"smaller than 1 MB":        small,
		"unique":                   unique,
		"no statistics":            unknown,
		"it's the FK's only index": bigUnused("a", "user_id"),
	}
	for why, ix := range cases {
		wantNone(t, why, findingsOfKind(ordersSchema(10, ix), KindUnusedIndex))
	}
}

func TestIndexIsReportedOnlyOnce(t *testing.T) {
	a := bigUnused("idx_a", "created_at")
	b := bigUnused("idx_b", "created_at")

	s := ordersSchema(10, a, b)

	if n := len(findingsOfKind(s, KindDuplicateIndex)); n != 1 {
		t.Fatalf("want 1 duplicate, got %d", n)
	}
	// idx_b is already "drop this, it's a duplicate"; it shouldn't also
	// appear as unused. idx_a (the one kept) still can.
	for _, f := range findingsOfKind(s, KindUnusedIndex) {
		if strings.Contains(f.Title, "idx_b") {
			t.Errorf("idx_b reported twice: %+v", f)
		}
	}
}

// --- inferred_relation --------------------------------------------------------

func TestInferredRelationSuggestsForeignKey(t *testing.T) {
	products := table("products", 10, col("id", "bigint"))
	products.PrimaryKey = []string{"id"}
	reviews := table("reviews", 10, col("id", "bigint"), col("product_id", "bigint"))
	reviews.PrimaryKey = []string{"id"}

	f := wantOne(t, findingsOfKind(analyzed(products, reviews), KindInferredRelation))

	for _, want := range []string{
		"ALTER TABLE public.reviews ADD CONSTRAINT reviews_product_id_fkey FOREIGN KEY (product_id) REFERENCES public.products (id) NOT VALID;",
		"ALTER TABLE public.reviews VALIDATE CONSTRAINT reviews_product_id_fkey;",
	} {
		if !strings.Contains(f.Suggestion, want) {
			t.Errorf("suggestion missing %q:\n%s", want, f.Suggestion)
		}
	}
}

func TestRealForeignKeyIsNotInferred(t *testing.T) {
	wantNone(t, "orders.user_id has a real FK", findingsOfKind(ordersSchema(10), KindInferredRelation))
}

// --- fk_type_mismatch ---------------------------------------------------------

func TestFKTypeMismatch(t *testing.T) {
	s := ordersSchema(10, btree("i", "user_id"))
	s.Table("public.orders").Column("user_id").Type = "integer"

	f := wantOne(t, findingsOfKind(s, KindFKTypeMismatch))

	if !strings.Contains(f.Title, "integer") || !strings.Contains(f.Title, "bigint") {
		t.Errorf("title should name both types: %s", f.Title)
	}
	if !strings.Contains(f.Suggestion, "ALTER COLUMN user_id TYPE bigint") {
		t.Errorf("unexpected suggestion: %s", f.Suggestion)
	}
}

func TestSameTypeWithDifferentLengthIsNotAMismatch(t *testing.T) {
	codes := table("countries", 10, col("code", "character varying(3)"))
	codes.PrimaryKey = []string{"code"}
	addr := table("addresses", 10, col("id", "bigint"), col("country", "character varying(10)"))
	addr.PrimaryKey = []string{"id"}
	addr.ForeignKeys = []*schema.ForeignKey{{Name: "addr_country_fkey", Columns: []string{"country"},
		RefSchema: "public", RefTable: "countries", RefColumns: []string{"code"}}}
	addr.Indexes = []*schema.Index{btree("i", "country")}

	wantNone(t, "varchar(10) → varchar(3) is the same type", findingsOfKind(analyzed(codes, addr), KindFKTypeMismatch))
}

// --- ordering and SQL helpers -------------------------------------------------

func TestFindingsAreSortedBySeverityThenTable(t *testing.T) {
	s := ordersSchema(50_000)                                                  // high: missing FK index on orders
	s.Tables = append(s.Tables, table("audit_log", 10, col("action", "text"))) // medium: no PK

	fs := Findings(s)

	if len(fs) < 2 || fs[0].Severity != schema.SeverityHigh || fs[1].Severity != schema.SeverityMedium {
		t.Errorf("high findings must come first: %+v", fs)
	}
}

func TestIdentQuotesOnlyWhenNeeded(t *testing.T) {
	cases := map[string]string{
		"users":  "users",
		"order":  `"order"`,
		"userId": `"userId"`,
		`we"ird`: `"we""ird"`,
		"line_2": "line_2",
	}
	for in, want := range cases {
		if got := ident(in); got != want {
			t.Errorf("ident(%q) = %s, want %s", in, got, want)
		}
	}
}
