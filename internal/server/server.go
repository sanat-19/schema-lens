// Package server serves the web UI and the JSON API it reads from, and
// decides what the UI is looking at: a live database, a saved graph, or a
// snapshot file.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sanat-19/schema-lens/internal/render"
	"github.com/sanat-19/schema-lens/internal/schema"
	"github.com/sanat-19/schema-lens/internal/store"
)

// ConnectRequest is what the Connect form sends: either a whole URL, or the
// separate fields.
type ConnectRequest struct {
	URL      string `json:"url"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
	SSLMode  string `json:"sslMode"`
	Schemas  string `json:"schemas"` // comma-separated; empty means all
}

// Database is an open, read-only connection the watcher can read from.
type Database struct {
	Load        func(context.Context) (*schema.Schema, error)
	Fingerprint func(context.Context) (string, error)
	Close       func()
	Source      schema.Source
}

// Opener connects to the database a ConnectRequest describes. main provides
// it, which keeps this package free of any particular database driver.
type Opener func(context.Context, ConnectRequest) (*Database, error)

// Options configures a Server.
type Options struct {
	UI         fs.FS
	Open       Opener       // nil: connecting from the page is turned off
	Store      *store.Store // nil: saving graphs is turned off
	WatchEvery time.Duration
	StatsEvery time.Duration
	// LocalOnly refuses requests whose Host isn't localhost. Set it when
	// listening on a loopback address; it stops DNS-rebinding attacks.
	LocalOnly bool
}

// Server owns what the UI is looking at and swaps it on request.
type Server struct {
	opts Options
	hub  *Hub
	life context.Context // watchers run until this is cancelled

	mu          sync.Mutex // held while switching sources
	db          *Database
	watcher     *Watcher
	stopWatcher func()
	layout      *store.Graph // the saved graph on screen, for its table positions
	sources     int          // numbers each source, for SourceInfo.Key
}

// New makes a server showing nothing yet. Watchers it starts stop when life
// is cancelled.
func New(life context.Context, opts Options) *Server {
	return &Server{opts: opts, hub: NewHub(ModeNone), life: life}
}

// Hub gives access to the current schema and status.
func (s *Server) Hub() *Hub { return s.hub }

// Handler returns the HTTP handler for the UI and the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(s.opts.UI))
	mux.HandleFunc("GET /api/schema", s.handleSchema)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/export/mermaid", s.handleMermaid)
	mux.HandleFunc("POST /api/connect", s.handleConnect)
	mux.HandleFunc("POST /api/disconnect", s.handleDisconnect)
	mux.HandleFunc("GET /api/layout", s.handleLayout)
	mux.HandleFunc("GET /api/graphs", s.handleListGraphs)
	mux.HandleFunc("POST /api/graphs", s.handleSaveGraph)
	mux.HandleFunc("POST /api/graphs/{id}/open", s.handleOpenGraph)
	mux.HandleFunc("DELETE /api/graphs/{id}", s.handleDeleteGraph)
	return guard(mux, s.opts.LocalOnly)
}

// --- switching sources ---------------------------------------------------------

// Connect opens a database, reads its schema and starts watching it. The
// graph on screen is only replaced once the first read has worked, so a
// wrong password or a missing permission leaves it alone.
func (s *Server) Connect(ctx context.Context, req ConnectRequest) error {
	if s.opts.Open == nil {
		return errors.New("connecting from the page is turned off")
	}
	db, err := s.opts.Open(ctx, req)
	if err != nil {
		return err
	}
	w := &Watcher{
		Load: db.Load, Fingerprint: db.Fingerprint, Hub: s.hub,
		WatchEvery: s.opts.WatchEvery, StatsEvery: s.opts.StatsEvery,
	}
	first, err := w.Prime(ctx)
	if err != nil {
		db.Close()
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()

	src := db.Source
	if err := s.hub.Replace(first, ModeLive, &SourceInfo{Key: s.nextKeyLocked(), Label: src.Label(), Conn: &src}); err != nil {
		db.Close()
		return err
	}
	s.db, s.watcher = db, w

	watchCtx, cancel := context.WithCancel(s.life)
	done := make(chan struct{})
	go func() {
		w.Run(watchCtx)
		close(done)
	}()
	s.stopWatcher = func() {
		cancel()
		<-done
		w.Stop()
	}
	log.Printf("schemalens: connected to %s (%d tables)", src.Label(), len(first.Tables))
	return nil
}

// Show displays a schema that isn't live: a snapshot file or a saved graph.
func (s *Server) Show(sc *schema.Schema, label string, saved *store.Graph) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()

	src := &SourceInfo{Key: s.nextKeyLocked(), Label: label}
	if saved != nil {
		src.Conn, src.SavedID, src.SavedAt = &saved.Source, saved.ID, saved.SavedAt
		s.layout = saved
	}
	return s.hub.Replace(sc, ModeSnapshot, src)
}

// Disconnect closes the database and goes back to the Connect screen.
func (s *Server) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	s.hub.Replace(nil, ModeNone, nil)
}

// Close stops watching and closes the database, for shutdown.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

// stopLocked stops the watcher and closes the database, if any. Once it
// returns nothing from the old source can reach the Hub. The caller holds s.mu.
func (s *Server) stopLocked() {
	if s.stopWatcher != nil {
		s.stopWatcher()
		s.stopWatcher = nil
	}
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
	s.watcher = nil
	s.layout = nil
}

func (s *Server) nextKeyLocked() string {
	s.sources++
	return "source-" + strconv.Itoa(s.sources)
}

// --- handlers --------------------------------------------------------------------

// handleSchema returns the current schema as JSON. With ?refresh=1 it
// re-reads a live database first, for anyone who doesn't want to wait for
// the watcher.
func (s *Server) handleSchema(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" {
		s.mu.Lock()
		watcher := s.watcher
		s.mu.Unlock()
		if watcher != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			if err := watcher.Reload(ctx); err != nil {
				// Still answer with the last good schema; the status says why
				// it may be stale.
				log.Printf("schemalens: refresh failed: %v", err)
				s.hub.Failed(err)
			}
		}
	}

	_, body, status := s.hub.Current()
	if body == nil {
		writeError(w, http.StatusServiceUnavailable, "not connected to a database")
		return
	}
	// The page compares these with what /api/events announces: the version
	// to know it's up to date, the source to know it's a different graph.
	w.Header().Set("X-Schema-Version", strconv.Itoa(status.Version))
	if status.Source != nil {
		w.Header().Set("X-Schema-Source", status.Source.Key)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(body)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.hub.Status())
}

// handleMermaid returns the current schema as a Mermaid erDiagram, so it is
// always as live as the graph.
func (s *Server) handleMermaid(w http.ResponseWriter, r *http.Request) {
	current, _, _ := s.hub.Current()
	if current == nil {
		writeError(w, http.StatusServiceUnavailable, "not connected to a database")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="schema.mmd"`)
	}
	if err := render.Mermaid(w, current); err != nil {
		log.Printf("schemalens: rendering mermaid: %v", err)
	}
}

// handleConnect connects to the database in the form. The password is used
// for this connection and kept in memory only: never logged, saved, or sent
// back.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req ConnectRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" && (req.Host == "" || req.Database == "" || req.User == "") {
		writeError(w, http.StatusBadRequest, "enter a connection URL, or host, database and user")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.Connect(ctx, req); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.hub.Status())
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	s.Disconnect()
	writeJSON(w, http.StatusOK, s.hub.Status())
}

// --- helpers ---------------------------------------------------------------------

// maxBody is plenty for a connection form or a layout of thousands of tables.
const maxBody = 8 << 20

// readJSON decodes the request body, answering 400 itself if it can't.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("schemalens: writing response: %v", err)
	}
}

// writeError answers {"error": "..."} so the page can show the message.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
