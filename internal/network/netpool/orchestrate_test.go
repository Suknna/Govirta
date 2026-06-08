package netpool

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/suknna/govirta/internal/network/networker"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
)

// macEqual compares two hardware addresses by their canonical string form.
func macEqual(a, b net.HardwareAddr) bool {
	return a.String() == b.String()
}

func TestEnsureNetworkOrchestrationOrder(t *testing.T) {
	svc, _, _, _, _, rec := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}

	if _, err := svc.EnsureNetwork(context.Background(), "net0"); err != nil {
		t.Fatalf("EnsureNetwork error = %v, want nil", err)
	}

	seq := rec.sequence()
	want := []string{
		"link.EnsureBridge",
		"route.CheckIPv4Forwarding",
		"firewall.EnsureMasquerade",
		"firewall.EnsureForwardAccept",
		"dhcp.Start",
	}
	if len(seq) < len(want) {
		t.Fatalf("recorded sequence %v shorter than expected prefix %v", seq, want)
	}
	if !reflect.DeepEqual(seq[:len(want)], want) {
		t.Fatalf("orchestration order = %v, want prefix %v", seq[:len(want)], want)
	}
}

func TestEnsureNICPassesGuestMACToAllThree(t *testing.T) {
	svc, fl, _, ff, fd, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC error = %v, want nil", err)
	}

	if _, err := svc.EnsureNIC(context.Background(), "net0", "vm1"); err != nil {
		t.Fatalf("EnsureNIC error = %v, want nil", err)
	}

	wantMAC := sampleNIC().MAC
	if !macEqual(fl.tapSpec.MAC, wantMAC) {
		t.Fatalf("TAP MAC = %v, want %v", fl.tapSpec.MAC, wantMAC)
	}
	if !macEqual(fd.bindingReq.MAC, wantMAC) {
		t.Fatalf("DHCP binding MAC = %v, want %v", fd.bindingReq.MAC, wantMAC)
	}
	if !macEqual(ff.endpointSpec.MAC, wantMAC) {
		t.Fatalf("anti-spoofing MAC = %v, want %v", ff.endpointSpec.MAC, wantMAC)
	}
	// All three must carry the identical guest MAC threaded unchanged.
	if !macEqual(fl.tapSpec.MAC, fd.bindingReq.MAC) || !macEqual(fd.bindingReq.MAC, ff.endpointSpec.MAC) {
		t.Fatalf("MAC mismatch across primitives: tap=%v binding=%v antispoof=%v",
			fl.tapSpec.MAC, fd.bindingReq.MAC, ff.endpointSpec.MAC)
	}
}

func TestEnsureNetworkDoesNotTearDownOnDHCPFailure(t *testing.T) {
	svc, _, _, _, fd, rec := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	fd.errs["Start"] = errors.New("dhcp start boom")

	_, err := svc.EnsureNetwork(context.Background(), "net0")
	if err == nil {
		t.Fatalf("EnsureNetwork error = nil, want non-nil on DHCP Start failure")
	}

	// Partial failure must not trigger any teardown of already-created resources.
	teardown := []string{
		"link.Delete",
		"firewall.DeleteMasquerade",
		"firewall.DeleteForwardAccept",
		"firewall.DeleteEndpointAntiSpoofing",
		"dhcp.Stop",
	}
	for _, name := range teardown {
		if got := rec.count(name); got != 0 {
			t.Fatalf("teardown call %q recorded %d times, want 0; sequence=%v", name, got, rec.sequence())
		}
	}
}

func TestDeleteNetworkReverseOrder(t *testing.T) {
	svc, _, _, _, _, rec := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	if _, err := svc.EnsureNetwork(context.Background(), "net0"); err != nil {
		t.Fatalf("EnsureNetwork error = %v, want nil", err)
	}

	// Clear the ensure-phase calls (including GetNetworkStatus reads) so the
	// delete-phase slice is clean.
	rec.reset()

	if err := svc.DeleteNetwork(context.Background(), "net0"); err != nil {
		t.Fatalf("DeleteNetwork error = %v, want nil", err)
	}

	got := rec.sequence()
	// The delete path now resolves each firewall rule ref live before deleting
	// it (callers carry no firewall handle), so a ListRules read precedes each
	// firewall delete. The reverse teardown order is otherwise unchanged.
	want := []string{
		"dhcp.Stop",
		"firewall.ListRules",
		"firewall.DeleteForwardAccept",
		"firewall.ListRules",
		"firewall.DeleteMasquerade",
		"link.Delete",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delete-phase order = %v, want %v", got, want)
	}
}

func TestDeleteNetworkIdempotentWhenRulesAbsent(t *testing.T) {
	svc, _, _, ff, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}

	// No EnsureNetwork: the masquerade and forward-accept rules were never
	// created, so the live ref resolution observes nothing. A delete that
	// cannot find a rule must treat it as already torn down rather than error,
	// so repeated Delete retries stay idempotent.
	if err := svc.DeleteNetwork(context.Background(), "net0"); err != nil {
		t.Fatalf("DeleteNetwork(rules absent) error = %v, want nil", err)
	}
	if len(ff.deletedRefs) != 0 {
		t.Fatalf("firewall deletes recorded = %v, want none when no rule observed", ff.deletedRefs)
	}
}

func TestDeleteNICResolvesAntiSpoofingRefLive(t *testing.T) {
	svc, _, _, ff, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC error = %v, want nil", err)
	}
	if _, err := svc.EnsureNIC(context.Background(), "net0", "vm1"); err != nil {
		t.Fatalf("EnsureNIC error = %v, want nil", err)
	}

	if err := svc.DeleteNIC(context.Background(), "net0", "vm1"); err != nil {
		t.Fatalf("DeleteNIC error = %v, want nil", err)
	}

	// The caller passed no ref; the service must resolve it live from observed
	// firewall state and delete the matching anti-spoofing rule.
	if len(ff.deletedRefs) != 1 {
		t.Fatalf("anti-spoofing deletes recorded = %d, want 1; refs=%v", len(ff.deletedRefs), ff.deletedRefs)
	}
	if got := ff.deletedRefs[0].Purpose; got != firewall.RulePurposeEndpointAntiSpoofing {
		t.Fatalf("deleted ref purpose = %q, want %q", got, firewall.RulePurposeEndpointAntiSpoofing)
	}
}

func TestDeleteNICIdempotentWhenRuleAbsent(t *testing.T) {
	svc, _, _, ff, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC error = %v, want nil", err)
	}

	// No EnsureNIC: the anti-spoofing rule was never created. The delete must
	// treat the missing rule as already torn down rather than error.
	if err := svc.DeleteNIC(context.Background(), "net0", "vm1"); err != nil {
		t.Fatalf("DeleteNIC(rule absent) error = %v, want nil", err)
	}
	if len(ff.deletedRefs) != 0 {
		t.Fatalf("firewall deletes recorded = %v, want none when no rule observed", ff.deletedRefs)
	}
}

func TestGetNetworkStatusReadsLiveState(t *testing.T) {
	svc, _, _, _, _, rec := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	// Ensure first so the firewall rules the status read observes actually
	// exist live, mirroring the real host flow.
	if _, err := svc.EnsureNetwork(context.Background(), "net0"); err != nil {
		t.Fatalf("EnsureNetwork error = %v, want nil", err)
	}
	rec.reset()

	status, err := svc.GetNetworkStatus(context.Background(), "net0")
	if err != nil {
		t.Fatalf("GetNetworkStatus error = %v, want nil", err)
	}

	// Status must be read live from the primitives, not the in-memory definition.
	liveReads := []string{"link.Get", "route.GetIPv4Forwarding", "firewall.ListRules", "dhcp.GetServer"}
	total := 0
	for _, name := range liveReads {
		total += rec.count(name)
	}
	if total == 0 {
		t.Fatalf("GetNetworkStatus performed no live primitive reads; sequence=%v", rec.sequence())
	}

	// The firewall fields must be populated from the observed rules, not left
	// as zero-value RuleInfo. Handle 1 = masquerade, 2 = forward-accept.
	if status.Masquerade.Ref.Purpose != firewall.RulePurposeMasquerade {
		t.Fatalf("Masquerade.Ref.Purpose = %q, want %q", status.Masquerade.Ref.Purpose, firewall.RulePurposeMasquerade)
	}
	if status.Masquerade.Summary.Masquerade == nil {
		t.Fatalf("Masquerade.Summary.Masquerade = nil, want populated summary")
	}
	if status.Forward.Ref.Purpose != firewall.RulePurposeForwardAccept {
		t.Fatalf("Forward.Ref.Purpose = %q, want %q", status.Forward.Ref.Purpose, firewall.RulePurposeForwardAccept)
	}
	if status.Forward.Summary.ForwardAccept == nil {
		t.Fatalf("Forward.Summary.ForwardAccept = nil, want populated summary")
	}
}

func TestGetNICStatusPopulatesAntiSpoofing(t *testing.T) {
	svc, _, _, _, _, rec := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC error = %v, want nil", err)
	}
	// Ensure first so the anti-spoofing rule the status read observes actually
	// exists live, mirroring the real host flow.
	if _, err := svc.EnsureNIC(context.Background(), "net0", "vm1"); err != nil {
		t.Fatalf("EnsureNIC error = %v, want nil", err)
	}
	rec.reset()

	status, err := svc.GetNICStatus(context.Background(), "net0", "vm1")
	if err != nil {
		t.Fatalf("GetNICStatus error = %v, want nil", err)
	}

	// AntiSpoofing must be read live and disambiguated by the NIC MAC.
	if rec.count("firewall.ListRules") == 0 {
		t.Fatalf("GetNICStatus performed no live firewall read; sequence=%v", rec.sequence())
	}
	if status.AntiSpoofing.Ref.Purpose != firewall.RulePurposeEndpointAntiSpoofing {
		t.Fatalf("AntiSpoofing.Ref.Purpose = %q, want %q", status.AntiSpoofing.Ref.Purpose, firewall.RulePurposeEndpointAntiSpoofing)
	}
	summary := status.AntiSpoofing.Summary.EndpointAntiSpoofing
	if summary == nil {
		t.Fatalf("AntiSpoofing.Summary.EndpointAntiSpoofing = nil, want populated summary")
	}
	if summary.MAC.String() != sampleNIC().MAC.String() {
		t.Fatalf("AntiSpoofing summary MAC = %s, want %s", summary.MAC, sampleNIC().MAC)
	}
}

func TestDeleteNetworkPropagatesResolveErrorWithoutShortCircuit(t *testing.T) {
	svc, fl, _, ff, _, rec := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}

	// A non-ErrNotFound ListRules failure means the rule ref cannot be resolved.
	// Unlike an absent rule (idempotent skip), this must propagate. Both the
	// forward-accept and masquerade resolves hit the same failure, and neither is
	// allowed to short-circuit the remaining teardown steps (project error-
	// propagation invariant: each step appends to errs, errors.Join at the end).
	boom := errors.New("listrules boom")
	ff.errs["ListRules"] = boom

	err := svc.DeleteNetwork(context.Background(), "net0")
	if err == nil {
		t.Fatalf("DeleteNetwork error = nil, want non-nil on resolve failure")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("DeleteNetwork error = %v, want it to wrap %v", err, boom)
	}

	// Non-short-circuit, part 1: both resolves were attempted. Two ListRules
	// reads prove the forward-accept resolve error did not stop the masquerade
	// resolve, so errors.Join carries both resolve failures forward.
	if got := rec.count("firewall.ListRules"); got != 2 {
		t.Fatalf("firewall.ListRules called %d times, want 2 (both resolves attempted); sequence=%v", got, rec.sequence())
	}
	// Non-short-circuit, part 2: the dhcp stop (before the failing resolves) and
	// the bridge delete (after them) still ran.
	if got := rec.count("dhcp.Stop"); got != 1 {
		t.Fatalf("dhcp.Stop called %d times, want 1; sequence=%v", got, rec.sequence())
	}
	if got := rec.count("link.Delete"); got != 1 {
		t.Fatalf("link.Delete (bridge) called %d times, want 1 despite resolve errors; sequence=%v", got, rec.sequence())
	}
	if len(fl.deleted) != 1 {
		t.Fatalf("bridge deletes recorded = %v, want exactly the bridge despite resolve errors", fl.deleted)
	}
	// Resolve failed, so no firewall rule delete may be attempted on a ref we
	// never resolved.
	if len(ff.deletedRefs) != 0 {
		t.Fatalf("firewall deletes recorded = %v, want none when resolve failed", ff.deletedRefs)
	}
}

func TestDeleteNICPropagatesResolveErrorWithoutShortCircuit(t *testing.T) {
	svc, fl, _, ff, _, rec := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC error = %v, want nil", err)
	}

	// A non-ErrNotFound ListRules failure blocks resolving the anti-spoofing ref.
	// This must propagate (unlike an absent rule, which is an idempotent skip)
	// yet not short-circuit the binding removal and tap delete that follow.
	boom := errors.New("listrules boom")
	ff.errs["ListRules"] = boom

	err := svc.DeleteNIC(context.Background(), "net0", "vm1")
	if err == nil {
		t.Fatalf("DeleteNIC error = nil, want non-nil on resolve failure")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("DeleteNIC error = %v, want it to wrap %v", err, boom)
	}

	// Non-short-circuit: the binding removal and tap delete still ran after the
	// resolve error was recorded.
	if got := rec.count("dhcp.RemoveBinding"); got != 1 {
		t.Fatalf("dhcp.RemoveBinding called %d times, want 1 despite resolve error; sequence=%v", got, rec.sequence())
	}
	if got := rec.count("link.Delete"); got != 1 {
		t.Fatalf("link.Delete (tap) called %d times, want 1 despite resolve error; sequence=%v", got, rec.sequence())
	}
	if len(fl.deleted) != 1 {
		t.Fatalf("tap deletes recorded = %v, want exactly the tap despite resolve error", fl.deleted)
	}
	// Resolve failed, so no anti-spoofing delete may be attempted on a ref we
	// never resolved.
	if len(ff.deletedRefs) != 0 {
		t.Fatalf("firewall deletes recorded = %v, want none when resolve failed", ff.deletedRefs)
	}
}

// TestDeleteNICClearsRegistryAllowingNetworkDelete is the regression for the
// defect where DeleteNIC tore down the live TAP/binding/anti-spoofing but never
// removed the in-memory record.nics entry. The lingering entry kept
// DeleteNetwork's nicCount>0 guard true forever, so a network whose only NIC had
// been deleted could never itself be deleted (ErrConflict), orphaning the
// bridge/masquerade/forward rules. After the fix a successful DeleteNIC clears
// the entry so the network's NIC count reflects reality and DeleteNetwork
// succeeds.
func TestDeleteNICClearsRegistryAllowingNetworkDelete(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC error = %v, want nil", err)
	}
	if _, err := svc.EnsureNIC(context.Background(), "net0", "vm1"); err != nil {
		t.Fatalf("EnsureNIC error = %v, want nil", err)
	}

	if err := svc.DeleteNIC(context.Background(), "net0", "vm1"); err != nil {
		t.Fatalf("DeleteNIC error = %v, want nil", err)
	}

	// The NIC's live resources are gone AND its registry entry must be cleared,
	// so the network now has zero NICs and DeleteNetwork must succeed rather than
	// return ErrConflict.
	if err := svc.DeleteNetwork(context.Background(), "net0"); err != nil {
		t.Fatalf("DeleteNetwork after sole NIC deleted error = %v, want nil (registry entry not cleared?)", err)
	}
}

// TestDeleteNICAllowsReRegisteringSameNIC proves the registry entry is truly
// removed (not merely ignored): after DeleteNIC the same VMID can be registered
// again, which RegisterNIC rejects with ErrAlreadyExists if the stale entry
// survived.
func TestDeleteNICAllowsReRegisteringSameNIC(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC error = %v, want nil", err)
	}
	if _, err := svc.EnsureNIC(context.Background(), "net0", "vm1"); err != nil {
		t.Fatalf("EnsureNIC error = %v, want nil", err)
	}
	if err := svc.DeleteNIC(context.Background(), "net0", "vm1"); err != nil {
		t.Fatalf("DeleteNIC error = %v, want nil", err)
	}

	// Stale entry would make this fail with ErrAlreadyExists.
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC after delete error = %v, want nil (registry entry not cleared?)", err)
	}
}

// TestDeleteNICKeepsRegistryOnTeardownFailure proves the registry entry is
// retained when teardown fails, so a Delete retry re-attempts teardown rather
// than losing track of a NIC whose live resources may still exist.
func TestDeleteNICKeepsRegistryOnTeardownFailure(t *testing.T) {
	svc, fl, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC error = %v, want nil", err)
	}
	if _, err := svc.EnsureNIC(context.Background(), "net0", "vm1"); err != nil {
		t.Fatalf("EnsureNIC error = %v, want nil", err)
	}
	// Force the TAP delete to fail so DeleteNIC returns a non-nil joined error.
	fl.errs["Delete"] = errors.New("tap delete boom")

	if err := svc.DeleteNIC(context.Background(), "net0", "vm1"); err == nil {
		t.Fatalf("DeleteNIC error = nil, want non-nil on TAP delete failure")
	}

	// Entry must survive so a retry re-attempts teardown: DeleteNetwork must
	// still see the NIC and refuse with ErrConflict.
	if err := svc.DeleteNetwork(context.Background(), "net0"); !errors.Is(err, networker.ErrConflict) {
		t.Fatalf("DeleteNetwork error = %v, want ErrConflict (entry should survive failed teardown)", err)
	}
}
