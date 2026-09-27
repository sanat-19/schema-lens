package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sanat-19/schema-lens/backend/models"
)

func sampleSchema(tables ...string) *models.Schema {
	s := &models.Schema{Database: "shop", CapturedAt: time.Now()}
	for _, name := range tables {
		s.Tables = append(s.Tables, &models.Table{Schema: "public", Name: name,
			Columns: []*models.Column{{Name: "id", Type: "bigint", IsPK: true}}, PrimaryKey: []string{"id"}})
	}
	return s
}

// --- a pretend database ---------------------------------------------------------

// fakeDatabase stands in for Postgres: tests change its tables and its
// fingerprint, or make it fail.
type fakeDatabase struct {
	mu     sync.Mutex
	name   string
	tables []string
	print  string
	down   bool
	loads  int
	closed bool
}

func newFakeDatabase(name string, tables ...string) *fakeDatabase {
	return &fakeDatabase{name: name, tables: tables, print: "v1"}
}

func (db *fakeDatabase) set(print string, tables ...string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.print, db.tables = print, tables
}

func (db *fakeDatabase) setDown(down bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.down = down
}

func (db *fakeDatabase) fingerprint(context.Context) (string, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.down {
		return "", errors.New("connection refused")
	}
	return db.print, nil
}

func (db *fakeDatabase) load(context.Context) (*models.Schema, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.down {
		return nil, errors.New("connection refused")
	}
	db.loads++
	s := sampleSchema(db.tables...)
	s.Database = db.name
	return s, nil
}

func (db *fakeDatabase) loadCount() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.loads
}

func (db *fakeDatabase) isClosed() bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.closed
}

// --- the hub ------------------------------------------------------------------------

func TestPublishIgnoresCaptureTimeOnly(t *testing.T) {
	hub := NewHub(models.ModeLive)

	if changed, _ := hub.Publish(sampleSchema("users")); !changed {
		t.Fatal("first schema is a change")
	}
	again := sampleSchema("users")
	again.CapturedAt = time.Now().Add(time.Minute)
	if changed, _ := hub.Publish(again); changed {
		t.Error("same schema read a minute later is not a change")
	}
	if changed, _ := hub.Publish(sampleSchema("users", "orders")); !changed {
		t.Error("a new table is a change")
	}
	if v := hub.Status().Version; v != 2 {
		t.Errorf("want version 2 after two real changes, got %d", v)
	}
}

func TestReplaceIsAlwaysANewVersion(t *testing.T) {
	hub := NewHub(models.ModeNone)
	hub.Publish(sampleSchema("users"))

	hub.Replace(sampleSchema("users"), models.ModeSnapshot, &models.SourceInfo{Key: "k2", Label: "saved"})

	st := hub.Status()
	if st.Version != 2 || st.Mode != models.ModeSnapshot || st.Source.Key != "k2" {
		t.Errorf("switching source must bump the version even if the schema looks the same: %+v", st)
	}
}

// --- the watcher ----------------------------------------------------------------------

func TestWatcherFollowsTheDatabase(t *testing.T) {
	db := newFakeDatabase("shop", "users")
	hub := NewHub(models.ModeLive)
	w := &Watcher{Load: db.load, Fingerprint: db.fingerprint, Hub: hub,
		WatchEvery: 5 * time.Millisecond, StatsEvery: time.Hour}

	if err := w.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Nothing changes: the watcher only fingerprints, it doesn't reload.
	time.Sleep(50 * time.Millisecond)
	if n := db.loadCount(); n != 1 {
		t.Errorf("unchanged fingerprint should not trigger a reload, got %d loads", n)
	}

	// A migration adds a table.
	db.set("v2", "users", "orders")
	waitFor(t, "the new table to show up", func() bool {
		s, _, _ := hub.Current()
		return len(s.Tables) == 2
	})

	// The database goes away: keep the last schema, say we're reconnecting.
	db.setDown(true)
	waitFor(t, "state to become reconnecting", func() bool { return hub.Status().State == "reconnecting" })
	if s, _, _ := hub.Current(); len(s.Tables) != 2 {
		t.Error("the last good schema must stay while the database is down")
	}

	// It comes back.
	db.setDown(false)
	waitFor(t, "state to become live again", func() bool { return hub.Status().State == models.ModeLive })
}

func TestWatcherRefreshesStatsOnItsOwnTimer(t *testing.T) {
	db := newFakeDatabase("shop", "users")
	w := &Watcher{Load: db.load, Fingerprint: db.fingerprint, Hub: NewHub(models.ModeLive),
		WatchEvery: 5 * time.Millisecond, StatsEvery: 20 * time.Millisecond}
	w.Reload(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	waitFor(t, "a stats reload without a structure change", func() bool { return db.loadCount() >= 2 })
}

func TestStoppedWatcherNeverPublishes(t *testing.T) {
	db := newFakeDatabase("shop", "users")
	hub := NewHub(models.ModeLive)
	w := &Watcher{Load: db.load, Fingerprint: db.fingerprint, Hub: hub}
	w.Reload(context.Background())

	w.Stop()
	db.set("v2", "users", "orders")
	w.Reload(context.Background())

	if s, _, _ := hub.Current(); len(s.Tables) != 1 {
		t.Error("a stopped watcher published after the UI moved on")
	}
}

// --- helpers --------------------------------------------------------------------------

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
