package api

import (
	"net/http"

	"github.com/sanat-19/schema-lens/backend/models"
)

// Saved graphs: the schema on screen plus where its tables sit, kept on disk
// so it can be opened again later, without the database.

func (h *Handlers) ListGraphs(w http.ResponseWriter, r *http.Request) {
	list, err := h.session.SavedGraphs()
	if err != nil {
		writeFailure(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) SaveGraph(w http.ResponseWriter, r *http.Request) {
	var req models.SaveGraphRequest
	if !readJSON(w, r, &req) {
		return
	}
	sum, err := h.session.Save(req)
	if err != nil {
		writeFailure(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, sum)
}

func (h *Handlers) OpenGraph(w http.ResponseWriter, r *http.Request) {
	if err := h.session.OpenSaved(r.PathValue("id")); err != nil {
		writeFailure(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.session.Status())
}

func (h *Handlers) DeleteGraph(w http.ResponseWriter, r *http.Request) {
	if err := h.session.DeleteSaved(r.PathValue("id")); err != nil {
		writeFailure(w, err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) Layout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.session.Layout())
}
