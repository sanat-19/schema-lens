// Package session decides what the UI is looking at (a live database, a
// saved graph, or a snapshot file) and swaps it on request. The api package
// calls it; it knows nothing about HTTP.
package session

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sanat-19/schema-lens/backend/models"
	"github.com/sanat-19/schema-lens/backend/pkg/store"
)

// Errors the api package turns into HTTP status codes.
var (
	ErrConnectOff   = errors.New("connecting from the page is turned off")
	ErrSavingOff    = errors.New("saving graphs is turned off")
	ErrNotConnected = errors.New("not connected to a database")
	ErrNothingSaved = errors.New("nothing to save: connect to a database first")
	ErrNotEnough    = errors.New("enter a connection URL, or host, database and user")
)

// Database is an open, read-only connection the watcher can read from.
type Database struct {
	Load        func(context.Context) (*models.Schema, error)
	Fingerprint func(context.Context) (string, error)
	Close       func()
	Source      models.Source
}

// Opener connects to the database a ConnectRequest describes. main provides
// it, which keeps this package free of any particular database driver.
type Opener func(context.Context, models.ConnectRequest) (*Database, error)

// Options configures a Session.
type Options struct {
	Open       Opener       // nil: connecting from the page is turned off
	Store      *store.Store // nil: saving graphs is turned off
	WatchEvery time.Duration
	StatsEvery time.Duration
}

// Session owns what the UI is looking at and swaps it on request.
type Session struct {
	opts Options
	hub  *Hub
	life context.Context // watchers run until this is cancelled

	mu          sync.Mutex // held while switching sources
	db          *Database
	watcher     *Watcher
	stopWatcher func()
	layout      *models.SavedGraph // the saved graph on screen, for its table positions
	sources     int                // numbers each source, for SourceInfo.Key
}

// New makes a session showing nothing yet. Watchers it starts stop when life
// is cancelled.
func New(life context.Context, opts Options) *Session {
	return &Session{opts: opts, hub: NewHub(models.ModeNone), life: life}
}

// Hub gives access to the current schema and status.
func (s *Session) Hub() *Hub { return s.hub }

// Status is the current status, as the sidebar shows it.
func (s *Session) Status() models.Status { return s.hub.Status() }

// --- switching sources ---------------------------------------------------------

// Connect opens a database, reads its schema and starts watching it. The
// graph on screen is only replaced once the first read has worked, so a
// wrong password or a missing permission leaves it alone.
func (s *Session) Connect(ctx context.Context, req models.ConnectRequest) error {
	if s.opts.Open == nil {
		return ErrConnectOff
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" && (req.Host == "" || req.Database == "" || req.User == "") {
		return ErrNotEnough
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
	if err := s.hub.Replace(first, models.ModeLive, &models.SourceInfo{Key: s.nextKeyLocked(), Label: src.Label(), Conn: &src}); err != nil {
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
func (s *Session) Show(sc *models.Schema, label string, saved *models.SavedGraph) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()

	src := &models.SourceInfo{Key: s.nextKeyLocked(), Label: label}
	if saved != nil {
		src.Conn, src.SavedID, src.SavedAt = &saved.Source, saved.ID, saved.SavedAt
		s.layout = saved
	}
	return s.hub.Replace(sc, models.ModeSnapshot, src)
}

// Disconnect closes the database and goes back to the Connect screen.
func (s *Session) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	s.hub.Replace(nil, models.ModeNone, nil)
}

// Close stops watching and closes the database, for shutdown.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

// stopLocked stops the watcher and closes the database, if any. Once it
// returns nothing from the old source can reach the Hub. The caller holds s.mu.
func (s *Session) stopLocked() {
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

func (s *Session) nextKeyLocked() string {
	s.sources++
	return "source-" + strconv.Itoa(s.sources)
}

// --- reading what's on screen ------------------------------------------------------

// Refresh re-reads a live database now, for anyone who doesn't want to wait
// for the watcher. It does nothing for a snapshot. On failure the last good
// schema stays, and the status says why it may be stale.
func (s *Session) Refresh(ctx context.Context) {
	s.mu.Lock()
	watcher := s.watcher
	s.mu.Unlock()
	if watcher == nil {
		return
	}
	if err := watcher.Reload(ctx); err != nil {
		log.Printf("schemalens: refresh failed: %v", err)
		s.hub.Failed(err)
	}
}

// Current is the schema on screen, or ErrNotConnected.
func (s *Session) Current() (*models.Schema, error) {
	current, _, _ := s.hub.Current()
	if current == nil {
		return nil, ErrNotConnected
	}
	return current, nil
}

// Layout is where the tables of the saved graph on screen go, if it is one.
func (s *Session) Layout() models.LayoutResponse {
	s.mu.Lock()
	layout := s.layout
	s.mu.Unlock()

	resp := models.LayoutResponse{}
	if src := s.hub.Status().Source; src != nil {
		resp.Source = src.Key
	}
	if layout != nil {
		resp.Positions = layout.Positions
		resp.ShowColumns = &layout.ShowColumns
	}
	return resp
}

// --- saved graphs --------------------------------------------------------------------

// SavedGraphs lists the saved graphs, newest first.
func (s *Session) SavedGraphs() ([]models.SavedGraphSummary, error) {
	if s.opts.Store == nil {
		return nil, ErrSavingOff
	}
	return s.opts.Store.List()
}

// Save saves the schema on screen, with where the page has put its tables.
func (s *Session) Save(req models.SaveGraphRequest) (models.SavedGraphSummary, error) {
	if s.opts.Store == nil {
		return models.SavedGraphSummary{}, ErrSavingOff
	}
	current, _, status := s.hub.Current()
	if current == nil {
		return models.SavedGraphSummary{}, ErrNothingSaved
	}
	g := models.SavedGraph{
		Name:        req.Name,
		Positions:   req.Positions,
		ShowColumns: req.ShowColumns,
		Schema:      current,
	}
	if status.Source != nil && status.Source.Conn != nil {
		g.Source = *status.Source.Conn // host, database, user; never a password
	}
	sum, err := s.opts.Store.Save(g)
	if err != nil {
		return models.SavedGraphSummary{}, err
	}
	log.Printf("schemalens: saved graph %q", sum.Name)
	return sum, nil
}

// OpenSaved shows a saved graph. It's a snapshot, so the live database (if
// any) is disconnected; the page offers to reconnect.
func (s *Session) OpenSaved(id string) error {
	if s.opts.Store == nil {
		return ErrSavingOff
	}
	g, err := s.opts.Store.Get(id)
	if err != nil {
		return err
	}
	return s.Show(g.Schema, g.Name, g)
}

// DeleteSaved deletes a saved graph.
func (s *Session) DeleteSaved(id string) error {
	if s.opts.Store == nil {
		return ErrSavingOff
	}
	return s.opts.Store.Delete(id)
}
