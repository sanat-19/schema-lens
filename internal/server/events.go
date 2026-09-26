package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// keepAlive is how often an idle event stream sends a comment line, so
// proxies and browsers don't decide the connection is dead.
const keepAlive = 15 * time.Second

// events streams changes to the browser with Server-Sent Events.
//
// We only say *that* something changed ("schema", version 7), not what; the
// browser then fetches /api/schema. That keeps events tiny, and a browser
// that missed a few versions just fetches the latest one.
//
// SSE rather than WebSockets because data only flows one way, it's plain
// HTTP, and the browser's EventSource reconnects on its own.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: don't hold events back

	events, stop := s.hub.Subscribe()
	defer stop()

	ticker := time.NewTicker(keepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		case e := <-events:
			data, err := json.Marshal(e.Status)
			if err != nil {
				log.Printf("schemalens: encoding event: %v", err)
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Name, data)
		}
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("schemalens: writing response: %v", err)
	}
}
