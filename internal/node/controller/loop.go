package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
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
// watchclient layer fills each Event.ResourceVersion from the wire event's
// metadata.resourceVersion (it can parse the concrete kind without leaking that
// knowledge into this kind-agnostic framework); feed records the last non-empty
// one and hands it to the next Watch so a reconnect resumes after the last
// version seen rather than replaying the source's full current state. This is
// the master↔node half of the resume contract proven against real etcd in the
// Store layer (resourceVersion → startRevision).
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

		// Drain the stream until the source closes it or ctx is cancelled,
		// advancing the resume cursor past the last version this connection saw.
		lastRV = consume(ctx, ch, q, lastRV)
	}
}

// consume forwards Events from ch into q until ch is closed by the source or ctx
// is cancelled. Selecting on ctx.Done() means the feeder exits promptly even if
// a misbehaving source forgets to close its channel on cancellation.
//
// It threads the resume cursor: starting from startRV, it returns the last
// non-empty Event.ResourceVersion it forwarded (or startRV unchanged if the
// connection delivered no versioned event), so the caller can resume the next
// watch after it.
func consume(ctx context.Context, ch <-chan Event, q *Queue, startRV string) string {
	lastRV := startRV
	for {
		select {
		case <-ctx.Done():
			return lastRV
		case ev, ok := <-ch:
			if !ok {
				return lastRV
			}
			if ev.ResourceVersion != "" {
				lastRV = ev.ResourceVersion
			}
			q.Add(ev)
		}
	}
}

// reconcileLoop pulls Events from q and reconciles them until q is shut down and
// drained (Get reports ok=false). A reconcile result can request immediate or
// delayed requeue. A reconcile error is not swallowed: it is re-enqueued for retry
// and logged at error level (with kind/key/requeue context) so a controller
// failing transiently is observable rather than spinning silently. After shutdown,
// a requeue's Add is dropped by the closed Queue, so the loop always terminates.
func reconcileLoop(ctx context.Context, c Controller, q *Queue) {
	for {
		ev, ok := q.Get()
		if !ok {
			return
		}
		result, err := c.Reconcile(ctx, ev)
		if err != nil && !result.ShouldRequeue() {
			result = Requeue()
		}
		if err != nil {
			// A reconcile error must not be silently swallowed (项目铁律: 不吞错误).
			// It is acted on by re-enqueuing below, but it is also recorded here —
			// this is the single chokepoint every controller's transient failure
			// flows through, and controllers do not all log their own transient
			// errors. Without this line a controller that fails every attempt
			// spins invisibly (the exact blind spot that hid a stuck reconcile in
			// e2e). A pure requeue with no error is a controller's own dependency
			// wait, which the controller already logs, so it is not logged here.
			zerolog.Ctx(ctx).Error().
				Err(err).
				Str("kind", c.Kind()).
				Str("key", ev.Key).
				Bool("requeue", result.ShouldRequeue()).
				Dur("requeue_after", result.RequeueAfter).
				Msg("controller reconcile failed")
		}
		switch {
		case result.RequeueAfter > 0:
			scheduleRequeue(ctx, q, ev, result.RequeueAfter)
		case result.Requeue:
			q.Add(ev)
		}
	}
}

// scheduleRequeue re-adds ev after delay unless ctx is cancelled first. The
// goroutine is owned by the controller run context, and Queue.Add drops the event
// if the queue has already been shut down.
func scheduleRequeue(ctx context.Context, q *Queue, ev Event, delay time.Duration) {
	timer := time.NewTimer(delay)
	go func() {
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			q.Add(ev)
		}
	}()
}
