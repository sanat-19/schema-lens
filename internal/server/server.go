// Package server serves the web UI and the JSON API it reads from.
package server

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/sanat-19/schema-lens/internal/render"
)

// New returns the HTTP handler for the UI and the API.
//
// reload re-reads the database on demand (the Refresh button). It is nil
// when serving a snapshot, in which case a refresh returns the snapshot
// unchanged.
func New(hub *Hub, ui fs.FS, reload func(context.Context) error) http.Handler {
	s := &server{hub: hub, reload: reload}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(ui))
	mux.HandleFunc("GET /api/schema", s.schema)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/export/mermaid", s.mermaid)
	return mux
}

type server struct {
	hub    *Hub
	reload func(context.Context) error
}

// schema returns the current schema as JSON. With ?refresh=1 it re-reads
// the database first, for anyone who doesn't want to wait for the watcher.
func (s *server) schema(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" && s.reload != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.reload(ctx); err != nil {
			// Still answer with the last good schema; the status says why
			// it may be stale.
			log.Printf("schemalens: refresh failed: %v", err)
			s.hub.Failed(err)
		}
	}

	_, body := s.hub.Current()
	if body == nil {
		http.Error(w, "schema not loaded yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(body)
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.hub.Status())
}

// mermaid returns the current schema as a Mermaid erDiagram, so it is always
// as live as the graph.
func (s *server) mermaid(w http.ResponseWriter, r *http.Request) {
	current, _ := s.hub.Current()
	if current == nil {
		http.Error(w, "schema not loaded yet", http.StatusServiceUnavailable)
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
