package controller

import (
	"context"
	"fmt"
	"sync"
)

// runController drives one Controller: a feeder goroutine streams events from
// the source into a private Queue, while the reconcile loop (running in this
// goroutine) drains the Queue and calls Reconcile. The feeder is the only extra
// goroutine and is joined via WaitGroup, so runController returns with nothing
// left running. Shutdown is unified: whatever stops the feeder (ctx cancel or a
// non-recoverable watch error) also closes the Queue, which unblocks the
// reconcile loop's Get and lets it drain remaining work before exiting.
//
// runController returns nil on a clean ctx-driven stop, or a wrapped error if
// the EventSource could not be watched.
func (m *Manager) runController(ctx context.Context, c Controller) error {
	q := New()
	var wg sync.WaitGroup
	feederErr := make(chan error, 1)

	wg.Add(1)
	go func() {
		defer wg.Done()
		// Whatever ends the feeder — ctx cancellation or a watch error — must
		// close the Queue so the reconcile loop's blocking Get returns and the
		// loop exits. Without this, a feeder error would deadlock the loop.
		defer q.Shutdown()
		feederErr <- m.feed(ctx, c.Kind(), q)
	}()

	// Reconcile loop runs inline in this goroutine; it exits once q is shut
	// down (and drained).
	reconcileLoop(ctx, c, q)

	wg.Wait()
	if err := <-feederErr; err != nil {
		return err
	}
	return nil
}

// feed streams Events for kind from the source into q, reconnecting whenever the
// source closes the watch channel (the server hung up). It exits — returning nil
// — when ctx is cancelled, or a wrapped error when the source refuses to open a
// watch. There is no backoff between reconnects: this is the thinnest possible
// resume (项目 minimal scope); a real retry/backoff policy is out of this task.
//
// startRevision (lastRV) is the resume cursor handed to each Watch call. The
// framework is kind-agnostic and Event carries no ResourceVersion field, so this
// minimal implementation never advances lastRV: every (re)connect starts from
// the source's current state (lastRV stays ""). Extracting an RV from the object
// bytes to resume precisely belongs to the watchclient layer (Task 5), which can
// parse the concrete kind without leaking that knowledge into this framework.
func (m *Manager) feed(ctx context.Context, kind string, q *Queue) error {
	lastRV := ""
	for {
		// Stop before reconnecting if ctx is already done.
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		ch, err := m.source.Watch(ctx, kind, lastRV)
		if err != nil {
			return fmt.Errorf("watch kind %q: %w", kind, err)
		}

		// Drain the stream until the source closes it or ctx is cancelled.
		consume(ctx, ch, q)
	}
}

// consume forwards Events from ch into q until ch is closed by the source or ctx
// is cancelled. Selecting on ctx.Done() means the feeder exits promptly even if
// a misbehaving source forgets to close its channel on cancellation.
func consume(ctx context.Context, ch <-chan Event, q *Queue) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			q.Add(ev)
		}
	}
}

// reconcileLoop pulls Events from q and reconciles them until q is shut down and
// drained (Get reports ok=false). A reconcile that requeues or errors re-Adds the
// same Event for another attempt — the thinnest retry, with no backoff. The error
// is not swallowed: it is acted on by re-enqueuing (and per the Controller
// contract is meant to be logged; wiring a logger is out of this task's scope).
// After shutdown, a requeue's Add is dropped by the closed Queue, so the loop
// always terminates.
func reconcileLoop(ctx context.Context, c Controller, q *Queue) {
	for {
		ev, ok := q.Get()
		if !ok {
			return
		}
		requeue, err := c.Reconcile(ctx, ev)
		if requeue || err != nil {
			q.Add(ev)
		}
	}
}
