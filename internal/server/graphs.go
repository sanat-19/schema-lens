package server

import (
	"errors"
	"log"
	"net/http"

	"github.com/sanat-19/schema-lens/internal/store"
)

// Saved graphs: the schema on screen plus where its tables sit, kept on disk
// so it can be opened again later, without the database.

// saveRequest is what the Save button sends. The schema itself is taken from
// the server, not the page, so what's saved is exactly what was read.
type saveRequest struct {
	Name        string                    `json:"name"`
	Positions   map[string]store.Position `json:"positions"`
	ShowColumns bool                      `json:"showColumns"`
}

func (s *Server) handleListGraphs(w http.ResponseWriter, r *http.Request) {
	if !s.canSave(w) {
		return
	}
	list, err := s.opts.Store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSaveGraph(w http.ResponseWriter, r *http.Request) {
	if !s.canSave(w) {
		return
	}
	var req saveRequest
	if !readJSON(w, r, &req) {
		return
	}
	current, _, status := s.hub.Current()
	if current == nil {
		writeError(w, http.StatusConflict, "nothing to save: connect to a database first")
		return
	}

	g := store.Graph{
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("schemalens: saved graph %q", sum.Name)
	writeJSON(w, http.StatusCreated, sum)
}

// handleOpenGraph shows a saved graph. It's a snapshot, so the live database
// (if any) is disconnected; the page offers to reconnect.
func (s *Server) handleOpenGraph(w http.ResponseWriter, r *http.Request) {
	if !s.canSave(w) {
		return
	}
	g, err := s.opts.Store.Get(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Show(g.Schema, g.Name, g); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.hub.Status())
}

func (s *Server) handleDeleteGraph(w http.ResponseWriter, r *http.Request) {
	if !s.canSave(w) {
		return
	}
	err := s.opts.Store.Delete(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// layoutResponse tells the page where to put the tables of a saved graph.
// Source says which graph the positions belong to, in case the page asks
// just as the source changes.
type layoutResponse struct {
	Source      string                    `json:"source"`
	Positions   map[string]store.Position `json:"positions,omitempty"`
	ShowColumns *bool                     `json:"showColumns,omitempty"`
}

func (s *Server) handleLayout(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	layout := s.layout
	s.mu.Unlock()

	resp := layoutResponse{}
	if src := s.hub.Status().Source; src != nil {
		resp.Source = src.Key
	}
	if layout != nil {
		resp.Positions = layout.Positions
		resp.ShowColumns = &layout.ShowColumns
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) canSave(w http.ResponseWriter) bool {
	if s.opts.Store == nil {
		writeError(w, http.StatusNotFound, "saving graphs is turned off")
		return false
	}
	return true
}
