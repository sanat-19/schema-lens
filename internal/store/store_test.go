package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sanat-19/schema-lens/internal/schema"
)

func shop(tables ...string) *schema.Schema {
	s := &schema.Schema{Database: "shop"}
	for _, name := range tables {
		s.Tables = append(s.Tables, &schema.Table{Schema: "public", Name: name})
	}
	return s
}

func TestSaveAndOpen(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sum, err := st.Save(Graph{
		Name:        "Shop before migration",
		Source:      schema.Source{Host: "db", Port: 5432, Database: "shop", User: "app"},
		Positions:   map[string]Position{"public.users": {X: 10, Y: 20}},
		ShowColumns: true,
		Schema:      shop("users", "orders"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !validID.MatchString(sum.ID) || sum.Tables != 2 {
		t.Errorf("unexpected summary %+v", sum)
	}

	g, err := st.Get(sum.ID)
	if err != nil {
		t.Fatal(err)
	}
	if g.Name != "Shop before migration" || g.Positions["public.users"] != (Position{10, 20}) ||
		!g.ShowColumns || len(g.Schema.Tables) != 2 || g.Source.User != "app" {
		t.Errorf("graph didn't survive the round trip: %+v", g)
	}
}

func TestFilesArePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "graphs")
	st, _ := Open(dir)
	sum, _ := st.Save(Graph{Name: "x", Schema: shop("users")})

	info, err := os.Stat(filepath.Join(dir, sum.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("saved graphs describe your schema; want mode 0600, got %v", info.Mode().Perm())
	}
	if dirInfo, _ := os.Stat(dir); dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("want directory mode 0700, got %v", dirInfo.Mode().Perm())
	}
}

func TestListIsNewestFirstAndSkipsJunk(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	old, _ := st.Save(Graph{Name: "old", SavedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Schema: shop("a")})
	recent, _ := st.Save(Graph{Name: "new", SavedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Schema: shop("a")})
	os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600)

	list, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != recent.ID || list[1].ID != old.ID {
		t.Errorf("want [new, old], got %+v", list)
	}
}

func TestSameNameSameSecondGetsItsOwnID(t *testing.T) {
	st, _ := Open(t.TempDir())
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	a, _ := st.Save(Graph{Name: "shop", SavedAt: at, Schema: shop("a")})
	b, _ := st.Save(Graph{Name: "shop", SavedAt: at, Schema: shop("a")})

	if a.ID == b.ID {
		t.Fatalf("second save overwrote the first: both %s", a.ID)
	}
}

func TestDelete(t *testing.T) {
	st, _ := Open(t.TempDir())
	sum, _ := st.Save(Graph{Name: "x", Schema: shop("a")})

	if err := st.Delete(sum.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(sum.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted graph should be gone, got %v", err)
	}
	if err := st.Delete(sum.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice should say not found, got %v", err)
	}
}

func TestBadIDsNeverTouchTheDisk(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(filepath.Join(dir, "graphs"))
	secret := filepath.Join(dir, "secret.json")
	os.WriteFile(secret, []byte(`{"schema": {"tables": []}}`), 0o600)

	for _, id := range []string{"../secret", "..", "", "UPPER", "a/b", `a\b`, "a.json"} {
		if _, err := st.Get(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) should be refused, got %v", id, err)
		}
		if err := st.Delete(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete(%q) should be refused, got %v", id, err)
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Error("a file outside the store was touched")
	}
}

func TestSaveNeedsASchema(t *testing.T) {
	st, _ := Open(t.TempDir())
	if _, err := st.Save(Graph{Name: "empty"}); err == nil {
		t.Error("saving with no schema loaded should fail")
	}
}
