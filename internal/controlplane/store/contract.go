package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// RunStoreContract exercises the Store contract against a real implementation.
// It is exported (not a TestXxx) so Task 2's in-memory fake and Task 3's etcd
// store can both run the same behavioral assertions against their own newStore
// factory. Each call to newStore must return a fresh, empty Store.
//
// We deliberately use real Store behavior (no mocks) because the whole point of
// the contract is that interchangeable implementations behave identically.
func RunStoreContract(t *testing.T, newStore func() Store) {
	t.Helper()

	t.Run("PutNewKeyReturnsResourceVersion", func(t *testing.T) {
		t.Helper()
		s := newStore()
		defer s.Close()
		ctx := context.Background()

		obj, err := s.Put(ctx, "/govirta/pod/a", []byte(`{"v":1}`), "")
		if err != nil {
			t.Fatalf("Put: unexpected error: %v", err)
		}
		if obj.ResourceVersion == "" {
			t.Fatalf("Put: expected non-empty ResourceVersion, got empty")
		}
	})

	t.Run("GetHitAndMiss", func(t *testing.T) {
		t.Helper()
		s := newStore()
		defer s.Close()
		ctx := context.Background()

		want := []byte(`{"v":1}`)
		if _, err := s.Put(ctx, "/govirta/pod/a", want, ""); err != nil {
			t.Fatalf("Put: unexpected error: %v", err)
		}

		got, err := s.Get(ctx, "/govirta/pod/a")
		if err != nil {
			t.Fatalf("Get hit: unexpected error: %v", err)
		}
		if string(got.Value) != string(want) {
			t.Fatalf("Get hit: value = %q, want %q", got.Value, want)
		}

		if _, err := s.Get(ctx, "/govirta/pod/missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get miss: error = %v, want ErrNotFound", err)
		}
	})

	t.Run("PutCompareAndSwap", func(t *testing.T) {
		t.Helper()
		s := newStore()
		defer s.Close()
		ctx := context.Background()

		first, err := s.Put(ctx, "/govirta/pod/a", []byte(`{"v":1}`), "")
		if err != nil {
			t.Fatalf("Put initial: unexpected error: %v", err)
		}

		// Wrong expectedVersion must conflict.
		if _, err := s.Put(ctx, "/govirta/pod/a", []byte(`{"v":2}`), "does-not-match"); !errors.Is(err, ErrRevisionConflict) {
			t.Fatalf("Put stale CAS: error = %v, want ErrRevisionConflict", err)
		}

		// Correct expectedVersion must succeed.
		second, err := s.Put(ctx, "/govirta/pod/a", []byte(`{"v":2}`), first.ResourceVersion)
		if err != nil {
			t.Fatalf("Put matching CAS: unexpected error: %v", err)
		}
		if second.ResourceVersion == first.ResourceVersion {
			t.Fatalf("Put matching CAS: ResourceVersion did not change (%q)", second.ResourceVersion)
		}
		if string(second.Value) != `{"v":2}` {
			t.Fatalf("Put matching CAS: value = %q, want %q", second.Value, `{"v":2}`)
		}
	})

	t.Run("ListSortedByKey", func(t *testing.T) {
		t.Helper()
		s := newStore()
		defer s.Close()
		ctx := context.Background()

		// Insert out of order; List must return sorted by key.
		for _, k := range []string{"/govirta/pod/c", "/govirta/pod/a", "/govirta/pod/b"} {
			if _, err := s.Put(ctx, k, []byte(`{}`), ""); err != nil {
				t.Fatalf("Put %s: unexpected error: %v", k, err)
			}
		}
		// A key outside the prefix must be excluded.
		if _, err := s.Put(ctx, "/govirta/node/x", []byte(`{}`), ""); err != nil {
			t.Fatalf("Put node/x: unexpected error: %v", err)
		}

		objs, err := s.List(ctx, "/govirta/pod/")
		if err != nil {
			t.Fatalf("List: unexpected error: %v", err)
		}
		gotKeys := make([]string, len(objs))
		for i, o := range objs {
			gotKeys[i] = o.Key
		}
		wantKeys := []string{"/govirta/pod/a", "/govirta/pod/b", "/govirta/pod/c"}
		if len(gotKeys) != len(wantKeys) {
			t.Fatalf("List: keys = %v, want %v", gotKeys, wantKeys)
		}
		for i := range wantKeys {
			if gotKeys[i] != wantKeys[i] {
				t.Fatalf("List: keys = %v, want %v", gotKeys, wantKeys)
			}
		}
	})

	t.Run("DeleteIsIdempotent", func(t *testing.T) {
		t.Helper()
		s := newStore()
		defer s.Close()
		ctx := context.Background()

		if _, err := s.Put(ctx, "/govirta/pod/a", []byte(`{}`), ""); err != nil {
			t.Fatalf("Put: unexpected error: %v", err)
		}
		if err := s.Delete(ctx, "/govirta/pod/a"); err != nil {
			t.Fatalf("Delete existing: unexpected error: %v", err)
		}
		if _, err := s.Get(ctx, "/govirta/pod/a"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get after delete: error = %v, want ErrNotFound", err)
		}
		// Deleting a missing key must not be an error.
		if err := s.Delete(ctx, "/govirta/pod/a"); err != nil {
			t.Fatalf("Delete missing: unexpected error: %v", err)
		}
	})

	t.Run("WatchAddedModifiedDeleted", func(t *testing.T) {
		t.Helper()
		s := newStore()
		defer s.Close()

		// Short timeout so a broken Watch cannot hang the suite.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		ch, err := s.Watch(ctx, "/govirta/pod/", "")
		if err != nil {
			t.Fatalf("Watch: unexpected error: %v", err)
		}

		first, err := s.Put(ctx, "/govirta/pod/a", []byte(`{"v":1}`), "")
		if err != nil {
			t.Fatalf("Put add: unexpected error: %v", err)
		}
		if ev := recvEvent(t, ctx, ch); ev.Type != EventAdded || ev.Object.Key != "/govirta/pod/a" {
			t.Fatalf("Watch add: event = %+v, want ADDED for /govirta/pod/a", ev)
		}

		if _, err := s.Put(ctx, "/govirta/pod/a", []byte(`{"v":2}`), first.ResourceVersion); err != nil {
			t.Fatalf("Put modify: unexpected error: %v", err)
		}
		if ev := recvEvent(t, ctx, ch); ev.Type != EventModified || ev.Object.Key != "/govirta/pod/a" {
			t.Fatalf("Watch modify: event = %+v, want MODIFIED for /govirta/pod/a", ev)
		}

		if err := s.Delete(ctx, "/govirta/pod/a"); err != nil {
			t.Fatalf("Delete: unexpected error: %v", err)
		}
		if ev := recvEvent(t, ctx, ch); ev.Type != EventDeleted || ev.Object.Key != "/govirta/pod/a" {
			t.Fatalf("Watch delete: event = %+v, want DELETED for /govirta/pod/a", ev)
		}
	})

	t.Run("WatchClosesOnContextCancel", func(t *testing.T) {
		t.Helper()
		s := newStore()
		defer s.Close()

		ctx, cancel := context.WithCancel(context.Background())
		ch, err := s.Watch(ctx, "/govirta/pod/", "")
		if err != nil {
			t.Fatalf("Watch: unexpected error: %v", err)
		}

		cancel()

		// The channel must close (not just stop delivering) once ctx is done.
		select {
		case _, ok := <-ch:
			if ok {
				// Drain at most one stray event, then require close.
				select {
				case _, ok := <-ch:
					if ok {
						t.Fatalf("Watch: channel did not close after context cancel")
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("Watch: channel did not close after context cancel (timeout)")
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("Watch: channel did not close after context cancel (timeout)")
		}
	})

	t.Run("WatchResumeReplaysRealEventSequence", func(t *testing.T) {
		t.Helper()
		s := newStore()
		defer s.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Produce a known sequence before any watch exists. The resume point is
		// the revision returned by the modify; events strictly after it must be
		// replayed in order, preserving their original type.
		first, err := s.Put(ctx, "/govirta/pod/a", []byte(`{"v":1}`), "")
		if err != nil {
			t.Fatalf("Put add a: unexpected error: %v", err)
		}
		modified, err := s.Put(ctx, "/govirta/pod/a", []byte(`{"v":2}`), first.ResourceVersion)
		if err != nil {
			t.Fatalf("Put modify a: unexpected error: %v", err)
		}
		// resumeFrom marks the last change the consumer already saw.
		resumeFrom := modified.ResourceVersion
		if err := s.Delete(ctx, "/govirta/pod/a"); err != nil {
			t.Fatalf("Delete a: unexpected error: %v", err)
		}
		if _, err := s.Put(ctx, "/govirta/pod/b", []byte(`{"v":1}`), ""); err != nil {
			t.Fatalf("Put add b: unexpected error: %v", err)
		}

		// Resuming from resumeFrom must replay the real events after it
		// (DELETED a, then ADDED b), with types preserved — not a current-state
		// snapshot. This is the authoritative etcd resume behavior; the fake
		// mirrors it, so both implementations are interchangeable on resume.
		ch, err := s.Watch(ctx, "/govirta/pod/", resumeFrom)
		if err != nil {
			t.Fatalf("Watch resume: unexpected error: %v", err)
		}

		ev1 := recvEvent(t, ctx, ch)
		if ev1.Type != EventDeleted || ev1.Object.Key != "/govirta/pod/a" {
			t.Fatalf("resume event 1 = %+v, want DELETED for /govirta/pod/a", ev1)
		}
		ev2 := recvEvent(t, ctx, ch)
		if ev2.Type != EventAdded || ev2.Object.Key != "/govirta/pod/b" {
			t.Fatalf("resume event 2 = %+v, want ADDED for /govirta/pod/b", ev2)
		}
	})

	t.Run("WatchEmptyStartRevisionListsThenWatches", func(t *testing.T) {
		t.Helper()
		s := newStore()
		defer s.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Two objects exist BEFORE any watch opens. An empty-startRevision watch
		// must still observe them as ADDED (list-then-watch / resourceVersion=0),
		// then deliver subsequent changes live. Without the initial list, an
		// object created before the watch opened is lost forever — the real
		// first-connect race that froze the node controllers in e2e.
		if _, err := s.Put(ctx, "/govirta/pod/a", []byte(`{"v":1}`), ""); err != nil {
			t.Fatalf("Put pre-existing a: unexpected error: %v", err)
		}
		if _, err := s.Put(ctx, "/govirta/pod/b", []byte(`{"v":1}`), ""); err != nil {
			t.Fatalf("Put pre-existing b: unexpected error: %v", err)
		}

		ch, err := s.Watch(ctx, "/govirta/pod/", "")
		if err != nil {
			t.Fatalf("Watch: unexpected error: %v", err)
		}

		// Both pre-existing objects must arrive as ADDED before any live event.
		got := map[string]bool{}
		for i := 0; i < 2; i++ {
			ev := recvEvent(t, ctx, ch)
			if ev.Type != EventAdded {
				t.Fatalf("snapshot event %d = %+v, want ADDED", i, ev)
			}
			got[ev.Object.Key] = true
		}
		if !got["/govirta/pod/a"] || !got["/govirta/pod/b"] {
			t.Fatalf("snapshot keys = %v, want both /govirta/pod/a and /govirta/pod/b", got)
		}

		// A change after the snapshot must still be delivered live on the same
		// watch (the cutover from list to watch loses nothing).
		if _, err := s.Put(ctx, "/govirta/pod/c", []byte(`{"v":1}`), ""); err != nil {
			t.Fatalf("Put live c: unexpected error: %v", err)
		}
		ev := recvEvent(t, ctx, ch)
		if ev.Type != EventAdded || ev.Object.Key != "/govirta/pod/c" {
			t.Fatalf("live event = %+v, want ADDED for /govirta/pod/c", ev)
		}
	})
}

// recvEvent receives one WatchEvent or fails if the context expires first.
func recvEvent(t *testing.T, ctx context.Context, ch <-chan WatchEvent) WatchEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatalf("Watch: channel closed before event arrived")
		}
		return ev
	case <-ctx.Done():
		t.Fatalf("Watch: timed out waiting for event: %v", ctx.Err())
		return WatchEvent{}
	}
}
