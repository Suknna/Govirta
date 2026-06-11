package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/storage"
	"github.com/suknna/govirta/internal/storage/volume"
	"github.com/suknna/govirta/internal/vmm"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// --- fakes ------------------------------------------------------------------

// fakeVolumeSnapshotter records SnapshotVolume / DeleteVolumeSnapshot calls and
// can be told to fail on the Nth SnapshotVolume call (1-based), so a test can
// drive a mid-fan-out failure and assert the all-or-nothing rollback. It honours
// ctx cancellation, faithful to *storage.VolumeService.
type fakeVolumeSnapshotter struct {
	snapshotCalls []storage.SnapshotVolumeRequest
	deleteCalls   []storage.DeleteVolumeSnapshotRequest

	// failSnapshotOnCall, when >0, makes the Nth SnapshotVolume call (1-based)
	// return snapshotErr.
	failSnapshotOnCall int
	snapshotErr        error
	// deleteErr, when set, is returned by every DeleteVolumeSnapshot call.
	deleteErr error
}

func (f *fakeVolumeSnapshotter) SnapshotVolume(ctx context.Context, req storage.SnapshotVolumeRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.snapshotCalls = append(f.snapshotCalls, req)
	if f.failSnapshotOnCall > 0 && len(f.snapshotCalls) == f.failSnapshotOnCall {
		if f.snapshotErr != nil {
			return f.snapshotErr
		}
		return errors.New("snapshot failed")
	}
	return nil
}

func (f *fakeVolumeSnapshotter) DeleteVolumeSnapshot(ctx context.Context, req storage.DeleteVolumeSnapshotRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.deleteCalls = append(f.deleteCalls, req)
	return f.deleteErr
}

// snapVMRunner is a VMRunner whose Status returns a configurable phase or error
// (so a test can model vmm.ErrNotFound). The mutating methods are inert: the
// snapshot controller only reads live phase via Status.
type snapVMRunner struct {
	phase     vmm.Phase
	statusErr error
}

func (f *snapVMRunner) Create(ctx context.Context, req vmm.CreateRequest) (vmm.VM, error) {
	return vmm.VM{}, nil
}
func (f *snapVMRunner) Redefine(ctx context.Context, uuid string, spec vmm.SpecSummary) (vmm.VM, error) {
	return vmm.VM{}, nil
}
func (f *snapVMRunner) Start(ctx context.Context, uuid string) (vmm.VM, error) { return vmm.VM{}, nil }
func (f *snapVMRunner) Stop(ctx context.Context, uuid string) error            { return nil }
func (f *snapVMRunner) Kill(ctx context.Context, uuid string) error            { return nil }
func (f *snapVMRunner) Delete(ctx context.Context, uuid string) error          { return nil }

func (f *snapVMRunner) Status(ctx context.Context, uuid string) (vmm.VM, error) {
	if err := ctx.Err(); err != nil {
		return vmm.VM{}, err
	}
	if f.statusErr != nil {
		return vmm.VM{}, f.statusErr
	}
	return vmm.VM{UUID: uuid, Phase: f.phase}, nil
}

// snapDepReader serves canned Get responses per (kind,name) and captures status
// patches + finalizer removals. It honours ctx cancellation, faithful to
// *client.Client.
type snapDepReader struct {
	objects  map[string][]byte
	notFound map[string]bool

	patches    []snapCapturedPatch
	patchErr   error
	patchCalls int

	removeFinalizerCalls int
	lastFinalizerName    string
	lastFinalizer        string
}

type snapCapturedPatch struct {
	kind   string
	name   string
	status snapshotv1.SnapshotStatus
}

func (f *snapDepReader) Get(ctx context.Context, kind, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := depKey(kind, name)
	if f.notFound != nil && f.notFound[key] {
		return nil, client.ErrNotFound
	}
	if raw, ok := f.objects[key]; ok {
		return raw, nil
	}
	return nil, client.ErrNotFound
}

func (f *snapDepReader) PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.patchCalls++
	if f.patchErr != nil {
		return nil, f.patchErr
	}
	var decoded snapshotv1.SnapshotStatus
	if err := json.Unmarshal(status, &decoded); err != nil {
		return nil, err
	}
	f.patches = append(f.patches, snapCapturedPatch{kind: kind, name: name, status: decoded})
	return status, nil
}

func (f *snapDepReader) RemoveFinalizer(ctx context.Context, kind, name, finalizer string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.removeFinalizerCalls++
	f.lastFinalizerName = name
	f.lastFinalizer = finalizer
	return nil
}

// --- builders ---------------------------------------------------------------

func snapshotObject(name, vmRef string) snapshotv1.Snapshot {
	return snapshotv1.Snapshot{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindSnapshot},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: name + "-uid"},
		Spec:       snapshotv1.SnapshotSpec{VMRef: vmRef},
	}
}

func deletingSnapshot(name, vmRef string) snapshotv1.Snapshot {
	s := snapshotObject(name, vmRef)
	s.ObjectMeta.DeletionTimestamp = "2026-01-02T15:04:05Z"
	return s
}

func snapVMObject(name string, volumeRefs ...string) vmv1.VM {
	return vmv1.VM{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindVM},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: name + "-uid"},
		Spec: vmv1.VMSpec{
			Arch:       "x86_64",
			VCPUs:      1,
			MemoryMiB:  512,
			VolumeRefs: volumeRefs,
			NICRefs:    []string{"nic-0"},
			PowerState: vmv1.PowerStateOff,
		},
	}
}

// snapVolumeObject builds a root Volume whose derived storage key the controller
// reconstructs as <VMRef>-<role>-<DiskIndex>.
func snapVolumeObject(name, vmRef, poolRef string, diskIndex int) volumev1.Volume {
	return volumev1.Volume{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindVolume},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name},
		Spec: volumev1.VolumeSpec{
			PoolRef:          poolRef,
			VMRef:            vmRef,
			VMName:           "vm-name",
			DiskIndex:        diskIndex,
			CapacityBytes:    4 << 30,
			Role:             volumev1.VolumeRoleRoot,
			ImageRef:         "img-a",
			ImageFilePoolRef: "file-pool",
		},
	}
}

func newSnapshotEvent(t *testing.T, evType controller.EventType, snap snapshotv1.Snapshot) controller.Event {
	t.Helper()
	return controller.Event{Type: evType, Key: snap.Name, Object: mustMarshal(t, snap)}
}

// snapDeps wires a dependency reader seeded with the given VM object and any
// volume objects.
func snapDeps(t *testing.T, vm vmv1.VM, vols ...volumev1.Volume) *snapDepReader {
	t.Helper()
	objects := map[string][]byte{
		depKey(string(metav1.KindVM), vm.Name): mustMarshal(t, vm),
	}
	for _, v := range vols {
		objects[depKey(string(metav1.KindVolume), v.Name)] = mustMarshal(t, v)
	}
	return &snapDepReader{objects: objects}
}

// --- tests: create path -----------------------------------------------------

func TestSnapshotCreateVMRunningNotColdPendingRequeue(t *testing.T) {
	vm := snapVMObject("vm-a", "vol-0")
	snap := snapshotObject("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseRunning}
	dep := snapDeps(t, vm, snapVolumeObject("vol-0", "vm-a", "block-pool", 0))
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventAdded, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile() RequeueAfter = 0, want delayed requeue while VM not cold")
	}
	if len(vols.snapshotCalls) != 0 {
		t.Errorf("SnapshotVolume called %d times, want 0 while VM not cold", len(vols.snapshotCalls))
	}
	if len(dep.patches) != 1 || dep.patches[0].status.Phase != snapshotv1.SnapshotPhasePending {
		t.Fatalf("expected one Pending patch, got %+v", dep.patches)
	}
}

func TestSnapshotCreateVMFailedNotCold(t *testing.T) {
	// PhaseFailed is intent=running (the VM controller may re-Start), so it is
	// NOT cold — proves Failed is excluded from the cold set (restart race).
	vm := snapVMObject("vm-a", "vol-0")
	snap := snapshotObject("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseFailed}
	dep := snapDeps(t, vm, snapVolumeObject("vol-0", "vm-a", "block-pool", 0))
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventAdded, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile() RequeueAfter = 0, want delayed requeue while VM Failed (not cold)")
	}
	if len(vols.snapshotCalls) != 0 {
		t.Errorf("SnapshotVolume called %d times, want 0 while VM Failed", len(vols.snapshotCalls))
	}
	if len(dep.patches) != 1 || dep.patches[0].status.Phase != snapshotv1.SnapshotPhasePending {
		t.Fatalf("expected one Pending patch, got %+v", dep.patches)
	}
}

func TestSnapshotCreateVMDefinedIsCold(t *testing.T) {
	// PhaseDefined (powerState=Off, never started) IS cold: a freshly-defined Off
	// VM snapshots without an On→Off cycle.
	vm := snapVMObject("vm-a", "vol-0")
	snap := snapshotObject("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseDefined}
	dep := snapDeps(t, vm, snapVolumeObject("vol-0", "vm-a", "block-pool", 0))
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventAdded, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.ShouldRequeue() {
		t.Fatalf("Reconcile() requeued, want done after a successful fan-out")
	}
	if len(vols.snapshotCalls) != 1 {
		t.Fatalf("SnapshotVolume called %d times, want 1 (Defined is cold)", len(vols.snapshotCalls))
	}
	if len(dep.patches) != 1 || dep.patches[0].status.Phase != snapshotv1.SnapshotPhaseReady {
		t.Fatalf("expected one Ready patch, got %+v", dep.patches)
	}
}

func TestSnapshotCreateAllDisksSucceedReady(t *testing.T) {
	vm := snapVMObject("vm-a", "vol-0", "vol-1")
	snap := snapshotObject("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseStopped}
	dep := snapDeps(t, vm,
		snapVolumeObject("vol-0", "vm-a", "block-pool", 0),
		snapVolumeObject("vol-1", "vm-a", "block-pool", 1),
	)
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventAdded, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.ShouldRequeue() {
		t.Fatalf("Reconcile() requeued, want done after all disks snapshotted")
	}
	if len(vols.snapshotCalls) != 2 {
		t.Fatalf("SnapshotVolume called %d times, want 2", len(vols.snapshotCalls))
	}
	for i, call := range vols.snapshotCalls {
		if call.SnapshotName != snap.UID {
			t.Errorf("call %d SnapshotName = %q, want %q (snapshot UID)", i, call.SnapshotName, snap.UID)
		}
		if call.PoolName != "block-pool" {
			t.Errorf("call %d PoolName = %q, want block-pool", i, call.PoolName)
		}
	}
	wantIDs := []volume.ID{"vm-a-root-0", "vm-a-root-1"}
	for i, want := range wantIDs {
		if vols.snapshotCalls[i].VolumeID != want {
			t.Errorf("call %d VolumeID = %q, want %q", i, vols.snapshotCalls[i].VolumeID, want)
		}
	}
	if len(dep.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(dep.patches))
	}
	patch := dep.patches[0]
	if patch.kind != string(metav1.KindSnapshot) {
		t.Errorf("patch kind = %q, want %q", patch.kind, metav1.KindSnapshot)
	}
	if patch.status.Phase != snapshotv1.SnapshotPhaseReady {
		t.Errorf("patch phase = %q, want Ready", patch.status.Phase)
	}
	if len(patch.status.DiskSnapshots) != 2 {
		t.Fatalf("patch DiskSnapshots len = %d, want 2", len(patch.status.DiskSnapshots))
	}
	for i, d := range patch.status.DiskSnapshots {
		if d.Result != snapshotv1.DiskSnapshotStateCreated {
			t.Errorf("disk %d result = %q, want Created", i, d.Result)
		}
	}
}

func TestSnapshotCreateMidDiskFailureRollsBack(t *testing.T) {
	vm := snapVMObject("vm-a", "vol-0", "vol-1")
	snap := snapshotObject("snap-a", "vm-a")
	snapErr := errors.New("qemu-img snapshot -c failed")
	vols := &fakeVolumeSnapshotter{failSnapshotOnCall: 2, snapshotErr: snapErr}
	runner := &snapVMRunner{phase: vmm.PhaseStopped}
	dep := snapDeps(t, vm,
		snapVolumeObject("vol-0", "vm-a", "block-pool", 0),
		snapVolumeObject("vol-1", "vm-a", "block-pool", 1),
	)
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventAdded, snap))
	if err == nil || !errors.Is(err, snapErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, snapErr)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile() RequeueAfter = 0, want delayed requeue on a failed fan-out")
	}
	// disk0 created then rolled back; disk1 failed.
	if len(vols.snapshotCalls) != 2 {
		t.Fatalf("SnapshotVolume called %d times, want 2", len(vols.snapshotCalls))
	}
	if len(vols.deleteCalls) != 1 {
		t.Fatalf("DeleteVolumeSnapshot (rollback) called %d times, want 1 (disk0)", len(vols.deleteCalls))
	}
	if vols.deleteCalls[0].VolumeID != volume.ID("vm-a-root-0") {
		t.Errorf("rollback VolumeID = %q, want vm-a-root-0", vols.deleteCalls[0].VolumeID)
	}
	if vols.deleteCalls[0].SnapshotName != snap.UID {
		t.Errorf("rollback SnapshotName = %q, want %q", vols.deleteCalls[0].SnapshotName, snap.UID)
	}
	if len(dep.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(dep.patches))
	}
	patch := dep.patches[0]
	if patch.status.Phase != snapshotv1.SnapshotPhaseFailed {
		t.Errorf("patch phase = %q, want Failed", patch.status.Phase)
	}
	if len(patch.status.DiskSnapshots) != 2 {
		t.Fatalf("patch DiskSnapshots len = %d, want 2", len(patch.status.DiskSnapshots))
	}
	if patch.status.DiskSnapshots[0].Result != snapshotv1.DiskSnapshotStateCreated {
		t.Errorf("disk0 result = %q, want Created", patch.status.DiskSnapshots[0].Result)
	}
	if patch.status.DiskSnapshots[1].Result != snapshotv1.DiskSnapshotStateFailed {
		t.Errorf("disk1 result = %q, want Failed", patch.status.DiskSnapshots[1].Result)
	}
	if patch.status.Message == "" {
		t.Errorf("patch message empty, want failure cause")
	}
}

func TestSnapshotCreateReadyIsNoOp(t *testing.T) {
	vm := snapVMObject("vm-a", "vol-0")
	snap := snapshotObject("snap-a", "vm-a")
	snap.Status.Phase = snapshotv1.SnapshotPhaseReady
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseStopped}
	dep := snapDeps(t, vm, snapVolumeObject("vol-0", "vm-a", "block-pool", 0))
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventModified, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.ShouldRequeue() {
		t.Fatalf("Reconcile() requeued, want done on a ready no-op")
	}
	if len(vols.snapshotCalls) != 0 {
		t.Errorf("SnapshotVolume called %d times, want 0 on ready no-op", len(vols.snapshotCalls))
	}
	if dep.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 on ready no-op", dep.patchCalls)
	}
}

// --- tests: delete path -----------------------------------------------------

func TestSnapshotDeleteVMRunningKeepsFinalizer(t *testing.T) {
	vm := snapVMObject("vm-a", "vol-0")
	snap := deletingSnapshot("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseRunning}
	dep := snapDeps(t, vm, snapVolumeObject("vol-0", "vm-a", "block-pool", 0))
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventModified, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile() RequeueAfter = 0, want delayed requeue while VM not cold")
	}
	if len(vols.deleteCalls) != 0 {
		t.Errorf("DeleteVolumeSnapshot called %d times, want 0 while VM not cold", len(vols.deleteCalls))
	}
	if dep.removeFinalizerCalls != 0 {
		t.Errorf("RemoveFinalizer called %d times, want 0 (finalizer kept while VM not cold)", dep.removeFinalizerCalls)
	}
	if len(dep.patches) != 1 || dep.patches[0].status.Phase != snapshotv1.SnapshotPhaseDeleting {
		t.Fatalf("expected one Deleting patch, got %+v", dep.patches)
	}
}

func TestSnapshotDeleteVMStoppedDeletesAndRemovesFinalizer(t *testing.T) {
	vm := snapVMObject("vm-a", "vol-0", "vol-1")
	snap := deletingSnapshot("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseStopped}
	dep := snapDeps(t, vm,
		snapVolumeObject("vol-0", "vm-a", "block-pool", 0),
		snapVolumeObject("vol-1", "vm-a", "block-pool", 1),
	)
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventModified, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil on successful teardown", err)
	}
	if result.ShouldRequeue() {
		t.Fatalf("Reconcile() requeued, want done after teardown + finalizer removal")
	}
	if len(vols.deleteCalls) != 2 {
		t.Fatalf("DeleteVolumeSnapshot called %d times, want 2", len(vols.deleteCalls))
	}
	for i, call := range vols.deleteCalls {
		if call.SnapshotName != snap.UID {
			t.Errorf("delete %d SnapshotName = %q, want %q", i, call.SnapshotName, snap.UID)
		}
	}
	if dep.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1", dep.removeFinalizerCalls)
	}
	if dep.lastFinalizerName != "snap-a" {
		t.Errorf("RemoveFinalizer name = %q, want snap-a", dep.lastFinalizerName)
	}
	if dep.lastFinalizer != string(metav1.FinalizerNodeTeardown) {
		t.Errorf("RemoveFinalizer finalizer = %q, want %q", dep.lastFinalizer, metav1.FinalizerNodeTeardown)
	}
}

func TestSnapshotDeleteVMRuntimeGoneIsCold(t *testing.T) {
	// Runtime gone (vmm.ErrNotFound) but VM object present → treated as cold so
	// teardown proceeds and the finalizer drops (does not requeue forever).
	vm := snapVMObject("vm-a", "vol-0")
	snap := deletingSnapshot("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{statusErr: vmm.ErrNotFound}
	dep := snapDeps(t, vm, snapVolumeObject("vol-0", "vm-a", "block-pool", 0))
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventModified, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.ShouldRequeue() {
		t.Fatalf("Reconcile() requeued, want done when runtime gone (cold)")
	}
	if len(vols.deleteCalls) != 1 {
		t.Fatalf("DeleteVolumeSnapshot called %d times, want 1", len(vols.deleteCalls))
	}
	if dep.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1", dep.removeFinalizerCalls)
	}
}

func TestSnapshotDeleteVolumeGoneIsTreatedAsDrained(t *testing.T) {
	// One of the VM's Volume objects is gone (master ErrNotFound). Its qcow2 (and
	// the internal snapshot inside it) is destroyed, so that disk's snapshot is
	// already torn down → skip it, keep draining the rest, and still drop the
	// finalizer. Symmetric with the VM-gone branch (parent gone → snapshot gone):
	// a Volume that disappears first must not permanently wedge the finalizer.
	vm := snapVMObject("vm-a", "vol-0", "vol-1")
	snap := deletingSnapshot("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseStopped}
	dep := snapDeps(t, vm,
		snapVolumeObject("vol-0", "vm-a", "block-pool", 0),
		snapVolumeObject("vol-1", "vm-a", "block-pool", 1),
	)
	// vol-0's Volume object is gone; vol-1 resolves normally.
	dep.notFound = map[string]bool{depKey(string(metav1.KindVolume), "vol-0"): true}
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventModified, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil (Volume gone is drained, not an error)", err)
	}
	if result.ShouldRequeue() {
		t.Fatalf("Reconcile() requeued, want done (gone Volume must not wedge teardown)")
	}
	// Only the live disk (vol-1, diskIndex 1) is deleted; the gone disk is skipped.
	if len(vols.deleteCalls) != 1 {
		t.Fatalf("DeleteVolumeSnapshot called %d times, want 1 (only the live disk)", len(vols.deleteCalls))
	}
	if vols.deleteCalls[0].VolumeID != volume.ID("vm-a-root-1") {
		t.Errorf("delete VolumeID = %q, want vm-a-root-1 (the live disk)", vols.deleteCalls[0].VolumeID)
	}
	if vols.deleteCalls[0].SnapshotName != snap.UID {
		t.Errorf("delete SnapshotName = %q, want %q", vols.deleteCalls[0].SnapshotName, snap.UID)
	}
	if dep.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1 (teardown completes despite gone Volume)", dep.removeFinalizerCalls)
	}
	if dep.lastFinalizer != string(metav1.FinalizerNodeTeardown) {
		t.Errorf("RemoveFinalizer finalizer = %q, want %q", dep.lastFinalizer, metav1.FinalizerNodeTeardown)
	}
}

func TestSnapshotDeleteVMObjectGoneRemovesFinalizer(t *testing.T) {
	// VM object gone (master ErrNotFound) → the qcow2 files are gone with the VM,
	// so drop the finalizer without touching the volume service.
	snap := deletingSnapshot("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseStopped}
	dep := &snapDepReader{
		objects:  map[string][]byte{},
		notFound: map[string]bool{depKey(string(metav1.KindVM), "vm-a"): true},
	}
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventModified, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.ShouldRequeue() {
		t.Fatalf("Reconcile() requeued, want done when VM object gone")
	}
	if len(vols.deleteCalls) != 0 {
		t.Errorf("DeleteVolumeSnapshot called %d times, want 0 when VM gone", len(vols.deleteCalls))
	}
	if dep.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1", dep.removeFinalizerCalls)
	}
}

// --- tests: misc ------------------------------------------------------------

func TestSnapshotCreateVMObjectGoneRequeues(t *testing.T) {
	// On the create path a missing VM object is a wait (requeue), not a finalizer
	// drop (the object is not deleting).
	snap := snapshotObject("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseStopped}
	dep := &snapDepReader{
		objects:  map[string][]byte{},
		notFound: map[string]bool{depKey(string(metav1.KindVM), "vm-a"): true},
	}
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventAdded, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile() RequeueAfter = 0, want delayed requeue when VM not yet present")
	}
	if len(vols.snapshotCalls) != 0 {
		t.Errorf("SnapshotVolume called %d times, want 0", len(vols.snapshotCalls))
	}
	if dep.removeFinalizerCalls != 0 {
		t.Errorf("RemoveFinalizer called %d times, want 0 on create path", dep.removeFinalizerCalls)
	}
}

func TestSnapshotStatusNoOpGuardSkipsPatch(t *testing.T) {
	// observed == desired → no PatchStatus (breaks the status→MODIFIED self-loop).
	vm := snapVMObject("vm-a", "vol-0")
	snap := snapshotObject("snap-a", "vm-a")
	// Seed observed status to the Pending value the not-cold path would write.
	snap.Status = snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhasePending, Message: "waiting for VM cold (stopped/defined)"}
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseRunning}
	dep := snapDeps(t, vm, snapVolumeObject("vol-0", "vm-a", "block-pool", 0))
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventModified, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile() RequeueAfter = 0, want delayed requeue while not cold")
	}
	if dep.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 when observed already equals desired", dep.patchCalls)
	}
}

func TestSnapshotDeletedEventIsNoOp(t *testing.T) {
	snap := snapshotObject("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseStopped}
	dep := &snapDepReader{objects: map[string][]byte{}}
	c := NewSnapshotController(vols, runner, dep)

	result, err := c.Reconcile(context.Background(), newSnapshotEvent(t, controller.EventDeleted, snap))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.ShouldRequeue() {
		t.Fatalf("Reconcile() requeued, want done on DELETED no-op")
	}
	if len(vols.snapshotCalls) != 0 || len(vols.deleteCalls) != 0 {
		t.Errorf("volume service touched on DELETED: snap=%d del=%d, want 0/0", len(vols.snapshotCalls), len(vols.deleteCalls))
	}
}

func TestSnapshotContextCancelledPropagates(t *testing.T) {
	snap := snapshotObject("snap-a", "vm-a")
	vols := &fakeVolumeSnapshotter{}
	runner := &snapVMRunner{phase: vmm.PhaseStopped}
	dep := snapDeps(t, snapVMObject("vm-a", "vol-0"), snapVolumeObject("vol-0", "vm-a", "block-pool", 0))
	c := NewSnapshotController(vols, runner, dep)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := c.Reconcile(ctx, newSnapshotEvent(t, controller.EventAdded, snap))
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconcile() error = %v, want wrapped context.Canceled", err)
	}
	if result.ShouldRequeue() {
		t.Fatalf("Reconcile() requeued, want done when ctx cancelled before work")
	}
	if len(vols.snapshotCalls) != 0 {
		t.Errorf("SnapshotVolume called %d times after ctx cancel, want 0", len(vols.snapshotCalls))
	}
}
