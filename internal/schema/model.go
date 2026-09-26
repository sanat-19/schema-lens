// Package schema holds the picture of a database that the rest of SchemaLens
// works with. It knows nothing about Postgres: a reader fills it in, and the
// graph, the findings, the exports and the web UI only ever read it.
//
// Everything here is serialised to JSON as-is for the browser and for
// snapshots, so field names are part of the UI's contract.
package schema

import (
	"fmt"
	"time"
)

// Schema is everything we know about one database at one moment.
type Schema struct {
	Database      string     `json:"database"`
	ServerVersion string     `json:"serverVersion"`
	Schemas       []string   `json:"schemas"`
	CapturedAt    time.Time  `json:"capturedAt"`
	Tables        []*Table   `json:"tables"`
	Relations     []Relation `json:"relations"`
	Findings      []Finding  `json:"findings"`
}

// Table returns the table with the given ID ("schema.name"), or nil.
func (s *Schema) Table(id string) *Table {
	for _, t := range s.Tables {
		if t.ID() == id {
			return t
		}
	}
	return nil
}

// Table is one table. A partitioned table appears once, with its partitions'
// rows and sizes added up, because that's how people think about it.
type Table struct {
	Schema      string        `json:"schema"`
	Name        string        `json:"name"`
	Comment     string        `json:"comment,omitempty"`
	RowEstimate int64         `json:"rowEstimate"` // -1 means never analyzed
	TotalBytes  int64         `json:"totalBytes"`
	Partitioned bool          `json:"partitioned,omitempty"`
	Columns     []*Column     `json:"columns"`
	PrimaryKey  []string      `json:"primaryKey,omitempty"`
	Uniques     [][]string    `json:"uniques,omitempty"`
	ForeignKeys []*ForeignKey `json:"foreignKeys,omitempty"`
	Indexes     []*Index      `json:"indexes,omitempty"`
}

// ID is the key we use for a table everywhere, including in the browser.
func (t *Table) ID() string { return t.Schema + "." + t.Name }

// Column returns the column with the given name, or nil.
func (t *Table) Column(name string) *Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Column is one column of a table.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Default  string `json:"default,omitempty"`
	Comment  string `json:"comment,omitempty"`
	Nullable bool   `json:"nullable"`
	IsPK     bool   `json:"isPK,omitempty"`
	IsFK     bool   `json:"isFK,omitempty"`
	IsUnique bool   `json:"isUnique,omitempty"`
}

// ForeignKey is a FOREIGN KEY constraint as declared on the child table.
// The parent may live in a schema we didn't load; the FK is still recorded.
type ForeignKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete"`
	OnUpdate   string   `json:"onUpdate"`
}

// RefID is the ID of the table this FK points at.
func (fk *ForeignKey) RefID() string { return fk.RefSchema + "." + fk.RefTable }

// Index is one index on a table.
type Index struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"` // key columns only; expressions as their SQL text
	Include    []string `json:"include,omitempty"`
	Unique     bool     `json:"unique,omitempty"`
	Primary    bool     `json:"primary,omitempty"`
	Partial    bool     `json:"partial,omitempty"`
	Predicate  string   `json:"predicate,omitempty"` // the WHERE of a partial index
	Method     string   `json:"method"`              // btree, gin, gist, ...
	Definition string   `json:"definition"`
	Scans      int64    `json:"scans"` // -1 if the database doesn't tell us
	Bytes      int64    `json:"bytes"`
}

// Relation is an edge in the graph: the child table (From) points at the
// parent table (To).
type Relation struct {
	ID          string   `json:"id"`
	From        string   `json:"from"`
	FromCols    []string `json:"fromCols"`
	To          string   `json:"to"`
	ToCols      []string `json:"toCols"`
	Cardinality string   `json:"cardinality"` // ManyToOne or OneToOne
	Optional    bool     `json:"optional"`    // a child row may point at nothing
	Inferred    bool     `json:"inferred"`    // guessed from a column name, no FK constraint
	Name        string   `json:"name"`
}

const (
	ManyToOne = "many-to-one"
	OneToOne  = "one-to-one"
)

// Finding is a structural problem worth telling the developer about.
type Finding struct {
	Kind       string `json:"kind"`
	Severity   string `json:"severity"`
	Table      string `json:"table"`           // table ID
	Index      string `json:"index,omitempty"` // for index findings, which index
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Suggestion string `json:"suggestion,omitempty"` // SQL to copy; SchemaLens never runs it
}

const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
)

// Source says where a schema was read from, so a saved graph can say which
// database it shows and the UI can offer to reconnect. It never holds a
// password.
type Source struct {
	Host     string   `json:"host,omitempty"`
	Port     int      `json:"port,omitempty"`
	Database string   `json:"database,omitempty"`
	User     string   `json:"user,omitempty"`
	SSLMode  string   `json:"sslMode,omitempty"`
	Schemas  []string `json:"schemas,omitempty"`
}

// Label is a short human name like "shop on db.internal:5432".
func (s Source) Label() string {
	if s.Host == "" {
		return s.Database
	}
	return fmt.Sprintf("%s on %s:%d", s.Database, s.Host, s.Port)
}
