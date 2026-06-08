package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/vmm"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
)

// fakeVMRunner records Create/Start calls and returns a configurable live phase
// from Status. statusErr controls the idempotency probe: vmm.ErrNotFound means
// "not yet created" (the normal create path), any other error is transient, and
// nil means an already-existing guest.
type fakeVMRunner struct {
	mu sync.Mutex

	statusErr   error
	statusPhase vmm.Phase

	createErr  error
	startErr   error
	startPhase vmm.Phase

	killErr   error
	deleteErr error

	createCalls int
	startCalls  int
	killCalls   int
	deleteCalls int
	lastCreate  vmm.CreateRequest
}

func (f *fakeVMRunner) Create(ctx context.Context, req vmm.CreateRequest) (vmm.VM, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.lastCreate = req
	if f.createErr != nil {
		return vmm.VM{}, f.createErr
	}
	return vmm.VM{UUID: req.UUID, Phase: vmm.PhaseDefined}, nil
}

func (f *fakeVMRunner) Start(ctx context.Context, uuid string) (vmm.VM, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	if f.startErr != nil {
		return vmm.VM{}, f.startErr
	}
	return vmm.VM{UUID: uuid, Phase: f.startPhase}, nil
}

func (f *fakeVMRunner) Status(ctx context.Context, uuid string) (vmm.VM, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return vmm.VM{}, f.statusErr
	}
	return vmm.VM{UUID: uuid, Phase: f.statusPhase}, nil
}

// Kill records the forced-destroy call the teardown state machine issues for
// a live guest and returns a canned error. Faithful to *vmm.VMMService.
func (f *fakeVMRunner) Kill(ctx context.Context, uuid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killCalls++
	return f.killErr
}

// Delete records the runtime-state removal the teardown state machine issues once
// the guest process is dead and returns a canned error. Faithful to
// *vmm.VMMService.
func (f *fakeVMRunner) Delete(ctx context.Context, uuid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	return f.deleteErr
}

// fakeVMDepReader serves Volume and NIC objects by kind/name for dependency
// gating and captures the VM status patches. Per-ref readiness is configured via
// the seeded raw objects; a ref absent from the maps returns client.ErrNotFound.
type fakeVMDepReader struct {
	mu sync.Mutex

	volumes map[string]volumev1.Volume
	nics    map[string]nicv1.NIC
	getErr  map[string]error

	patched []vmv1.VMStatus

	removeFinalizerErr   error
	removeFinalizerCalls int
	lastFinalizerName    string
	lastFinalizer        string
}

func (f *fakeVMDepReader) Get(ctx context.Context, kind, name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.getErr[name]; err != nil {
		return nil, err
	}
	switch kind {
	case string(metav1.KindVolume):
		vol, ok := f.volumes[name]
		if !ok {
			return nil, client.ErrNotFound
		}
		return json.Marshal(vol)
	case string(metav1.KindNIC):
		nic, ok := f.nics[name]
		if !ok {
			return nil, client.ErrNotFound
		}
		return json.Marshal(nic)
	default:
		return nil, client.ErrNotFound
	}
}

func (f *fakeVMDepReader) PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s vmv1.VMStatus
	if err := json.Unmarshal(status, &s); err != nil {
		return nil, err
	}
	f.patched = append(f.patched, s)
	return status, nil
}

// RemoveFinalizer records the teardown finalizer removal so a test can assert
// the controller dropped the finalizer only once the guest is fully gone.
// Faithful to *client.Client.
func (f *fakeVMDepReader) RemoveFinalizer(ctx context.Context, kind, name, finalizer string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeFinalizerCalls++
	f.lastFinalizerName = name
	f.lastFinalizer = finalizer
	return f.removeFinalizerErr
}

func (f *fakeVMDepReader) lastPatch(t *testing.T) vmv1.VMStatus {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.patched) == 0 {
		t.Fatalf("no status was patched")
	}
	return f.patched[len(f.patched)-1]
}

func readyVolume(name, path string) volumev1.Volume {
	return volumev1.Volume{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindVolume},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name},
		Status:     volumev1.VolumeStatus{Phase: volumev1.VolumePhaseReady, VolumePath: path},
	}
}

func readyNIC(name, tap string) nicv1.NIC {
	return nicv1.NIC{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindNIC},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name},
		Status:     nicv1.NICStatus{Phase: nicv1.NICPhaseReady, TapName: tap},
	}
}

func vmEvent(t *testing.T, evType controller.EventType, vm vmv1.VM) controller.Event {
	t.Helper()
	raw, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("marshal vm: %v", err)
	}
	return controller.Event{Type: evType, Key: vm.Name, Object: raw}
}

func validVMObject() vmv1.VM {
	return vmv1.VM{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindVM},
		ObjectMeta: metav1.ObjectMeta{Name: "vm-a", UID: "uid-vm-a"},
		Spec: vmv1.VMSpec{
			Arch:       "x86_64",
			VCPUs:      2,
			MemoryMiB:  2048,
			VolumeRefs: []string{"vol-a"},
			NICRefs:    []string{"nic-a"},
			PowerState: vmv1.PowerStateOn,
		},
	}
}

func TestVMReconcileAllReadyCreatesAndStarts(t *testing.T) {
	runner := &fakeVMRunner{statusErr: vmm.ErrNotFound, startPhase: vmm.PhaseRunning}
	dep := &fakeVMDepReader{
		volumes: map[string]volumev1.Volume{"vol-a": readyVolume("vol-a", "/var/lib/govirta/vol-a.qcow2")},
		nics:    map[string]nicv1.NIC{"nic-a": readyNIC("nic-a", "gvabc1234.0")},
	}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventAdded, validVMObject()))
	if err != nil {
		t.Fatalf("Reconcile: unexpected error: %v", err)
	}
	if requeue {
		t.Fatalf("requeue = true, want false on successful create+start")
	}
	if runner.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", runner.createCalls)
	}
	if runner.startCalls != 1 {
		t.Fatalf("startCalls = %d, want 1", runner.startCalls)
	}

	// The builder must carry the dependency-resolved disk path and tap name in the
	// summary spec.
	if got := runner.lastCreate.Spec.DiskPaths; len(got) != 1 || got[0] != "/var/lib/govirta/vol-a.qcow2" {
		t.Fatalf("create DiskPaths = %v, want [/var/lib/govirta/vol-a.qcow2]", got)
	}
	if got := runner.lastCreate.Spec.TapNames; len(got) != 1 || got[0] != "gvabc1234.0" {
		t.Fatalf("create TapNames = %v, want [gvabc1234.0]", got)
	}
	if runner.lastCreate.UUID != "uid-vm-a" {
		t.Fatalf("create UUID = %q, want uid-vm-a", runner.lastCreate.UUID)
	}
	if runner.lastCreate.Builder == nil {
		t.Fatalf("create Builder is nil; controller must pass a configured builder")
	}

	if phase := dep.lastPatch(t).Phase; phase != vmv1.VMPhaseRunning {
		t.Fatalf("patched phase = %q, want running", phase)
	}
}

func TestVMReconcileVolumeNotReadyRequeuesWithoutCreate(t *testing.T) {
	pending := readyVolume("vol-a", "/p")
	pending.Status.Phase = volumev1.VolumePhasePending
	runner := &fakeVMRunner{statusErr: vmm.ErrNotFound}
	dep := &fakeVMDepReader{
		volumes: map[string]volumev1.Volume{"vol-a": pending},
		nics:    map[string]nicv1.NIC{"nic-a": readyNIC("nic-a", "tap0")},
	}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventAdded, validVMObject()))
	if err != nil {
		t.Fatalf("Reconcile: unexpected error: %v", err)
	}
	if !requeue {
		t.Fatalf("requeue = false, want true when a volume dependency is not ready")
	}
	if runner.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0 (must not create before dependencies ready)", runner.createCalls)
	}
}

func TestVMReconcileNICNotReadyRequeuesWithoutCreate(t *testing.T) {
	pending := readyNIC("nic-a", "tap0")
	pending.Status.Phase = nicv1.NICPhasePending
	runner := &fakeVMRunner{statusErr: vmm.ErrNotFound}
	dep := &fakeVMDepReader{
		volumes: map[string]volumev1.Volume{"vol-a": readyVolume("vol-a", "/p")},
		nics:    map[string]nicv1.NIC{"nic-a": pending},
	}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventAdded, validVMObject()))
	if err != nil {
		t.Fatalf("Reconcile: unexpected error: %v", err)
	}
	if !requeue {
		t.Fatalf("requeue = false, want true when a NIC dependency is not ready")
	}
	if runner.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", runner.createCalls)
	}
}

func TestVMReconcileMissingDependencyRequeues(t *testing.T) {
	runner := &fakeVMRunner{statusErr: vmm.ErrNotFound}
	dep := &fakeVMDepReader{
		// vol-a absent → ErrNotFound → wait.
		nics: map[string]nicv1.NIC{"nic-a": readyNIC("nic-a", "tap0")},
	}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventAdded, validVMObject()))
	if err != nil {
		t.Fatalf("Reconcile: unexpected error: %v", err)
	}
	if !requeue {
		t.Fatalf("requeue = false, want true when a dependency object is missing")
	}
	if runner.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0", runner.createCalls)
	}
}

func TestVMReconcileAlreadyRunningReReportsWithoutCreate(t *testing.T) {
	// Status returns nil error → guest already exists; controller must re-report
	// its live phase and never create/start again (存活循环 + 进程解耦).
	runner := &fakeVMRunner{statusErr: nil, statusPhase: vmm.PhaseRunning}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, validVMObject()))
	if err != nil {
		t.Fatalf("Reconcile: unexpected error: %v", err)
	}
	if requeue {
		t.Fatalf("requeue = true, want false for an already-running guest")
	}
	if runner.createCalls != 0 || runner.startCalls != 0 {
		t.Fatalf("create=%d start=%d, want 0/0 for an existing guest", runner.createCalls, runner.startCalls)
	}
	if phase := dep.lastPatch(t).Phase; phase != vmv1.VMPhaseRunning {
		t.Fatalf("re-reported phase = %q, want running", phase)
	}
}

func TestVMReconcileAlreadyStartingRequeuesToTrackTerminalPhase(t *testing.T) {
	// An existing guest in a transient phase (Starting) must be requeued so the
	// controller self-drives tracking it to a terminal phase, instead of freezing
	// the master at Starting until an unrelated watch event arrives (M-1). It
	// still must not create/start again (存活循环 + 进程解耦).
	runner := &fakeVMRunner{statusErr: nil, statusPhase: vmm.PhaseStarting}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, validVMObject()))
	if err != nil {
		t.Fatalf("Reconcile: unexpected error: %v", err)
	}
	if !requeue {
		t.Fatalf("requeue = false, want true for a guest in a transient (Starting) phase")
	}
	if runner.createCalls != 0 || runner.startCalls != 0 {
		t.Fatalf("create=%d start=%d, want 0/0 for an existing guest", runner.createCalls, runner.startCalls)
	}
	if phase := dep.lastPatch(t).Phase; phase != vmv1.VMPhaseStarting {
		t.Fatalf("re-reported phase = %q, want starting", phase)
	}
}

func TestVMReconcileCreateFailureRequeues(t *testing.T) {
	runner := &fakeVMRunner{statusErr: vmm.ErrNotFound, createErr: errors.New("spawn failed")}
	dep := &fakeVMDepReader{
		volumes: map[string]volumev1.Volume{"vol-a": readyVolume("vol-a", "/p")},
		nics:    map[string]nicv1.NIC{"nic-a": readyNIC("nic-a", "tap0")},
	}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventAdded, validVMObject()))
	if err == nil {
		t.Fatalf("Reconcile: expected error on create failure")
	}
	if !requeue {
		t.Fatalf("requeue = false, want true on transient create failure")
	}
	if phase := dep.lastPatch(t).Phase; phase != vmv1.VMPhaseFailed {
		t.Fatalf("patched phase = %q, want failed", phase)
	}
	if runner.startCalls != 0 {
		t.Fatalf("startCalls = %d, want 0 when create failed", runner.startCalls)
	}
}

func TestVMReconcileUnsupportedArchIsPermanentFailure(t *testing.T) {
	runner := &fakeVMRunner{statusErr: vmm.ErrNotFound}
	dep := &fakeVMDepReader{
		volumes: map[string]volumev1.Volume{"vol-a": readyVolume("vol-a", "/p")},
		nics:    map[string]nicv1.NIC{"nic-a": readyNIC("nic-a", "tap0")},
	}
	c := NewVMController(runner, dep, cpu.ModelHost)

	obj := validVMObject()
	obj.Spec.Arch = "riscv64"
	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventAdded, obj))
	if err != nil {
		t.Fatalf("Reconcile: unexpected error: %v", err)
	}
	if requeue {
		t.Fatalf("requeue = true, want false for a permanent arch config error")
	}
	if runner.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0 for unsupported arch", runner.createCalls)
	}
	if phase := dep.lastPatch(t).Phase; phase != vmv1.VMPhaseFailed {
		t.Fatalf("patched phase = %q, want failed", phase)
	}
}

func TestVMReconcileStatusTransientErrorRequeues(t *testing.T) {
	runner := &fakeVMRunner{statusErr: errors.New("state file unreadable")}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventAdded, validVMObject()))
	if err == nil {
		t.Fatalf("Reconcile: expected error on transient status failure")
	}
	if !requeue {
		t.Fatalf("requeue = false, want true on transient status error")
	}
	if runner.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0 when status probe failed transiently", runner.createCalls)
	}
}

func TestVMReconcileDeletedIsNoOp(t *testing.T) {
	runner := &fakeVMRunner{statusErr: vmm.ErrNotFound}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventDeleted, validVMObject()))
	if err != nil {
		t.Fatalf("Reconcile: unexpected error: %v", err)
	}
	if requeue {
		t.Fatalf("requeue = true, want false for DELETED no-op")
	}
	if runner.createCalls != 0 || runner.startCalls != 0 {
		t.Fatalf("create=%d start=%d, want 0/0 for DELETED", runner.createCalls, runner.startCalls)
	}
}

func TestVMReconcileContextCancelledPropagates(t *testing.T) {
	runner := &fakeVMRunner{statusErr: vmm.ErrNotFound}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	requeue, err := c.Reconcile(ctx, vmEvent(t, controller.EventAdded, validVMObject()))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if requeue {
		t.Fatalf("requeue = true, want false on cancelled context")
	}
}

// deletingVMObject returns a valid VM stamped for deletion (carrying a
// deletionTimestamp), driving the controller into its teardown branch.
func deletingVMObject() vmv1.VM {
	vm := validVMObject()
	vm.ObjectMeta.DeletionTimestamp = "2026-01-02T15:04:05Z"
	return vm
}

// TestVMReconcileTeardownRunningKillsAndRequeuesKeepingFinalizer proves the
// multi-step teardown's first leg: a live (Running) guest is forcibly destroyed
// (vmm.Kill — QMP quit + SIGKILL fallback, not ACPI-graceful, since delete means
// destroy and minimal guests like CirrOS ignore ACPI powerdown) and the reconcile
// requeues WITHOUT dropping the finalizer, so the object stays "deleting" until a
// later pass observes the process gone. vmm.Delete must not run on a running
// guest (it would return ErrConflict).
func TestVMReconcileTeardownRunningKillsAndRequeuesKeepingFinalizer(t *testing.T) {
	runner := &fakeVMRunner{statusErr: nil, statusPhase: vmm.PhaseRunning}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, deletingVMObject()))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil while teardown in progress", err)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true while awaiting process exit")
	}
	if runner.killCalls != 1 {
		t.Fatalf("Kill called %d times, want 1 for a running guest", runner.killCalls)
	}
	if runner.deleteCalls != 0 {
		t.Fatalf("Delete called %d times, want 0 for a still-running guest (would ErrConflict)", runner.deleteCalls)
	}
	if dep.removeFinalizerCalls != 0 {
		t.Fatalf("RemoveFinalizer called %d times, want 0 while teardown in progress (finalizer kept)", dep.removeFinalizerCalls)
	}
	if runner.createCalls != 0 || runner.startCalls != 0 {
		t.Fatalf("create=%d start=%d, want 0/0 during teardown", runner.createCalls, runner.startCalls)
	}
}

// TestVMReconcileTeardownStartingKillsAndRequeues proves a guest still coming up
// (Starting) is also forcibly killed and requeued without dropping the
// finalizer — the same first-leg behavior as Running.
func TestVMReconcileTeardownStartingKillsAndRequeues(t *testing.T) {
	runner := &fakeVMRunner{statusErr: nil, statusPhase: vmm.PhaseStarting}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, deletingVMObject()))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil while teardown in progress", err)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true while awaiting process exit")
	}
	if runner.killCalls != 1 {
		t.Fatalf("Kill called %d times, want 1 for a starting guest", runner.killCalls)
	}
	if runner.deleteCalls != 0 {
		t.Fatalf("Delete called %d times, want 0 for a not-yet-terminal guest", runner.deleteCalls)
	}
	if dep.removeFinalizerCalls != 0 {
		t.Fatalf("RemoveFinalizer called %d times, want 0 while teardown in progress", dep.removeFinalizerCalls)
	}
}

// TestVMReconcileTeardownStoppingRequeuesWithoutKillOrDelete proves a kill
// already in flight (Stopping) is left alone: no second Kill, no Delete, and the
// finalizer is kept while the reconcile requeues to await the terminal state.
func TestVMReconcileTeardownStoppingRequeuesWithoutKillOrDelete(t *testing.T) {
	runner := &fakeVMRunner{statusErr: nil, statusPhase: vmm.PhaseStopping}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, deletingVMObject()))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil while teardown in progress", err)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true while awaiting terminal state")
	}
	if runner.killCalls != 0 {
		t.Fatalf("Kill called %d times, want 0 when kill already in flight", runner.killCalls)
	}
	if runner.deleteCalls != 0 {
		t.Fatalf("Delete called %d times, want 0 while still stopping", runner.deleteCalls)
	}
	if dep.removeFinalizerCalls != 0 {
		t.Fatalf("RemoveFinalizer called %d times, want 0 while teardown in progress", dep.removeFinalizerCalls)
	}
}

// TestVMReconcileTeardownStoppedDeletesAndRemovesFinalizer proves the multi-step
// teardown's second leg: a dead guest (Stopped) has its persisted runtime state
// removed (vmm.Delete) and, once gone, the node-teardown finalizer is dropped so
// apiserver can finalize the delete.
func TestVMReconcileTeardownStoppedDeletesAndRemovesFinalizer(t *testing.T) {
	runner := &fakeVMRunner{statusErr: nil, statusPhase: vmm.PhaseStopped}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, deletingVMObject()))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil on successful teardown", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false after delete + finalizer removal")
	}
	if runner.killCalls != 0 {
		t.Fatalf("Kill called %d times, want 0 for an already-dead guest", runner.killCalls)
	}
	if runner.deleteCalls != 1 {
		t.Fatalf("Delete called %d times, want 1 for a stopped guest", runner.deleteCalls)
	}
	if dep.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1", dep.removeFinalizerCalls)
	}
	if dep.lastFinalizerName != "vm-a" {
		t.Errorf("RemoveFinalizer name = %q, want %q", dep.lastFinalizerName, "vm-a")
	}
	if dep.lastFinalizer != string(metav1.FinalizerNodeTeardown) {
		t.Errorf("RemoveFinalizer finalizer = %q, want %q", dep.lastFinalizer, metav1.FinalizerNodeTeardown)
	}
}

// TestVMReconcileTeardownFailedDeletesAndRemovesFinalizer proves a guest that
// died abnormally (Failed) is also a terminal state the teardown deletes, then
// drops the finalizer.
func TestVMReconcileTeardownFailedDeletesAndRemovesFinalizer(t *testing.T) {
	runner := &fakeVMRunner{statusErr: nil, statusPhase: vmm.PhaseFailed}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, deletingVMObject()))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil on successful teardown", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false after delete + finalizer removal")
	}
	if runner.deleteCalls != 1 {
		t.Fatalf("Delete called %d times, want 1 for a failed (terminal) guest", runner.deleteCalls)
	}
	if dep.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1", dep.removeFinalizerCalls)
	}
}

// TestVMReconcileTeardownAlreadyGoneRemovesFinalizer proves that when the guest's
// state is already gone (vmm.ErrNotFound from Status), teardown treats it as torn
// down and drops the finalizer without calling Stop or Delete.
func TestVMReconcileTeardownAlreadyGoneRemovesFinalizer(t *testing.T) {
	runner := &fakeVMRunner{statusErr: vmm.ErrNotFound}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, deletingVMObject()))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for already-gone guest", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false when guest already gone")
	}
	if runner.killCalls != 0 || runner.deleteCalls != 0 {
		t.Fatalf("kill=%d delete=%d, want 0/0 when state already gone", runner.killCalls, runner.deleteCalls)
	}
	if dep.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1 (already gone is torn down)", dep.removeFinalizerCalls)
	}
}

// TestVMReconcileTeardownDeleteNotFoundIsIdempotent proves Delete returning
// vmm.ErrNotFound (the state vanished between Status and Delete) is an idempotent
// success: the finalizer is still dropped.
func TestVMReconcileTeardownDeleteNotFoundIsIdempotent(t *testing.T) {
	runner := &fakeVMRunner{statusErr: nil, statusPhase: vmm.PhaseStopped, deleteErr: vmm.ErrNotFound}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, deletingVMObject()))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for idempotent delete-not-found", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false on idempotent delete")
	}
	if runner.deleteCalls != 1 {
		t.Fatalf("Delete called %d times, want 1", runner.deleteCalls)
	}
	if dep.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1 (delete NotFound is idempotent success)", dep.removeFinalizerCalls)
	}
}

// TestVMReconcileTeardownKillFailureRequeuesKeepingFinalizer proves a Kill failure
// keeps the finalizer and requeues: the guest could not be confirmed gone, so the
// object stays "deleting".
func TestVMReconcileTeardownKillFailureRequeuesKeepingFinalizer(t *testing.T) {
	killErr := errors.New("qmp quit + sigkill failed")
	runner := &fakeVMRunner{statusErr: nil, statusPhase: vmm.PhaseRunning, killErr: killErr}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, deletingVMObject()))
	if err == nil || !errors.Is(err, killErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, killErr)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on kill failure")
	}
	if dep.removeFinalizerCalls != 0 {
		t.Fatalf("RemoveFinalizer called %d times, want 0 when kill fails (finalizer kept)", dep.removeFinalizerCalls)
	}
}

// TestVMReconcileTeardownDeleteFailureRequeuesKeepingFinalizer proves a real
// (non-NotFound) Delete failure keeps the finalizer and requeues.
func TestVMReconcileTeardownDeleteFailureRequeuesKeepingFinalizer(t *testing.T) {
	deleteErr := errors.New("remove state dir failed")
	runner := &fakeVMRunner{statusErr: nil, statusPhase: vmm.PhaseStopped, deleteErr: deleteErr}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, deletingVMObject()))
	if err == nil || !errors.Is(err, deleteErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, deleteErr)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on delete failure")
	}
	if dep.removeFinalizerCalls != 0 {
		t.Fatalf("RemoveFinalizer called %d times, want 0 when delete fails (finalizer kept)", dep.removeFinalizerCalls)
	}
}

// TestVMReconcileTeardownStatusErrorRequeuesKeepingFinalizer proves a transient
// Status error (not ErrNotFound) keeps the finalizer and requeues: readiness to
// tear down could not be assessed.
func TestVMReconcileTeardownStatusErrorRequeuesKeepingFinalizer(t *testing.T) {
	statusErr := errors.New("state file unreadable")
	runner := &fakeVMRunner{statusErr: statusErr}
	dep := &fakeVMDepReader{}
	c := NewVMController(runner, dep, cpu.ModelHost)

	requeue, err := c.Reconcile(context.Background(), vmEvent(t, controller.EventModified, deletingVMObject()))
	if err == nil || !errors.Is(err, statusErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, statusErr)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on transient status error during teardown")
	}
	if runner.killCalls != 0 || runner.deleteCalls != 0 {
		t.Fatalf("kill=%d delete=%d, want 0/0 when status could not be read", runner.killCalls, runner.deleteCalls)
	}
	if dep.removeFinalizerCalls != 0 {
		t.Fatalf("RemoveFinalizer called %d times, want 0 when status read fails (finalizer kept)", dep.removeFinalizerCalls)
	}
}
