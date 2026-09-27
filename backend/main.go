// Command schemalens is the SchemaLens API: the frontend's Vite server
// forwards /api here. It reads PostgreSQL schemas, works out how the tables
// relate, flags structural problems, and keeps the page up to date.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sanat-19/schema-lens/backend/api"
	"github.com/sanat-19/schema-lens/backend/pkg/connect"
	"github.com/sanat-19/schema-lens/backend/pkg/session"
	"github.com/sanat-19/schema-lens/backend/pkg/store"
	"github.com/sanat-19/schema-lens/backend/router"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	dataDir := flag.String("data-dir", "", "where saved graphs are kept (default: your config directory)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	graphs, err := openStore(*dataDir)
	if err != nil {
		// Not fatal: everything but saving still works.
		log.Printf("saving graphs is off: %v", err)
	} else {
		log.Printf("saved graphs: %s", graphs.Dir())
	}

	sess := session.New(ctx, session.Options{
		Open:       connect.Postgres,
		Store:      graphs,
		WatchEvery: 2 * time.Second,  // how often to check for schema changes
		StatsEvery: 30 * time.Second, // how often to refresh row counts and sizes
	})
	defer sess.Close()

	localOnly := router.IsLocalHost(*addr)
	server := &http.Server{
		Addr:              *addr,
		Handler:           router.New(api.New(sess), localOnly),
		ReadHeaderTimeout: 10 * time.Second,
		// Tie every request to ctx, so open event streams end on Ctrl+C
		// instead of holding up the shutdown.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()

	if !localOnly {
		log.Printf("warning: listening on %s; anyone on your network can see your schemas and use this to connect to databases", *addr)
	}
	log.Printf("SchemaLens API on http://%s  (Ctrl+C to stop)", *addr)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
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
