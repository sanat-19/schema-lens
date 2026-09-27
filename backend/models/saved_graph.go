package models

import "time"

// SavedGraph is one saved graph: the schema at the moment of saving plus
// where each table was on screen, so it reopens exactly as it was left. It
// records which database it came from, but never the password.
type SavedGraph struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	SavedAt     time.Time           `json:"savedAt"`
	Source      Source              `json:"source"`
	Positions   map[string]Position `json:"positions,omitempty"` // table ID → centre on screen
	ShowColumns bool                `json:"showColumns"`
	Schema      *Schema             `json:"schema"`
}

// Position is where a table sat on the graph.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// SavedGraphSummary is what the list of saved graphs shows, without the
// whole schema.
type SavedGraphSummary struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	SavedAt  time.Time `json:"savedAt"`
	Source   Source    `json:"source"`
	Tables   int       `json:"tables"`
	Findings int       `json:"findings"`
}

// Summary is g without its schema, for the list of saved graphs.
func (g *SavedGraph) Summary() SavedGraphSummary {
	return SavedGraphSummary{
		ID:       g.ID,
		Name:     g.Name,
		SavedAt:  g.SavedAt,
		Source:   g.Source,
		Tables:   len(g.Schema.Tables),
		Findings: len(g.Schema.Findings),
	}
}
