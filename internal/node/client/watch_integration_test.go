package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/controlplane/apiserver"
	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	"github.com/suknna/govirta/internal/node/controller"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
)

// TestWatchSourceReceivesEveryRapidLiveEventThroughRealAPIServer is the
// integration reproduction for the e2e "first object on a stream is silently
// lost" defect. The store contract (etcd) and the apiserver handler (50×+race)
// both deliver every rapid live event in isolation, so this test exercises the
// one seam they skip: the real WatchSource HTTP client consuming a real
// apiserver chunked stream, in the exact e2e timing — a watch opened on an empty
// store, then several same-kind same-node objects applied in rapid succession
// through the real Apply handler. Every applied object must arrive as an event.
func TestWatchSourceReceivesEveryRapidLiveEventThroughRealAPIServer(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	src := NewWatchSource(ts.URL, ts.Client(), nodeName)
	ch, err := src.Watch(ctx, string(metav1.KindStoragePool), "")
	if err != nil {
		t.Fatalf("open watch: %v", err)
	}

	// Apply several StoragePool objects AFTER the watch is open, mimicking the
	// e2e timing where govirtlet's watch is established before the manifests are
	// applied. Every one must surface on the stream.
	names := []string{"pool-block", "pool-file", "pool-extra"}
	for _, name := range names {
		applyStoragePool(t, ts, nodeName, name)
	}

	got := map[string]bool{}
	for i := 0; i < len(names); i++ {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("watch channel closed after %d/%d events; got=%v", i, len(names), got)
			}
			if ev.Type != controller.EventAdded {
				t.Fatalf("event %d type = %q, want ADDED", i, ev.Type)
			}
			got[ev.Key] = true
		case <-ctx.Done():
			t.Fatalf("timed out after %d/%d events; got=%v", i, len(names), got)
		}
	}
	for _, name := range names {
		if !got[name] {
			t.Fatalf("StoragePool %q was applied but never delivered on the watch; got=%v", name, got)
		}
	}
}

// applyStoragePool POSTs a minimal valid StoragePool bound to nodeName through the
// real apiserver Apply route, exactly as govirtctl does.
func applyStoragePool(t *testing.T, ts *httptest.Server, nodeName, name string) {
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
	body, err := json.Marshal(sp)
	if err != nil {
		t.Fatalf("marshal %q: %v", name, err)
	}
	url := fmt.Sprintf("%s/apis/%s/%s", ts.URL, metav1.KindStoragePool, name)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build apply request %q: %v", name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("apply %q: %v", name, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Fatalf("close apply response %q: %v", name, cerr)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("apply %q: status %d", name, resp.StatusCode)
	}
}
