package models

import "time"

// The modes the UI can be in.
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
	Key     string    `json:"key"`
	Label   string    `json:"label"`          // "shop on localhost:5432", or a saved graph's name
	Conn    *Source   `json:"conn,omitempty"` // where it came from; never a password
	SavedID string    `json:"savedId,omitempty"`
	SavedAt time.Time `json:"savedAt,omitzero"`
}

// Event is one message to the browsers: "schema" when a new version is
// ready to fetch, "status" when the connection state changes.
type Event struct {
	Name   string
	Status Status
}
