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
	"runtime"
	"syscall"
	"time"

	"github.com/sanat-19/schema-lens/internal/postgres"
	"github.com/sanat-19/schema-lens/internal/schema"
	"github.com/sanat-19/schema-lens/internal/server"
	"github.com/sanat-19/schema-lens/web"
)

// runServe starts the web UI. With --dsn it watches the database and the
// page follows every schema change. With --from it shows a saved snapshot.
func runServe(args []string, stderr io.Writer) error {
	fs := newFlagSet("serve", stderr)
	var db dbFlags
	db.register(fs)
	from := fs.String("from", "", "show a snapshot file instead of a live database")
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on")
	open := fs.Bool("open", false, "open the UI in your browser")
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

	var (
		hub    *server.Hub
		reload func(context.Context) error
		what   string
	)
	if *from != "" {
		s, err := schema.LoadSnapshot(*from)
		if err != nil {
			return usageError{err.Error()}
		}
		hub = server.NewHub("snapshot")
		if _, err := hub.Publish(s); err != nil {
			return err
		}
		what = fmt.Sprintf("snapshot of %s from %s", s.Database, s.CapturedAt.Format(time.DateTime))
	} else {
		dsn, schemas, err := db.connection()
		if err != nil {
			return err
		}
		pool, err := postgres.Connect(ctx, dsn)
		if err != nil {
			return err
		}
		defer pool.Close()

		introspector := postgres.NewIntrospector(pool, schemas)
		hub = server.NewHub("live")
		watcher := &server.Watcher{
			Load: func(ctx context.Context) (*schema.Schema, error) {
				s, err := introspector.Introspect(ctx)
				if err != nil {
					return nil, err
				}
				enrich(s)
				return s, nil
			},
			Fingerprint: introspector.Fingerprint,
			Hub:         hub,
			WatchEvery:  *watchEvery,
			StatsEvery:  *statsEvery,
		}

		// The first read has to work, or there's nothing to show.
		first, cancel := context.WithTimeout(ctx, time.Minute)
		err = watcher.Reload(first)
		cancel()
		if err != nil {
			return err
		}
		go watcher.Run(ctx)

		reload = watcher.Reload
		current, _, _ := hub.Current()
		what = fmt.Sprintf("live view of %s (%d tables), checking for changes every %s",
			current.Database, len(current.Tables), *watchEvery)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	url := "http://" + displayAddr(ln.Addr())
	fmt.Fprintf(stderr, "SchemaLens: %s\n", what)
	fmt.Fprintf(stderr, "Open %s  (Ctrl+C to stop)\n", url)
	if !isLoopback(ln.Addr()) {
		fmt.Fprintf(stderr, "Warning: listening on %s; anyone on your network can see this schema.\n", ln.Addr())
	}
	if *open {
		openBrowser(url)
	}

	srv := &http.Server{
		Handler:           server.New(hub, web.FS, reload),
		ReadHeaderTimeout: 10 * time.Second,
		// Tie every request to ctx, so open event streams end on Ctrl+C
		// instead of holding up the shutdown.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
