package controller

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
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

func TestLoop_DelayedRequeueWaitsBeforeRetry(t *testing.T) {
	q := New()
	q.Add(Event{Type: EventAdded, Key: "vm-a", Object: []byte(`{}`)})
	ctrl := &scriptedController{
		kind:       "VM",
		reconciled: make(chan Event, 4),
		results: []ReconcileResult{
			RequeueAfter(30 * time.Millisecond),
			Done(),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		reconcileLoop(ctx, ctrl, q)
		close(done)
	}()

	select {
	case <-ctrl.reconciled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first reconcile")
	}

	select {
	case <-ctrl.reconciled:
		t.Fatal("delayed requeue reconciled immediately")
	case <-time.After(5 * time.Millisecond):
	}

	select {
	case <-ctrl.reconciled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delayed reconcile")
	}

	q.Shutdown()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconcile loop did not stop after queue shutdown")
	}
}

func TestLoop_CancelBeforeDelayedRequeuePreventsRetry(t *testing.T) {
	q := New()
	q.Add(Event{Type: EventAdded, Key: "vm-a", Object: []byte(`{}`)})
	ctrl := &scriptedController{
		kind:       "VM",
		reconciled: make(chan Event, 4),
		results: []ReconcileResult{
			RequeueAfter(30 * time.Millisecond),
			Done(),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reconcileLoop(ctx, ctrl, q)
		close(done)
	}()

	select {
	case <-ctrl.reconciled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first reconcile")
	}
	cancel()
	q.Shutdown()

	select {
	case <-ctrl.reconciled:
		t.Fatal("delayed requeue reconciled after cancellation")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconcile loop did not stop after cancellation")
	}
}

func TestLoop_ErrorWithEmptyResultDefaultsToImmediateRetry(t *testing.T) {
	q := New()
	q.Add(Event{Type: EventAdded, Key: "vm-a", Object: []byte(`{}`)})
	wantErr := errors.New("boom")
	ctrl := &scriptedController{
		kind:       "VM",
		reconciled: make(chan Event, 4),
		results: []ReconcileResult{
			Done(),
			Done(),
		},
		errs: []error{
			wantErr,
			nil,
		},
	}
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		reconcileLoop(ctx, ctrl, q)
		close(done)
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-ctrl.reconciled:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for reconcile %d", i+1)
		}
	}

	q.Shutdown()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconcile loop did not stop after queue shutdown")
	}
}

type scriptedController struct {
	kind       string
	results    []ReconcileResult
	errs       []error
	reconciled chan Event

	mu    sync.Mutex
	calls int
}

func (c *scriptedController) Kind() string { return c.kind }

func (c *scriptedController) Reconcile(ctx context.Context, ev Event) (ReconcileResult, error) {
	c.mu.Lock()
	call := c.calls
	c.calls++
	c.mu.Unlock()

	select {
	case c.reconciled <- ev:
	case <-ctx.Done():
	}

	result := Done()
	if call < len(c.results) {
		result = c.results[call]
	}
	var err error
	if call < len(c.errs) {
		err = c.errs[call]
	}
	return result, err
}

// resumeSource records the startRevision handed to each Watch call and, on the
// first connection only, delivers preset events then closes the channel to force
// a reconnect. Later connections hold open until ctx is cancelled. This lets a
// test assert the feeder advanced its resume cursor: the second Watch must carry
// the last non-empty ResourceVersion seen on the first connection.
type resumeSource struct {
	firstEvents []Event

	mu          sync.Mutex
	startRevs   []string
	connectedCh chan struct{}
}

func (s *resumeSource) Watch(ctx context.Context, kind string, startRevision string) (<-chan Event, error) {
	s.mu.Lock()
	s.startRevs = append(s.startRevs, startRevision)
	conn := len(s.startRevs)
	if s.connectedCh != nil {
		// Signal this connection so the test can sequence its assertions.
		select {
		case s.connectedCh <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()

	ch := make(chan Event)
	go func() {
		defer close(ch)
		if conn == 1 {
			// First connection: deliver preset events then close to force a
			// reconnect that must carry the advanced resume cursor.
			for _, ev := range s.firstEvents {
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
			return
		}
		// Subsequent connections hold open until cancellation.
		<-ctx.Done()
	}()
	return ch, nil
}

func (s *resumeSource) revisions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.startRevs))
	copy(out, s.startRevs)
	return out
}

func TestLoop_AdvancesResumeCursorAcrossReconnect(t *testing.T) {
	// First connection delivers two versioned events; the feeder must resume
	// the second Watch from the last non-empty ResourceVersion ("7"), proving
	// the master↔node half of the resume contract is actually wired (not a
	// write-only field). The trailing event has an empty RV to confirm the
	// cursor keeps the last *non-empty* value rather than regressing to "".
	src := &resumeSource{
		firstEvents: []Event{
			{Type: EventAdded, Key: "vm-a", ResourceVersion: "5", Object: []byte(`{}`)},
			{Type: EventModified, Key: "vm-a", ResourceVersion: "7", Object: []byte(`{}`)},
			{Type: EventModified, Key: "vm-a", ResourceVersion: "", Object: []byte(`{}`)},
		},
		connectedCh: make(chan struct{}, 8),
	}
	ctrl := newFakeController("VM")
	mgr := NewManager(src, []Controller{ctrl})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- mgr.Run(ctx) }()

	// Wait until at least two Watch calls happened (initial + reconnect).
	for i := 0; i < 2; i++ {
		select {
		case <-src.connectedCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for Watch connection %d; revs=%v", i+1, src.revisions())
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

	revs := src.revisions()
	if len(revs) < 2 {
		t.Fatalf("expected at least 2 Watch calls, got %d (%v)", len(revs), revs)
	}
	if revs[0] != "" {
		t.Errorf("first Watch startRevision = %q, want %q (no cursor yet)", revs[0], "")
	}
	if revs[1] != "7" {
		t.Errorf("second Watch startRevision = %q, want %q (last non-empty RV from first connection)", revs[1], "7")
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

// errController returns a fixed error on every reconcile, exercising the loop's
// transient-failure path. It signals each reconcile attempt so a test can stop
// the manager after the failure has been observed and logged.
type errController struct {
	kind       string
	err        error
	reconciled chan struct{}
}

func (c *errController) Kind() string { return c.kind }

func (c *errController) Reconcile(ctx context.Context, ev Event) (ReconcileResult, error) {
	select {
	case c.reconciled <- struct{}{}:
	case <-ctx.Done():
	}
	return Done(), c.err
}

// TestLoop_ReconcileErrorIsLogged proves the reconcile loop does not silently
// swallow a controller error: it records the failure (with kind/key/err) at the
// single chokepoint every controller's transient failure flows through. This is
// the observability fix for the e2e blind spot where a controller failing every
// attempt spun invisibly.
func TestLoop_ReconcileErrorIsLogged(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := zerolog.New(&lockedWriter{w: &buf, mu: &mu})
	ctx, cancel := context.WithCancel(logger.WithContext(context.Background()))
	defer cancel()

	wantErr := errors.New("reconcile boom")
	ctrl := &errController{kind: "VM", err: wantErr, reconciled: make(chan struct{}, 1)}
	src := &fakeSource{events: map[string][]Event{
		"VM": {{Type: EventAdded, Key: "vm-a", Object: []byte(`{}`)}},
	}}
	mgr := NewManager(src, []Controller{ctrl})

	runErr := make(chan error, 1)
	go func() { runErr <- mgr.Run(ctx) }()

	// Wait until the controller has failed at least once so the loop has had a
	// chance to log it.
	select {
	case <-ctrl.reconciled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the controller to reconcile")
	}

	// Poll the buffer until the error line appears (the log write races the
	// reconcile signal slightly), then stop the manager.
	deadline := time.Now().Add(2 * time.Second)
	var logged string
	for time.Now().Before(deadline) {
		mu.Lock()
		logged = buf.String()
		mu.Unlock()
		if strings.Contains(logged, "reconcile boom") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-runErr

	if !strings.Contains(logged, "reconcile boom") {
		t.Fatalf("reconcile error was not logged; log=%q", logged)
	}
	if !strings.Contains(logged, `"kind":"VM"`) {
		t.Fatalf("log missing kind field; log=%q", logged)
	}
	if !strings.Contains(logged, `"key":"vm-a"`) {
		t.Fatalf("log missing key field; log=%q", logged)
	}
}

// lockedWriter serializes concurrent writes to the underlying buffer so the
// test reads a consistent view while the manager's goroutine logs.
type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
