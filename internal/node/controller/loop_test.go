package controller

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLoop_RequeueReprocessesKey(t *testing.T) {
	// vm-a returns requeue=true on its first reconcile, so the loop must
	// re-enqueue and reconcile it a second time (where it succeeds).
	src := &fakeSource{events: map[string][]Event{
		"VM": {{Type: EventAdded, Key: "vm-a", Object: []byte(`{}`)}},
	}}
	ctrl := newFakeController("VM", "vm-a")
	mgr := NewManager(src, []Controller{ctrl})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- mgr.Run(ctx) }()

	// Expect vm-a to be reconciled at least twice: the requeued attempt plus
	// the successful one.
	for i := 0; i < 2; i++ {
		select {
		case ev := <-ctrl.reconciled:
			if ev.Key != "vm-a" {
				t.Fatalf("reconcile %d: got key %q, want vm-a", i, ev.Key)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for reconcile %d; seen=%v", i, ctrl.seenKeys())
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
}

func TestLoop_WatchErrorSurfaces(t *testing.T) {
	wantErr := errors.New("boom")
	src := &fakeSource{watchErr: wantErr}
	ctrl := newFakeController("VM")
	mgr := NewManager(src, []Controller{ctrl})

	// Watch fails immediately, so the feeder returns a wrapped error, shuts the
	// queue, and Run surfaces that error without needing cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := mgr.Run(ctx)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run returned %v, want it to wrap %v", err, wantErr)
	}
}
