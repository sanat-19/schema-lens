// Package postgres reads a PostgreSQL database into the schema model.
// It is the only package in SchemaLens that talks to Postgres.
package postgres

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a small connection pool that cannot change the database.
//
// Every session starts with default_transaction_read_only on, so even a bug
// in SchemaLens can't write, and with a statement timeout, so a slow catalog
// query on a huge database gives up instead of hanging.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// pgx errors can echo the DSN back, password included.
		return nil, fmt.Errorf("invalid connection string %s", Redact(dsn))
	}

	params := cfg.ConnConfig.RuntimeParams
	params["default_transaction_read_only"] = "on"
	params["statement_timeout"] = "10s"
	params["application_name"] = "schemalens"

	// One connection for the watcher, one for a refresh, a couple spare.
	cfg.MaxConns = 4
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", Redact(dsn), err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to %s: %w", Redact(dsn), err)
	}
	return pool, nil
}

var keywordPassword = regexp.MustCompile(`(?i)(password\s*=\s*)('(?:[^'\\]|\\.)*'|\S+)`)

// Redact hides the password in a connection string so it can be printed.
// It handles both URL style (postgres://user:pw@host/db) and keyword style
// (host=... password=...).
func Redact(dsn string) string {
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "<unparseable connection string>"
		}
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
		q := u.Query()
		if q.Has("password") {
			q.Set("password", "xxxxx")
			u.RawQuery = q.Encode()
		}
		return u.String()
	}
	return keywordPassword.ReplaceAllString(dsn, "${1}xxxxx")
}
