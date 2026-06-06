package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/node/controller"
)

// flushingWatchServer wires an httptest server whose /apis/{kind} handler writes
// the given newline-delimited JSON lines, flushing after each so the client
// observes a live chunked stream rather than one buffered blob. It also records
// the query the client sent so a test can assert nodeName/resourceVersion were
// encoded. hold, when non-nil, blocks the handler from returning (keeping the
// stream open) until the test closes it — used to exercise ctx cancellation.
func flushingWatchServer(t *testing.T, lines []string, gotQuery *url.Values, hold <-chan struct{}) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /apis/{kind}", func(w http.ResponseWriter, r *http.Request) {
		if gotQuery != nil {
			*gotQuery = r.URL.Query()
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flush", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		for _, line := range lines {
			if _, err := fmt.Fprint(w, line); err != nil {
				return
			}
			flusher.Flush()
		}

		if hold != nil {
			// Keep the response open so the stream does not EOF; the test drives
			// teardown via ctx cancellation, then releases the handler.
			select {
			case <-hold:
			case <-r.Context().Done():
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestWatchTranslatesEvents(t *testing.T) {
	// Three newline-delimited wire events: the client must translate each into a
	// controller.Event, projecting metadata.name → Key and
	// metadata.resourceVersion → ResourceVersion while forwarding the raw object.
	lines := []string{
		`{"type":"ADDED","object":{"metadata":{"name":"vm-a","resourceVersion":"10"}}}` + "\n",
		`{"type":"MODIFIED","object":{"metadata":{"name":"vm-a","resourceVersion":"12"}}}` + "\n",
		`{"type":"DELETED","object":{"metadata":{"name":"vm-b","resourceVersion":"15"}}}` + "\n",
	}
	srv := flushingWatchServer(t, lines, nil, nil)

	src := NewWatchSource(srv.URL, srv.Client(), "node-a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := src.Watch(ctx, "VM", "")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	want := []controller.Event{
		{Type: controller.EventAdded, Key: "vm-a", ResourceVersion: "10"},
		{Type: controller.EventModified, Key: "vm-a", ResourceVersion: "12"},
		{Type: controller.EventDeleted, Key: "vm-b", ResourceVersion: "15"},
	}

	for i, w := range want {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("event %d: channel closed early", i)
			}
			if ev.Type != w.Type {
				t.Errorf("event %d Type = %q, want %q", i, ev.Type, w.Type)
			}
			if ev.Key != w.Key {
				t.Errorf("event %d Key = %q, want %q", i, ev.Key, w.Key)
			}
			if ev.ResourceVersion != w.ResourceVersion {
				t.Errorf("event %d ResourceVersion = %q, want %q", i, ev.ResourceVersion, w.ResourceVersion)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d: timed out waiting for event", i)
		}
	}

	// After the server's lines are exhausted it returns, the stream EOFs, and the
	// source closes the channel — no leaked goroutine.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed after stream end")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close after stream end")
	}
}

func TestWatchObjectForwardedVerbatim(t *testing.T) {
	// The raw object bytes must reach Event.Object untouched so the controller can
	// decode the concrete apis object itself.
	obj := `{"metadata":{"name":"vm-a","resourceVersion":"7"},"spec":{"cpus":2}}`
	lines := []string{`{"type":"ADDED","object":` + obj + `}` + "\n"}
	srv := flushingWatchServer(t, lines, nil, nil)

	src := NewWatchSource(srv.URL, srv.Client(), "node-a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := src.Watch(ctx, "VM", "")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before first event")
		}
		if string(ev.Object) != obj {
			t.Errorf("Event.Object = %q, want verbatim %q", ev.Object, obj)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestWatchSendsNodeNameAndResourceVersion(t *testing.T) {
	// The watch URL must carry watch=true, the injected nodeName, and the caller's
	// startRevision as resourceVersion so the master can scope and resume the stream.
	var gotQuery url.Values
	srv := flushingWatchServer(t, nil, &gotQuery, nil)

	src := NewWatchSource(srv.URL, srv.Client(), "node-a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := src.Watch(ctx, "VM", "42")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	// Drain to completion so the handler has certainly recorded the query.
	for range ch {
	}

	if got := gotQuery.Get("watch"); got != "true" {
		t.Errorf("query watch = %q, want \"true\"", got)
	}
	if got := gotQuery.Get("nodeName"); got != "node-a" {
		t.Errorf("query nodeName = %q, want \"node-a\"", got)
	}
	if got := gotQuery.Get("resourceVersion"); got != "42" {
		t.Errorf("query resourceVersion = %q, want \"42\"", got)
	}
}

func TestWatchCancelClosesChannel(t *testing.T) {
	// With the server holding the stream open, cancelling ctx must make the source
	// stop, close the channel, and let its goroutine exit (no leak). -race plus the
	// channel-close assertion guards against a lingering goroutine.
	hold := make(chan struct{})
	lines := []string{
		`{"type":"ADDED","object":{"metadata":{"name":"vm-a","resourceVersion":"1"}}}` + "\n",
	}
	srv := flushingWatchServer(t, lines, nil, hold)
	t.Cleanup(func() { close(hold) })

	src := NewWatchSource(srv.URL, srv.Client(), "node-a")
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := src.Watch(ctx, "VM", "")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	// Consume the first event so we know the stream is live and the goroutine is
	// parked on the next decode/select.
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before first event")
		}
		if ev.Key != "vm-a" {
			t.Fatalf("first event Key = %q, want vm-a", ev.Key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first event")
	}

	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			// A buffered-in-flight event may arrive first; drain until closed.
			for range ch {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close after ctx cancel")
	}
}

func TestWatchNon200Errors(t *testing.T) {
	// A pre-stream non-200 must come back as a wrapped error carrying the server's
	// body, with no channel returned.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "watch requires nodeName", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	src := NewWatchSource(srv.URL, srv.Client(), "node-a")
	_, err := src.Watch(context.Background(), "VM", "")
	if err == nil {
		t.Fatal("Watch against 400 returned nil error")
	}
	if want := "watch requires nodeName"; !contains(err.Error(), want) {
		t.Fatalf("Watch error %q does not include server body %q", err, want)
	}
}

func TestWatchNilHTTPClientUsesDefault(t *testing.T) {
	// hc==nil falls back to http.DefaultClient, matching New.
	src := NewWatchSource("http://example.invalid", nil, "node-a")
	if src.http != http.DefaultClient {
		t.Fatal("NewWatchSource with nil hc did not adopt http.DefaultClient")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
