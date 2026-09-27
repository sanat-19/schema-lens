// Package connect turns what the Connect form sends into an open, read-only
// Postgres connection the session can watch.
package connect

import (
	"context"
	"strings"

	"github.com/sanat-19/schema-lens/backend/models"
	"github.com/sanat-19/schema-lens/backend/pkg/analyze"
	"github.com/sanat-19/schema-lens/backend/pkg/graph"
	"github.com/sanat-19/schema-lens/backend/pkg/postgres"
	"github.com/sanat-19/schema-lens/backend/pkg/session"
)

// Postgres builds the URL if the form sent separate fields, connects
// read-only, and hands back what the watcher needs. Every read comes with
// its relations and findings worked out.
func Postgres(ctx context.Context, req models.ConnectRequest) (*session.Database, error) {
	dsn := req.URL
	if dsn == "" {
		var err error
		dsn, err = postgres.BuildDSN(postgres.Fields{
			Host: req.Host, Port: req.Port, Database: req.Database,
			User: req.User, Password: req.Password, SSLMode: req.SSLMode,
		})
		if err != nil {
			return nil, err
		}
	}
	schemas := splitList(req.Schemas)
	source, err := postgres.Describe(dsn, schemas)
	if err != nil {
		return nil, err
	}
	pool, err := postgres.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	introspector := postgres.NewIntrospector(pool, schemas)
	return &session.Database{
		Load: func(ctx context.Context) (*models.Schema, error) {
			s, err := introspector.Introspect(ctx)
			if err != nil {
				return nil, err
			}
			s.Relations = graph.Relations(s)
			s.Findings = analyze.Findings(s)
			return s, nil
		},
		Fingerprint: introspector.Fingerprint,
		Close:       pool.Close,
		Source:      source,
	}, nil
}

// splitList turns "public, billing" into ["public", "billing"].
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
