// Command schemalens reads a PostgreSQL schema, works out how the tables
// relate, flags structural problems, and shows it all as a live graph.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Exit codes, so scripts can tell "you typed it wrong" from "the database
// said no".
const (
	exitOK      = 0
	exitUsage   = 1
	exitFailure = 2
)

const usage = `SchemaLens: see your PostgreSQL schema as a live relationship graph.

Usage:
  schemalens serve    --dsn <url> [--schemas public,billing] [--addr 127.0.0.1:8080] [--open]
  schemalens serve    --from snapshot.json
  schemalens snapshot --dsn <url> [--schemas ...] -o snapshot.json
  schemalens export   --dsn <url> [--schemas ...] --format json|mermaid [-o file]

The connection string can also come from the DATABASE_URL environment variable.
SchemaLens only ever reads: every session is read-only.

Run "schemalens <command> -h" for the flags of one command.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	var err error
	switch cmd, rest := args[0], args[1:]; cmd {
	case "export":
		err = runExport(rest, stdout, stderr)
	case "snapshot":
		err = runSnapshot(rest, stderr)
	case "serve":
		err = runServe(rest, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "schemalens: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}

	var ue usageError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "schemalens: %v\n", err)
		return exitUsage
	case errors.Is(err, errHelpShown):
		return exitOK
	default:
		fmt.Fprintf(stderr, "schemalens: %v\n", err)
		return exitFailure
	}
}

// usageError means the command line was wrong, as opposed to the database
// being unreachable. It maps to exit code 1.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error {
	return usageError{fmt.Sprintf(format, a...)}
}

// errHelpShown is returned when the user asked for -h; flag already printed it.
var errHelpShown = errors.New("help shown")
