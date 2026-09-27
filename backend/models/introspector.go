package models

import "context"

// Introspector reads a live database into a Schema. Postgres is the only
// implementation today; MySQL would be a second one.
//
// It only fills in what the database itself says: tables, columns, keys and
// indexes. Relations and findings are worked out afterwards from the model.
type Introspector interface {
	Introspect(ctx context.Context) (*Schema, error)
}
