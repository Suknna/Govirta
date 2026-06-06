package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/controlplane/store"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

// watchEventLine is the decoded shape of one newline-delimited watch event. The
// object is kept raw so a test can decode it into the concrete kind it seeded.
type watchEventLine struct {
	Type   store.EventType `json:"type"`
	Object json.RawMessage `json:"object"`
}

// startWatch opens a streaming watch against path on a real httptest.Server
// (httptest.ResponseRecorder does not stream, so a live server is required to
// exercise chunked flushing and client-disconnect teardown). It returns the
// http.Response and a cancel func the test calls to simulate client disconnect.
// The server and its store are torn down via t.Cleanup.
func startWatch(t *testing.T, srv *Server, path string) (*http.Response, context.CancelFunc) {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
	if err != nil {
		cancel()
		t.Fatalf("new watch request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open watch: %v", err)
	}
	return resp, cancel
}

// readWatchLine reads and decodes the next newline-delimited event from a watch
// stream, failing the test if the line cannot be read or decoded before the
// reader hits its deadline.
func readWatchLine(t *testing.T, dec *json.Decoder) watchEventLine {
	t.Helper()
	var ev watchEventLine
	if err := dec.Decode(&ev); err != nil {
		t.Fatalf("decode watch event: %v", err)
	}
	return ev
}

func TestWatchMissingNodeNameReturns400(t *testing.T) {
	srv, _ := newTestServer(t)

	// httptest.ResponseRecorder is enough here: the failure happens before any
	// streaming, so no live flushing is needed.
	rec := doGet(t, srv, "/apis/"+string(metav1.KindVM)+"?watch=true")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

func TestWatchDeliversMatchingNodeNameEvent(t *testing.T) {
	srv, st := newTestServer(t)

	const nodeName = "node-1"

	resp, cancel := startWatch(t, srv, "/apis/"+string(metav1.KindVM)+"?watch=true&nodeName="+nodeName)
	defer cancel()
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close watch body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Seed a VM bound to the watched node directly through the store so the watch
	// observes it as an ADDED event. nodeName lives in metadata for every kind.
	vm := validVM()
	vm.NodeName = nodeName
	data, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("marshal vm: %v", err)
	}
	if _, err := st.Put(context.Background(), storeKey(metav1.KindVM, vm.Name), data, ""); err != nil {
		t.Fatalf("seed vm: %v", err)
	}

	dec := json.NewDecoder(resp.Body)
	ev := readWatchLine(t, dec)
	if ev.Type != store.EventAdded {
		t.Fatalf("event type = %q, want %q", ev.Type, store.EventAdded)
	}

	var got vmv1.VM
	if err := json.Unmarshal(ev.Object, &got); err != nil {
		t.Fatalf("decode event object: %v", err)
	}
	if got.Name != vm.Name {
		t.Fatalf("event object name = %q, want %q", got.Name, vm.Name)
	}
	if got.NodeName != nodeName {
		t.Fatalf("event object nodeName = %q, want %q", got.NodeName, nodeName)
	}
}

func TestWatchFiltersOutNonMatchingNodeName(t *testing.T) {
	srv, st := newTestServer(t)

	const watchedNode = "node-1"
	const otherNode = "node-2"

	resp, cancel := startWatch(t, srv, "/apis/"+string(metav1.KindVM)+"?watch=true&nodeName="+watchedNode)
	defer cancel()
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close watch body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Seed a VM on a different node first (must be filtered out), then one on the
	// watched node (must come through). The first event the stream yields must be
	// the matching one, proving the non-matching object was skipped.
	other := validVM()
	other.Name = "vm-other"
	other.UID = "uid-vm-other"
	other.NodeName = otherNode
	otherData, err := json.Marshal(other)
	if err != nil {
		t.Fatalf("marshal other vm: %v", err)
	}
	if _, err := st.Put(context.Background(), storeKey(metav1.KindVM, other.Name), otherData, ""); err != nil {
		t.Fatalf("seed other vm: %v", err)
	}

	match := validVM()
	match.NodeName = watchedNode
	matchData, err := json.Marshal(match)
	if err != nil {
		t.Fatalf("marshal match vm: %v", err)
	}
	if _, err := st.Put(context.Background(), storeKey(metav1.KindVM, match.Name), matchData, ""); err != nil {
		t.Fatalf("seed match vm: %v", err)
	}

	dec := json.NewDecoder(resp.Body)
	ev := readWatchLine(t, dec)

	var got vmv1.VM
	if err := json.Unmarshal(ev.Object, &got); err != nil {
		t.Fatalf("decode event object: %v", err)
	}
	if got.Name != match.Name {
		t.Fatalf("first delivered event name = %q, want %q (non-matching node must be filtered)", got.Name, match.Name)
	}
	if got.NodeName != watchedNode {
		t.Fatalf("delivered nodeName = %q, want %q", got.NodeName, watchedNode)
	}
}

func TestWatchClientDisconnectStopsHandler(t *testing.T) {
	srv, st := newTestServer(t)

	const nodeName = "node-1"

	resp, cancel := startWatch(t, srv, "/apis/"+string(metav1.KindVM)+"?watch=true&nodeName="+nodeName)
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Confirm the stream is live by delivering one event.
	vm := validVM()
	vm.NodeName = nodeName
	data, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("marshal vm: %v", err)
	}
	if _, err := st.Put(context.Background(), storeKey(metav1.KindVM, vm.Name), data, ""); err != nil {
		t.Fatalf("seed vm: %v", err)
	}

	dec := json.NewDecoder(resp.Body)
	_ = readWatchLine(t, dec)

	// Simulate client disconnect: cancelling the request context tears the
	// connection down. The handler observes ctx.Done() and returns; the store's
	// watcher (which shares the same ctx) deregisters itself. We assert the read
	// side ends rather than hanging, and that the store sheds the watcher so no
	// goroutine leaks (the -race run guards the concurrency claim).
	cancel()
	if err := resp.Body.Close(); err != nil {
		// A cancelled in-flight request commonly surfaces as a closed-connection
		// error on Close; that is expected, not a test failure.
		t.Logf("watch body close after cancel: %v", err)
	}

	// The store deregisters a watcher when its pump goroutine exits on ctx.Done().
	// Poll until the watcher count drops to zero so the assertion is not racing
	// the teardown.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if st.WatcherCount() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("store still has %d watcher(s) after client disconnect; handler/watcher leaked", st.WatcherCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
