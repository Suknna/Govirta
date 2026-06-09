package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

// doPatchStatus submits statusBody to PATCH /apis/{kind}/{name}/status through
// the server's handler and returns the recorded response.
func doPatchStatus(t *testing.T, srv *Server, kind metav1.Kind, name string, statusBody []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/apis/"+string(kind)+"/"+name+"/status", bytes.NewReader(statusBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestStatusPatchUpdatesStatusPreservesSpec(t *testing.T) {
	srv, st := newTestServer(t)

	// Seed a VM through apply so it carries a real ResourceVersion and a zero
	// status (phase ""). The reported status must land on .status while .spec is
	// carried through byte-for-byte — the up-reconcile rule: only status moves.
	vm := validVM()
	if rec := doApply(t, srv, metav1.KindVM, vm.Name, vm); rec.Code != http.StatusCreated {
		t.Fatalf("seed apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	before := storedRaw(t, st, metav1.KindVM, vm.Name)

	// Report an observed running phase from the node.
	reported := vmv1.VMStatus{
		Phase:              vmv1.VMPhaseRunning,
		ObservedPowerState: vmv1.ObservedPowerStateOn,
		PowerTransition:    vmv1.PowerTransitionNone,
		Message:            "qmp up",
	}
	statusBody, err := json.Marshal(reported)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}

	rec := doPatchStatus(t, srv, metav1.KindVM, vm.Name, statusBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if hv := rec.Header().Get(resourceVersionHeader); hv == "" {
		t.Fatalf("%s header is empty; expected store-assigned ResourceVersion", resourceVersionHeader)
	}

	// The write must have bumped the stored ResourceVersion (a real CAS write).
	after := storedRaw(t, st, metav1.KindVM, vm.Name)
	if after.ResourceVersion == before.ResourceVersion {
		t.Fatalf("ResourceVersion unchanged %q; status patch must write a new revision", after.ResourceVersion)
	}

	// Decode the stored object back: status updated, spec identical to seed.
	var got vmv1.VM
	if err := json.Unmarshal(after.Value, &got); err != nil {
		t.Fatalf("decode stored VM: %v", err)
	}
	if got.Status.Phase != vmv1.VMPhaseRunning {
		t.Fatalf("stored status.phase = %q, want %q", got.Status.Phase, vmv1.VMPhaseRunning)
	}
	if got.Status.Message != "qmp up" {
		t.Fatalf("stored status.message = %q, want %q", got.Status.Message, "qmp up")
	}
	// Spec must be byte-for-byte the seeded spec: status patch never touches spec.
	if got.Spec.Arch != vm.Spec.Arch || got.Spec.VCPUs != vm.Spec.VCPUs || got.Spec.MemoryMiB != vm.Spec.MemoryMiB {
		t.Fatalf("spec scalar fields changed: got %+v, want %+v", got.Spec, vm.Spec)
	}
	if len(got.Spec.VolumeRefs) != len(vm.Spec.VolumeRefs) || (len(got.Spec.VolumeRefs) > 0 && got.Spec.VolumeRefs[0] != vm.Spec.VolumeRefs[0]) {
		t.Fatalf("spec.volumeRefs changed: got %v, want %v", got.Spec.VolumeRefs, vm.Spec.VolumeRefs)
	}
	if len(got.Spec.NICRefs) != len(vm.Spec.NICRefs) || (len(got.Spec.NICRefs) > 0 && got.Spec.NICRefs[0] != vm.Spec.NICRefs[0]) {
		t.Fatalf("spec.nicRefs changed: got %v, want %v", got.Spec.NICRefs, vm.Spec.NICRefs)
	}

	// The response body is the freshly stored object: it must round-trip the
	// updated status and the preserved spec/identity too.
	var resp vmv1.VM
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response VM: %v", err)
	}
	if resp.Status.Phase != vmv1.VMPhaseRunning {
		t.Fatalf("response status.phase = %q, want %q", resp.Status.Phase, vmv1.VMPhaseRunning)
	}
	if resp.Name != vm.Name {
		t.Fatalf("response name = %q, want %q (identity must be preserved)", resp.Name, vm.Name)
	}
	if resp.Spec.Arch != vm.Spec.Arch {
		t.Fatalf("response spec.arch = %q, want %q", resp.Spec.Arch, vm.Spec.Arch)
	}
}

func TestStatusPatchMissingObjectReturns404(t *testing.T) {
	srv, _ := newTestServer(t)

	statusBody, err := json.Marshal(vmv1.VMStatus{
		Phase:              vmv1.VMPhaseRunning,
		ObservedPowerState: vmv1.ObservedPowerStateOn,
		PowerTransition:    vmv1.PowerTransitionNone,
	})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}

	rec := doPatchStatus(t, srv, metav1.KindVM, "nonexistent", statusBody)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

func TestStatusPatchRejectsFullObjectBody(t *testing.T) {
	srv, _ := newTestServer(t)
	vm := validVM()
	if rec := doApply(t, srv, metav1.KindVM, vm.Name, vm); rec.Code != http.StatusCreated {
		t.Fatalf("seed apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	body, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("marshal vm: %v", err)
	}
	rec := doPatchStatus(t, srv, metav1.KindVM, vm.Name, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for full-object status body; body=%s", rec.Code, rec.Body.String())
	}
}

func TestStatusPatchRejectsInvalidPhase(t *testing.T) {
	srv, _ := newTestServer(t)
	vm := validVM()
	if rec := doApply(t, srv, metav1.KindVM, vm.Name, vm); rec.Code != http.StatusCreated {
		t.Fatalf("seed apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	rec := doPatchStatus(t, srv, metav1.KindVM, vm.Name, []byte(`{"phase":"teleporting","observedPowerState":"On","powerTransition":"None"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for invalid VM phase; body=%s", rec.Code, rec.Body.String())
	}
}

func TestStatusPatchRejectsUnknownStatusField(t *testing.T) {
	srv, _ := newTestServer(t)
	vm := validVM()
	if rec := doApply(t, srv, metav1.KindVM, vm.Name, vm); rec.Code != http.StatusCreated {
		t.Fatalf("seed apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	rec := doPatchStatus(t, srv, metav1.KindVM, vm.Name, []byte(`{"phase":"running","observedPowerState":"On","powerTransition":"None","unexpected":true}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unknown status field; body=%s", rec.Code, rec.Body.String())
	}
}

func TestStatusPatchRejectsDeletingObjectWithNoFinalizers(t *testing.T) {
	srv, st := newTestServer(t)
	vm := validVM()
	vm.DeletionTimestamp = "2026-06-09T00:00:00Z"
	vm.Finalizers = nil
	seedStoreObject(t, st, metav1.KindVM, vm.Name, vm)

	statusBody, err := json.Marshal(vmv1.VMStatus{
		Phase:              vmv1.VMPhaseStopped,
		ObservedPowerState: vmv1.ObservedPowerStateOff,
		PowerTransition:    vmv1.PowerTransitionNone,
	})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}

	rec := doPatchStatus(t, srv, metav1.KindVM, vm.Name, statusBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for deleting object with no finalizers; body=%s", rec.Code, rec.Body.String())
	}
}

// stalePatchStore wraps the fake store and forces a fixed number of CAS conflicts
// on Put before delegating to the real store. It lets a test exercise the
// read-modify-write retry loop deterministically: the first N conditional Puts
// fail with ErrRevisionConflict (as if a concurrent writer bumped the revision
// between Get and Put), after which the write proceeds normally.
type stalePatchStore struct {
	store.Store
	failsRemaining int
}

func (s *stalePatchStore) Put(ctx context.Context, key string, value []byte, expectedVersion string) (store.RawObject, error) {
	// Only interfere with conditional (status) writes; unconditional seeds pass.
	if expectedVersion != "" && s.failsRemaining > 0 {
		s.failsRemaining--
		return store.RawObject{}, store.ErrRevisionConflict
	}
	return s.Store.Put(ctx, key, value, expectedVersion)
}

type statusFinalizerClearingStore struct {
	store.Store
	targetKey          string
	clearOnGet         int
	gets               int
	conflictsRemaining int
}

func (s *statusFinalizerClearingStore) Get(ctx context.Context, key string) (store.RawObject, error) {
	raw, err := s.Store.Get(ctx, key)
	if err != nil || key != s.targetKey {
		return raw, err
	}
	s.gets++
	if s.gets != s.clearOnGet {
		return raw, nil
	}

	var vm vmv1.VM
	if err := json.Unmarshal(raw.Value, &vm); err != nil {
		return store.RawObject{}, err
	}
	vm.Finalizers = nil
	data, err := json.Marshal(vm)
	if err != nil {
		return store.RawObject{}, err
	}
	if _, err := s.Store.Put(ctx, key, data, raw.ResourceVersion); err != nil {
		return store.RawObject{}, err
	}
	return s.Store.Get(ctx, key)
}

func (s *statusFinalizerClearingStore) Put(ctx context.Context, key string, value []byte, expectedVersion string) (store.RawObject, error) {
	if key == s.targetKey && expectedVersion != "" && s.conflictsRemaining > 0 {
		s.conflictsRemaining--
		return store.RawObject{}, store.ErrRevisionConflict
	}
	return s.Store.Put(ctx, key, value, expectedVersion)
}

func TestStatusPatchRechecksTargetLifecycleAfterGet(t *testing.T) {
	base := fake.New()
	t.Cleanup(func() {
		if err := base.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x0000ff)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	alloc := mac.NewAllocator(pool, base)

	vm := validVM()
	vm.DeletionTimestamp = "2026-06-09T00:00:00Z"
	vm.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	seedStoreObject(t, base, metav1.KindVM, vm.Name, vm)

	wrapped := &statusFinalizerClearingStore{Store: base, targetKey: storeKey(metav1.KindVM, vm.Name), clearOnGet: 2}
	srv := NewServer(wrapped, alloc, scheduler.NewNoopScheduler(), []string{"node-1"}, "")
	statusBody, err := json.Marshal(vmv1.VMStatus{
		Phase:              vmv1.VMPhaseStopped,
		ObservedPowerState: vmv1.ObservedPowerStateOff,
		PowerTransition:    vmv1.PowerTransitionNone,
	})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}

	rec := doPatchStatus(t, srv, metav1.KindVM, vm.Name, statusBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 after current object loses finalizers; body=%s", rec.Code, rec.Body.String())
	}
}

func TestStatusPatchRechecksTargetLifecycleAfterCASConflict(t *testing.T) {
	base := fake.New()
	t.Cleanup(func() {
		if err := base.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x0000ff)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	alloc := mac.NewAllocator(pool, base)

	vm := validVM()
	vm.DeletionTimestamp = "2026-06-09T00:00:00Z"
	vm.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	seedStoreObject(t, base, metav1.KindVM, vm.Name, vm)

	wrapped := &statusFinalizerClearingStore{
		Store:              base,
		targetKey:          storeKey(metav1.KindVM, vm.Name),
		clearOnGet:         3,
		conflictsRemaining: 1,
	}
	srv := NewServer(wrapped, alloc, scheduler.NewNoopScheduler(), []string{"node-1"}, "")
	statusBody, err := json.Marshal(vmv1.VMStatus{
		Phase:              vmv1.VMPhaseStopped,
		ObservedPowerState: vmv1.ObservedPowerStateOff,
		PowerTransition:    vmv1.PowerTransitionNone,
	})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}

	rec := doPatchStatus(t, srv, metav1.KindVM, vm.Name, statusBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 after retry reads object with no finalizers; body=%s", rec.Code, rec.Body.String())
	}
	if wrapped.conflictsRemaining != 0 {
		t.Fatalf("forced CAS conflict was not exercised; conflictsRemaining=%d", wrapped.conflictsRemaining)
	}
}

func TestStatusPatchRetriesThenSucceedsOnConflict(t *testing.T) {
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x0000ff)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	alloc := mac.NewAllocator(pool, st)

	// One forced conflict: under statusRetryLimit (3), so the loop re-reads and
	// the second attempt commits.
	wrapped := &stalePatchStore{Store: st, failsRemaining: 1}
	srv := NewServer(wrapped, alloc, scheduler.NewNoopScheduler(), []string{"node-1"}, "")

	vm := validVM()
	if rec := doApply(t, srv, metav1.KindVM, vm.Name, vm); rec.Code != http.StatusCreated {
		t.Fatalf("seed apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	statusBody, err := json.Marshal(vmv1.VMStatus{
		Phase:              vmv1.VMPhaseRunning,
		ObservedPowerState: vmv1.ObservedPowerStateOn,
		PowerTransition:    vmv1.PowerTransitionNone,
	})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}

	rec := doPatchStatus(t, srv, metav1.KindVM, vm.Name, statusBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (retry should converge); body=%s", rec.Code, rec.Body.String())
	}
	if wrapped.failsRemaining != 0 {
		t.Fatalf("forced conflict not consumed; failsRemaining = %d", wrapped.failsRemaining)
	}

	got := storedRaw(t, st, metav1.KindVM, vm.Name)
	var stored vmv1.VM
	if err := json.Unmarshal(got.Value, &stored); err != nil {
		t.Fatalf("decode stored VM: %v", err)
	}
	if stored.Status.Phase != vmv1.VMPhaseRunning {
		t.Fatalf("stored status.phase = %q, want %q after retry", stored.Status.Phase, vmv1.VMPhaseRunning)
	}
}

func TestStatusPatchExhaustedRetriesReturns409(t *testing.T) {
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x0000ff)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	alloc := mac.NewAllocator(pool, st)

	// Force more conflicts than statusRetryLimit so every attempt fails and the
	// handler surfaces 409 rather than looping forever.
	wrapped := &stalePatchStore{Store: st, failsRemaining: statusRetryLimit + 1}
	srv := NewServer(wrapped, alloc, scheduler.NewNoopScheduler(), []string{"node-1"}, "")

	vm := validVM()
	if rec := doApply(t, srv, metav1.KindVM, vm.Name, vm); rec.Code != http.StatusCreated {
		t.Fatalf("seed apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	statusBody, err := json.Marshal(vmv1.VMStatus{
		Phase:              vmv1.VMPhaseRunning,
		ObservedPowerState: vmv1.ObservedPowerStateOn,
		PowerTransition:    vmv1.PowerTransitionNone,
	})
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}

	rec := doPatchStatus(t, srv, metav1.KindVM, vm.Name, statusBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 after exhausting retries; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}
