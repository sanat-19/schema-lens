package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/sanat-19/schema-lens/internal/postgres"
	"github.com/sanat-19/schema-lens/internal/schema"
	"github.com/sanat-19/schema-lens/internal/server"
	"github.com/sanat-19/schema-lens/internal/store"
	"github.com/sanat-19/schema-lens/web"
)

// runServe starts the web UI.
//
//   - With --dsn (or $DATABASE_URL) it connects straight away and the page
//     follows every schema change.
//   - With --from it shows a snapshot file.
//   - With neither it opens on the Connect screen, where you can enter a
//     connection URL or credentials, or open a saved graph.
func runServe(args []string, stderr io.Writer) error {
	fs := newFlagSet("serve", stderr)
	var db dbFlags
	db.register(fs)
	from := fs.String("from", "", "show a snapshot file instead of a live database")
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on")
	open := fs.Bool("open", false, "open the UI in your browser")
	dataDir := fs.String("data-dir", "", "where saved graphs are kept (default: your config directory)")
	watchEvery := fs.Duration("watch-interval", 2*time.Second, "how often to check the database for schema changes")
	statsEvery := fs.Duration("stats-interval", 30*time.Second, "how often to refresh row counts, sizes and index usage")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *from != "" && db.dsn != "" {
		return usagef("use either --from or --dsn, not both")
	}
	if *watchEvery < 500*time.Millisecond {
		return usagef("--watch-interval must be at least 500ms")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}

	graphs, err := openStore(*dataDir)
	if err != nil {
		// Not fatal: everything but saving still works.
		fmt.Fprintf(stderr, "Warning: saving graphs is off: %v\n", err)
	}

	srv := server.New(ctx, server.Options{
		UI:         web.FS,
		Open:       openPostgres,
		Store:      graphs,
		WatchEvery: *watchEvery,
		StatsEvery: *statsEvery,
		LocalOnly:  isLoopback(ln.Addr()),
	})
	defer srv.Close()

	var what string
	switch dsn := db.dsnOrEnv(); {
	case *from != "":
		s, err := schema.LoadSnapshot(*from)
		if err != nil {
			return usageError{err.Error()}
		}
		if err := srv.Show(s, filepath.Base(*from), nil); err != nil {
			return err
		}
		what = fmt.Sprintf("snapshot of %s from %s", s.Database, s.CapturedAt.Format(time.DateTime))

	case dsn != "":
		// The first read has to work, or there's nothing to show.
		first, cancel := context.WithTimeout(ctx, time.Minute)
		err := srv.Connect(first, server.ConnectRequest{URL: dsn, Schemas: db.schemas})
		cancel()
		if err != nil {
			return err
		}
		current, _, _ := srv.Hub().Current()
		what = fmt.Sprintf("live view of %s (%d tables), checking for changes every %s",
			current.Database, len(current.Tables), *watchEvery)

	default:
		what = "no database yet: connect from the page"
	}

	url := "http://" + displayAddr(ln.Addr())
	fmt.Fprintf(stderr, "SchemaLens: %s\n", what)
	if graphs != nil {
		fmt.Fprintf(stderr, "Saved graphs: %s\n", graphs.Dir())
	}
	fmt.Fprintf(stderr, "Open %s  (Ctrl+C to stop)\n", url)
	if !isLoopback(ln.Addr()) {
		fmt.Fprintf(stderr, "Warning: listening on %s; anyone on your network can see this schema and use it to connect to databases.\n", ln.Addr())
	}
	if *open {
		openBrowser(url)
	}

	httpServer := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Tie every request to ctx, so open event streams end on Ctrl+C
		// instead of holding up the shutdown.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdown)
	}()
	if err := httpServer.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openPostgres is how the server connects when asked from the page (or by
// --dsn): build the URL if the form sent separate fields, then connect
// read-only and hand back what the watcher needs.
func openPostgres(ctx context.Context, req server.ConnectRequest) (*server.Database, error) {
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
	return &server.Database{
		Load: func(ctx context.Context) (*schema.Schema, error) {
			s, err := introspector.Introspect(ctx)
			if err != nil {
				return nil, err
			}
			enrich(s)
			return s, nil
		},
		Fingerprint: introspector.Fingerprint,
		Close:       pool.Close,
		Source:      source,
	}, nil
}

// openStore opens the saved-graphs directory.
func openStore(dir string) (*store.Store, error) {
	if dir == "" {
		var err error
		if dir, err = store.DefaultDir(); err != nil {
			return nil, err
		}
	}
	return store.Open(dir)
}

// displayAddr turns [::]:8080 or 0.0.0.0:8080 into something a browser can
// open.
func displayAddr(a net.Addr) string {
	tcp, ok := a.(*net.TCPAddr)
	if !ok {
		return a.String()
	}
	if tcp.IP.IsUnspecified() {
		return fmt.Sprintf("localhost:%d", tcp.Port)
	}
	return tcp.String()
}

func isLoopback(a net.Addr) bool {
	tcp, ok := a.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

// openBrowser does its best; if it fails, the URL is printed anyway.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
