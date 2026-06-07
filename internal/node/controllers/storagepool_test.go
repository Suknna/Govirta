package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/storage/pool"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
)

// fakePoolRegistrar records RegisterPool calls and serves canned usage/errors.
// It is faithful to *pool.Service: RegisterPool reports an idempotent
// ErrPoolAlreadyExists when configured, and GetPoolUsage honours ctx
// cancellation before returning.
type fakePoolRegistrar struct {
	registered     []*pool.Pool
	registerErr    error
	usage          pool.Usage
	usageErr       error
	usageCallCount int
}

func (f *fakePoolRegistrar) RegisterPool(p *pool.Pool) error {
	f.registered = append(f.registered, p)
	return f.registerErr
}

func (f *fakePoolRegistrar) GetPoolUsage(ctx context.Context, name string) (pool.Usage, error) {
	if err := ctx.Err(); err != nil {
		return pool.Usage{}, err
	}
	f.usageCallCount++
	if f.usageErr != nil {
		return pool.Usage{}, f.usageErr
	}
	return f.usage, nil
}

// fakeStatusReporter captures the last status JSON patched and honours ctx
// cancellation, faithful to *client.Client.
type fakeStatusReporter struct {
	patches    []capturedPatch
	patchErr   error
	patchCalls int
}

type capturedPatch struct {
	kind   string
	name   string
	status storagepoolv1.StoragePoolStatus
}

func (f *fakeStatusReporter) PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.patchCalls++
	if f.patchErr != nil {
		return nil, f.patchErr
	}
	var decoded storagepoolv1.StoragePoolStatus
	if err := json.Unmarshal(status, &decoded); err != nil {
		return nil, err
	}
	f.patches = append(f.patches, capturedPatch{kind: kind, name: name, status: decoded})
	return status, nil
}

func newStoragePoolEvent(t *testing.T, evType controller.EventType, sp storagepoolv1.StoragePool) controller.Event {
	t.Helper()
	raw, err := json.Marshal(sp)
	if err != nil {
		t.Fatalf("marshal StoragePool: %v", err)
	}
	return controller.Event{Type: evType, Key: sp.Name, Object: raw}
}

func validStoragePool(name string) storagepoolv1.StoragePool {
	return storagepoolv1.StoragePool{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindStoragePool},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name},
		Spec: storagepoolv1.StoragePoolSpec{
			Backend:       storagepoolv1.BackendLocalBlock,
			Type:          storagepoolv1.PoolTypeBlock,
			StorageRoot:   "/var/lib/govirta",
			CapacityBytes: 10 << 30,
		},
	}
}

func TestStoragePoolReconcileAddedReady(t *testing.T) {
	pools := &fakePoolRegistrar{usage: pool.Usage{AllocatedBytes: 4096}}
	reporter := &fakeStatusReporter{}
	c := NewStoragePoolController(pools, reporter)

	sp := validStoragePool("pool-a")
	ev := newStoragePoolEvent(t, controller.EventAdded, sp)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}

	if len(pools.registered) != 1 {
		t.Fatalf("RegisterPool called %d times, want 1", len(pools.registered))
	}
	got := pools.registered[0]
	if got.Config.Name != "pool-a" {
		t.Errorf("registered pool name = %q, want %q", got.Config.Name, "pool-a")
	}
	if got.Config.Backend != pool.BackendLocalBlock {
		t.Errorf("registered backend = %q, want %q", got.Config.Backend, pool.BackendLocalBlock)
	}
	if got.Config.Type != pool.PoolTypeBlock {
		t.Errorf("registered type = %q, want %q", got.Config.Type, pool.PoolTypeBlock)
	}
	if got.Config.StorageRoot != "/var/lib/govirta" {
		t.Errorf("registered storageRoot = %q, want %q", got.Config.StorageRoot, "/var/lib/govirta")
	}
	if got.Config.CapacityBytes != 10<<30 {
		t.Errorf("registered capacity = %d, want %d", got.Config.CapacityBytes, int64(10<<30))
	}

	if len(reporter.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(reporter.patches))
	}
	patch := reporter.patches[0]
	if patch.kind != string(metav1.KindStoragePool) {
		t.Errorf("patch kind = %q, want %q", patch.kind, metav1.KindStoragePool)
	}
	if patch.name != "pool-a" {
		t.Errorf("patch name = %q, want %q", patch.name, "pool-a")
	}
	if patch.status.Phase != storagepoolv1.PoolPhaseReady {
		t.Errorf("patch phase = %q, want %q", patch.status.Phase, storagepoolv1.PoolPhaseReady)
	}
	if patch.status.AllocatedBytes != 4096 {
		t.Errorf("patch allocatedBytes = %d, want 4096", patch.status.AllocatedBytes)
	}
	if patch.status.Message != "" {
		t.Errorf("patch message = %q, want empty on ready", patch.status.Message)
	}
}

// TestStoragePoolReconcileNoOpWhenStatusAlreadyDesired proves the level-triggered
// no-op guard: when the watched object already carries the exact status the
// controller would write (phase ready + matching allocatedBytes), Reconcile must
// register/read usage as usual but skip the PATCH entirely. Without this guard the
// PATCH produced a MODIFIED watch event that re-triggered Reconcile, spinning a
// status→MODIFIED→reconcile→PATCH feedback loop (observed ~70 reconciles/sec in
// e2e). Zero PATCH calls here is the regression assertion.
func TestStoragePoolReconcileNoOpWhenStatusAlreadyDesired(t *testing.T) {
	pools := &fakePoolRegistrar{usage: pool.Usage{AllocatedBytes: 4096}}
	reporter := &fakeStatusReporter{}
	c := NewStoragePoolController(pools, reporter)

	sp := validStoragePool("pool-steady")
	// The object arrives already carrying the status the controller would derive
	// (ready + allocatedBytes 4096), mimicking the MODIFIED event a prior PATCH
	// produced. The controller must recognize observed == desired and not PATCH.
	sp.Status = storagepoolv1.StoragePoolStatus{
		Phase:          storagepoolv1.PoolPhaseReady,
		AllocatedBytes: 4096,
	}
	ev := newStoragePoolEvent(t, controller.EventModified, sp)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}
	if reporter.patchCalls != 0 {
		t.Fatalf("PatchStatus called %d times, want 0 when observed status already equals desired", reporter.patchCalls)
	}
}

func TestStoragePoolReconcileAlreadyRegisteredIsIdempotent(t *testing.T) {
	pools := &fakePoolRegistrar{registerErr: pool.ErrPoolAlreadyExists, usage: pool.Usage{AllocatedBytes: 1 << 20}}
	reporter := &fakeStatusReporter{}
	c := NewStoragePoolController(pools, reporter)

	ev := newStoragePoolEvent(t, controller.EventModified, validStoragePool("pool-idem"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for already-registered pool", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}
	if pools.usageCallCount != 1 {
		t.Fatalf("GetPoolUsage called %d times, want 1", pools.usageCallCount)
	}
	if len(reporter.patches) != 1 || reporter.patches[0].status.Phase != storagepoolv1.PoolPhaseReady {
		t.Fatalf("expected one ready patch, got %+v", reporter.patches)
	}
}

func TestStoragePoolReconcileRegisterFailureRequeues(t *testing.T) {
	registerErr := errors.New("backend offline")
	pools := &fakePoolRegistrar{registerErr: registerErr}
	reporter := &fakeStatusReporter{}
	c := NewStoragePoolController(pools, reporter)

	ev := newStoragePoolEvent(t, controller.EventAdded, validStoragePool("pool-fail"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err == nil {
		t.Fatalf("Reconcile() error = nil, want non-nil on register failure")
	}
	if !errors.Is(err, registerErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, registerErr)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on register failure")
	}

	if pools.usageCallCount != 0 {
		t.Fatalf("GetPoolUsage called %d times, want 0 when register fails", pools.usageCallCount)
	}
	if len(reporter.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(reporter.patches))
	}
	patch := reporter.patches[0]
	if patch.status.Phase != storagepoolv1.PoolPhaseFailed {
		t.Errorf("patch phase = %q, want %q", patch.status.Phase, storagepoolv1.PoolPhaseFailed)
	}
	if patch.status.Message == "" {
		t.Errorf("patch message empty, want failure cause")
	}
	if patch.status.AllocatedBytes != 0 {
		t.Errorf("patch allocatedBytes = %d, want 0 on failure", patch.status.AllocatedBytes)
	}
}

func TestStoragePoolReconcileUsageFailureRequeues(t *testing.T) {
	usageErr := errors.New("usage probe failed")
	pools := &fakePoolRegistrar{usageErr: usageErr}
	reporter := &fakeStatusReporter{}
	c := NewStoragePoolController(pools, reporter)

	ev := newStoragePoolEvent(t, controller.EventAdded, validStoragePool("pool-usage"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err == nil || !errors.Is(err, usageErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, usageErr)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on usage failure")
	}
	if len(pools.registered) != 1 {
		t.Fatalf("RegisterPool called %d times, want 1", len(pools.registered))
	}
	if len(reporter.patches) != 1 || reporter.patches[0].status.Phase != storagepoolv1.PoolPhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reporter.patches)
	}
}

func TestStoragePoolReconcileInvalidBackendIsPermanentFailure(t *testing.T) {
	pools := &fakePoolRegistrar{}
	reporter := &fakeStatusReporter{}
	c := NewStoragePoolController(pools, reporter)

	sp := validStoragePool("pool-bad")
	sp.Spec.Backend = storagepoolv1.BackendType("ceph-magic")
	ev := newStoragePoolEvent(t, controller.EventAdded, sp)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for permanent mapping failure", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false for permanent mapping failure")
	}
	if len(pools.registered) != 0 {
		t.Fatalf("RegisterPool called %d times, want 0 on mapping failure", len(pools.registered))
	}
	if len(reporter.patches) != 1 || reporter.patches[0].status.Phase != storagepoolv1.PoolPhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reporter.patches)
	}
}

func TestStoragePoolReconcileDeletedIsNoOp(t *testing.T) {
	pools := &fakePoolRegistrar{}
	reporter := &fakeStatusReporter{}
	c := NewStoragePoolController(pools, reporter)

	ev := newStoragePoolEvent(t, controller.EventDeleted, validStoragePool("pool-del"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}
	if len(pools.registered) != 0 {
		t.Errorf("RegisterPool called %d times on DELETED, want 0", len(pools.registered))
	}
	if reporter.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times on DELETED, want 0", reporter.patchCalls)
	}
}

func TestStoragePoolReconcileContextCancelledPropagates(t *testing.T) {
	pools := &fakePoolRegistrar{usage: pool.Usage{AllocatedBytes: 1}}
	reporter := &fakeStatusReporter{}
	c := NewStoragePoolController(pools, reporter)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ev := newStoragePoolEvent(t, controller.EventAdded, validStoragePool("pool-ctx"))

	requeue, err := c.Reconcile(ctx, ev)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconcile() error = %v, want wrapped context.Canceled", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false when context cancelled before work")
	}
	if len(pools.registered) != 0 {
		t.Errorf("RegisterPool called %d times after ctx cancel, want 0", len(pools.registered))
	}
	if reporter.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times after ctx cancel, want 0", reporter.patchCalls)
	}
}

// TestStoragePoolReconcileBlockPoolAttachesDriver proves the block backend wires
// a non-nil block.Driver onto the registered pool, since pool.Service.RegisterPool
// rejects a block pool without one. (Driver gap fix: buildPool now attaches the
// host-local driver rather than leaving it nil.)
func TestStoragePoolReconcileBlockPoolAttachesDriver(t *testing.T) {
	pools := &fakePoolRegistrar{usage: pool.Usage{AllocatedBytes: 4096}}
	reporter := &fakeStatusReporter{}
	c := NewStoragePoolController(pools, reporter)

	sp := validStoragePool("pool-block")
	sp.Spec.Backend = storagepoolv1.BackendLocalBlock
	sp.Spec.Type = storagepoolv1.PoolTypeBlock
	ev := newStoragePoolEvent(t, controller.EventAdded, sp)

	if _, err := c.Reconcile(context.Background(), ev); err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if len(pools.registered) != 1 {
		t.Fatalf("RegisterPool called %d times, want 1", len(pools.registered))
	}
	got := pools.registered[0]
	if got.Driver == nil {
		t.Fatalf("block pool registered with nil Driver; RegisterPool would reject it")
	}
	if got.ImageDriver != nil {
		t.Fatalf("block pool must not carry an ImageDriver")
	}
}

// TestStoragePoolReconcileFilePoolAttachesImageDriver proves the file backend
// wires a non-nil image.Driver, the symmetric requirement RegisterPool enforces
// for file pools.
func TestStoragePoolReconcileFilePoolAttachesImageDriver(t *testing.T) {
	pools := &fakePoolRegistrar{usage: pool.Usage{AllocatedBytes: 0}}
	reporter := &fakeStatusReporter{}
	c := NewStoragePoolController(pools, reporter)

	sp := validStoragePool("pool-file")
	sp.Spec.Backend = storagepoolv1.BackendLocalFile
	sp.Spec.Type = storagepoolv1.PoolTypeFile
	ev := newStoragePoolEvent(t, controller.EventAdded, sp)

	if _, err := c.Reconcile(context.Background(), ev); err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if len(pools.registered) != 1 {
		t.Fatalf("RegisterPool called %d times, want 1", len(pools.registered))
	}
	got := pools.registered[0]
	if got.ImageDriver == nil {
		t.Fatalf("file pool registered with nil ImageDriver; RegisterPool would reject it")
	}
	if got.Driver != nil {
		t.Fatalf("file pool must not carry a block Driver")
	}
}

// TestStoragePoolReconcileNFSBackendIsPermanentFailure proves a backend with no
// host-local driver implementation (nfs-block) is a permanent config failure: it
// is reported failed, not requeued, and never registered.
func TestStoragePoolReconcileNFSBackendIsPermanentFailure(t *testing.T) {
	pools := &fakePoolRegistrar{}
	reporter := &fakeStatusReporter{}
	c := NewStoragePoolController(pools, reporter)

	sp := validStoragePool("pool-nfs")
	sp.Spec.Backend = storagepoolv1.BackendNFSBlock
	sp.Spec.Type = storagepoolv1.PoolTypeBlock
	ev := newStoragePoolEvent(t, controller.EventAdded, sp)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil (permanent failure reported via status)", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false for a backend with no driver")
	}
	if len(pools.registered) != 0 {
		t.Fatalf("RegisterPool called %d times, want 0 for unsupported backend", len(pools.registered))
	}
	if len(reporter.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1 (failed)", len(reporter.patches))
	}
	if phase := reporter.patches[0].status.Phase; phase != storagepoolv1.PoolPhaseFailed {
		t.Fatalf("patched phase = %q, want %q", phase, storagepoolv1.PoolPhaseFailed)
	}
}
