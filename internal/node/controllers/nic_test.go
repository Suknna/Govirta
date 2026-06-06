package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"

	"github.com/suknna/govirta/internal/network/netpool"
	"github.com/suknna/govirta/internal/network/networker"
	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/node/identity"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	"github.com/suknna/govirta/pkg/hostnet/link"
)

// fakeNICEnsurer captures the NICDefinition passed to RegisterNIC and serves
// canned ensure/status results. It is faithful to *network.NICService:
// RegisterNIC can report an idempotent ErrAlreadyExists, and EnsureNIC/
// GetNICStatus honour ctx cancellation before returning.
type fakeNICEnsurer struct {
	registered     []netpool.NICDefinition
	registerErr    error
	ensureStatus   netpool.NICStatus
	ensureErr      error
	statusResult   netpool.NICStatus
	statusErr      error
	ensureCalls    int
	getStatusCalls int
}

func (f *fakeNICEnsurer) RegisterNIC(def netpool.NICDefinition) error {
	f.registered = append(f.registered, def)
	return f.registerErr
}

func (f *fakeNICEnsurer) EnsureNIC(ctx context.Context, networkName netpool.NetworkName, vmID netpool.VMID) (netpool.NICStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NICStatus{}, err
	}
	f.ensureCalls++
	if f.ensureErr != nil {
		return netpool.NICStatus{}, f.ensureErr
	}
	return f.ensureStatus, nil
}

func (f *fakeNICEnsurer) GetNICStatus(ctx context.Context, networkName netpool.NetworkName, vmID netpool.VMID) (netpool.NICStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NICStatus{}, err
	}
	f.getStatusCalls++
	if f.statusErr != nil {
		return netpool.NICStatus{}, f.statusErr
	}
	return f.statusResult, nil
}

// fakeDependencyReader serves a canned Network object (by phase) for the gate
// read and captures the NIC status JSON patched. It honours ctx cancellation,
// faithful to *client.Client, and can report client.ErrNotFound for the gate.
type fakeNICDependencyReader struct {
	networkPhase   networkv1.NetworkPhase
	networkMissing bool
	getErr         error
	patchErr       error
	getCalls       int
	patchCalls     int
	patches        []capturedNICPatch
}

type capturedNICPatch struct {
	kind   string
	name   string
	status nicv1.NICStatus
}

func (f *fakeNICDependencyReader) Get(ctx context.Context, kind, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.networkMissing {
		return nil, client.ErrNotFound
	}
	n := networkv1.Network{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindNetwork},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     networkv1.NetworkStatus{Phase: f.networkPhase},
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (f *fakeNICDependencyReader) PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.patchCalls++
	if f.patchErr != nil {
		return nil, f.patchErr
	}
	var decoded nicv1.NICStatus
	if err := json.Unmarshal(status, &decoded); err != nil {
		return nil, err
	}
	f.patches = append(f.patches, capturedNICPatch{kind: kind, name: name, status: decoded})
	return status, nil
}

// testMAC is the apiserver-allocated MAC the controller must thread through
// unchanged. It is a stable, valid locally-administered unicast address.
const testMAC = "52:54:00:12:34:56"

func newNICEvent(t *testing.T, evType controller.EventType, n nicv1.NIC) controller.Event {
	t.Helper()
	raw, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal NIC: %v", err)
	}
	return controller.Event{Type: evType, Key: n.Name, Object: raw}
}

func validNIC(name string) nicv1.NIC {
	return nicv1.NIC{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindNIC},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name},
		Spec: nicv1.NICSpec{
			NetworkRef: "net-a",
			VMRef:      "vm-" + name,
			MAC:        testMAC,
			IP:         "192.168.100.10",
			Hostname:   "host-" + name,
		},
	}
}

func newReadyNICController(nics NICEnsurer, reader DependencyReader) *NICController {
	return NewNICController(nics, reader, link.ExplicitUID(107), link.ExplicitGID(107))
}

func TestNICReconcileAddedReady(t *testing.T) {
	nics := &fakeNICEnsurer{}
	reader := &fakeNICDependencyReader{networkPhase: networkv1.NetworkPhaseReady}
	c := newReadyNICController(nics, reader)

	nic := validNIC("nic-a")
	ev := newNICEvent(t, controller.EventAdded, nic)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}

	// RegisterNIC + EnsureNIC + GetNICStatus each called once.
	if len(nics.registered) != 1 {
		t.Fatalf("RegisterNIC called %d times, want 1", len(nics.registered))
	}
	if nics.ensureCalls != 1 {
		t.Fatalf("EnsureNIC called %d times, want 1", nics.ensureCalls)
	}
	if nics.getStatusCalls != 1 {
		t.Fatalf("GetNICStatus called %d times, want 1", nics.getStatusCalls)
	}

	got := nics.registered[0]

	// Semantic intent from the spec is parsed into the definition.
	if got.NetworkName != netpool.NetworkName("net-a") {
		t.Errorf("def.NetworkName = %q, want %q", got.NetworkName, "net-a")
	}
	if got.VMID != netpool.VMID("vm-nic-a") {
		t.Errorf("def.VMID = %q, want %q", got.VMID, "vm-nic-a")
	}
	if got.IP.String() != "192.168.100.10" {
		t.Errorf("def.IP = %q, want %q", got.IP, "192.168.100.10")
	}
	if got.TapMTU != tapMTU {
		t.Errorf("def.TapMTU = %d, want %d", got.TapMTU, tapMTU)
	}
	if !got.Hostname.Set || got.Hostname.Value != "host-nic-a" {
		t.Errorf("def.Hostname = %+v, want set %q", got.Hostname, "host-nic-a")
	}

	// TAP owner identity is the injected config principal, never derived.
	if !got.OwnerUID.Set || got.OwnerUID.Value != 107 {
		t.Errorf("def.OwnerUID = %+v, want {107 true}", got.OwnerUID)
	}
	if !got.OwnerGID.Set || got.OwnerGID.Value != 107 {
		t.Errorf("def.OwnerGID = %+v, want {107 true}", got.OwnerGID)
	}

	// MAC 铁律: the MAC threaded into the definition must equal the apiserver
	// value byte-for-byte; the controller never generates or rewrites it.
	wantMAC, err := net.ParseMAC(testMAC)
	if err != nil {
		t.Fatalf("parse testMAC: %v", err)
	}
	if got.MAC.String() != wantMAC.String() {
		t.Errorf("def.MAC = %q, want spec MAC %q (透传, 绝不生成)", got.MAC, wantMAC)
	}
	if got.MAC.String() != testMAC {
		t.Errorf("def.MAC = %q, want raw spec value %q", got.MAC, testMAC)
	}

	// 决策核心断言：内核 TAP/反欺骗身份不来自 spec，由 VM ref 经
	// identity.DeriveNICIdentity 确定性派生后填入定义。逐字段对照派生结果。
	wantID := identity.DeriveNICIdentity(nic.Spec.VMRef, nicIndex)
	if got.TapName == "" {
		t.Errorf("def.TapName empty, want derived non-empty")
	}
	if got.TapName != wantID.TapName {
		t.Errorf("def.TapName = %q, want derived %q", got.TapName, wantID.TapName)
	}
	if got.VNetHeader != wantID.VNetHeader {
		t.Errorf("def.VNetHeader = %q, want derived %q", got.VNetHeader, wantID.VNetHeader)
	}
	if got.AntiSpoofTable != wantID.AntiSpoofTable {
		t.Errorf("def.AntiSpoofTable = %q, want derived %q", got.AntiSpoofTable, wantID.AntiSpoofTable)
	}
	if got.AntiSpoofChain != wantID.AntiSpoofChain {
		t.Errorf("def.AntiSpoofChain = %q, want derived %q", got.AntiSpoofChain, wantID.AntiSpoofChain)
	}
	if got.AntiSpoofPriority != wantID.AntiSpoofPriority {
		t.Errorf("def.AntiSpoofPriority = %v, want derived %v", got.AntiSpoofPriority, wantID.AntiSpoofPriority)
	}

	// Status patched ready, carrying the derived TapName, no message.
	if len(reader.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(reader.patches))
	}
	patch := reader.patches[0]
	if patch.kind != string(metav1.KindNIC) {
		t.Errorf("patch kind = %q, want %q", patch.kind, metav1.KindNIC)
	}
	if patch.name != "nic-a" {
		t.Errorf("patch name = %q, want %q", patch.name, "nic-a")
	}
	if patch.status.Phase != nicv1.NICPhaseReady {
		t.Errorf("patch phase = %q, want %q", patch.status.Phase, nicv1.NICPhaseReady)
	}
	if patch.status.TapName != string(wantID.TapName) {
		t.Errorf("patch tapName = %q, want derived %q", patch.status.TapName, wantID.TapName)
	}
	if patch.status.Message != "" {
		t.Errorf("patch message = %q, want empty on ready", patch.status.Message)
	}
}

func TestNICReconcileNetworkNotReadyRequeues(t *testing.T) {
	tests := []struct {
		name           string
		networkPhase   networkv1.NetworkPhase
		networkMissing bool
	}{
		{name: "pending", networkPhase: networkv1.NetworkPhasePending},
		{name: "failed", networkPhase: networkv1.NetworkPhaseFailed},
		{name: "missing", networkMissing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nics := &fakeNICEnsurer{}
			reader := &fakeNICDependencyReader{networkPhase: tt.networkPhase, networkMissing: tt.networkMissing}
			c := newReadyNICController(nics, reader)

			ev := newNICEvent(t, controller.EventAdded, validNIC("nic-wait"))

			requeue, err := c.Reconcile(context.Background(), ev)
			if err != nil {
				t.Fatalf("Reconcile() error = %v, want nil when network not ready (wait)", err)
			}
			if !requeue {
				t.Fatalf("Reconcile() requeue = false, want true when network not ready")
			}

			// Gating must short-circuit before any NIC work and any status patch.
			if len(nics.registered) != 0 {
				t.Errorf("RegisterNIC called %d times, want 0 when network not ready", len(nics.registered))
			}
			if nics.ensureCalls != 0 {
				t.Errorf("EnsureNIC called %d times, want 0 when network not ready", nics.ensureCalls)
			}
			if nics.getStatusCalls != 0 {
				t.Errorf("GetNICStatus called %d times, want 0 when network not ready", nics.getStatusCalls)
			}
			if reader.patchCalls != 0 {
				t.Errorf("PatchStatus called %d times, want 0 when only waiting on network", reader.patchCalls)
			}
		})
	}
}

func TestNICReconcileNetworkReadFailureRequeuesNoPatch(t *testing.T) {
	readErr := errors.New("master unreachable")
	nics := &fakeNICEnsurer{}
	reader := &fakeNICDependencyReader{getErr: readErr}
	c := newReadyNICController(nics, reader)

	ev := newNICEvent(t, controller.EventAdded, validNIC("nic-readfail"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err == nil || !errors.Is(err, readErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, readErr)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on transient network read failure")
	}
	if len(nics.registered) != 0 {
		t.Errorf("RegisterNIC called %d times, want 0 when gate read fails", len(nics.registered))
	}
	if reader.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 when readiness could not be assessed", reader.patchCalls)
	}
}

func TestNICReconcileEnsureFailureRequeues(t *testing.T) {
	ensureErr := errors.New("tap create failed")
	nics := &fakeNICEnsurer{ensureErr: ensureErr}
	reader := &fakeNICDependencyReader{networkPhase: networkv1.NetworkPhaseReady}
	c := newReadyNICController(nics, reader)

	ev := newNICEvent(t, controller.EventAdded, validNIC("nic-ensurefail"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err == nil {
		t.Fatalf("Reconcile() error = nil, want non-nil on ensure failure")
	}
	if !errors.Is(err, ensureErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, ensureErr)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on ensure failure")
	}

	// Registered before ensure; status never read after ensure failed.
	if len(nics.registered) != 1 {
		t.Fatalf("RegisterNIC called %d times, want 1", len(nics.registered))
	}
	if nics.getStatusCalls != 0 {
		t.Fatalf("GetNICStatus called %d times, want 0 when ensure fails", nics.getStatusCalls)
	}
	if len(reader.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1 (failed)", len(reader.patches))
	}
	patch := reader.patches[0]
	if patch.status.Phase != nicv1.NICPhaseFailed {
		t.Errorf("patch phase = %q, want %q", patch.status.Phase, nicv1.NICPhaseFailed)
	}
	if patch.status.Message == "" {
		t.Errorf("patch message empty, want failure cause")
	}
}

func TestNICReconcileGetStatusFailureRequeues(t *testing.T) {
	statusErr := errors.New("tap lookup failed")
	nics := &fakeNICEnsurer{statusErr: statusErr}
	reader := &fakeNICDependencyReader{networkPhase: networkv1.NetworkPhaseReady}
	c := newReadyNICController(nics, reader)

	ev := newNICEvent(t, controller.EventAdded, validNIC("nic-statusfail"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err == nil || !errors.Is(err, statusErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, statusErr)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on status-read failure")
	}
	if nics.ensureCalls != 1 {
		t.Fatalf("EnsureNIC called %d times, want 1", nics.ensureCalls)
	}
	if nics.getStatusCalls != 1 {
		t.Fatalf("GetNICStatus called %d times, want 1", nics.getStatusCalls)
	}
	if len(reader.patches) != 1 || reader.patches[0].status.Phase != nicv1.NICPhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reader.patches)
	}
}

func TestNICReconcileRegisterFailureRequeues(t *testing.T) {
	registerErr := errors.New("register rejected")
	nics := &fakeNICEnsurer{registerErr: registerErr}
	reader := &fakeNICDependencyReader{networkPhase: networkv1.NetworkPhaseReady}
	c := newReadyNICController(nics, reader)

	ev := newNICEvent(t, controller.EventAdded, validNIC("nic-regfail"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err == nil || !errors.Is(err, registerErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, registerErr)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on register failure")
	}
	// Register failed (non-idempotent), so ensure is never reached.
	if nics.ensureCalls != 0 {
		t.Fatalf("EnsureNIC called %d times, want 0 when register fails", nics.ensureCalls)
	}
	if len(reader.patches) != 1 || reader.patches[0].status.Phase != nicv1.NICPhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reader.patches)
	}
}

func TestNICReconcileAlreadyRegisteredIsIdempotent(t *testing.T) {
	nics := &fakeNICEnsurer{registerErr: networker.ErrAlreadyExists}
	reader := &fakeNICDependencyReader{networkPhase: networkv1.NetworkPhaseReady}
	c := newReadyNICController(nics, reader)

	ev := newNICEvent(t, controller.EventModified, validNIC("nic-idem"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for already-registered NIC", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}
	// An already-registered NIC must still be ensured and reported ready.
	if nics.ensureCalls != 1 {
		t.Fatalf("EnsureNIC called %d times, want 1", nics.ensureCalls)
	}
	if nics.getStatusCalls != 1 {
		t.Fatalf("GetNICStatus called %d times, want 1", nics.getStatusCalls)
	}
	if len(reader.patches) != 1 || reader.patches[0].status.Phase != nicv1.NICPhaseReady {
		t.Fatalf("expected one ready patch, got %+v", reader.patches)
	}
}

func TestNICReconcileEmptyMACIsPermanentFailure(t *testing.T) {
	nics := &fakeNICEnsurer{}
	reader := &fakeNICDependencyReader{networkPhase: networkv1.NetworkPhaseReady}
	c := newReadyNICController(nics, reader)

	nic := validNIC("nic-nomac")
	nic.Spec.MAC = "" // apiserver allocation absent: permanent config error.
	ev := newNICEvent(t, controller.EventAdded, nic)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for permanent config failure", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false for empty MAC (永久配置错误)")
	}
	// The controller never generates a MAC: an empty MAC must not reach register.
	if len(nics.registered) != 0 {
		t.Fatalf("RegisterNIC called %d times, want 0 on empty MAC", len(nics.registered))
	}
	if nics.ensureCalls != 0 {
		t.Fatalf("EnsureNIC called %d times, want 0 on empty MAC", nics.ensureCalls)
	}
	if len(reader.patches) != 1 || reader.patches[0].status.Phase != nicv1.NICPhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reader.patches)
	}
	if reader.patches[0].status.Message == "" {
		t.Errorf("patch message empty, want config-error cause")
	}
}

func TestNICReconcileMalformedMACIsPermanentFailure(t *testing.T) {
	nics := &fakeNICEnsurer{}
	reader := &fakeNICDependencyReader{networkPhase: networkv1.NetworkPhaseReady}
	c := newReadyNICController(nics, reader)

	nic := validNIC("nic-badmac")
	nic.Spec.MAC = "not-a-mac"
	ev := newNICEvent(t, controller.EventAdded, nic)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for permanent config failure", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false for malformed MAC")
	}
	if len(nics.registered) != 0 {
		t.Fatalf("RegisterNIC called %d times, want 0 on malformed MAC", len(nics.registered))
	}
	if len(reader.patches) != 1 || reader.patches[0].status.Phase != nicv1.NICPhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reader.patches)
	}
}

func TestNICReconcileMalformedIPIsPermanentFailure(t *testing.T) {
	nics := &fakeNICEnsurer{}
	reader := &fakeNICDependencyReader{networkPhase: networkv1.NetworkPhaseReady}
	c := newReadyNICController(nics, reader)

	nic := validNIC("nic-badip")
	nic.Spec.IP = "not-an-ip"
	ev := newNICEvent(t, controller.EventAdded, nic)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for permanent config failure", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false for malformed IP")
	}
	if len(nics.registered) != 0 {
		t.Fatalf("RegisterNIC called %d times, want 0 on malformed IP", len(nics.registered))
	}
	if len(reader.patches) != 1 || reader.patches[0].status.Phase != nicv1.NICPhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reader.patches)
	}
}

func TestNICReconcileDeletedIsNoOp(t *testing.T) {
	nics := &fakeNICEnsurer{}
	reader := &fakeNICDependencyReader{networkPhase: networkv1.NetworkPhaseReady}
	c := newReadyNICController(nics, reader)

	ev := newNICEvent(t, controller.EventDeleted, validNIC("nic-del"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}
	if len(nics.registered) != 0 {
		t.Errorf("RegisterNIC called %d times on DELETED, want 0", len(nics.registered))
	}
	if nics.ensureCalls != 0 {
		t.Errorf("EnsureNIC called %d times on DELETED, want 0", nics.ensureCalls)
	}
	if reader.getCalls != 0 {
		t.Errorf("Get called %d times on DELETED, want 0", reader.getCalls)
	}
	if reader.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times on DELETED, want 0", reader.patchCalls)
	}
}

func TestNICReconcileContextCancelledPropagates(t *testing.T) {
	nics := &fakeNICEnsurer{}
	reader := &fakeNICDependencyReader{networkPhase: networkv1.NetworkPhaseReady}
	c := newReadyNICController(nics, reader)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ev := newNICEvent(t, controller.EventAdded, validNIC("nic-ctx"))

	requeue, err := c.Reconcile(ctx, ev)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconcile() error = %v, want wrapped context.Canceled", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false when context cancelled before work")
	}
	if len(nics.registered) != 0 {
		t.Errorf("RegisterNIC called %d times after ctx cancel, want 0", len(nics.registered))
	}
	if reader.getCalls != 0 {
		t.Errorf("Get called %d times after ctx cancel, want 0", reader.getCalls)
	}
	if reader.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times after ctx cancel, want 0", reader.patchCalls)
	}
}
