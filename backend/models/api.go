package models

// Bodies of the requests the page sends and the responses it gets back that
// aren't one of the types above.

// ConnectRequest is what the Connect form sends: either a whole URL, or the
// separate fields.
type ConnectRequest struct {
	URL      string `json:"url"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
	SSLMode  string `json:"sslMode"`
	Schemas  string `json:"schemas"` // comma-separated; empty means all
}

// SaveGraphRequest is what the Save button sends. The schema itself is
// taken from the server, not the page, so what's saved is exactly what was
// read.
type SaveGraphRequest struct {
	Name        string              `json:"name"`
	Positions   map[string]Position `json:"positions"`
	ShowColumns bool                `json:"showColumns"`
}

// LayoutResponse tells the page where to put the tables of a saved graph.
// Source says which graph the positions belong to, in case the page asks
// just as the source changes.
type LayoutResponse struct {
	Source      string              `json:"source"`
	Positions   map[string]Position `json:"positions,omitempty"`
	ShowColumns *bool               `json:"showColumns,omitempty"`
}
