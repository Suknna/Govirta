package controller

import (
	"context"
	"sync"
)

// EventSource is the watch backend the Manager pulls change events from. Watch
// opens a stream of Events for one kind, optionally resuming after a known
// revision, and returns a channel the caller ranges over. The source owns the
// channel and must close it when the underlying stream ends (e.g. the server
// hangs up) or when ctx is cancelled; the framework reconnects by calling Watch
// again. Watch is kind-agnostic: it never parses the Object bytes. Task 4's
// watchclient implements this against the master's HTTP watch contract; tests
// supply a fake.
type EventSource interface {
	// Watch streams Events for kind, resuming after startRevision (empty means
	// "from the current state"). The returned channel is closed by the source
	// when the stream ends or ctx is cancelled.
	Watch(ctx context.Context, kind string, startRevision string) (<-chan Event, error)
}

// Manager hosts a fixed set of Controllers, one reconcile loop each, all fed
// from a shared EventSource. Run starts every loop and blocks until ctx is
// cancelled, at which point all loops drain and stop with no leaked goroutines.
type Manager struct {
	source      EventSource
	controllers []Controller
}

// NewManager returns a Manager that drives the given controllers off source.
func NewManager(source EventSource, controllers []Controller) *Manager {
	return &Manager{source: source, controllers: controllers}
}

// Run starts one loop per controller and blocks until ctx is cancelled. Each
// loop runs in its own goroutine joined via WaitGroup, so Run returns only once
// every loop (and its feeder) has fully stopped — no fire-and-forget goroutines
// outlive Run. The normal stop path returns ctx.Err(); if a controller's feeder
// fails to watch (a non-recoverable source error), that error is surfaced
// instead, wrapped by runController. The first loop failure cancels the shared
// run context so the remaining loops, their feeds, and pending delayed requeues
// all stop before Run returns.
func (m *Manager) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, len(m.controllers))

	for _, c := range m.controllers {
		wg.Add(1)
		go func(c Controller) {
			defer wg.Done()
			if err := m.runController(runCtx, c); err != nil {
				errs <- err
				cancel()
			}
		}(c)
	}

	wg.Wait()
	close(errs)

	// Prefer a real controller failure over the (expected) cancellation error.
	if err := <-errs; err != nil {
		return err
	}
	return ctx.Err()
}
