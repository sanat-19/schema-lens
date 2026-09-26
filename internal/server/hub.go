package server

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"

	"github.com/sanat-19/schema-lens/internal/schema"
)

// Hub holds the schema the UI is showing right now, and tells every
// connected browser when it changes.
//
// The watcher writes to it; HTTP handlers read from it. Readers always get a
// complete schema: a new one is swapped in whole, never edited in place.
type Hub struct {
	mu          sync.RWMutex
	current     *schema.Schema
	body        []byte // current, already encoded as JSON for /api/schema
	fingerprint []byte // current encoded without CapturedAt, to spot real changes
	status      Status
	subscribers map[chan Event]struct{}
}

// Status is what the sidebar shows: are we live, and when did the schema
// last change?
type Status struct {
	Mode      string    `json:"mode"`  // "live" (watching a database) or "snapshot"
	State     string    `json:"state"` // "live", "reconnecting" or "snapshot"
	Version   int       `json:"version"`
	ChangedAt time.Time `json:"changedAt"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// Event is one message to the browsers: "schema" when a new version is
// ready to fetch, "status" when the connection state changes.
type Event struct {
	Name   string
	Status Status
}

// NewHub starts in the given mode, with no schema yet.
func NewHub(mode string) *Hub {
	return &Hub{
		status:      Status{Mode: mode, State: mode},
		subscribers: map[chan Event]struct{}{},
	}
}

// Publish makes s the current schema, if it differs from what we have.
// CapturedAt alone doesn't count as a change, or every stats refresh would
// make every browser re-fetch. It reports whether anything changed.
func (h *Hub) Publish(s *schema.Schema) (bool, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return false, err
	}
	withoutTime := *s
	withoutTime.CapturedAt = time.Time{}
	fingerprint, err := json.Marshal(withoutTime)
	if err != nil {
		return false, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now().UTC()
	h.status.CheckedAt = now
	if bytes.Equal(fingerprint, h.fingerprint) {
		return false, nil
	}
	h.current, h.body, h.fingerprint = s, body, fingerprint
	h.status.Version++
	h.status.ChangedAt = now
	h.broadcast(Event{Name: "schema", Status: h.status})
	return true, nil
}

// Checked records a successful check that found nothing new, and clears
// any "reconnecting" state left from an earlier failure.
func (h *Hub) Checked() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status.CheckedAt = time.Now().UTC()
	h.setState(h.status.Mode, "")
}

// Failed records that the database couldn't be reached. The last good
// schema stays on screen; the UI shows that it may be out of date.
func (h *Hub) Failed(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setState("reconnecting", err.Error())
}

// setState changes the state and tells the browsers, but only if it changed.
// The caller holds h.mu.
func (h *Hub) setState(state, errText string) {
	if h.status.State == state && h.status.Error == errText {
		return
	}
	h.status.State, h.status.Error = state, errText
	h.broadcast(Event{Name: "status", Status: h.status})
}

// Current returns the schema, its JSON and its version, all from the same
// moment. The schema and JSON are nil before the first Publish.
func (h *Hub) Current() (s *schema.Schema, body []byte, version int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current, h.body, h.status.Version
}

// Status returns the current status.
func (h *Hub) Status() Status {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.status
}

// Subscribe returns a channel of events and a function to stop listening.
// The status at the time of subscribing is sent straight away, so a newly
// connected browser knows where things stand.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 8)

	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	ch <- Event{Name: "status", Status: h.status}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		delete(h.subscribers, ch)
		h.mu.Unlock()
	}
}

// broadcast sends an event to every subscriber. A browser that isn't keeping
// up misses the event rather than holding everyone else up; the next event
// carries the latest version anyway. The caller holds h.mu.
func (h *Hub) broadcast(e Event) {
	for ch := range h.subscribers {
		select {
		case ch <- e:
		default:
		}
	}
}
