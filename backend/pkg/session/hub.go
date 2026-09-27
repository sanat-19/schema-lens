package session

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"

	"github.com/sanat-19/schema-lens/backend/models"
)

// Hub holds the schema the UI is showing right now, and tells every
// connected browser when it changes.
//
// The watcher writes to it; HTTP handlers read from it. Readers always get a
// complete schema: a new one is swapped in whole, never edited in place.
type Hub struct {
	mu          sync.RWMutex
	current     *models.Schema
	body        []byte // current, already encoded as JSON for /api/schema
	fingerprint []byte // current encoded without CapturedAt, to spot real changes
	status      models.Status
	subscribers map[chan models.Event]struct{}
}

// NewHub starts in the given mode, with no schema yet.
func NewHub(mode string) *Hub {
	return &Hub{
		status:      models.Status{Mode: mode, State: mode},
		subscribers: map[chan models.Event]struct{}{},
	}
}

// Publish makes s the current schema, if it differs from what we have.
// CapturedAt alone doesn't count as a change, or every stats refresh would
// make every browser re-fetch. It reports whether anything changed.
func (h *Hub) Publish(s *models.Schema) (bool, error) {
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
	h.broadcast(models.Event{Name: "schema", Status: h.status})
	return true, nil
}

// Replace switches to a different source: another database, a saved graph,
// or nothing at all (s == nil). Unlike Publish it always counts as a new
// version, even if the new schema happens to look the same.
func (h *Hub) Replace(s *models.Schema, mode string, src *models.SourceInfo) error {
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
	h.status = models.Status{
		Mode:      mode,
		State:     mode,
		Version:   h.status.Version + 1,
		ChangedAt: now,
		CheckedAt: now,
		Source:    src,
	}
	h.broadcast(models.Event{Name: "schema", Status: h.status})
	return nil
}

// encode returns the JSON for /api/schema and the same without the capture
// time, which Publish compares to spot real changes.
func encode(s *models.Schema) (body, fingerprint []byte, err error) {
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
	h.broadcast(models.Event{Name: "status", Status: h.status})
}

// Current returns the schema, its JSON and the status, all from the same
// moment. The schema and JSON are nil when nothing is loaded.
func (h *Hub) Current() (s *models.Schema, body []byte, status models.Status) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current, h.body, h.status
}

// Status returns the current status.
func (h *Hub) Status() models.Status {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.status
}

// Subscribe returns a channel of events and a function to stop listening.
// The status at the time of subscribing is sent straight away, so a newly
// connected browser knows where things stand.
func (h *Hub) Subscribe() (<-chan models.Event, func()) {
	ch := make(chan models.Event, 8)

	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	ch <- models.Event{Name: "status", Status: h.status}
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
func (h *Hub) broadcast(e models.Event) {
	for ch := range h.subscribers {
		select {
		case ch <- e:
		default:
		}
	}
}
