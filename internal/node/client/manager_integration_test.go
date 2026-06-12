package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/controlplane/apiserver"
	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
)

// recordingController records the keys it receives for one kind. It mirrors the
// real controllers' shape (Kind + Reconcile) but does no work — the test only
// cares which ADDED events reach the reconcile loop, which is the exact thing
// the e2e defect lost (pool-block and net-e2e, each the first object on its
// stream, never reached their controller).
type recordingController struct {
	kind string
	mu   sync.Mutex
	seen map[string]bool
}

func newRecordingController(kind string) *recordingController {
	return &recordingController{kind: kind, seen: map[string]bool{}}
}

func (c *recordingController) Kind() string { return c.kind }

func (c *recordingController) Reconcile(ctx context.Context, ev controller.Event) (controller.ReconcileResult, error) {
	c.mu.Lock()
	c.seen[ev.Key] = true
	c.mu.Unlock()
	return controller.Done(), nil
}

func (c *recordingController) sawKey(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[key]
}

// TestManagerDeliversFirstObjectOnEveryConcurrentStream is the decisive node-side
// integration reproduction for the third e2e defect. It runs the real Manager
// (one feeder goroutine per controller, all concurrent) over the real WatchSource
// HTTP client against the real apiserver, in the exact e2e timing: the manager
// starts and opens every kind's watch on an empty store, THEN objects are applied
// in dependency order through the real Apply route. Every object — especially the
// first one on each stream (pool-block on StoragePool, net-e2e on Network), which
// the e2e run silently lost — must reach its controller.
//
// The store contract, the apiserver handler, and a single WatchSource were each
// proven correct in isolation; the seam none of them exercised is several
// concurrent watches driven by the real Manager sharing one HTTP client, which is
// exactly what govirtlet does.
func TestManagerDeliversFirstObjectOnEveryConcurrentStream(t *testing.T) {
	const nodeName = "node0"

	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x00ffff)
	if err != nil {
		t.Fatalf("new mac pool: %v", err)
	}
	alloc := mac.NewAllocator(pool, st)
	srv := apiserver.NewServer(st, alloc, scheduler.NewNoopScheduler(), []string{nodeName}, "")

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	src := client.NewWatchSource(ts.URL, ts.Client(), nodeName)

	// One recording controller per kind, exactly like the real six-controller
	// manager. We exercise the two kinds whose first/only object the e2e run
	// lost (StoragePool, Network) plus a third to keep the concurrency realistic.
	poolCtrl := newRecordingController(string(metav1.KindStoragePool))
	netCtrl := newRecordingController(string(metav1.KindNetwork))
	mgr := controller.NewManager(src, []controller.Controller{poolCtrl, netCtrl})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- mgr.Run(ctx) }()

	// Give the manager a moment to open both watches on the empty store, then
	// apply each kind's objects — the first object on each stream is the one the
	// e2e defect lost. pool-block is applied before pool-file, matching e2e.
	time.Sleep(200 * time.Millisecond)
	applyPool(t, ts, nodeName, "pool-block")
	applyPool(t, ts, nodeName, "pool-file")
	applyNetwork(t, ts, nodeName, "net-e2e")

	// Poll until every applied object has reached its controller, or fail with
	// exactly which first-on-stream object was lost.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if poolCtrl.sawKey("pool-block") && poolCtrl.sawKey("pool-file") && netCtrl.sawKey("net-e2e") {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("not every object reached its controller: pool-block=%v pool-file=%v net-e2e=%v",
				poolCtrl.sawKey("pool-block"), poolCtrl.sawKey("pool-file"), netCtrl.sawKey("net-e2e"))
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	if err := <-runErr; err != nil && err != context.Canceled {
		t.Fatalf("manager Run returned %v, want context.Canceled", err)
	}
}

func applyPool(t *testing.T, ts *httptest.Server, nodeName, name string) {
	t.Helper()
	sp := storagepoolv1.StoragePool{
		TypeMeta: metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindStoragePool},
		ObjectMeta: metav1.ObjectMeta{
			Name:     name,
			UID:      "uid-" + name,
			NodeName: nodeName,
		},
		Spec: storagepoolv1.StoragePoolSpec{
			Backend:       storagepoolv1.BackendLocalBlock,
			Type:          storagepoolv1.PoolTypeBlock,
			StorageRoot:   "/var/lib/govirta/" + name,
			CapacityBytes: 1 << 30,
		},
	}
	applyObject(t, ts, metav1.KindStoragePool, name, sp)
}

func applyNetwork(t *testing.T, ts *httptest.Server, nodeName, name string) {
	t.Helper()
	nw := networkv1.Network{
		TypeMeta: metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindNetwork},
		ObjectMeta: metav1.ObjectMeta{
			Name:     name,
			UID:      "uid-" + name,
			NodeName: nodeName,
		},
		Spec: networkv1.NetworkSpec{
			BridgeName:      "govirta0",
			Subnet:          "192.168.100.0/24",
			GatewayCIDR:     "192.168.100.1/24",
			DHCPRangeStart:  "192.168.100.10",
			DHCPRangeEnd:    "192.168.100.100",
			EgressInterface: "eth0",
		},
	}
	applyObject(t, ts, metav1.KindNetwork, name, nw)
}

func applyObject(t *testing.T, ts *httptest.Server, kind metav1.Kind, name string, obj any) {
	t.Helper()
	body, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal %s/%s: %v", kind, name, err)
	}
	url := fmt.Sprintf("%s/apis/%s/%s", ts.URL, kind, name)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build apply %s/%s: %v", kind, name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("apply %s/%s: %v", kind, name, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Fatalf("close apply response %s/%s: %v", kind, name, cerr)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("apply %s/%s: status %d", kind, name, resp.StatusCode)
	}
}
