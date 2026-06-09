package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

// runTestServer binds a loopback listener on an OS-chosen port and runs the
// Server's serve loop in a goroutine, returning the live base URL and a stop
// func that cancels Run and waits for serve to return. Binding here (rather than
// through Run, which calls net.Listen on s.addr) lets the test learn the real
// port without racing an OS-chosen address through Run.
func runTestServer(t *testing.T) (string, func()) {
	t.Helper()

	st := fake.New()
	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x0000ff)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	alloc := mac.NewAllocator(pool, st)
	seedApplyReferences(t, st, validVM())
	// A real node candidate so the apply VM branch binds rather than 503s, and a
	// noop scheduler so the chosen node is deterministic (the first candidate).
	srv := NewServer(st, alloc, scheduler.NewNoopScheduler(), []string{"node-1"}, "")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.serve(ctx, ln)
	}()

	stop := func() {
		cancel()
		select {
		case err := <-done:
			// A clean ctx-triggered shutdown returns nil; anything else is a bug in
			// the teardown path we want surfaced.
			if err != nil {
				t.Errorf("serve returned error on shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("serve did not return within 5s of ctx cancel; shutdown leaked")
		}
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}

	return "http://" + ln.Addr().String(), stop
}

// TestServerRunRoutesReachable proves Run-style serving makes the full /apis
// surface reachable over a real listener: an apply lands an object and a get
// reads it back. This is the端到端 routing smoke test.
func TestServerRunRoutesReachable(t *testing.T) {
	base, stop := runTestServer(t)
	defer stop()

	vm := validVM()
	body, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("marshal vm: %v", err)
	}

	// Apply route: PUT /apis/VM/{name} must hit Apply and persist (201).
	applyURL := base + "/apis/" + string(metav1.KindVM) + "/" + vm.Name
	req, err := http.NewRequest(http.MethodPut, applyURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new apply request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("apply request: %v", err)
	}
	drainClose(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("apply status = %d, want 201", resp.StatusCode)
	}

	// Get route: GET /apis/VM/{name} must hit Get and return the stored object.
	getResp, err := http.Get(applyURL)
	if err != nil {
		t.Fatalf("get request: %v", err)
	}
	defer func() {
		if err := getResp.Body.Close(); err != nil {
			t.Errorf("close get body: %v", err)
		}
	}()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", getResp.StatusCode)
	}
	var got vmv1.VM
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode get body: %v", err)
	}
	if got.Name != vm.Name {
		t.Fatalf("get object name = %q, want %q", got.Name, vm.Name)
	}
	// The scheduler bound the VM to the sole candidate node at apply time; the
	// stored object must carry that binding (proving apply wired the scheduler).
	if got.NodeName != "node-1" {
		t.Fatalf("stored VM nodeName = %q, want %q (scheduler binding)", got.NodeName, "node-1")
	}
}

// TestServerRunStatusRoute proves the status sub-resource route is wired: after
// an apply, a PATCH .../status updates the object's status and returns 200.
func TestServerRunStatusRoute(t *testing.T) {
	base, stop := runTestServer(t)
	defer stop()

	vm := validVM()
	body, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("marshal vm: %v", err)
	}
	applyURL := base + "/apis/" + string(metav1.KindVM) + "/" + vm.Name
	req, err := http.NewRequest(http.MethodPut, applyURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new apply request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("apply request: %v", err)
	}
	drainClose(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("apply status = %d, want 201", resp.StatusCode)
	}

	statusBody, err := json.Marshal(vmv1.VMStatus{Phase: vmv1.VMPhaseRunning})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	statusReq, err := http.NewRequest(http.MethodPatch, applyURL+"/status", bytes.NewReader(statusBody))
	if err != nil {
		t.Fatalf("new status request: %v", err)
	}
	statusResp, err := http.DefaultClient.Do(statusReq)
	if err != nil {
		t.Fatalf("status request: %v", err)
	}
	drainClose(t, statusResp)
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("status patch = %d, want 200", statusResp.StatusCode)
	}
}

// TestServerRunWatchRoute proves the list/watch route is wired over a real
// listener: opening a watch stream returns 200 and the chunked headers, and
// cancelling the request tears the stream down (no hang).
func TestServerRunWatchRoute(t *testing.T) {
	base, stop := runTestServer(t)
	defer stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchURL := base + "/apis/" + string(metav1.KindVM) + "?watch=true&nodeName=node-1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, watchURL, nil)
	if err != nil {
		t.Fatalf("new watch request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("watch request: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			// A cancelled in-flight watch commonly surfaces a closed-connection
			// error on Close; that is expected, not a failure.
			t.Logf("close watch body after cancel: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watch status = %d, want 200", resp.StatusCode)
	}
	// Cancelling the request must end the stream rather than hang; the handler
	// observes ctx.Done and returns.
	cancel()
}

// TestServerRunUnknownKind404 confirms an unrouted kind reaches Apply and is
// classified 404 (dispatch reaches the handler, not a mux miss).
func TestServerRunUnknownKind404(t *testing.T) {
	base, stop := runTestServer(t)
	defer stop()

	req, err := http.NewRequest(http.MethodPut, base+"/apis/Widget/w-a", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	drainClose(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestServerRunCtxCancelShutsDown proves ctx cancellation triggers a graceful
// Shutdown and serve returns nil. The stop func (which cancels and asserts a nil
// return within a deadline) carries the assertion; reaching it without the
// deadline firing proves Shutdown was wired to ctx.Done.
func TestServerRunCtxCancelShutsDown(t *testing.T) {
	st := fake.New()
	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x0000ff)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	alloc := mac.NewAllocator(pool, st)
	srv := NewServer(st, alloc, scheduler.NewNoopScheduler(), []string{"node-1"}, "")
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.serve(ctx, ln)
	}()

	// Give serve a beat to enter Serve, then cancel and require a prompt nil return.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v on ctx cancel, want nil (graceful shutdown)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("serve did not return within 5s of ctx cancel")
	}
}

// TestServerRunListenError proves Run propagates a bind failure instead of
// swallowing it: binding a port already held by another listener must error.
func TestServerRunListenError(t *testing.T) {
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x0000ff)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	alloc := mac.NewAllocator(pool, st)

	// Occupy a port, then point Run at it so net.Listen fails.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() {
		if err := occupied.Close(); err != nil {
			t.Errorf("close occupied listener: %v", err)
		}
	}()

	srv := NewServer(st, alloc, scheduler.NewNoopScheduler(), []string{"node-1"}, occupied.Addr().String())
	err = srv.Run(context.Background())
	if err == nil {
		t.Fatalf("Run returned nil, want a bind error on an occupied port")
	}
}

// drainClose fully reads and closes a response body so the underlying connection
// can be reused and no read-side resource is leaked.
func drainClose(t *testing.T, resp *http.Response) {
	t.Helper()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Errorf("drain response body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}
