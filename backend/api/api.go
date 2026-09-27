// Package api is the HTTP side of SchemaLens: it reads what the page sends, calls pkg/session to do the work, and writes the
// answer back as JSON.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/sanat-19/schema-lens/backend/pkg/session"
	"github.com/sanat-19/schema-lens/backend/pkg/store"
)

// Handlers answers the page's requests. The router decides which request
// goes to which method.
type Handlers struct {
	session *session.Session
}

// New answers requests for sess.
func New(sess *session.Session) *Handlers {
	return &Handlers{session: sess}
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

// writeFailure answers with the status code that fits err. fallback is for
// errors the session doesn't name, such as a failed connection.
func writeFailure(w http.ResponseWriter, err error, fallback int) {
	code := fallback
	switch {
	case errors.Is(err, session.ErrNotEnough):
		code = http.StatusBadRequest
	case errors.Is(err, session.ErrSavingOff), errors.Is(err, store.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, session.ErrNotConnected):
		code = http.StatusServiceUnavailable
	case errors.Is(err, session.ErrNothingSaved):
		code = http.StatusConflict
	}
	writeError(w, code, err.Error())
}
