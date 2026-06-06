package controller

import "sync"

// Queue is a concurrency-safe, deduplicating workqueue feeding the controller's
// reconcile loop. Watch events for the same Key collapse into a single pending
// item (newest Event wins), so a burst of updates to one object is reconciled
// once against its latest state instead of replaying every intermediate change.
// A failed reconcile re-Adds the Key, which re-enqueues it for another attempt.
type Queue struct {
	mu    sync.Mutex
	items map[string]Event
	// order preserves FIFO insertion order of pending keys; a re-Add of a key
	// already present updates items[key] in place without changing its position.
	order []string
	// notify is buffered(1) and acts as a wakeup signal, not a value channel:
	// Add pokes it non-blockingly so a parked Get re-checks the queue. The
	// buffer of 1 coalesces multiple Adds into a single pending wakeup, which is
	// enough because Get drains the whole order slice once awoken.
	notify chan struct{}
	// done is closed exactly once by Shutdown to unblock every parked Get. We
	// use a dedicated channel (rather than closing notify) so Add can never send
	// on a closed channel and panic; closed stays readable, so all waiters wake.
	done   chan struct{}
	closed bool
}

// New returns an empty, ready-to-use Queue.
func New() *Queue {
	return &Queue{
		items:  make(map[string]Event),
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

// Add enqueues ev for processing, deduplicating by ev.Key: if the key is already
// pending, its Event is overwritten with the newer ev (latest state wins) and
// its FIFO position is kept. Adds after Shutdown are dropped, since no consumer
// will ever drain them.
func (q *Queue) Add(ev Event) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	if _, exists := q.items[ev.Key]; !exists {
		q.order = append(q.order, ev.Key)
	}
	q.items[ev.Key] = ev
	q.mu.Unlock()

	// Non-blocking poke: if a wakeup is already pending the default branch keeps
	// Add from blocking, and Get's drain loop will still observe this item.
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// Get blocks until an item is available or the queue is shut down. It returns the
// oldest pending Event and ok=true, or (Event{}, false) once the queue is closed
// and drained. Pending items are always drained before a closed queue reports
// false, so Shutdown never discards already-enqueued work.
func (q *Queue) Get() (Event, bool) {
	for {
		q.mu.Lock()
		if len(q.order) > 0 {
			key := q.order[0]
			q.order = q.order[1:]
			ev := q.items[key]
			delete(q.items, key)
			q.mu.Unlock()
			return ev, true
		}
		closed := q.closed
		q.mu.Unlock()

		if closed {
			return Event{}, false
		}

		// Park until Add pokes notify or Shutdown closes done. Looping re-checks
		// the queue under lock, so a wakeup never races a concurrent drain.
		select {
		case <-q.notify:
		case <-q.done:
		}
	}
}

// Shutdown marks the queue closed and unblocks all parked Get callers. It is
// idempotent: a second call is a no-op and never panics on the done channel.
func (q *Queue) Shutdown() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.mu.Unlock()

	close(q.done)
}
