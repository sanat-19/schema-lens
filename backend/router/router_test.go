package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sanat-19/schema-lens/backend/api"
	"github.com/sanat-19/schema-lens/backend/models"
	"github.com/sanat-19/schema-lens/backend/pkg/session"
	"github.com/sanat-19/schema-lens/backend/pkg/store"
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

// opener hands out fake databases by name; the request's Database field
// picks one, as the form would.
func opener(dbs ...*fakeDatabase) session.Opener {
	return func(_ context.Context, req models.ConnectRequest) (*session.Database, error) {
		for _, db := range dbs {
			if db.name == req.Database {
				return &session.Database{
					Load: db.load, Fingerprint: db.fingerprint,
					Close: func() {
						db.mu.Lock()
						db.closed = true
						db.mu.Unlock()
					},
					Source: models.Source{Host: req.Host, Port: 5432, Database: db.name, User: req.User},
				}, nil
			}
		}
		return nil, errors.New(`password authentication failed for user "` + req.User + `"`)
	}
}

// options are the session options a test needs, plus the guard's.
type options struct {
	Open      session.Opener
	Store     *store.Store
	LocalOnly bool
}

func newTestServer(t *testing.T, opts options) (*session.Session, *httptest.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sess := session.New(ctx, session.Options{
		Open: opts.Open, Store: opts.Store,
		WatchEvery: 5 * time.Millisecond, StatsEvery: time.Hour,
	})
	web := httptest.NewServer(New(api.New(sess), opts.LocalOnly))
	t.Cleanup(func() {
		web.Close()
		sess.Close()
		cancel()
	})
	return sess, web
}

// --- viewing ------------------------------------------------------------------------

func TestSchemaEndpoints(t *testing.T) {
	srv, web := newTestServer(t, options{})

	if res := get(t, web.URL+"/api/schema"); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("before anything is loaded, want 503, got %d", res.StatusCode)
	}

	srv.Show(sampleSchema("users"), "snap.json", nil)

	res := get(t, web.URL+"/api/schema?refresh=1")
	if res.Header.Get("X-Schema-Source") == "" || res.Header.Get("X-Schema-Version") == "" {
		t.Error("the page needs the source and version headers")
	}
	var got models.Schema
	decode(t, res, &got)
	if len(got.Tables) != 1 || got.Tables[0].Name != "users" {
		t.Errorf("snapshot refresh should return the snapshot unchanged, got %+v", got.Tables)
	}

	body := readAll(t, get(t, web.URL+"/api/export/mermaid"))
	if !strings.HasPrefix(body, "erDiagram") || !strings.Contains(body, "users {") {
		t.Errorf("unexpected mermaid:\n%s", body)
	}
}

func TestEventsTellBrowsersAboutChanges(t *testing.T) {
	srv, web := newTestServer(t, options{})
	srv.Show(sampleSchema("users"), "snap.json", nil)

	res := get(t, web.URL+"/api/events")
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("want an event stream, got %s", ct)
	}
	events := bufio.NewReader(res.Body)

	name, status := nextEvent(t, events)
	if name != "status" || status.Mode != models.ModeSnapshot || status.Version != 1 {
		t.Errorf("a new browser should first get the current status, got %s %+v", name, status)
	}

	srv.Hub().Publish(sampleSchema("users", "orders"))

	name, status = nextEvent(t, events)
	if name != "schema" || status.Version != 2 {
		t.Errorf("want a schema event for version 2, got %s %+v", name, status)
	}
}

// --- connecting from the page -------------------------------------------------------

func TestConnectFromThePage(t *testing.T) {
	shop := newFakeDatabase("shop", "users", "orders")
	srv, web := newTestServer(t, options{Open: opener(shop)})

	res := postJSON(t, web.URL+"/api/connect", models.ConnectRequest{Host: "db", Database: "shop", User: "app", Password: "secret"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("connect failed: %d %s", res.StatusCode, readAll(t, res))
	}
	var status models.Status
	decode(t, res, &status)
	if status.Mode != models.ModeLive || status.Source == nil || status.Source.Label != "shop on db:5432" {
		t.Errorf("want a live view of shop, got %+v", status)
	}
	if strings.Contains(readAll(t, get(t, web.URL+"/api/status")), "secret") {
		t.Error("the password must never be sent back to the page")
	}

	// It's live: a migration shows up without doing anything.
	shop.set("v2", "users", "orders", "wishlists")
	waitFor(t, "the new table", func() bool {
		s, _, _ := srv.Hub().Current()
		return len(s.Tables) == 3
	})
}

func TestConnectToAnotherDatabaseReplacesTheFirst(t *testing.T) {
	shop, crm := newFakeDatabase("shop", "users"), newFakeDatabase("crm", "accounts", "contacts")
	srv, web := newTestServer(t, options{Open: opener(shop, crm)})

	postJSON(t, web.URL+"/api/connect", models.ConnectRequest{Host: "db", Database: "shop", User: "app"}).Body.Close()
	first := srv.Hub().Status().Source.Key
	postJSON(t, web.URL+"/api/connect", models.ConnectRequest{Host: "db", Database: "crm", User: "app"}).Body.Close()

	current, _, status := srv.Hub().Current()
	if current.Database != "crm" || status.Source.Key == first {
		t.Errorf("want crm under a new source key, got %s (%s)", current.Database, status.Source.Key)
	}
	if !shop.isClosed() {
		t.Error("the first database's connection should be closed")
	}

	// The old watcher must be gone: changing shop now changes nothing.
	shop.set("v9", "users", "orders", "payments")
	time.Sleep(50 * time.Millisecond)
	if current, _, _ := srv.Hub().Current(); current.Database != "crm" {
		t.Error("the old database's watcher is still publishing")
	}
}

func TestFailedConnectKeepsTheCurrentGraph(t *testing.T) {
	shop := newFakeDatabase("shop", "users")
	srv, web := newTestServer(t, options{Open: opener(shop)})
	postJSON(t, web.URL+"/api/connect", models.ConnectRequest{Host: "db", Database: "shop", User: "app"}).Body.Close()

	res := postJSON(t, web.URL+"/api/connect", models.ConnectRequest{Host: "db", Database: "nope", User: "bob", Password: "wrong"})

	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("want 502, got %d", res.StatusCode)
	}
	var body map[string]string
	decode(t, res, &body)
	if !strings.Contains(body["error"], "password authentication failed") {
		t.Errorf("the page should get the database's reason, got %q", body["error"])
	}
	if current, _, _ := srv.Hub().Current(); current.Database != "shop" {
		t.Error("a failed connect must not replace what's on screen")
	}
}

func TestDatabaseThatCantBeReadIsClosed(t *testing.T) {
	broken := newFakeDatabase("broken", "users")
	broken.setDown(true)
	_, web := newTestServer(t, options{Open: opener(broken)})

	res := postJSON(t, web.URL+"/api/connect", models.ConnectRequest{Host: "db", Database: "broken", User: "app"})
	res.Body.Close()

	if res.StatusCode != http.StatusBadGateway || !broken.isClosed() {
		t.Errorf("a connection we can't read from should be refused and closed (status %d)", res.StatusCode)
	}
}

func TestConnectNeedsEnoughToGoOn(t *testing.T) {
	_, web := newTestServer(t, options{Open: opener()})

	res := postJSON(t, web.URL+"/api/connect", models.ConnectRequest{Host: "db"})
	res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400 without a database and user, got %d", res.StatusCode)
	}
}

func TestDisconnect(t *testing.T) {
	shop := newFakeDatabase("shop", "users")
	srv, web := newTestServer(t, options{Open: opener(shop)})
	postJSON(t, web.URL+"/api/connect", models.ConnectRequest{Host: "db", Database: "shop", User: "app"}).Body.Close()

	postJSON(t, web.URL+"/api/disconnect", struct{}{}).Body.Close()

	if st := srv.Hub().Status(); st.Mode != models.ModeNone || !shop.isClosed() {
		t.Errorf("want the Connect screen and a closed connection, got %+v", st)
	}
}

// --- saved graphs --------------------------------------------------------------------

func TestSaveOpenAndDeleteGraphs(t *testing.T) {
	dir := t.TempDir()
	graphs, _ := store.Open(dir)
	shop := newFakeDatabase("shop", "users", "orders")
	srv, web := newTestServer(t, options{Open: opener(shop), Store: graphs})
	postJSON(t, web.URL+"/api/connect", models.ConnectRequest{Host: "db", Database: "shop", User: "app", Password: "hunter2"}).Body.Close()

	// Save, with the tables where the user dragged them.
	res := postJSON(t, web.URL+"/api/graphs", models.SaveGraphRequest{
		Name:      "Before the migration",
		Positions: map[string]models.Position{"public.users": {X: 100, Y: 200}},
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("save failed: %d %s", res.StatusCode, readAll(t, res))
	}
	var saved models.SavedGraphSummary
	decode(t, res, &saved)

	file, _ := os.ReadFile(filepath.Join(dir, saved.ID+".json"))
	if bytes.Contains(file, []byte("hunter2")) {
		t.Fatal("the password must never be written to disk")
	}

	var list []models.SavedGraphSummary
	decode(t, get(t, web.URL+"/api/graphs"), &list)
	if len(list) != 1 || list[0].Name != "Before the migration" || list[0].Source.User != "app" || list[0].Tables != 2 {
		t.Errorf("unexpected list %+v", list)
	}

	// The database moves on; opening the saved graph shows it as it was.
	shop.set("v2", "users", "orders", "wishlists")
	res = postJSON(t, web.URL+"/api/graphs/"+saved.ID+"/open", struct{}{})
	var status models.Status
	decode(t, res, &status)
	if status.Mode != models.ModeSnapshot || status.Source.SavedID != saved.ID || status.Source.Conn.Database != "shop" {
		t.Errorf("want the saved graph as a snapshot, got %+v", status)
	}
	if current, _, _ := srv.Hub().Current(); len(current.Tables) != 2 {
		t.Errorf("want the 2 tables as saved, got %d", len(current.Tables))
	}

	var layout models.LayoutResponse
	decode(t, get(t, web.URL+"/api/layout"), &layout)
	if layout.Source != status.Source.Key || layout.Positions["public.users"] != (models.Position{X: 100, Y: 200}) {
		t.Errorf("the saved positions should come back, got %+v", layout)
	}

	req, _ := http.NewRequest(http.MethodDelete, web.URL+"/api/graphs/"+saved.ID, nil)
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != http.StatusNoContent {
		t.Errorf("delete: want 204, got %d", res.StatusCode)
	}
	decode(t, get(t, web.URL+"/api/graphs"), &list)
	if len(list) != 0 {
		t.Errorf("want no saved graphs after deleting, got %+v", list)
	}
}

func TestSaveWithNothingLoaded(t *testing.T) {
	graphs, _ := store.Open(t.TempDir())
	_, web := newTestServer(t, options{Store: graphs})

	res := postJSON(t, web.URL+"/api/graphs", models.SaveGraphRequest{Name: "empty"})
	res.Body.Close()

	if res.StatusCode != http.StatusConflict {
		t.Errorf("want 409 with nothing to save, got %d", res.StatusCode)
	}
}

// --- the guard ------------------------------------------------------------------------

func TestOtherWebsitesCantUseTheAPI(t *testing.T) {
	_, web := newTestServer(t, options{Open: opener(newFakeDatabase("shop")), LocalOnly: true})
	body := `{"host":"db","database":"shop","user":"app"}`

	cases := []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"form post (no preflight)", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType},
		{"foreign origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden},
		{"DNS rebinding", func(r *http.Request) { r.Host = "evil.example:8080" }, http.StatusForbidden},
		{"our own page", func(r *http.Request) { r.Header.Set("Origin", "http://"+r.Host) }, http.StatusOK},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(http.MethodPost, web.URL+"/api/connect", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		c.mutate(req)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Errorf("%s: want %d, got %d", c.name, c.want, res.StatusCode)
		}
	}
}

func TestIsLocalHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost:8080": true, "127.0.0.1:8080": true, "[::1]:8080": true, "localhost": true,
		"evil.example:8080": false, "192.168.1.10:8080": false, "localhost.evil.example": false,
	} {
		if got := IsLocalHost(host); got != want {
			t.Errorf("IsLocalHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// --- helpers --------------------------------------------------------------------------

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func postJSON(t *testing.T, url string, v any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(v)
	res, err := http.Post(url, "application/json", bytes.NewReader(body))
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
func nextEvent(t *testing.T, r *bufio.Reader) (string, models.Status) {
	t.Helper()
	var name string
	var status models.Status
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
