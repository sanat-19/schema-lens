package api

import (
	"context"
	"net/http"
	"time"

	"github.com/sanat-19/schema-lens/backend/models"
)

// Connect connects to the database in the form. The password is used
// for this connection and kept in memory only: never logged, saved, or sent
// back.
func (h *Handlers) Connect(w http.ResponseWriter, r *http.Request) {
	var req models.ConnectRequest
	if !readJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := h.session.Connect(ctx, req); err != nil {
		writeFailure(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, h.session.Status())
}

func (h *Handlers) Disconnect(w http.ResponseWriter, r *http.Request) {
	h.session.Disconnect()
	writeJSON(w, http.StatusOK, h.session.Status())
}
