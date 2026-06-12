package etcd_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/suknna/govirta/internal/controlplane/store"
	etcdstore "github.com/suknna/govirta/internal/controlplane/store/etcd"
)

// TestEtcdStoreContract runs the shared Store behavioral suite from Task 1
// against the real etcd-backed store. It requires a reachable etcd cluster, so
// it is gated on GOVIRTA_ETCD_ENDPOINTS (comma-separated). When the variable is
// unset the test skips rather than fails, so a plain `go test ./...` on a
// machine without etcd stays green; CI / local integration runs set the
// variable to exercise the contract against real etcd.
func TestEtcdStoreContract(t *testing.T) {
	endpointsEnv := os.Getenv("GOVIRTA_ETCD_ENDPOINTS")
	if endpointsEnv == "" {
		t.Skip("set GOVIRTA_ETCD_ENDPOINTS to run")
	}
	endpoints := strings.Split(endpointsEnv, ",")

	// The contract suite reuses the same keys (e.g. /govirta/pod/a) across many
	// subtests, each calling newStore. To keep subtests and repeated runs from
	// colliding on a shared real cluster, every newStore call gets a unique key
	// prefix and clears it before handing back the store. Because RunStoreContract
	// drives keys under /govirta/, we redirect those onto a per-store namespace
	// via a thin prefixing wrapper.
	newStore := func() store.Store {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		s, err := etcdstore.New(ctx, clientv3.Config{
			Endpoints:   endpoints,
			DialTimeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("etcd New: unexpected error: %v", err)
		}

		// Unique namespace per store instance so subtests do not interfere and a
		// re-run does not see stale keys from a previous run.
		ns := fmt.Sprintf("/govirta-test/%d/", time.Now().UnixNano())
		w := &prefixStore{inner: s, ns: ns}

		// Clean the namespace up front (defensive against a crashed prior run).
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := w.deleteNamespace(cleanupCtx); err != nil {
			t.Fatalf("etcd cleanup: unexpected error: %v", err)
		}
		return w
	}

	store.RunStoreContract(t, newStore)
}

// prefixStore wraps an etcd store.Store and rewrites every "/govirta/" key onto
// a unique per-instance namespace, then rewrites results back. This isolates
// the contract suite's fixed keys across subtests and repeated runs on a shared
// real cluster without changing the contract or the production store.
type prefixStore struct {
	inner store.Store
	ns    string
}

const contractRoot = "/govirta/"

// toInner maps a contract-facing key onto the namespaced key.
func (p *prefixStore) toInner(key string) string {
	if strings.HasPrefix(key, contractRoot) {
		return p.ns + strings.TrimPrefix(key, contractRoot)
	}
	return p.ns + strings.TrimPrefix(key, "/")
}

// toOuter maps a namespaced key back to the contract-facing key.
func (p *prefixStore) toOuter(key string) string {
	return contractRoot + strings.TrimPrefix(key, p.ns)
}

func (p *prefixStore) Put(ctx context.Context, key string, value []byte, expectedVersion string) (store.RawObject, error) {
	obj, err := p.inner.Put(ctx, p.toInner(key), value, expectedVersion)
	if err != nil {
		return store.RawObject{}, err
	}
	obj.Key = p.toOuter(obj.Key)
	return obj, nil
}

func (p *prefixStore) Get(ctx context.Context, key string) (store.RawObject, error) {
	obj, err := p.inner.Get(ctx, p.toInner(key))
	if err != nil {
		return store.RawObject{}, err
	}
	obj.Key = p.toOuter(obj.Key)
	return obj, nil
}

func (p *prefixStore) List(ctx context.Context, prefix string) ([]store.RawObject, error) {
	objs, err := p.inner.List(ctx, p.toInner(prefix))
	if err != nil {
		return nil, err
	}
	for i := range objs {
		objs[i].Key = p.toOuter(objs[i].Key)
	}
	return objs, nil
}

func (p *prefixStore) Delete(ctx context.Context, key string) error {
	return p.inner.Delete(ctx, p.toInner(key))
}

func (p *prefixStore) DeleteIfVersion(ctx context.Context, key string, expectedVersion string) error {
	return p.inner.DeleteIfVersion(ctx, p.toInner(key), expectedVersion)
}

func (p *prefixStore) Watch(ctx context.Context, prefix string, startRevision string) (<-chan store.WatchEvent, error) {
	inCh, err := p.inner.Watch(ctx, p.toInner(prefix), startRevision)
	if err != nil {
		return nil, err
	}
	outCh := make(chan store.WatchEvent)
	go func() {
		defer close(outCh)
		for ev := range inCh {
			ev.Object.Key = p.toOuter(ev.Object.Key)
			select {
			case outCh <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return outCh, nil
}

func (p *prefixStore) Close() error {
	return p.inner.Close()
}

// deleteNamespace removes every key under the instance namespace so a store
// starts empty, as RunStoreContract requires.
func (p *prefixStore) deleteNamespace(ctx context.Context) error {
	objs, err := p.inner.List(ctx, p.ns)
	if err != nil {
		return err
	}
	for _, o := range objs {
		if err := p.inner.Delete(ctx, o.Key); err != nil {
			return err
		}
	}
	return nil
}

// TestEtcdWatchEmptySnapshotThenRapidLiveEvents reproduces the exact e2e timing
// the node controllers hit: a watch opens on an EMPTY prefix (the snapshot is
// empty because govirtlet starts before any manifest is applied), then several
// objects are Put in quick succession on that same stream. Every one must be
// delivered as a live ADDED event. This is the combination the contract suite
// did not cover (its list-then-watch case has a non-empty snapshot and only one
// live event), and it is the precise shape of the third e2e defect where the
// first live event on a freshly-opened empty-snapshot watch went missing.
//
// Gated on GOVIRTA_ETCD_ENDPOINTS like the contract test.
func TestEtcdWatchEmptySnapshotThenRapidLiveEvents(t *testing.T) {
	endpointsEnv := os.Getenv("GOVIRTA_ETCD_ENDPOINTS")
	if endpointsEnv == "" {
		t.Skip("set GOVIRTA_ETCD_ENDPOINTS to run")
	}
	endpoints := strings.Split(endpointsEnv, ",")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s, err := etcdstore.New(ctx, clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("etcd New: %v", err)
	}
	defer s.Close()

	ns := fmt.Sprintf("/govirta-rapid/%d/", time.Now().UnixNano())
	ps := &prefixStore{inner: s, ns: ns}
	if err := ps.deleteNamespace(ctx); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Open the watch on the empty prefix (empty snapshot, live path follows).
	ch, err := ps.Watch(ctx, "/govirta/pod/", "")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Apply several objects in quick succession on the same stream.
	const n = 5
	names := make([]string, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("/govirta/pod/p%d", i)
		names[i] = name
		if _, err := ps.Put(ctx, name, []byte(fmt.Sprintf(`{"v":%d}`, i)), ""); err != nil {
			t.Fatalf("Put %s: %v", name, err)
		}
	}

	// Every applied object must arrive as ADDED. Collect by key (etcd may batch
	// or reorder within a revision window, so assert set-equality, not index).
	got := map[string]bool{}
	for i := 0; i < n; i++ {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("watch channel closed after %d/%d events; got=%v", i, n, got)
			}
			if ev.Type != store.EventAdded {
				t.Fatalf("event %d type = %q, want ADDED (key=%s)", i, ev.Type, ev.Object.Key)
			}
			got[ev.Object.Key] = true
		case <-ctx.Done():
			t.Fatalf("timed out after %d/%d events; got=%v", i, n, got)
		}
	}
	for _, name := range names {
		if !got[name] {
			t.Fatalf("object %s never delivered; got=%v", name, got)
		}
	}
}
