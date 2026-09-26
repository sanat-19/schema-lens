package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sanat-19/schema-lens/internal/schema"
)

func sampleSchema(tables ...string) *schema.Schema {
	s := &schema.Schema{Database: "shop", CapturedAt: time.Now()}
	for _, name := range tables {
		s.Tables = append(s.Tables, &schema.Table{Schema: "public", Name: name,
			Columns: []*schema.Column{{Name: "id", Type: "bigint", IsPK: true}}, PrimaryKey: []string{"id"}})
	}
	return s
}

var ui = fstest.MapFS{"index.html": {Data: []byte("<h1>SchemaLens</h1>")}}

func TestPublishIgnoresCaptureTimeOnly(t *testing.T) {
	hub := NewHub("live")

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

func TestSchemaEndpoints(t *testing.T) {
	hub := NewHub("snapshot")
	srv := httptest.NewServer(New(hub, ui, nil))
	defer srv.Close()

	if res := get(t, srv.URL+"/api/schema"); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("before the first load, want 503, got %d", res.StatusCode)
	}

	hub.Publish(sampleSchema("users"))

	var got schema.Schema
	decode(t, get(t, srv.URL+"/api/schema?refresh=1"), &got)
	if len(got.Tables) != 1 || got.Tables[0].Name != "users" {
		t.Errorf("snapshot refresh should return the snapshot unchanged, got %+v", got.Tables)
	}

	body := readAll(t, get(t, srv.URL+"/api/export/mermaid"))
	if !strings.HasPrefix(body, "erDiagram") || !strings.Contains(body, "users {") {
		t.Errorf("unexpected mermaid:\n%s", body)
	}

	if body := readAll(t, get(t, srv.URL+"/")); !strings.Contains(body, "SchemaLens") {
		t.Errorf("/ should serve the UI, got %q", body)
	}
}

func TestRefreshReloadsLiveDatabase(t *testing.T) {
	hub := NewHub("live")
	hub.Publish(sampleSchema("users"))
	reload := func(context.Context) error {
		_, err := hub.Publish(sampleSchema("users", "orders"))
		return err
	}
	srv := httptest.NewServer(New(hub, ui, reload))
	defer srv.Close()

	var got schema.Schema
	decode(t, get(t, srv.URL+"/api/schema?refresh=1"), &got)
	if len(got.Tables) != 2 {
		t.Errorf("refresh should return the freshly read schema, got %d tables", len(got.Tables))
	}
}

func TestEventsTellBrowsersAboutChanges(t *testing.T) {
	hub := NewHub("live")
	hub.Publish(sampleSchema("users"))
	srv := httptest.NewServer(New(hub, ui, nil))
	defer srv.Close()

	res := get(t, srv.URL+"/api/events")
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("want an event stream, got %s", ct)
	}
	events := bufio.NewReader(res.Body)

	name, status := nextEvent(t, events)
	if name != "status" || status.State != "live" || status.Version != 1 {
		t.Errorf("a new browser should first get the current status, got %s %+v", name, status)
	}

	hub.Publish(sampleSchema("users", "orders"))

	name, status = nextEvent(t, events)
	if name != "schema" || status.Version != 2 {
		t.Errorf("want a schema event for version 2, got %s %+v", name, status)
	}
}

// --- watcher --------------------------------------------------------------------

// fakeDatabase stands in for Postgres: tests change its tables and its
// fingerprint, or make it fail.
type fakeDatabase struct {
	mu     sync.Mutex
	tables []string
	print  string
	down   bool
	loads  int
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

func (db *fakeDatabase) load(context.Context) (*schema.Schema, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.down {
		return nil, errors.New("connection refused")
	}
	db.loads++
	return sampleSchema(db.tables...), nil
}

func (db *fakeDatabase) loadCount() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.loads
}

func TestWatcherFollowsTheDatabase(t *testing.T) {
	db := &fakeDatabase{}
	db.set("v1", "users")
	hub := NewHub("live")
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
	waitFor(t, "state to become live again", func() bool { return hub.Status().State == "live" })
}

func TestWatcherRefreshesStatsOnItsOwnTimer(t *testing.T) {
	db := &fakeDatabase{}
	db.set("v1", "users")
	w := &Watcher{Load: db.load, Fingerprint: db.fingerprint, Hub: NewHub("live"),
		WatchEvery: 5 * time.Millisecond, StatsEvery: 20 * time.Millisecond}
	w.Reload(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	waitFor(t, "a stats reload without a structure change", func() bool { return db.loadCount() >= 2 })
}

// --- helpers --------------------------------------------------------------------

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func readAll(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decode(t *testing.T, res *http.Response, v any) {
	t.Helper()
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

// nextEvent reads one SSE event, skipping keep-alive comments.
func nextEvent(t *testing.T, r *bufio.Reader) (string, Status) {
	t.Helper()
	var name string
	var status Status
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &status); err != nil {
				t.Fatal(err)
			}
		case line == "" && name != "":
			return name, status
		}
	}
}

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
