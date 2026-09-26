package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sanat-19/schema-lens/internal/analyze"
	"github.com/sanat-19/schema-lens/internal/graph"
)

// These tests need a real Postgres loaded with testdata/sample_schema.sql:
//
//	make db-up
//	SCHEMALENS_TEST_DSN=postgres://schemalens:schemalens@localhost:5433/shop go test ./internal/postgres
//
// Without SCHEMALENS_TEST_DSN they are skipped, so "go test ./..." works on
// any laptop.

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("SCHEMALENS_TEST_DSN")
	if dsn == "" {
		t.Skip("SCHEMALENS_TEST_DSN not set; skipping tests that need Postgres")
	}
	return dsn
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := Connect(ctx, testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestIntrospectSampleSchema(t *testing.T) {
	pool := testPool(t)
	s, err := NewIntrospector(pool, []string{"public", "billing"}).Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(s.Tables) != 17 {
		t.Errorf("want 17 tables (partitions folded), got %d", len(s.Tables))
	}

	fks := 0
	for _, tbl := range s.Tables {
		fks += len(tbl.ForeignKeys)
	}
	if fks != 18 {
		t.Errorf("want 18 foreign keys, got %d", fks)
	}

	events := s.Table("public.events")
	if events == nil || !events.Partitioned {
		t.Fatal("events should be one partitioned table")
	}
	if events.RowEstimate < 50_000 || events.TotalBytes == 0 {
		t.Errorf("events should add up its partitions' rows and size, got %d rows, %d bytes",
			events.RowEstimate, events.TotalBytes)
	}
	if s.Table("public.events_2026_01") != nil {
		t.Error("partitions must not show up as tables of their own")
	}

	payments := s.Table("billing.payments")
	if payments == nil || len(payments.ForeignKeys) != 1 || payments.ForeignKeys[0].RefID() != "public.orders" {
		t.Error("billing.payments should reference public.orders across schemas")
	}

	s.Relations = graph.Relations(s)
	s.Findings = analyze.Findings(s)

	kinds := map[string]bool{}
	for _, f := range s.Findings {
		kinds[f.Kind] = true
	}
	for _, want := range []string{
		analyze.KindMissingFKIndex, analyze.KindNoPrimaryKey, analyze.KindRedundantIndex,
		analyze.KindDuplicateIndex, analyze.KindUnusedIndex, analyze.KindInferredRelation,
		analyze.KindFKTypeMismatch,
	} {
		if !kinds[want] {
			t.Errorf("the demo schema should produce a %s finding", want)
		}
	}
}

func TestConnectionIsReadOnly(t *testing.T) {
	pool := testPool(t)

	_, err := pool.Exec(context.Background(), "CREATE TABLE schemalens_should_not_exist (id int)")

	if err == nil {
		pool.Exec(context.Background(), "DROP TABLE schemalens_should_not_exist")
		t.Fatal("SchemaLens connections must not be able to write")
	}
}

func TestFingerprintFollowsStructureOnly(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	// A normal, writable connection to play the part of a developer running
	// migrations, in a schema of its own so the demo tables stay untouched.
	dev, err := pgx.Connect(ctx, testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	// Cleanups run last-in first-out: the schema is dropped, then the
	// connection closed. (A defer would close it before the drop.)
	t.Cleanup(func() { dev.Close(context.Background()) })
	exec := func(sql string) {
		t.Helper()
		if _, err := dev.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("DROP SCHEMA IF EXISTS fingerprint_test CASCADE")
	exec("CREATE SCHEMA fingerprint_test")
	t.Cleanup(func() {
		if _, err := dev.Exec(context.Background(), "DROP SCHEMA fingerprint_test CASCADE"); err != nil {
			t.Errorf("cleaning up: %v", err)
		}
	})
	exec("CREATE TABLE fingerprint_test.t (id int PRIMARY KEY)")

	in := NewIntrospector(pool, []string{"fingerprint_test"})
	print := func() string {
		t.Helper()
		fp, err := in.Fingerprint(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}

	before := print()
	if print() != before {
		t.Fatal("fingerprint must be stable when nothing changes")
	}

	exec("INSERT INTO fingerprint_test.t SELECT generate_series(1, 1000)")
	exec("ANALYZE fingerprint_test.t")
	if print() != before {
		t.Error("inserting rows is not a schema change")
	}

	steps := []string{
		"ALTER TABLE fingerprint_test.t ADD COLUMN name text DEFAULT 'x'",
		"ALTER TABLE fingerprint_test.t ALTER COLUMN name SET NOT NULL",
		"ALTER TABLE fingerprint_test.t ALTER COLUMN name SET DEFAULT 'y'",
		"ALTER TABLE fingerprint_test.t ALTER COLUMN name TYPE varchar(20)",
		"CREATE INDEX t_name ON fingerprint_test.t (name)",
		"COMMENT ON COLUMN fingerprint_test.t.name IS 'hello'",
		"CREATE TABLE fingerprint_test.child (id int PRIMARY KEY, t_id int REFERENCES fingerprint_test.t)",
		"ALTER TABLE fingerprint_test.child RENAME TO kid",
		"DROP TABLE fingerprint_test.kid",
	}
	// Compared with the previous step only: dropping the table we just
	// created rightly gives back an earlier hash.
	prev := before
	for _, step := range steps {
		exec(step)
		fp := print()
		if fp == prev {
			t.Errorf("fingerprint didn't change after: %s", step)
		}
		prev = fp
	}
}

func BenchmarkFingerprint(b *testing.B) {
	dsn := os.Getenv("SCHEMALENS_TEST_DSN")
	if dsn == "" {
		b.Skip("SCHEMALENS_TEST_DSN not set")
	}
	pool, err := Connect(context.Background(), dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()
	in := NewIntrospector(pool, nil)

	for b.Loop() {
		if _, err := in.Fingerprint(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
