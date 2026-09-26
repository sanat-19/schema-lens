package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sanat-19/schema-lens/internal/analyze"
	"github.com/sanat-19/schema-lens/internal/graph"
	"github.com/sanat-19/schema-lens/internal/postgres"
	"github.com/sanat-19/schema-lens/internal/render"
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
	var write func(io.Writer, *schema.Schema) error
	switch *format {
	case "json":
		write = schema.WriteJSON
	case "mermaid":
		write = render.Mermaid
	default:
		return usagef("unknown --format %q (want json or mermaid)", *format)
	}

	s, err := loadFromDatabase(db)
	if err != nil {
		return err
	}
	return writeOutput(*out, stdout, func(w io.Writer) error { return write(w, s) })
}

// runSnapshot saves the schema to a file so it can be viewed later with
// "serve --from", without access to the database. A snapshot is the same
// JSON that "export --format json" prints, relations and findings included.
func runSnapshot(args []string, stderr io.Writer) error {
	fs := newFlagSet("snapshot", stderr)
	var db dbFlags
	db.register(fs)
	out := fs.String("o", "", "file to write the snapshot to (required)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *out == "" {
		return usagef("snapshot needs -o <file>")
	}

	s, err := loadFromDatabase(db)
	if err != nil {
		return err
	}
	if err := writeOutput(*out, nil, func(w io.Writer) error { return schema.WriteJSON(w, s) }); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Saved %d tables, %d relations and %d findings to %s\n",
		len(s.Tables), len(s.Relations), len(s.Findings), *out)
	return nil
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

	s, err := postgres.NewIntrospector(pool, schemas).Introspect(ctx)
	if err != nil {
		return nil, err
	}
	enrich(s)
	return s, nil
}

// enrich works out everything that follows from the raw schema: how the
// tables relate, and what's wrong with them. Snapshots already contain this.
func enrich(s *schema.Schema) {
	s.Relations = graph.Relations(s)
	s.Findings = analyze.Findings(s)
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
