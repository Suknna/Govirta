package fake_test

import (
	"context"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/controlplane/store"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
)

// TestFakeStoreContract runs the shared Store behavioral suite from Task 1
// against the in-memory fake. Passing this is the contract that makes the fake
// interchangeable with the etcd-backed store.
func TestFakeStoreContract(t *testing.T) {
	store.RunStoreContract(t, func() store.Store { return fake.New() })
}

// TestWatchFanOut asserts a single change is delivered to every watcher whose
// prefix matches: two concurrent watchers must both observe the same ADDED
// event. This is the concurrency-sensitive path, so the suite is also run with
// -race in CI.
func TestWatchFanOut(t *testing.T) {
	s := fake.New()
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch1, err := s.Watch(ctx, "/govirta/pod/", "")
	if err != nil {
		t.Fatalf("Watch ch1: unexpected error: %v", err)
	}
	ch2, err := s.Watch(ctx, "/govirta/pod/", "")
	if err != nil {
		t.Fatalf("Watch ch2: unexpected error: %v", err)
	}

	if _, err := s.Put(ctx, "/govirta/pod/a", []byte(`{"v":1}`), ""); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	for i, ch := range []<-chan store.WatchEvent{ch1, ch2} {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("watcher %d: channel closed before event", i)
			}
			if ev.Type != store.EventAdded || ev.Object.Key != "/govirta/pod/a" {
				t.Fatalf("watcher %d: event = %+v, want ADDED for /govirta/pod/a", i, ev)
			}
		case <-ctx.Done():
			t.Fatalf("watcher %d: timed out waiting for event: %v", i, ctx.Err())
		}
	}
}

// TestWatchPrefixFiltersFanOut asserts an event is delivered only to watchers
// whose prefix matches the changed key, not to every watcher.
func TestWatchPrefixFiltersFanOut(t *testing.T) {
	s := fake.New()
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	podCh, err := s.Watch(ctx, "/govirta/pod/", "")
	if err != nil {
		t.Fatalf("Watch pod: unexpected error: %v", err)
	}
	nodeCh, err := s.Watch(ctx, "/govirta/node/", "")
	if err != nil {
		t.Fatalf("Watch node: unexpected error: %v", err)
	}

	if _, err := s.Put(ctx, "/govirta/pod/a", []byte(`{}`), ""); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	// pod watcher must receive it.
	select {
	case ev, ok := <-podCh:
		if !ok {
			t.Fatalf("pod watcher: channel closed before event")
		}
		if ev.Object.Key != "/govirta/pod/a" {
			t.Fatalf("pod watcher: event key = %q, want /govirta/pod/a", ev.Object.Key)
		}
	case <-ctx.Done():
		t.Fatalf("pod watcher: timed out: %v", ctx.Err())
	}

	// node watcher must NOT receive a pod event.
	select {
	case ev, ok := <-nodeCh:
		if ok {
			t.Fatalf("node watcher: unexpectedly received event %+v", ev)
		}
	case <-time.After(200 * time.Millisecond):
		// Expected: no delivery to the non-matching prefix.
	}
}

// TestWatchClosesOnContextCancel asserts that cancelling a watcher's ctx closes
// its channel (rather than merely stopping delivery), so consumers can range
// over the channel and exit cleanly.
func TestWatchClosesOnContextCancel(t *testing.T) {
	s := fake.New()
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := s.Watch(ctx, "/govirta/pod/", "")
	if err != nil {
		t.Fatalf("Watch: unexpected error: %v", err)
	}

	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			// Allow at most one buffered event, then require the close.
			select {
			case _, ok := <-ch:
				if ok {
					t.Fatalf("channel did not close after context cancel")
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("channel did not close after context cancel (timeout)")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("channel did not close after context cancel (timeout)")
	}
}

// TestWatchOneCancelDoesNotAffectOther asserts cancelling one watcher's ctx
// closes only that channel; a sibling watcher keeps receiving events.
func TestWatchOneCancelDoesNotAffectOther(t *testing.T) {
	s := fake.New()
	defer s.Close()

	liveCtx, liveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer liveCancel()
	deadCtx, deadCancel := context.WithCancel(context.Background())

	liveCh, err := s.Watch(liveCtx, "/govirta/pod/", "")
	if err != nil {
		t.Fatalf("Watch live: unexpected error: %v", err)
	}
	deadCh, err := s.Watch(deadCtx, "/govirta/pod/", "")
	if err != nil {
		t.Fatalf("Watch dead: unexpected error: %v", err)
	}

	deadCancel()

	// The cancelled watcher's channel must close.
	select {
	case _, ok := <-deadCh:
		for ok {
			_, ok = <-deadCh
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("cancelled watcher channel did not close")
	}

	// The live watcher must still receive new events.
	if _, err := s.Put(liveCtx, "/govirta/pod/a", []byte(`{}`), ""); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	select {
	case ev, ok := <-liveCh:
		if !ok {
			t.Fatalf("live watcher: channel closed unexpectedly")
		}
		if ev.Type != store.EventAdded || ev.Object.Key != "/govirta/pod/a" {
			t.Fatalf("live watcher: event = %+v, want ADDED for /govirta/pod/a", ev)
		}
	case <-liveCtx.Done():
		t.Fatalf("live watcher: timed out: %v", liveCtx.Err())
	}
}

// TestClosedStoreReturnsErrClosed asserts operations fail with ErrClosed after
// Close, and that watcher channels close on store shutdown.
func TestClosedStoreReturnsErrClosed(t *testing.T) {
	s := fake.New()

	ctx := context.Background()
	ch, err := s.Watch(ctx, "/govirta/pod/", "")
	if err != nil {
		t.Fatalf("Watch: unexpected error: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}

	// Closing the store must close existing watcher channels.
	select {
	case _, ok := <-ch:
		for ok {
			_, ok = <-ch
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("watcher channel did not close on store Close")
	}

	if _, err := s.Put(ctx, "/govirta/pod/a", []byte(`{}`), ""); err != store.ErrClosed {
		t.Fatalf("Put after Close: error = %v, want ErrClosed", err)
	}
	if _, err := s.Get(ctx, "/govirta/pod/a"); err != store.ErrClosed {
		t.Fatalf("Get after Close: error = %v, want ErrClosed", err)
	}
	if _, err := s.List(ctx, "/govirta/pod/"); err != store.ErrClosed {
		t.Fatalf("List after Close: error = %v, want ErrClosed", err)
	}
	if err := s.Delete(ctx, "/govirta/pod/a"); err != store.ErrClosed {
		t.Fatalf("Delete after Close: error = %v, want ErrClosed", err)
	}
	if _, err := s.Watch(ctx, "/govirta/pod/", ""); err != store.ErrClosed {
		t.Fatalf("Watch after Close: error = %v, want ErrClosed", err)
	}
}
