package netpool

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"

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
