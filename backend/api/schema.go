package api

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/sanat-19/schema-lens/backend/pkg/render"
)

// Schema returns the current schema as JSON. With ?refresh=1 it
// re-reads a live database first, for anyone who doesn't want to wait for
// the watcher.
func (h *Handlers) Schema(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		h.session.Refresh(ctx)
		cancel()
	}

	_, body, status := h.session.Hub().Current()
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

func (h *Handlers) Status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.session.Status())
}

// Mermaid returns the current schema as a Mermaid erDiagram, so it is
// always as live as the graph.
func (h *Handlers) Mermaid(w http.ResponseWriter, r *http.Request) {
	current, err := h.session.Current()
	if err != nil {
		writeFailure(w, err, http.StatusInternalServerError)
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
