package controller

import (
	"testing"
	"time"
)

func TestQueueDedupsSameKey(t *testing.T) {
	q := New()
	q.Add(Event{Type: EventAdded, Key: "a", Object: []byte(`{"v":1}`)})
	q.Add(Event{Type: EventModified, Key: "a", Object: []byte(`{"v":2}`)})
	ev, ok := q.Get()
	if !ok || ev.Key != "a" || string(ev.Object) != `{"v":2}` {
		t.Fatalf("dedup: got %+v ok=%v, want latest v:2 for key a", ev, ok)
	}
	q.Shutdown()
	if _, ok := q.Get(); ok {
		t.Fatalf("expected queue drained after single dedup'd item")
	}
}

func TestQueueShutdownUnblocksGet(t *testing.T) {
	q := New()
	done := make(chan struct{})
	go func() {
		_, ok := q.Get()
		if ok {
			t.Errorf("Get after shutdown: ok=true, want false")
		}
		close(done)
	}()
	q.Shutdown()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Shutdown did not unblock Get")
	}
}

// TestQueueReAddAfterGet verifies the failure-requeue path: once an item is
// taken out via Get, re-Adding the same key (as a failed reconcile would) makes
// it retrievable again, proving dedup state is per-pending-item, not permanent.
func TestQueueReAddAfterGet(t *testing.T) {
	q := New()
	q.Add(Event{Type: EventAdded, Key: "a", Object: []byte(`{"v":1}`)})

	ev, ok := q.Get()
	if !ok || ev.Key != "a" {
		t.Fatalf("first get: got %+v ok=%v, want key a", ev, ok)
	}

	// Simulate a failed reconcile re-enqueueing the same key with newer state.
	q.Add(Event{Type: EventModified, Key: "a", Object: []byte(`{"v":2}`)})

	ev, ok = q.Get()
	if !ok || ev.Key != "a" || string(ev.Object) != `{"v":2}` {
		t.Fatalf("re-add get: got %+v ok=%v, want latest v:2 for key a", ev, ok)
	}

	q.Shutdown()
	if _, ok := q.Get(); ok {
		t.Fatalf("expected queue drained after re-add item consumed")
	}
}

// TestQueueFIFOOrder confirms distinct keys are drained in insertion order and
// that Get blocks then wakes when an Add arrives concurrently.
func TestQueueFIFOOrder(t *testing.T) {
	q := New()
	q.Add(Event{Type: EventAdded, Key: "a"})
	q.Add(Event{Type: EventAdded, Key: "b"})

	first, ok := q.Get()
	if !ok || first.Key != "a" {
		t.Fatalf("first: got %+v ok=%v, want key a", first, ok)
	}
	second, ok := q.Get()
	if !ok || second.Key != "b" {
		t.Fatalf("second: got %+v ok=%v, want key b", second, ok)
	}

	// Now Get should block; an async Add must wake it.
	got := make(chan Event, 1)
	go func() {
		ev, ok := q.Get()
		if ok {
			got <- ev
		}
	}()
	q.Add(Event{Type: EventAdded, Key: "c"})

	select {
	case ev := <-got:
		if ev.Key != "c" {
			t.Fatalf("blocked get woke with %+v, want key c", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Add did not unblock a parked Get")
	}

	q.Shutdown()
}
