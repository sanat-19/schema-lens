package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"strings"
)

// dbFlags are the flags every command that talks to a database shares.
type dbFlags struct {
	dsn     string
	schemas string
}

func (f *dbFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.dsn, "dsn", "", "PostgreSQL connection string (default: $DATABASE_URL)")
	fs.StringVar(&f.schemas, "schemas", "", "comma-separated schemas to read (default: all non-system schemas)")
}

// connection returns the DSN to use and the schemas to read.
func (f *dbFlags) connection() (dsn string, schemas []string, err error) {
	dsn = f.dsn
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		return "", nil, usagef("no database: pass --dsn or set DATABASE_URL")
	}
	for _, s := range strings.Split(f.schemas, ",") {
		if s = strings.TrimSpace(s); s != "" {
			schemas = append(schemas, s)
		}
	}
	return dsn, schemas, nil
}

// newFlagSet makes a FlagSet that reports errors to us instead of exiting.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("schemalens "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parse wraps fs.Parse so -h and bad flags come back as the right error.
func parse(fs *flag.FlagSet, args []string) error {
	err := fs.Parse(args)
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
		return errHelpShown
	default:
		return usageError{err.Error()}
	}
	if fs.NArg() > 0 {
		return usagef("unexpected argument %q", fs.Arg(0))
	}
	return nil
}
