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

// The modes the hub can be in.
const (
	ModeNone     = "none"     // nothing loaded yet: the page shows the Connect screen
	ModeLive     = "live"     // watching a database
	ModeSnapshot = "snapshot" // showing a saved graph or a snapshot file
)

// Status is what the sidebar shows: what are we looking at, is it live, and
// when did it last change?
type Status struct {
	Mode      string      `json:"mode"`  // ModeNone, ModeLive or ModeSnapshot
	State     string      `json:"state"` // the mode, or "reconnecting" while a live database is unreachable
	Version   int         `json:"version"`
	ChangedAt time.Time   `json:"changedAt"`
	CheckedAt time.Time   `json:"checkedAt,omitzero"`
	Error     string      `json:"error,omitempty"`
	Source    *SourceInfo `json:"source,omitempty"`
}

// SourceInfo describes what's on screen. Key changes every time the source
// is replaced, so the page knows to start a fresh graph instead of patching
// the old one.
type SourceInfo struct {
	Key     string         `json:"key"`
	Label   string         `json:"label"`          // "shop on localhost:5432", or a saved graph's name
	Conn    *schema.Source `json:"conn,omitempty"` // where it came from; never a password
	SavedID string         `json:"savedId,omitempty"`
	SavedAt time.Time      `json:"savedAt,omitzero"`
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
	body, fingerprint, err := encode(s)
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

// Replace switches to a different source: another database, a saved graph,
// or nothing at all (s == nil). Unlike Publish it always counts as a new
// version, even if the new schema happens to look the same.
func (h *Hub) Replace(s *schema.Schema, mode string, src *SourceInfo) error {
	var body, fingerprint []byte
	if s != nil {
		var err error
		if body, fingerprint, err = encode(s); err != nil {
			return err
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now().UTC()
	h.current, h.body, h.fingerprint = s, body, fingerprint
	h.status = Status{
		Mode:      mode,
		State:     mode,
		Version:   h.status.Version + 1,
		ChangedAt: now,
		CheckedAt: now,
		Source:    src,
	}
	h.broadcast(Event{Name: "schema", Status: h.status})
	return nil
}

// encode returns the JSON for /api/schema and the same without the capture
// time, which Publish compares to spot real changes.
func encode(s *schema.Schema) (body, fingerprint []byte, err error) {
	if body, err = json.Marshal(s); err != nil {
		return nil, nil, err
	}
	withoutTime := *s
	withoutTime.CapturedAt = time.Time{}
	fingerprint, err = json.Marshal(withoutTime)
	return body, fingerprint, err
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

// Current returns the schema, its JSON and the status, all from the same
// moment. The schema and JSON are nil when nothing is loaded.
func (h *Hub) Current() (s *schema.Schema, body []byte, status Status) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current, h.body, h.status
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
