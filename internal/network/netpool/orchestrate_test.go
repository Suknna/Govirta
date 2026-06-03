package netpool

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/firewall"
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

	if err := svc.DeleteNetwork(context.Background(), "net0", firewall.RuleRef{}, firewall.RuleRef{}); err != nil {
		t.Fatalf("DeleteNetwork error = %v, want nil", err)
	}

	got := rec.sequence()
	want := []string{
		"dhcp.Stop",
		"firewall.DeleteForwardAccept",
		"firewall.DeleteMasquerade",
		"link.Delete",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delete-phase order = %v, want %v", got, want)
	}
}

func TestGetNetworkStatusReadsLiveState(t *testing.T) {
	svc, _, _, _, _, rec := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}

	if _, err := svc.GetNetworkStatus(context.Background(), "net0"); err != nil {
		t.Fatalf("GetNetworkStatus error = %v, want nil", err)
	}

	// Status must be read live from the primitives, not the in-memory definition.
	liveReads := []string{"link.Get", "route.GetIPv4Forwarding", "dhcp.GetServer"}
	total := 0
	for _, name := range liveReads {
		total += rec.count(name)
	}
	if total == 0 {
		t.Fatalf("GetNetworkStatus performed no live primitive reads; sequence=%v", rec.sequence())
	}
}
