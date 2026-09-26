package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sanat-19/schema-lens/internal/postgres"
	"github.com/sanat-19/schema-lens/internal/schema"
)

// runExport prints the schema once, as JSON or Mermaid.
func runExport(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("export", stderr)
	var db dbFlags
	db.register(fs)
	format := fs.String("format", "json", "output format: json or mermaid")
	out := fs.String("o", "", "write to this file instead of stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *format != "json" {
		return usagef("unknown --format %q (want json)", *format)
	}

	s, err := loadFromDatabase(db)
	if err != nil {
		return err
	}
	return writeOutput(*out, stdout, func(w io.Writer) error {
		return schema.WriteJSON(w, s)
	})
}

func runSnapshot(args []string, stderr io.Writer) error {
	return usagef("snapshot is not built yet")
}

func runServe(args []string, stderr io.Writer) error {
	return usagef("serve is not built yet")
}

// loadFromDatabase connects, reads the schema once, and disconnects.
func loadFromDatabase(db dbFlags) (*schema.Schema, error) {
	dsn, schemas, err := db.connection()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	pool, err := postgres.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer pool.Close()

	return postgres.NewIntrospector(pool, schemas).Introspect(ctx)
}

// writeOutput sends output to a file if one was named, else to stdout.
func writeOutput(path string, stdout io.Writer, write func(io.Writer) error) error {
	if path == "" {
		return write(stdout)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
