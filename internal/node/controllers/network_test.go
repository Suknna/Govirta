package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/network/netpool"
	"github.com/suknna/govirta/internal/network/networker"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/node/identity"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	"github.com/suknna/govirta/pkg/hostnet/dhcp"
)

// fakeNetworkEnsurer captures the NetworkDefinition passed to RegisterNetwork
// and serves canned ensure/status results. It is faithful to
// *network.NetworkService: RegisterNetwork can report an idempotent
// ErrAlreadyExists, and EnsureNetwork/GetNetworkStatus honour ctx cancellation
// before returning.
type fakeNetworkEnsurer struct {
	registered     []netpool.NetworkDefinition
	registerErr    error
	ensureStatus   netpool.NetworkStatus
	ensureErr      error
	statusResult   netpool.NetworkStatus
	statusErr      error
	ensureCalls    int
	getStatusCalls int

	deleteErr   error
	deleteCalls int
	lastDeleted netpool.NetworkName
}

func (f *fakeNetworkEnsurer) RegisterNetwork(def netpool.NetworkDefinition) error {
	f.registered = append(f.registered, def)
	return f.registerErr
}

func (f *fakeNetworkEnsurer) EnsureNetwork(ctx context.Context, name netpool.NetworkName) (netpool.NetworkStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NetworkStatus{}, err
	}
	f.ensureCalls++
	if f.ensureErr != nil {
		return netpool.NetworkStatus{}, f.ensureErr
	}
	return f.ensureStatus, nil
}

func (f *fakeNetworkEnsurer) GetNetworkStatus(ctx context.Context, name netpool.NetworkName) (netpool.NetworkStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NetworkStatus{}, err
	}
	f.getStatusCalls++
	if f.statusErr != nil {
		return netpool.NetworkStatus{}, f.statusErr
	}
	return f.statusResult, nil
}

// DeleteNetwork records the teardown delete and returns a canned error so a test
// can assert the controller tore the network down by name. It honours ctx
// cancellation, faithful to *network.NetworkService.
func (f *fakeNetworkEnsurer) DeleteNetwork(ctx context.Context, name netpool.NetworkName) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.deleteCalls++
	f.lastDeleted = name
	return f.deleteErr
}

// fakeNetworkStatusReporter captures the last NetworkStatus JSON patched and
// honours ctx cancellation, faithful to *client.Client.
type fakeNetworkStatusReporter struct {
	patches              []capturedNetworkPatch
	patchErr             error
	patchCalls           int
	removeFinalizerErr   error
	removeFinalizerCalls int
	lastFinalizerName    string
	lastFinalizer        string
}

type capturedNetworkPatch struct {
	kind   string
	name   string
	status networkv1.NetworkStatus
}

func (f *fakeNetworkStatusReporter) PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.patchCalls++
	if f.patchErr != nil {
		return nil, f.patchErr
	}
	var decoded networkv1.NetworkStatus
	if err := json.Unmarshal(status, &decoded); err != nil {
		return nil, err
	}
	f.patches = append(f.patches, capturedNetworkPatch{kind: kind, name: name, status: decoded})
	return status, nil
}

// RemoveFinalizer records the teardown finalizer removal so a test can assert
// the controller dropped the finalizer after a successful teardown. Faithful to
// *client.Client.
func (f *fakeNetworkStatusReporter) RemoveFinalizer(ctx context.Context, kind, name, finalizer string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.removeFinalizerCalls++
	f.lastFinalizerName = name
	f.lastFinalizer = finalizer
	return f.removeFinalizerErr
}

func newNetworkEvent(t *testing.T, evType controller.EventType, n networkv1.Network) controller.Event {
	t.Helper()
	raw, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal Network: %v", err)
	}
	return controller.Event{Type: evType, Key: n.Name, Object: raw}
}

func validNetwork(name string) networkv1.Network {
	return networkv1.Network{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindNetwork},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name},
		Spec: networkv1.NetworkSpec{
			BridgeName:      "br-" + name,
			Subnet:          "192.168.100.0/24",
			GatewayCIDR:     "192.168.100.1/24",
			DHCPRangeStart:  "192.168.100.10",
			DHCPRangeEnd:    "192.168.100.200",
			EgressInterface: "eth0",
			DNS:             []string{"1.1.1.1"},
			Router:          []string{"192.168.100.1"},
			LeaseSeconds:    3600,
		},
	}
}

// readyStatus is a live netpool status whose DHCP responder is ready, which the
// controller maps to NetworkPhaseReady.
func readyStatus(name string) netpool.NetworkStatus {
	return netpool.NetworkStatus{
		Name: netpool.NetworkName(name),
		DHCP: dhcp.ServerInfo{ID: dhcp.ServerID(name), State: dhcp.ServerStateReady},
	}
}

func TestNetworkReconcileAddedReady(t *testing.T) {
	networks := &fakeNetworkEnsurer{statusResult: readyStatus("net-a")}
	reporter := &fakeNetworkStatusReporter{}
	c := NewNetworkController(networks, reporter)

	n := validNetwork("net-a")
	ev := newNetworkEvent(t, controller.EventAdded, n)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}

	// RegisterNetwork + EnsureNetwork + GetNetworkStatus each called once.
	if len(networks.registered) != 1 {
		t.Fatalf("RegisterNetwork called %d times, want 1", len(networks.registered))
	}
	if networks.ensureCalls != 1 {
		t.Fatalf("EnsureNetwork called %d times, want 1", networks.ensureCalls)
	}
	if networks.getStatusCalls != 1 {
		t.Fatalf("GetNetworkStatus called %d times, want 1", networks.getStatusCalls)
	}

	got := networks.registered[0]

	// Semantic intent from the spec is parsed into the definition.
	if got.Name != netpool.NetworkName("net-a") {
		t.Errorf("def.Name = %q, want %q", got.Name, "net-a")
	}
	if string(got.BridgeName) != "br-net-a" {
		t.Errorf("def.BridgeName = %q, want %q", got.BridgeName, "br-net-a")
	}
	if got.Subnet.String() != "192.168.100.0/24" {
		t.Errorf("def.Subnet = %q, want %q", got.Subnet, "192.168.100.0/24")
	}
	if got.GatewayCIDR.String() != "192.168.100.1/24" {
		t.Errorf("def.GatewayCIDR = %q, want %q", got.GatewayCIDR, "192.168.100.1/24")
	}
	if got.Pool.Start.String() != "192.168.100.10" || got.Pool.End.String() != "192.168.100.200" {
		t.Errorf("def.Pool = %v..%v, want 192.168.100.10..192.168.100.200", got.Pool.Start, got.Pool.End)
	}
	if string(got.EgressIface) != "eth0" {
		t.Errorf("def.EgressIface = %q, want %q", got.EgressIface, "eth0")
	}
	if got.LeaseDuration.Seconds() != 3600 {
		t.Errorf("def.LeaseDuration = %v, want 3600s", got.LeaseDuration)
	}
	if got.Router.Mode != dhcp.DHCPOptionEnabled || len(got.Router.Addrs) != 1 || got.Router.Addrs[0].String() != "192.168.100.1" {
		t.Errorf("def.Router = %+v, want enabled [192.168.100.1]", got.Router)
	}
	if got.DNS.Mode != dhcp.DHCPOptionEnabled || len(got.DNS.Addrs) != 1 || got.DNS.Addrs[0].String() != "1.1.1.1" {
		t.Errorf("def.DNS = %+v, want enabled [1.1.1.1]", got.DNS)
	}

	// 决策 A 核心断言：内核 firewall 身份不来自 spec（spec 无这些字段），由网络名
	// 经 identity.DeriveNetworkIdentity 确定性派生后填入定义。逐字段对照派生结果。
	wantID := identity.DeriveNetworkIdentity("net-a")
	if got.FirewallTable == "" {
		t.Errorf("def.FirewallTable empty, want derived non-empty")
	}
	if got.FirewallTable != wantID.FirewallTable {
		t.Errorf("def.FirewallTable = %q, want derived %q", got.FirewallTable, wantID.FirewallTable)
	}
	if got.MasqueradeChain != wantID.MasqueradeChain {
		t.Errorf("def.MasqueradeChain = %q, want derived %q", got.MasqueradeChain, wantID.MasqueradeChain)
	}
	if got.ForwardChain != wantID.ForwardChain {
		t.Errorf("def.ForwardChain = %q, want derived %q", got.ForwardChain, wantID.ForwardChain)
	}
	if got.RuleOwner != wantID.RuleOwner {
		t.Errorf("def.RuleOwner = %q, want derived %q", got.RuleOwner, wantID.RuleOwner)
	}
	if got.MasqueradePriority != wantID.MasqueradePriority {
		t.Errorf("def.MasqueradePriority = %v, want derived %v", got.MasqueradePriority, wantID.MasqueradePriority)
	}
	if got.ForwardPriority != wantID.ForwardPriority {
		t.Errorf("def.ForwardPriority = %v, want derived %v", got.ForwardPriority, wantID.ForwardPriority)
	}
	// Chains must embed the network name so distinct networks get distinct chains.
	if got.MasqueradeChain == "" || got.ForwardChain == "" || got.RuleOwner == "" {
		t.Errorf("derived chain/owner unexpectedly empty: %+v", got)
	}

	// Status patched ready, no message.
	if len(reporter.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(reporter.patches))
	}
	patch := reporter.patches[0]
	if patch.kind != string(metav1.KindNetwork) {
		t.Errorf("patch kind = %q, want %q", patch.kind, metav1.KindNetwork)
	}
	if patch.name != "net-a" {
		t.Errorf("patch name = %q, want %q", patch.name, "net-a")
	}
	if patch.status.Phase != networkv1.NetworkPhaseReady {
		t.Errorf("patch phase = %q, want %q", patch.status.Phase, networkv1.NetworkPhaseReady)
	}
	if patch.status.Message != "" {
		t.Errorf("patch message = %q, want empty on ready", patch.status.Message)
	}
}

func TestNetworkReconcileDerivedIdentityIsDeterministic(t *testing.T) {
	// Reconciling the same network twice (independent controllers/fakes) must
	// yield byte-identical derived firewall identities: the derivation is pure.
	reconcileOnce := func(name string) netpool.NetworkDefinition {
		networks := &fakeNetworkEnsurer{statusResult: readyStatus(name)}
		c := NewNetworkController(networks, &fakeNetworkStatusReporter{})
		ev := newNetworkEvent(t, controller.EventAdded, validNetwork(name))
		if _, err := c.Reconcile(context.Background(), ev); err != nil {
			t.Fatalf("Reconcile(%q) error = %v", name, err)
		}
		if len(networks.registered) != 1 {
			t.Fatalf("RegisterNetwork called %d times for %q, want 1", len(networks.registered), name)
		}
		return networks.registered[0]
	}

	a1 := reconcileOnce("net-x")
	a2 := reconcileOnce("net-x")
	if a1.FirewallTable != a2.FirewallTable ||
		a1.MasqueradeChain != a2.MasqueradeChain ||
		a1.ForwardChain != a2.ForwardChain ||
		a1.RuleOwner != a2.RuleOwner ||
		a1.MasqueradePriority != a2.MasqueradePriority ||
		a1.ForwardPriority != a2.ForwardPriority {
		t.Fatalf("derived identity not deterministic: %+v vs %+v", a1, a2)
	}

	// A distinct network name yields distinct per-network chains/owner while
	// sharing the single project-owned table.
	b := reconcileOnce("net-y")
	if b.FirewallTable != a1.FirewallTable {
		t.Errorf("distinct networks should share the project table: %q vs %q", b.FirewallTable, a1.FirewallTable)
	}
	if b.MasqueradeChain == a1.MasqueradeChain {
		t.Errorf("distinct networks should get distinct masquerade chains, both %q", b.MasqueradeChain)
	}
	if b.ForwardChain == a1.ForwardChain {
		t.Errorf("distinct networks should get distinct forward chains, both %q", b.ForwardChain)
	}
	if b.RuleOwner == a1.RuleOwner {
		t.Errorf("distinct networks should get distinct rule owners, both %q", b.RuleOwner)
	}
}

func TestNetworkReconcileEnsureFailureRequeues(t *testing.T) {
	ensureErr := errors.New("bridge create failed")
	networks := &fakeNetworkEnsurer{ensureErr: ensureErr}
	reporter := &fakeNetworkStatusReporter{}
	c := NewNetworkController(networks, reporter)

	ev := newNetworkEvent(t, controller.EventAdded, validNetwork("net-fail"))

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
	if len(networks.registered) != 1 {
		t.Fatalf("RegisterNetwork called %d times, want 1", len(networks.registered))
	}
	if networks.getStatusCalls != 0 {
		t.Fatalf("GetNetworkStatus called %d times, want 0 when ensure fails", networks.getStatusCalls)
	}
	if len(reporter.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(reporter.patches))
	}
	patch := reporter.patches[0]
	if patch.status.Phase != networkv1.NetworkPhaseFailed {
		t.Errorf("patch phase = %q, want %q", patch.status.Phase, networkv1.NetworkPhaseFailed)
	}
	if patch.status.Message == "" {
		t.Errorf("patch message empty, want failure cause")
	}
}

func TestNetworkReconcileAlreadyRegisteredIsIdempotent(t *testing.T) {
	networks := &fakeNetworkEnsurer{
		registerErr:  networker.ErrAlreadyExists,
		statusResult: readyStatus("net-idem"),
	}
	reporter := &fakeNetworkStatusReporter{}
	c := NewNetworkController(networks, reporter)

	ev := newNetworkEvent(t, controller.EventModified, validNetwork("net-idem"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for already-registered network", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}
	// An already-registered network must still be ensured and reported ready.
	if networks.ensureCalls != 1 {
		t.Fatalf("EnsureNetwork called %d times, want 1", networks.ensureCalls)
	}
	if networks.getStatusCalls != 1 {
		t.Fatalf("GetNetworkStatus called %d times, want 1", networks.getStatusCalls)
	}
	if len(reporter.patches) != 1 || reporter.patches[0].status.Phase != networkv1.NetworkPhaseReady {
		t.Fatalf("expected one ready patch, got %+v", reporter.patches)
	}
}

func TestNetworkReconcileRepeatedReconcileIsIdempotent(t *testing.T) {
	// Reconciling the same Network twice through one controller (the second
	// register reporting ErrAlreadyExists, as the real service would) yields the
	// same ready patch and a stable derived definition both times.
	networks := &fakeNetworkEnsurer{statusResult: readyStatus("net-rep")}
	reporter := &fakeNetworkStatusReporter{}
	c := NewNetworkController(networks, reporter)
	ev := newNetworkEvent(t, controller.EventAdded, validNetwork("net-rep"))

	if _, err := c.Reconcile(context.Background(), ev); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	// Second pass: the service now reports the network already exists.
	networks.registerErr = networker.ErrAlreadyExists
	if requeue, err := c.Reconcile(context.Background(), ev); err != nil || requeue {
		t.Fatalf("second Reconcile() = (requeue=%v, err=%v), want (false, nil)", requeue, err)
	}

	if len(networks.registered) != 2 {
		t.Fatalf("RegisterNetwork called %d times, want 2", len(networks.registered))
	}
	first, second := networks.registered[0], networks.registered[1]
	if first.FirewallTable != second.FirewallTable ||
		first.MasqueradeChain != second.MasqueradeChain ||
		first.ForwardChain != second.ForwardChain ||
		first.RuleOwner != second.RuleOwner {
		t.Fatalf("repeated reconcile derived divergent identities: %+v vs %+v", first, second)
	}
	if len(reporter.patches) != 2 {
		t.Fatalf("PatchStatus captured %d patches, want 2", len(reporter.patches))
	}
	for i, p := range reporter.patches {
		if p.status.Phase != networkv1.NetworkPhaseReady {
			t.Errorf("patch %d phase = %q, want ready", i, p.status.Phase)
		}
	}
}

func TestNetworkReconcileInvalidSpecIsPermanentFailure(t *testing.T) {
	networks := &fakeNetworkEnsurer{}
	reporter := &fakeNetworkStatusReporter{}
	c := NewNetworkController(networks, reporter)

	n := validNetwork("net-bad")
	n.Spec.Subnet = "not-a-cidr"
	ev := newNetworkEvent(t, controller.EventAdded, n)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for permanent parse failure", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false for permanent parse failure")
	}
	if len(networks.registered) != 0 {
		t.Fatalf("RegisterNetwork called %d times, want 0 on parse failure", len(networks.registered))
	}
	if networks.ensureCalls != 0 {
		t.Fatalf("EnsureNetwork called %d times, want 0 on parse failure", networks.ensureCalls)
	}
	if len(reporter.patches) != 1 || reporter.patches[0].status.Phase != networkv1.NetworkPhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reporter.patches)
	}
}

func TestNetworkReconcileDeletedIsNoOp(t *testing.T) {
	networks := &fakeNetworkEnsurer{}
	reporter := &fakeNetworkStatusReporter{}
	c := NewNetworkController(networks, reporter)

	ev := newNetworkEvent(t, controller.EventDeleted, validNetwork("net-del"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}
	if len(networks.registered) != 0 {
		t.Errorf("RegisterNetwork called %d times on DELETED, want 0", len(networks.registered))
	}
	if networks.ensureCalls != 0 {
		t.Errorf("EnsureNetwork called %d times on DELETED, want 0", networks.ensureCalls)
	}
	if reporter.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times on DELETED, want 0", reporter.patchCalls)
	}
}

func TestNetworkReconcileContextCancelledPropagates(t *testing.T) {
	networks := &fakeNetworkEnsurer{statusResult: readyStatus("net-ctx")}
	reporter := &fakeNetworkStatusReporter{}
	c := NewNetworkController(networks, reporter)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ev := newNetworkEvent(t, controller.EventAdded, validNetwork("net-ctx"))

	requeue, err := c.Reconcile(ctx, ev)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconcile() error = %v, want wrapped context.Canceled", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false when context cancelled before work")
	}
	if len(networks.registered) != 0 {
		t.Errorf("RegisterNetwork called %d times after ctx cancel, want 0", len(networks.registered))
	}
	if reporter.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times after ctx cancel, want 0", reporter.patchCalls)
	}
}

// deletingNetwork returns a valid network stamped for deletion (carrying a
// deletionTimestamp), driving the controller into its teardown branch.
func deletingNetwork(name string) networkv1.Network {
	n := validNetwork(name)
	n.ObjectMeta.DeletionTimestamp = "2026-01-02T15:04:05Z"
	return n
}

// TestNetworkReconcileTeardownDeletesAndRemovesFinalizer proves the teardown
// branch: a deletion-stamped network is deleted from the network service (keyed
// by name) and, once deleted, the node-teardown finalizer is removed so apiserver
// can finalize the delete. The ensure path (RegisterNetwork/EnsureNetwork) must
// not run.
func TestNetworkReconcileTeardownDeletesAndRemovesFinalizer(t *testing.T) {
	networks := &fakeNetworkEnsurer{}
	reporter := &fakeNetworkStatusReporter{}
	c := NewNetworkController(networks, reporter)

	ev := newNetworkEvent(t, controller.EventModified, deletingNetwork("net-del"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil on successful teardown", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false after teardown + finalizer removal")
	}
	if networks.deleteCalls != 1 {
		t.Fatalf("DeleteNetwork called %d times, want 1", networks.deleteCalls)
	}
	if networks.lastDeleted != netpool.NetworkName("net-del") {
		t.Errorf("DeleteNetwork name = %q, want %q", networks.lastDeleted, "net-del")
	}
	if len(networks.registered) != 0 {
		t.Errorf("RegisterNetwork called %d times during teardown, want 0", len(networks.registered))
	}
	if networks.ensureCalls != 0 {
		t.Errorf("EnsureNetwork called %d times during teardown, want 0", networks.ensureCalls)
	}
	if reporter.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1", reporter.removeFinalizerCalls)
	}
	if reporter.lastFinalizerName != "net-del" {
		t.Errorf("RemoveFinalizer name = %q, want %q", reporter.lastFinalizerName, "net-del")
	}
	if reporter.lastFinalizer != string(metav1.FinalizerNodeTeardown) {
		t.Errorf("RemoveFinalizer finalizer = %q, want %q", reporter.lastFinalizer, metav1.FinalizerNodeTeardown)
	}
}

// TestNetworkReconcileTeardownAlreadyGoneIsIdempotent proves a teardown where the
// network is already gone (networker.ErrNotFound) still drops the finalizer: an
// already-deleted network is a tear-down success, not a stall.
func TestNetworkReconcileTeardownAlreadyGoneIsIdempotent(t *testing.T) {
	networks := &fakeNetworkEnsurer{deleteErr: networker.ErrNotFound}
	reporter := &fakeNetworkStatusReporter{}
	c := NewNetworkController(networks, reporter)

	ev := newNetworkEvent(t, controller.EventModified, deletingNetwork("net-gone"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for already-deleted network", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false when network already gone")
	}
	if networks.deleteCalls != 1 {
		t.Fatalf("DeleteNetwork called %d times, want 1", networks.deleteCalls)
	}
	if reporter.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1 (NotFound is idempotent success)", reporter.removeFinalizerCalls)
	}
}

// TestNetworkReconcileTeardownConflictRequeuesKeepingFinalizer proves a real
// conflict (networker.ErrConflict: the network still has registered NICs) keeps
// the finalizer and requeues so the referencing NICs tear down first.
func TestNetworkReconcileTeardownConflictRequeuesKeepingFinalizer(t *testing.T) {
	networks := &fakeNetworkEnsurer{deleteErr: networker.ErrConflict}
	reporter := &fakeNetworkStatusReporter{}
	c := NewNetworkController(networks, reporter)

	ev := newNetworkEvent(t, controller.EventModified, deletingNetwork("net-busy"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err == nil || !errors.Is(err, networker.ErrConflict) {
		t.Fatalf("Reconcile() error = %v, want wrapped networker.ErrConflict", err)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on a real teardown conflict")
	}
	if reporter.removeFinalizerCalls != 0 {
		t.Fatalf("RemoveFinalizer called %d times, want 0 when teardown conflicts (finalizer kept)", reporter.removeFinalizerCalls)
	}
}
