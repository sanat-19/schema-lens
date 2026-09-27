package session

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/sanat-19/schema-lens/backend/models"
)

// Watcher keeps the Hub in step with a live database.
//
// Every WatchEvery it asks the database for a fingerprint of the schema's
// structure: one small query. Only when the fingerprint changes does it read
// the whole schema again. Statistics (row counts, sizes, index scans) don't
// change the fingerprint, so every StatsEvery it re-reads anyway, and the Hub
// only tells browsers if something actually moved.
type Watcher struct {
	Load        func(context.Context) (*models.Schema, error)
	Fingerprint func(context.Context) (string, error)
	Hub         *Hub
	WatchEvery  time.Duration
	StatsEvery  time.Duration

	mu        sync.Mutex // one reload at a time
	lastPrint string
	lastLoad  time.Time
	stopped   bool // after Stop, nothing more is published
}

// Prime does the first read without publishing it, and returns the schema.
// Connecting uses it so that a database we can't read never replaces the
// graph on screen.
func (w *Watcher) Prime(ctx context.Context) (*models.Schema, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	fp, err := w.Fingerprint(ctx)
	if err != nil {
		return nil, err
	}
	s, err := w.Load(ctx)
	if err != nil {
		return nil, err
	}
	w.lastPrint, w.lastLoad = fp, time.Now()
	return s, nil
}

// Stop makes sure this watcher never publishes again. A reload that is
// already running finishes first, so once Stop returns the Hub is ours to
// switch to another source.
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
}

// maxBackoff caps how long we wait between tries while the database is down.
const maxBackoff = 30 * time.Second

// Run checks for changes until ctx is cancelled. When the database can't be
// reached it keeps the last good schema and retries, waiting longer each
// time, up to maxBackoff.
func (w *Watcher) Run(ctx context.Context) {
	wait := w.WatchEvery
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		if err := w.check(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			w.Hub.Failed(err)
			wait = min(wait*2, maxBackoff)
			log.Printf("schemalens: database check failed, retrying in %s: %v", wait, err)
			continue
		}
		w.Hub.Checked()
		wait = w.WatchEvery
	}
}

// check reloads if the structure changed or the stats are due.
func (w *Watcher) check(ctx context.Context) error {
	fp, err := w.Fingerprint(ctx)
	if err != nil {
		return err
	}

	w.mu.Lock()
	changed := fp != w.lastPrint
	statsDue := time.Since(w.lastLoad) >= w.StatsEvery
	w.mu.Unlock()

	if changed || statsDue {
		return w.reload(ctx, fp)
	}
	return nil
}

// Reload reads the schema now, whatever the fingerprint says. It backs the
// UI's Refresh button.
func (w *Watcher) Reload(ctx context.Context) error {
	fp, err := w.Fingerprint(ctx)
	if err != nil {
		return err
	}
	return w.reload(ctx, fp)
}

// reload reads the whole schema and hands it to the Hub.
//
// The fingerprint was taken before the load. If a migration lands in
// between, the load already includes it and the next check sees a new
// fingerprint and loads once more; nothing is missed.
func (w *Watcher) reload(ctx context.Context, fp string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return nil // the UI has moved on to another source
	}

	s, err := w.Load(ctx)
	if err != nil {
		return err
	}
	changed, err := w.Hub.Publish(s)
	if err != nil {
		return err
	}
	if changed && w.lastPrint != "" {
		log.Printf("schemalens: schema updated (%d tables, %d findings)", len(s.Tables), len(s.Findings))
	}
	w.lastPrint = fp
	w.lastLoad = time.Now()
	return nil
}
