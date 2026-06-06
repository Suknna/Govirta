package controller

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

// fakeSource is an in-memory EventSource for tests. It serves a fixed set of
// preset events per kind, then holds the watch channel open until ctx is
// cancelled (rather than closing it eagerly) so the feeder does not reconnect
// and replay events. Every spawned goroutine is tracked in wg so tests can join
// them when checking for leaks. watchErr, if set, makes Watch fail.
type fakeSource struct {
	events   map[string][]Event
	watchErr error
	wg       sync.WaitGroup
}

func (s *fakeSource) Watch(ctx context.Context, kind string, startRevision string) (<-chan Event, error) {
	if s.watchErr != nil {
		return nil, s.watchErr
	}
	ch := make(chan Event)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(ch)
		for _, ev := range s.events[kind] {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
		// Hold the stream open until cancellation so feed() does not see a
		// closed channel and reconnect (which would replay the preset events).
		<-ctx.Done()
	}()
	return ch, nil
}

// fakeController records the keys it reconciles and signals each call on the
// reconciled channel. Keys listed in requeueOnce return requeue=true the first
// time they are seen and false thereafter, exercising the loop's retry path.
type fakeController struct {
	kind string

	reconciled chan Event

	mu          sync.Mutex
	seen        []string
	requeueOnce map[string]bool
	requeued    map[string]bool
}

func newFakeController(kind string, requeueOnce ...string) *fakeController {
	ro := make(map[string]bool, len(requeueOnce))
	for _, k := range requeueOnce {
		ro[k] = true
	}
	return &fakeController{
		kind:        kind,
		reconciled:  make(chan Event, 64),
		requeueOnce: ro,
		requeued:    make(map[string]bool),
	}
}

func (f *fakeController) Kind() string { return f.kind }

func (f *fakeController) Reconcile(ctx context.Context, ev Event) (bool, error) {
	f.mu.Lock()
	f.seen = append(f.seen, ev.Key)
	var requeue bool
	if f.requeueOnce[ev.Key] && !f.requeued[ev.Key] {
		f.requeued[ev.Key] = true
		requeue = true
	}
	f.mu.Unlock()

	select {
	case f.reconciled <- ev:
	case <-ctx.Done():
	}
	return requeue, nil
}

func (f *fakeController) seenKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.seen))
	copy(out, f.seen)
	return out
}

func TestManager_DeliversEventsToReconcile(t *testing.T) {
	src := &fakeSource{events: map[string][]Event{
		"VM": {
			{Type: EventAdded, Key: "vm-a", Object: []byte(`{}`)},
			{Type: EventAdded, Key: "vm-b", Object: []byte(`{}`)},
		},
	}}
	ctrl := newFakeController("VM")
	mgr := NewManager(src, []Controller{ctrl})

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- mgr.Run(ctx) }()

	// Both preset events must reach Reconcile.
	got := make(map[string]bool)
	for i := 0; i < 2; i++ {
		select {
		case ev := <-ctrl.reconciled:
			got[ev.Key] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %d; seen=%v", i, ctrl.seenKeys())
		}
	}
	if !got["vm-a"] || !got["vm-b"] {
		t.Fatalf("expected both vm-a and vm-b reconciled, got %v", got)
	}

	cancel()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestManager_NoGoroutineLeak(t *testing.T) {
	// Let any goroutines from earlier subtests settle before sampling.
	settle()
	before := runtime.NumGoroutine()

	src := &fakeSource{events: map[string][]Event{
		"VM":  {{Type: EventAdded, Key: "vm-a", Object: []byte(`{}`)}},
		"Net": {{Type: EventAdded, Key: "net-a", Object: []byte(`{}`)}},
	}}
	vm := newFakeController("VM")
	net := newFakeController("Net")
	mgr := NewManager(src, []Controller{vm, net})

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- mgr.Run(ctx) }()

	// Wait until each controller has handled its event so all goroutines are
	// genuinely running before we tear down.
	for _, c := range []*fakeController{vm, net} {
		select {
		case <-c.reconciled:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s reconcile", c.kind)
		}
	}

	cancel()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	// Join the source's own goroutines, then let the runtime settle.
	src.wg.Wait()

	after := waitGoroutines(before)
	if after > before {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}

// settle yields a few times so transient goroutines from prior work can exit.
func settle() {
	for i := 0; i < 5; i++ {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
}

// waitGoroutines polls NumGoroutine until it drops to at most target or a short
// deadline passes, returning the last sample. This avoids flaking on goroutines
// that are mid-teardown when we sample.
func waitGoroutines(target int) int {
	deadline := time.Now().Add(2 * time.Second)
	n := runtime.NumGoroutine()
	for n > target && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}
