// Package fake provides an in-memory store.Store implementation for unit tests
// of upper layers (allocator / handlers / server). It has no external
// dependencies and is safe for concurrent use, so a test can drive Put/Delete
// from one goroutine while several Watch consumers read in parallel.
//
// The fake is interchangeable with the etcd-backed store: it passes the same
// store.RunStoreContract behavioral suite, which is the whole point of the raw
// key/value + watch boundary.
package fake

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/suknna/govirta/internal/controlplane/store"
)

// Store is an in-memory store.Store. The zero value is not usable; construct it
// with New so its internal maps and shutdown channel are initialized.
type Store struct {
	// mu guards every mutable field below. We deliberately serialize all
	// operations under one mutex: the data set is tiny in tests and a single
	// lock keeps revision assignment and event fan-out atomic, which is what
	// guarantees watchers observe changes in revision order.
	mu sync.Mutex
	// rev is a monotonically increasing revision counter. It bumps on every
	// state-changing Put/Delete so a successful compare-and-swap always yields a
	// ResourceVersion distinct from the prior one (Task 1 contract).
	rev int64
	// data maps full key to its current object.
	data map[string]store.RawObject
	// watchers is the set of live subscriptions. A watcher deregisters itself
	// from this set when its pump goroutine exits, so the set does not grow
	// unbounded across a test's Watch calls.
	watchers map[*watcher]struct{}
	// closed records whether Close has run; further operations return ErrClosed.
	closed bool
	// done is closed by Close to unblock and terminate every watcher pump
	// goroutine, which then closes its outbound channel.
	done chan struct{}
}

// New returns an empty in-memory Store ready for concurrent use.
func New() *Store {
	return &Store{
		data:     make(map[string]store.RawObject),
		watchers: make(map[*watcher]struct{}),
		done:     make(chan struct{}),
	}
}

// Put stores value at key, assigning a fresh ResourceVersion. When
// expectedVersion is non-empty it is a compare-and-swap: a mismatch with the
// currently stored version returns store.ErrRevisionConflict and the write does
// not happen. An empty expectedVersion is an unconditional create-or-overwrite.
func (s *Store) Put(ctx context.Context, key string, value []byte, expectedVersion string) (store.RawObject, error) {
	if err := ctx.Err(); err != nil {
		return store.RawObject{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return store.RawObject{}, store.ErrClosed
	}

	existing, found := s.data[key]
	// Empty expectedVersion means "no precondition"; only enforce CAS when the
	// caller supplied a version to match against.
	if expectedVersion != "" {
		// A CAS against a missing key, or against a different stored version,
		// cannot succeed: there is nothing matching to swap.
		if !found || existing.ResourceVersion != expectedVersion {
			return store.RawObject{}, store.ErrRevisionConflict
		}
	}

	s.rev++
	obj := store.RawObject{
		Key:             key,
		Value:           append([]byte(nil), value...), // copy so caller mutations cannot alias stored bytes
		ResourceVersion: strconv.FormatInt(s.rev, 10),
	}
	s.data[key] = obj

	eventType := store.EventAdded
	if found {
		eventType = store.EventModified
	}
	s.broadcastLocked(store.WatchEvent{Type: eventType, Object: obj})

	return obj, nil
}

// Get returns the object at key or store.ErrNotFound.
func (s *Store) Get(ctx context.Context, key string) (store.RawObject, error) {
	if err := ctx.Err(); err != nil {
		return store.RawObject{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return store.RawObject{}, store.ErrClosed
	}

	obj, found := s.data[key]
	if !found {
		return store.RawObject{}, store.ErrNotFound
	}
	// Return a copy of the value so callers cannot mutate stored bytes in place.
	out := obj
	out.Value = append([]byte(nil), obj.Value...)
	return out, nil
}

// List returns every object whose key starts with prefix, sorted by key.
func (s *Store) List(ctx context.Context, prefix string) ([]store.RawObject, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, store.ErrClosed
	}

	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	objs := make([]store.RawObject, 0, len(keys))
	for _, k := range keys {
		obj := s.data[k]
		obj.Value = append([]byte(nil), obj.Value...)
		objs = append(objs, obj)
	}
	return objs, nil
}

// Delete removes key. Deleting a missing key is a no-op (idempotent) and emits
// no event; deleting a present key bumps the revision and emits DELETED.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return store.ErrClosed
	}

	if _, found := s.data[key]; !found {
		// Idempotent: nothing stored, nothing to change, no event.
		return nil
	}

	delete(s.data, key)
	s.rev++
	// For a deletion the post-change object carries the key and the new revision;
	// Value is intentionally empty because the object no longer exists.
	s.broadcastLocked(store.WatchEvent{
		Type: store.EventDeleted,
		Object: store.RawObject{
			Key:             key,
			ResourceVersion: strconv.FormatInt(s.rev, 10),
		},
	})
	return nil
}

// Watch streams events for keys under prefix. With startRevision == "" delivery
// begins with changes that happen after this call (current-and-after). With a
// non-empty startRevision the current matching objects whose revision is newer
// than startRevision are replayed as ADDED first, so a reconnecting consumer
// does not miss objects created while it was disconnected. The returned channel
// is closed when ctx is done or the store is closed.
func (s *Store) Watch(ctx context.Context, prefix string, startRevision string) (<-chan store.WatchEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, store.ErrClosed
	}

	w := &watcher{
		prefix: prefix,
		ctx:    ctx,
		out:    make(chan store.WatchEvent),
		notify: make(chan struct{}, 1),
	}

	// Optional catch-up replay for a non-empty startRevision. Empty means "from
	// now", so we skip replay to honor the Task 1 contract semantics exactly.
	if startRevision != "" {
		if from, err := strconv.ParseInt(startRevision, 10, 64); err == nil {
			keys := make([]string, 0, len(s.data))
			for k := range s.data {
				if strings.HasPrefix(k, prefix) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				obj := s.data[k]
				if rv, perr := strconv.ParseInt(obj.ResourceVersion, 10, 64); perr == nil && rv > from {
					replay := obj
					replay.Value = append([]byte(nil), obj.Value...)
					w.enqueue(store.WatchEvent{Type: store.EventAdded, Object: replay})
				}
			}
		}
	}

	s.watchers[w] = struct{}{}
	go s.pump(w)
	return w.out, nil
}

// Close releases store resources, terminating every watcher (which closes its
// channel). After Close, all operations return store.ErrClosed. Close is
// idempotent.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	// Closing done unblocks every pump goroutine; each closes its out channel
	// and deregisters itself.
	close(s.done)
	return nil
}

// broadcastLocked enqueues ev to every watcher whose prefix matches the event's
// key. It must be called with s.mu held so revision order is preserved across
// watchers. Enqueue is non-blocking, so holding the store lock here cannot
// stall on a slow consumer.
func (s *Store) broadcastLocked(ev store.WatchEvent) {
	for w := range s.watchers {
		if strings.HasPrefix(ev.Object.Key, w.prefix) {
			w.enqueue(ev)
		}
	}
}

// pump drains a watcher's buffered events to its outbound channel in order,
// exiting (and closing the channel) when the caller's ctx is done or the store
// closes. On exit it deregisters the watcher so the registry stays bounded.
func (s *Store) pump(w *watcher) {
	defer func() {
		s.mu.Lock()
		delete(s.watchers, w)
		s.mu.Unlock()
		close(w.out)
	}()

	for {
		ev, ok := w.dequeue()
		if ok {
			select {
			case w.out <- ev:
			case <-w.ctx.Done():
				return
			case <-s.done:
				return
			}
			continue
		}

		// Queue empty: wait for a new event, cancellation, or store shutdown.
		select {
		case <-w.notify:
		case <-w.ctx.Done():
			return
		case <-s.done:
			return
		}
	}
}

// watcher is one live subscription. Events are appended to buf under buf's own
// mutex (decoupled from the store mutex) and the pump goroutine forwards them to
// out, so a slow consumer never blocks Put/Delete.
type watcher struct {
	prefix string
	ctx    context.Context
	out    chan store.WatchEvent

	// notify is a size-1 wakeup signal; a send is best-effort (dropped if one is
	// already pending) because its only job is to wake the pump, not to count.
	notify chan struct{}

	bufMu sync.Mutex
	buf   []store.WatchEvent
}

// enqueue appends ev and wakes the pump. It never blocks the caller (the store,
// under its lock), so fan-out cannot deadlock on a slow watcher.
func (w *watcher) enqueue(ev store.WatchEvent) {
	w.bufMu.Lock()
	w.buf = append(w.buf, ev)
	w.bufMu.Unlock()

	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// dequeue pops the oldest buffered event, reporting whether one was present.
func (w *watcher) dequeue() (store.WatchEvent, bool) {
	w.bufMu.Lock()
	defer w.bufMu.Unlock()
	if len(w.buf) == 0 {
		return store.WatchEvent{}, false
	}
	ev := w.buf[0]
	w.buf = w.buf[1:]
	return ev, true
}
