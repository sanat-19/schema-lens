package schema

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	want := &Schema{
		Database:   "shop",
		CapturedAt: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
		Tables: []*Table{{Schema: "public", Name: "users", RowEstimate: -1,
			Columns: []*Column{{Name: "id", Type: "bigint", IsPK: true}}}},
		Relations: []Relation{{ID: "r", From: "public.users", To: "public.users", Cardinality: ManyToOne}},
		Findings:  []Finding{{Kind: "no_primary_key", Severity: SeverityMedium, Table: "public.users"}},
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(f, want); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tables[0].ID() != "public.users" || got.Tables[0].RowEstimate != -1 ||
		!got.CapturedAt.Equal(want.CapturedAt) || len(got.Relations) != 1 || len(got.Findings) != 1 {
		t.Errorf("snapshot didn't survive the round trip: %+v", got)
	}
}

func TestLoadSnapshotRejectsOtherJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package.json")
	os.WriteFile(path, []byte(`{"name": "not-a-schema"}`), 0o644)

	if _, err := LoadSnapshot(path); err == nil {
		t.Error("a JSON file without tables is not a snapshot")
	}
}
