// Package router maps each URL to the api handler that answers it and puts
// the guard in front of all of it. The page itself is served by the
// frontend's Vite server, which forwards /api here.
package router

import (
	"net/http"

	"github.com/sanat-19/schema-lens/backend/api"
)

// New returns the handler for the API. localOnly refuses requests whose Host isn't
// localhost; set it when listening on a loopback address, since it stops
// DNS-rebinding attacks.
func New(h *api.Handlers, localOnly bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/schema", h.Schema)
	mux.HandleFunc("GET /api/status", h.Status)
	mux.HandleFunc("GET /api/events", h.Events)
	mux.HandleFunc("GET /api/export/mermaid", h.Mermaid)
	mux.HandleFunc("POST /api/connect", h.Connect)
	mux.HandleFunc("POST /api/disconnect", h.Disconnect)
	mux.HandleFunc("GET /api/layout", h.Layout)
	mux.HandleFunc("GET /api/graphs", h.ListGraphs)
	mux.HandleFunc("POST /api/graphs", h.SaveGraph)
	mux.HandleFunc("POST /api/graphs/{id}/open", h.OpenGraph)
	mux.HandleFunc("DELETE /api/graphs/{id}", h.DeleteGraph)
	return guard(mux, localOnly)
}
