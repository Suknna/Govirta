package coredhcp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/dhcp/dhcperr"
)

func TestApplyBindingCreatesReservedLease(t *testing.T) {
	manager, spec := startBindingTestManager(t)
	req := bindingRequest(spec.ID, "02:00:00:00:00:01", "192.168.100.10")
	req.Hostname = dhcp.BindingHostname{Value: "guest-1", Set: true}

	lease, err := manager.ApplyBinding(context.Background(), req)
	if err != nil {
		t.Fatalf("ApplyBinding returned error: %v", err)
	}
	if lease.ServerID != spec.ID || lease.IP != req.IP || lease.State != dhcp.LeaseStateReserved || !lease.ExpiresAt.IsZero() {
		t.Fatalf("unexpected lease: %#v", lease)
	}
	if lease.Hostname != req.Hostname {
		t.Fatalf("expected hostname %#v, got %#v", req.Hostname, lease.Hostname)
	}
	assertMACEqual(t, lease.MAC, req.MAC)
	lease.MAC[0] = 0xff
	stored, err := manager.GetLease(context.Background(), dhcp.BindingQuery{ServerID: spec.ID, MAC: req.MAC})
	if err != nil {
		t.Fatalf("GetLease returned error: %v", err)
	}
	assertMACEqual(t, stored.MAC, req.MAC)
}

func TestApplyBindingIsIdempotentForSameMACAndIP(t *testing.T) {
	manager, spec := startBindingTestManager(t)
	req := bindingRequest(spec.ID, "02:00:00:00:00:01", "192.168.100.10")

	first, err := manager.ApplyBinding(context.Background(), req)
	if err != nil {
		t.Fatalf("first ApplyBinding returned error: %v", err)
	}
	second, err := manager.ApplyBinding(context.Background(), req)
	if err != nil {
		t.Fatalf("second ApplyBinding returned error: %v", err)
	}
	if first.IP != second.IP || first.State != second.State || !first.ExpiresAt.Equal(second.ExpiresAt) {
		t.Fatalf("expected idempotent lease, first=%#v second=%#v", first, second)
	}
}

func TestApplyBindingReconcilesChangedHostname(t *testing.T) {
	manager, spec := startBindingTestManager(t)
	mac := "02:00:00:00:00:01"
	first := bindingRequest(spec.ID, mac, "192.168.100.10")
	first.Hostname = dhcp.BindingHostname{Value: "guest-a", Set: true}
	if _, err := manager.ApplyBinding(context.Background(), first); err != nil {
		t.Fatalf("first ApplyBinding returned error: %v", err)
	}

	// Re-apply the same MAC+IP with a changed hostname: the mutable hostname
	// must be reconciled toward the requested state, not silently dropped.
	second := bindingRequest(spec.ID, mac, "192.168.100.10")
	second.Hostname = dhcp.BindingHostname{Value: "guest-b", Set: true}
	lease, err := manager.ApplyBinding(context.Background(), second)
	if err != nil {
		t.Fatalf("second ApplyBinding returned error: %v", err)
	}
	if lease.Hostname != second.Hostname {
		t.Fatalf("returned lease hostname = %#v, want %#v", lease.Hostname, second.Hostname)
	}
	stored, err := manager.GetLease(context.Background(), dhcp.BindingQuery{ServerID: spec.ID, MAC: second.MAC})
	if err != nil {
		t.Fatalf("GetLease returned error: %v", err)
	}
	if stored.Hostname != second.Hostname {
		t.Fatalf("stored lease hostname = %#v, want %#v", stored.Hostname, second.Hostname)
	}
}

func TestApplyBindingRejectsSameMACDifferentIP(t *testing.T) {
	manager, spec := startBindingTestManager(t)
	mac := "02:00:00:00:00:01"
	original := bindingRequest(spec.ID, mac, "192.168.100.10")
	if _, err := manager.ApplyBinding(context.Background(), original); err != nil {
		t.Fatalf("ApplyBinding returned error: %v", err)
	}

	_, err := manager.ApplyBinding(context.Background(), bindingRequest(spec.ID, mac, "192.168.100.11"))
	if !errors.Is(err, dhcperr.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	assertExistingLease(t, manager, spec.ID, original.MAC, original.IP)
}

func TestApplyBindingRejectsDifferentMACSameIP(t *testing.T) {
	manager, spec := startBindingTestManager(t)
	original := bindingRequest(spec.ID, "02:00:00:00:00:01", "192.168.100.10")
	if _, err := manager.ApplyBinding(context.Background(), original); err != nil {
		t.Fatalf("ApplyBinding returned error: %v", err)
	}

	_, err := manager.ApplyBinding(context.Background(), bindingRequest(spec.ID, "02:00:00:00:00:02", "192.168.100.10"))
	if !errors.Is(err, dhcperr.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	assertExistingLease(t, manager, spec.ID, original.MAC, original.IP)
}

func TestApplyBindingRejectsIPOutsidePool(t *testing.T) {
	manager, spec := startBindingTestManager(t)
	_, err := manager.ApplyBinding(context.Background(), bindingRequest(spec.ID, "02:00:00:00:00:01", "192.168.100.21"))
	if !errors.Is(err, dhcperr.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestApplyBindingRejectsInvalidMAC(t *testing.T) {
	manager, spec := startBindingTestManager(t)
	req := bindingRequest(spec.ID, "02:00:00:00:00:01", "192.168.100.10")
	req.MAC = net.HardwareAddr{0x01, 0x00, 0x00, 0x00, 0x00, 0x01}

	_, err := manager.ApplyBinding(context.Background(), req)
	if !errors.Is(err, dhcperr.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestApplyBindingRejectsInvalidHostname(t *testing.T) {
	manager, spec := startBindingTestManager(t)
	req := bindingRequest(spec.ID, "02:00:00:00:00:01", "192.168.100.10")
	req.Hostname = dhcp.BindingHostname{Value: "-bad", Set: true}

	_, err := manager.ApplyBinding(context.Background(), req)
	if !errors.Is(err, dhcperr.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestRemoveBindingDeletesLease(t *testing.T) {
	manager, spec := startBindingTestManager(t)
	req := bindingRequest(spec.ID, "02:00:00:00:00:01", "192.168.100.10")
	if _, err := manager.ApplyBinding(context.Background(), req); err != nil {
		t.Fatalf("ApplyBinding returned error: %v", err)
	}

	query := dhcp.BindingQuery{ServerID: spec.ID, MAC: req.MAC}
	if err := manager.RemoveBinding(context.Background(), query); err != nil {
		t.Fatalf("RemoveBinding returned error: %v", err)
	}
	_, err := manager.GetLease(context.Background(), query)
	if !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after remove, got %v", err)
	}
	if _, err := manager.ApplyBinding(context.Background(), req); err != nil {
		t.Fatalf("expected removed IP to be reusable, got %v", err)
	}
}

func TestGetLeaseMissingBindingReturnsNotFound(t *testing.T) {
	manager, spec := startBindingTestManager(t)
	query := dhcp.BindingQuery{ServerID: spec.ID, MAC: mustMAC("02:00:00:00:00:01")}

	_, err := manager.GetLease(context.Background(), query)
	if !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListLeasesSortsByMAC(t *testing.T) {
	manager, spec := startBindingTestManager(t)
	requests := []dhcp.BindingRequest{
		bindingRequest(spec.ID, "02:00:00:00:00:03", "192.168.100.13"),
		bindingRequest(spec.ID, "02:00:00:00:00:01", "192.168.100.11"),
		bindingRequest(spec.ID, "02:00:00:00:00:02", "192.168.100.12"),
	}
	for _, req := range requests {
		if _, err := manager.ApplyBinding(context.Background(), req); err != nil {
			t.Fatalf("ApplyBinding returned error: %v", err)
		}
	}

	leases, err := manager.ListLeases(context.Background(), dhcp.LeaseFilter{ServerID: spec.ID})
	if err != nil {
		t.Fatalf("ListLeases returned error: %v", err)
	}
	if len(leases) != 3 {
		t.Fatalf("expected 3 leases, got %d", len(leases))
	}
	for i, want := range []string{"02:00:00:00:00:01", "02:00:00:00:00:02", "02:00:00:00:00:03"} {
		if got := leases[i].MAC.String(); got != want {
			t.Fatalf("lease %d MAC = %s, want %s", i, got, want)
		}
	}
}

func startBindingTestManager(t *testing.T) (*Manager, dhcp.ServerSpec) {
	t.Helper()
	manager := newManager(&fakeStarter{servers: &fakeServers{}})
	spec := validServerSpec()
	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	return manager, spec
}

func bindingRequest(serverID dhcp.ServerID, mac, ip string) dhcp.BindingRequest {
	return dhcp.BindingRequest{ServerID: serverID, MAC: mustMAC(mac), IP: netip.MustParseAddr(ip)}
}

func mustMAC(value string) net.HardwareAddr {
	mac, err := net.ParseMAC(value)
	if err != nil {
		panic(err)
	}
	return mac
}

func assertExistingLease(t *testing.T, manager *Manager, serverID dhcp.ServerID, mac net.HardwareAddr, ip netip.Addr) {
	t.Helper()
	lease, err := manager.GetLease(context.Background(), dhcp.BindingQuery{ServerID: serverID, MAC: mac})
	if err != nil {
		t.Fatalf("GetLease returned error: %v", err)
	}
	if lease.IP != ip || lease.State != dhcp.LeaseStateReserved || !lease.ExpiresAt.Equal(time.Time{}) {
		t.Fatalf("unexpected preserved lease: %#v", lease)
	}
}

func assertMACEqual(t *testing.T, got, want net.HardwareAddr) {
	t.Helper()
	if got.String() != want.String() {
		t.Fatalf("MAC = %s, want %s", got, want)
	}
}
