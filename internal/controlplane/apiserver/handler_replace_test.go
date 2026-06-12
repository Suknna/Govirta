package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

func doReplace(t *testing.T, srv *Server, kind metav1.Kind, name string, obj any) *httptest.ResponseRecorder {
	t.Helper()
	seedApplyReferences(t, srv.store, obj)
	return doReplaceWithoutReferenceSeeds(t, srv, kind, name, obj)
}

func doReplaceWithoutReferenceSeeds(t *testing.T, srv *Server, kind metav1.Kind, name string, obj any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal replace object: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/apis/"+string(kind)+"/"+name, bytes.NewReader(data))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestReplaceMissingObjectReturns404(t *testing.T) {
	srv, _ := newTestServer(t)
	obj := validStoragePool()
	obj.ResourceVersion = "1"

	rec := doReplace(t, srv, metav1.KindStoragePool, obj.Name, obj)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

func TestReplaceMissingResourceVersionReturns400(t *testing.T) {
	srv, _ := newTestServer(t)
	obj := validStoragePool()
	seedStoreObject(t, srv.store, metav1.KindStoragePool, obj.Name, obj)

	rec := doReplace(t, srv, metav1.KindStoragePool, obj.Name, obj)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

func TestReplaceStaleResourceVersionReturns409(t *testing.T) {
	srv, _ := newTestServer(t)
	obj := validStoragePool()
	seedStoreObject(t, srv.store, metav1.KindStoragePool, obj.Name, obj)
	obj.ResourceVersion = "stale"

	rec := doReplace(t, srv, metav1.KindStoragePool, obj.Name, obj)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

func TestReplaceMatchingResourceVersionUpdatesSpecAndBumpsRV(t *testing.T) {
	srv, st := newTestServer(t)
	obj := validVM()
	seedApplyReferences(t, st, obj)
	seedStoreObject(t, st, metav1.KindVM, obj.Name, obj)
	raw := storedRaw(t, st, metav1.KindVM, obj.Name)

	obj.ResourceVersion = raw.ResourceVersion
	obj.Spec.PowerState = vmv1.PowerStateOff
	obj.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
	rec := doReplaceWithoutReferenceSeeds(t, srv, metav1.KindVM, obj.Name, obj)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var stored vmv1.VM
	if err := json.Unmarshal(storedRaw(t, st, metav1.KindVM, obj.Name).Value, &stored); err != nil {
		t.Fatalf("decode stored VM: %v", err)
	}
	if stored.Spec.PowerState != obj.Spec.PowerState || stored.Spec.PowerOffMode != obj.Spec.PowerOffMode {
		t.Fatalf("stored power intent = %q/%q, want %q/%q", stored.Spec.PowerState, stored.Spec.PowerOffMode, obj.Spec.PowerState, obj.Spec.PowerOffMode)
	}
	if stored.ResourceVersion != "" {
		t.Fatalf("stored body RV = %q, want empty store-owned body field", stored.ResourceVersion)
	}

	var resp vmv1.VM
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode replace response: %v", err)
	}
	newRaw := storedRaw(t, st, metav1.KindVM, obj.Name)
	if resp.ResourceVersion != newRaw.ResourceVersion {
		t.Fatalf("response RV = %q, want new store RV %q", resp.ResourceVersion, newRaw.ResourceVersion)
	}
	if resp.ResourceVersion == raw.ResourceVersion {
		t.Fatalf("response RV did not bump: %q", resp.ResourceVersion)
	}
}

func TestReplacePreservesStatusAndFinalizersAndDeletionTimestamp(t *testing.T) {
	srv, st := newTestServer(t)
	obj := validStoragePool()
	obj.Status = storagepoolv1.StoragePoolStatus{Phase: storagepoolv1.PoolPhaseReady, AllocatedBytes: 64}
	obj.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	obj.DeletionTimestamp = "2026-06-12T00:00:00Z"
	seedStoreObject(t, st, metav1.KindStoragePool, obj.Name, obj)
	raw := storedRaw(t, st, metav1.KindStoragePool, obj.Name)

	replacement := validStoragePool()
	replacement.ResourceVersion = raw.ResourceVersion
	replacement.Finalizers = obj.Finalizers
	replacement.DeletionTimestamp = obj.DeletionTimestamp
	rec := doReplace(t, srv, metav1.KindStoragePool, replacement.Name, replacement)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	stored := decodeStoredStoragePool(t, storedRaw(t, st, metav1.KindStoragePool, obj.Name).Value)
	if stored.Status != obj.Status {
		t.Fatalf("status = %+v, want %+v", stored.Status, obj.Status)
	}
	if stored.DeletionTimestamp != obj.DeletionTimestamp {
		t.Fatalf("deletionTimestamp = %q, want %q", stored.DeletionTimestamp, obj.DeletionTimestamp)
	}
	if len(stored.Finalizers) != 1 || stored.Finalizers[0] != metav1.FinalizerNodeTeardown {
		t.Fatalf("finalizers = %v, want node teardown finalizer", stored.Finalizers)
	}
}

func TestReplaceGetEditReplaceWorkflowAllowsUnchangedServerOwnedMetadata(t *testing.T) {
	srv, st := newTestServer(t)
	vm := validVM()
	vm.NodeName = "node-1"
	vm.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	vm.Status = vmv1.VMStatus{
		Phase:              vmv1.VMPhaseRunning,
		ObservedPowerState: vmv1.ObservedPowerStateOn,
		PowerTransition:    vmv1.PowerTransitionNone,
	}
	seedApplyReferences(t, st, vm)
	seedStoreObject(t, st, metav1.KindVM, vm.Name, vm)

	getRec := doGet(t, srv, "/apis/"+string(metav1.KindVM)+"/"+vm.Name)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	var edited vmv1.VM
	if err := json.Unmarshal(getRec.Body.Bytes(), &edited); err != nil {
		t.Fatalf("decode get VM: %v", err)
	}
	if edited.ResourceVersion == "" || len(edited.Finalizers) == 0 || edited.Status.Phase == "" {
		t.Fatalf("GET body missing workflow fields: rv=%q finalizers=%v status=%+v", edited.ResourceVersion, edited.Finalizers, edited.Status)
	}
	edited.Spec.PowerState = vmv1.PowerStateOff
	edited.Spec.PowerOffMode = vmv1.PowerOffModeAcpi

	replaceRec := doReplaceWithoutReferenceSeeds(t, srv, metav1.KindVM, edited.Name, edited)
	if replaceRec.Code != http.StatusOK {
		t.Fatalf("replace status = %d, want 200; body=%s", replaceRec.Code, replaceRec.Body.String())
	}
	var stored vmv1.VM
	if err := json.Unmarshal(storedRaw(t, st, metav1.KindVM, vm.Name).Value, &stored); err != nil {
		t.Fatalf("decode stored VM: %v", err)
	}
	if stored.Status != vm.Status {
		t.Fatalf("stored status = %+v, want %+v", stored.Status, vm.Status)
	}
	if len(stored.Finalizers) != 1 || stored.Finalizers[0] != metav1.FinalizerNodeTeardown {
		t.Fatalf("stored finalizers = %v, want unchanged finalizer", stored.Finalizers)
	}
}

func TestReplaceRejectsServerOwnedMetadataChanges(t *testing.T) {
	srv, st := newTestServer(t)
	obj := validStoragePool()
	obj.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	obj.DeletionTimestamp = "2026-06-12T00:00:00Z"
	seedStoreObject(t, st, metav1.KindStoragePool, obj.Name, obj)
	raw := storedRaw(t, st, metav1.KindStoragePool, obj.Name)

	cases := []struct {
		name   string
		mutate func(*storagepoolv1.StoragePool)
	}{
		{name: "finalizers", mutate: func(o *storagepoolv1.StoragePool) { o.Finalizers = nil }},
		{name: "deletionTimestamp", mutate: func(o *storagepoolv1.StoragePool) { o.DeletionTimestamp = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replacement := obj
			replacement.ResourceVersion = raw.ResourceVersion
			tc.mutate(&replacement)
			rec := doReplace(t, srv, metav1.KindStoragePool, replacement.Name, replacement)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestReplaceNICEmptyMACInheritsOldMAC(t *testing.T) {
	srv, st := newTestServer(t)
	nic := validNIC()
	nic.Spec.MAC = "02:00:00:aa:bb:cc"
	seedApplyReferences(t, st, nic)
	seedStoreObject(t, st, metav1.KindNIC, nic.Name, nic)
	raw := storedRaw(t, st, metav1.KindNIC, nic.Name)

	replacement := validNIC()
	replacement.ResourceVersion = raw.ResourceVersion
	rec := doReplaceWithoutReferenceSeeds(t, srv, metav1.KindNIC, replacement.Name, replacement)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var stored nicv1.NIC
	if err := json.Unmarshal(storedRaw(t, st, metav1.KindNIC, nic.Name).Value, &stored); err != nil {
		t.Fatalf("decode stored NIC: %v", err)
	}
	if stored.Spec.MAC != nic.Spec.MAC {
		t.Fatalf("stored MAC = %q, want inherited %q", stored.Spec.MAC, nic.Spec.MAC)
	}
}

func TestReplaceNICExplicitMACChangeRejected(t *testing.T) {
	srv, st := newTestServer(t)
	nic := validNIC()
	nic.Spec.MAC = "02:00:00:aa:bb:cc"
	seedApplyReferences(t, st, nic)
	seedStoreObject(t, st, metav1.KindNIC, nic.Name, nic)
	raw := storedRaw(t, st, metav1.KindNIC, nic.Name)

	replacement := validNIC()
	replacement.ResourceVersion = raw.ResourceVersion
	replacement.Spec.MAC = "02:00:00:aa:bb:dd"
	rec := doReplaceWithoutReferenceSeeds(t, srv, metav1.KindNIC, replacement.Name, replacement)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

func TestReplaceSnapshotDerivesNodeNameFromTargetVM(t *testing.T) {
	srv, st := newTestServer(t)
	seedSnapshotVMRef(t, st, "vm-a", "node-1")
	snap := validSnapshot()
	snap.NodeName = "node-stale"
	seedStoreObject(t, st, metav1.KindSnapshot, snap.Name, snap)
	raw := storedRaw(t, st, metav1.KindSnapshot, snap.Name)

	replacement := validSnapshot()
	replacement.ResourceVersion = raw.ResourceVersion
	rec := doReplaceWithoutReferenceSeeds(t, srv, metav1.KindSnapshot, replacement.Name, replacement)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var stored snapshotv1.Snapshot
	if err := json.Unmarshal(storedRaw(t, st, metav1.KindSnapshot, snap.Name).Value, &stored); err != nil {
		t.Fatalf("decode stored Snapshot: %v", err)
	}
	if stored.NodeName != "node-1" {
		t.Fatalf("snapshot nodeName = %q, want target VM node", stored.NodeName)
	}
}

func TestReplacePreservesVMNodeName(t *testing.T) {
	srv, st := newTestServer(t)
	vm := validVM()
	vm.NodeName = "node-1"
	seedApplyReferences(t, st, vm)
	seedStoreObject(t, st, metav1.KindVM, vm.Name, vm)
	raw := storedRaw(t, st, metav1.KindVM, vm.Name)

	replacement := validVM()
	replacement.ResourceVersion = raw.ResourceVersion
	replacement.Spec.PowerState = vmv1.PowerStateOff
	replacement.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
	rec := doReplaceWithoutReferenceSeeds(t, srv, metav1.KindVM, replacement.Name, replacement)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var stored vmv1.VM
	if err := json.Unmarshal(storedRaw(t, st, metav1.KindVM, vm.Name).Value, &stored); err != nil {
		t.Fatalf("decode stored VM: %v", err)
	}
	if stored.NodeName != vm.NodeName {
		t.Fatalf("nodeName = %q, want %q", stored.NodeName, vm.NodeName)
	}
}

func TestReplaceImmutableFieldStillRejected(t *testing.T) {
	srv, st := newTestServer(t)
	vm := validVM()
	seedApplyReferences(t, st, vm)
	seedStoreObject(t, st, metav1.KindVM, vm.Name, vm)
	raw := storedRaw(t, st, metav1.KindVM, vm.Name)

	replacement := validVM()
	replacement.ResourceVersion = raw.ResourceVersion
	replacement.Spec.Arch = "aarch64"
	rec := doReplaceWithoutReferenceSeeds(t, srv, metav1.KindVM, replacement.Name, replacement)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

func TestApplyPostStillWorksAsUnguardedApply(t *testing.T) {
	srv, st := newTestServer(t)
	obj := validVM()

	rec := doApply(t, srv, metav1.KindVM, obj.Name, obj)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	firstRaw := storedRaw(t, st, metav1.KindVM, obj.Name)
	obj.Spec.PowerState = vmv1.PowerStateOff
	obj.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
	rec = doApply(t, srv, metav1.KindVM, obj.Name, obj)
	if rec.Code != http.StatusCreated {
		t.Fatalf("second apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	secondRaw := storedRaw(t, st, metav1.KindVM, obj.Name)
	if firstRaw.ResourceVersion == secondRaw.ResourceVersion {
		t.Fatalf("resourceVersion did not change after unguarded POST apply")
	}
	var stored vmv1.VM
	if err := json.Unmarshal(secondRaw.Value, &stored); err != nil {
		t.Fatalf("decode stored VM: %v", err)
	}
	if stored.Spec.PowerState != obj.Spec.PowerState || stored.Spec.PowerOffMode != obj.Spec.PowerOffMode {
		t.Fatalf("power intent = %q/%q, want %q/%q", stored.Spec.PowerState, stored.Spec.PowerOffMode, obj.Spec.PowerState, obj.Spec.PowerOffMode)
	}
}

func TestReplaceStoreConflictKeepsExistingObject(t *testing.T) {
	srv, st := newTestServer(t)
	obj := validStoragePool()
	seedStoreObject(t, st, metav1.KindStoragePool, obj.Name, obj)
	raw := storedRaw(t, st, metav1.KindStoragePool, obj.Name)

	other := obj
	other.Spec.CapacityBytes = 4 << 30
	data, err := json.Marshal(other)
	if err != nil {
		t.Fatalf("marshal concurrent object: %v", err)
	}
	if _, err := st.Put(context.Background(), storeKey(metav1.KindStoragePool, obj.Name), data, raw.ResourceVersion); err != nil {
		t.Fatalf("concurrent store update: %v", err)
	}

	obj.ResourceVersion = raw.ResourceVersion
	obj.Spec.CapacityBytes = 5 << 30
	rec := doReplace(t, srv, metav1.KindStoragePool, obj.Name, obj)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	stored := decodeStoredStoragePool(t, storedRaw(t, st, metav1.KindStoragePool, obj.Name).Value)
	if stored.Spec.CapacityBytes != other.Spec.CapacityBytes {
		t.Fatalf("capacity after stale replace = %d, want %d", stored.Spec.CapacityBytes, other.Spec.CapacityBytes)
	}
}

func decodeStoredStoragePool(t *testing.T, data []byte) storagepoolv1.StoragePool {
	t.Helper()
	var obj storagepoolv1.StoragePool
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("decode stored StoragePool: %v", err)
	}
	return obj
}
