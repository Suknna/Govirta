package coredhcp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/suknna/govirta/pkg/hostnet/dhcp"
	"github.com/suknna/govirta/pkg/hostnet/dhcp/dhcperr"
)

func TestHandlerUnknownMACDropsRequest(t *testing.T) {
	rt, _ := handlerRuntime(t)
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeDiscover, mustMAC("02:00:00:00:00:99"))

	got, stop := h(req, resp)
	if got != nil || !stop {
		t.Fatalf("expected unknown MAC to drop and stop, got packet=%#v stop=%v", got, stop)
	}
}

func TestHandlerDiscoverKnownMACOffersBoundIP(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeDiscover, mac)

	got, stop := h(req, resp)
	if got == nil || stop {
		t.Fatalf("expected OFFER response to continue plugin chain, got packet=%#v stop=%v", got, stop)
	}
	if got.MessageType() != dhcpv4.MessageTypeOffer {
		t.Fatalf("message type = %s, want OFFER", got.MessageType())
	}
	if !got.YourIPAddr.Equal(net.ParseIP("192.168.100.10")) {
		t.Fatalf("YourIPAddr = %s, want 192.168.100.10", got.YourIPAddr)
	}
	assertCommonOptions(t, got, spec)
	lease, err := rt.getLease(dhcp.BindingQuery{ServerID: spec.ID, MAC: mac})
	if err != nil {
		t.Fatalf("getLease returned error: %v", err)
	}
	if lease.State != dhcp.LeaseStateReserved || !lease.ExpiresAt.IsZero() {
		t.Fatalf("DISCOVER must not bind lease, got %#v", lease)
	}
}

func TestHandlerRequestKnownMACAcksBoundIP(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeRequest, mac)
	req.UpdateOption(dhcpv4.OptRequestedIPAddress(net.ParseIP("192.168.100.10")))

	got, stop := h(req, resp)
	if got == nil || stop {
		t.Fatalf("expected ACK response to continue plugin chain, got packet=%#v stop=%v", got, stop)
	}
	if got.MessageType() != dhcpv4.MessageTypeAck {
		t.Fatalf("message type = %s, want ACK", got.MessageType())
	}
	if !got.YourIPAddr.Equal(net.ParseIP("192.168.100.10")) {
		t.Fatalf("YourIPAddr = %s, want 192.168.100.10", got.YourIPAddr)
	}
	assertCommonOptions(t, got, spec)
}

func TestHandlerRequestDifferentIPDropsWithoutNAK(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeRequest, mac)
	req.UpdateOption(dhcpv4.OptRequestedIPAddress(net.ParseIP("192.168.100.11")))

	got, stop := h(req, resp)
	if got != nil || !stop {
		t.Fatalf("expected conflicting REQUEST to drop and stop, got packet=%#v stop=%v", got, stop)
	}
	if resp.MessageType() == dhcpv4.MessageTypeNak {
		t.Fatalf("handler must not turn conflicting REQUEST into NAK")
	}
	lease, err := rt.getLease(dhcp.BindingQuery{ServerID: spec.ID, MAC: mac})
	if err != nil {
		t.Fatalf("getLease returned error: %v", err)
	}
	if lease.State != dhcp.LeaseStateReserved || !lease.ExpiresAt.IsZero() {
		t.Fatalf("conflicting REQUEST must not mutate lease, got %#v", lease)
	}
}

func TestHandlerRequestOtherServerIdentifierDropsWithoutNAK(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeRequest, mac)
	req.UpdateOption(dhcpv4.OptRequestedIPAddress(net.ParseIP("192.168.100.10")))
	req.UpdateOption(dhcpv4.OptServerIdentifier(net.ParseIP("192.168.100.254")))

	got, stop := h(req, resp)
	if got != nil || !stop {
		t.Fatalf("expected other server identifier to drop and stop, got packet=%#v stop=%v", got, stop)
	}
	if resp.MessageType() == dhcpv4.MessageTypeNak {
		t.Fatalf("handler must not turn other-server REQUEST into NAK")
	}
	assertReservedLease(t, rt, spec.ID, mac)
}

func TestHandlerRequestMalformedServerIdentifierDropsWithoutNAK(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeRequest, mac)
	req.UpdateOption(dhcpv4.OptRequestedIPAddress(net.ParseIP("192.168.100.10")))
	req.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionServerIdentifier, []byte{192, 168, 100}))

	got, stop := h(req, resp)
	if got != nil || !stop {
		t.Fatalf("expected malformed server identifier to drop and stop, got packet=%#v stop=%v", got, stop)
	}
	if resp.MessageType() == dhcpv4.MessageTypeNak {
		t.Fatalf("handler must not turn malformed-server-id REQUEST into NAK")
	}
	assertReservedLease(t, rt, spec.ID, mac)
}

func TestHandlerRequestMissingRequestedIPAndCIAddrDropsWithoutNAK(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeRequest, mac)

	got, stop := h(req, resp)
	if got != nil || !stop {
		t.Fatalf("expected REQUEST without explicit address to drop and stop, got packet=%#v stop=%v", got, stop)
	}
	if resp.MessageType() == dhcpv4.MessageTypeNak {
		t.Fatalf("handler must not turn addressless REQUEST into NAK")
	}
	assertReservedLease(t, rt, spec.ID, mac)
}

func TestHandlerRequestMalformedRequestedIPWithMatchingCIAddrDropsWithoutNAK(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeRequest, mac)
	req.ClientIPAddr = net.ParseIP("192.168.100.10")
	req.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionRequestedIPAddress, []byte{192, 168, 100}))

	got, stop := h(req, resp)
	if got != nil || !stop {
		t.Fatalf("expected malformed requested IP to drop and stop, got packet=%#v stop=%v", got, stop)
	}
	if resp.MessageType() == dhcpv4.MessageTypeNak {
		t.Fatalf("handler must not turn malformed-requested-ip REQUEST into NAK")
	}
	assertReservedLease(t, rt, spec.ID, mac)
}

func TestHandlerRequestMalformedRequestedIPWithoutCIAddrDropsWithoutNAK(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeRequest, mac)
	req.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionRequestedIPAddress, []byte{192, 168, 100}))

	got, stop := h(req, resp)
	if got != nil || !stop {
		t.Fatalf("expected malformed requested IP without ciaddr to drop and stop, got packet=%#v stop=%v", got, stop)
	}
	if resp.MessageType() == dhcpv4.MessageTypeNak {
		t.Fatalf("handler must not turn malformed-requested-ip REQUEST into NAK")
	}
	assertReservedLease(t, rt, spec.ID, mac)
}

func TestHandlerRequestMatchingCIAddrAcksBoundIP(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeRequest, mac)
	req.ClientIPAddr = net.ParseIP("192.168.100.10")

	got, stop := h(req, resp)
	if got == nil || stop {
		t.Fatalf("expected matching ciaddr REQUEST to ACK, got packet=%#v stop=%v", got, stop)
	}
	if got.MessageType() != dhcpv4.MessageTypeAck || !got.YourIPAddr.Equal(net.ParseIP("192.168.100.10")) {
		t.Fatalf("unexpected ACK packet: %#v", got)
	}
	assertCommonOptions(t, got, spec)
}

func TestHandlerRequestDifferentCIAddrDropsWithoutNAK(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeRequest, mac)
	req.ClientIPAddr = net.ParseIP("192.168.100.11")

	got, stop := h(req, resp)
	if got != nil || !stop {
		t.Fatalf("expected different ciaddr REQUEST to drop and stop, got packet=%#v stop=%v", got, stop)
	}
	if resp.MessageType() == dhcpv4.MessageTypeNak {
		t.Fatalf("handler must not turn different-ciaddr REQUEST into NAK")
	}
	assertReservedLease(t, rt, spec.ID, mac)
}

func TestHandlerSetsRouterAndDNSOnlyWhenEnabled(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeDiscover, mac)

	got, stop := h(req, resp)
	if got == nil || stop {
		t.Fatalf("expected OFFER response, got packet=%#v stop=%v", got, stop)
	}
	if routers := got.Router(); len(routers) != 1 || !routers[0].Equal(net.IP(spec.ServerAddr.AsSlice())) {
		t.Fatalf("router option = %#v, want %s", routers, spec.ServerAddr)
	}
	if got.Options.Has(dhcpv4.OptionDomainNameServer) {
		t.Fatalf("DNS option must be absent when disabled")
	}

	dnsAddr := netip.MustParseAddr("192.168.100.53")
	rt.mu.Lock()
	rt.spec.Router = dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionDisabled}
	rt.spec.DNS = dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionEnabled, Addrs: []netip.Addr{dnsAddr}}
	rt.mu.Unlock()
	req, resp = dhcpPacketPair(t, dhcpv4.MessageTypeDiscover, mac)
	resp.UpdateOption(dhcpv4.OptRouter(net.ParseIP("192.168.100.1")))

	got, stop = h(req, resp)
	if got == nil || stop {
		t.Fatalf("expected OFFER response, got packet=%#v stop=%v", got, stop)
	}
	if got.Options.Has(dhcpv4.OptionRouter) {
		t.Fatalf("router option must be absent when disabled")
	}
	if servers := got.DNS(); len(servers) != 1 || !servers[0].Equal(net.IP(dnsAddr.AsSlice())) {
		t.Fatalf("DNS option = %#v, want %s", servers, dnsAddr)
	}
}

func TestHandlerAckMarksLeaseBoundAndSetsExpiry(t *testing.T) {
	rt, spec := handlerRuntime(t)
	now := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	rt.now = func() time.Time { return now }
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeRequest, mac)
	req.UpdateOption(dhcpv4.OptRequestedIPAddress(net.ParseIP("192.168.100.10")))

	got, stop := h(req, resp)
	if got == nil || stop {
		t.Fatalf("expected ACK response, got packet=%#v stop=%v", got, stop)
	}
	lease, err := rt.getLease(dhcp.BindingQuery{ServerID: spec.ID, MAC: mac})
	if err != nil {
		t.Fatalf("getLease returned error: %v", err)
	}
	if lease.State != dhcp.LeaseStateBound || !lease.ExpiresAt.Equal(now.Add(spec.LeaseDuration)) {
		t.Fatalf("unexpected bound lease: %#v", lease)
	}
}

func TestHandlerStoppingServerDropsRequest(t *testing.T) {
	rt, spec := handlerRuntime(t)
	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, rt, spec.ID, mac, "192.168.100.10")
	rt.mu.Lock()
	rt.state = dhcp.ServerStateStopping
	rt.mu.Unlock()
	h := newHandler4(rt)
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeDiscover, mac)

	got, stop := h(req, resp)
	if got != nil || !stop {
		t.Fatalf("expected stopping server to drop and stop, got packet=%#v stop=%v", got, stop)
	}
}

func TestStartStopRegistersAndRemovesRuntime(t *testing.T) {
	manager := newManager(&fakeStarter{servers: &fakeServers{}})
	spec := validServerSpec()
	t.Cleanup(func() { runtimeRegistry.Delete(string(spec.ID)) })

	if _, err := manager.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if _, err := lookupRuntime(spec.ID); err != nil {
		t.Fatalf("expected registered runtime, got %v", err)
	}
	if _, err := setupHandler4(string(spec.ID)); err != nil {
		t.Fatalf("setupHandler4 returned error for registered runtime: %v", err)
	}
	if err := manager.Stop(context.Background(), spec.ID); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if _, err := lookupRuntime(spec.ID); !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("expected runtime registry cleanup, got %v", err)
	}
}

func TestRuntimeRegistryRejectsDifferentManagerWithSameServerID(t *testing.T) {
	firstManager := newManager(&fakeStarter{servers: &fakeServers{}})
	secondManager := newManager(&fakeStarter{servers: &fakeServers{}})
	spec := validServerSpec()
	t.Cleanup(func() { runtimeRegistry.Delete(string(spec.ID)) })

	if _, err := firstManager.Start(context.Background(), spec); err != nil {
		t.Fatalf("first Start returned error: %v", err)
	}
	firstRuntime, err := lookupRuntime(spec.ID)
	if err != nil {
		t.Fatalf("lookupRuntime returned error: %v", err)
	}

	_, err = secondManager.Start(context.Background(), spec)
	if !errors.Is(err, dhcperr.ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning from second Start, got %v", err)
	}
	currentRuntime, err := lookupRuntime(spec.ID)
	if err != nil {
		t.Fatalf("lookupRuntime after failed second Start returned error: %v", err)
	}
	if currentRuntime != firstRuntime {
		t.Fatalf("second Start overwrote runtime registry")
	}
	if _, err := secondManager.GetServer(context.Background(), spec.ID); !errors.Is(err, dhcperr.ErrNotFound) {
		t.Fatalf("failed second Start must clean manager reservation, got %v", err)
	}

	mac := mustMAC("02:00:00:00:00:01")
	bindHandlerLease(t, firstRuntime, spec.ID, mac, "192.168.100.10")
	h, err := setupHandler4(string(spec.ID))
	if err != nil {
		t.Fatalf("setupHandler4 returned error: %v", err)
	}
	req, resp := dhcpPacketPair(t, dhcpv4.MessageTypeDiscover, mac)
	got, stop := h(req, resp)
	if got == nil || stop || !got.YourIPAddr.Equal(net.ParseIP("192.168.100.10")) {
		t.Fatalf("handler lookup did not use first runtime, got packet=%#v stop=%v", got, stop)
	}
}

// TestHandlerConcurrentWithBindingMutation drives the DHCPv4 handler under -race
// while other goroutines mutate the same runtime's bindings. The handler reads
// bindings (bindingForMAC / bindLease) under rt.mu while ApplyBinding /
// RemoveBinding take rt.mu for writes; this is the highest-risk concurrency path
// (guest packets racing control-plane binding updates) and was previously
// unproven by any -race test. A data race here fails the test under -race.
func TestHandlerConcurrentWithBindingMutation(t *testing.T) {
	rt, spec := handlerRuntime(t)
	h := newHandler4(rt)

	const workers = 8
	const iterations = 50
	macFor := func(i int) net.HardwareAddr {
		return net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x10, byte(i)}
	}
	ipFor := func(i int) netip.Addr {
		return netip.AddrFrom4([4]byte{192, 168, 100, byte(20 + i)})
	}

	var wg sync.WaitGroup

	// Mutators: repeatedly add and remove each worker's binding.
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mac := macFor(i)
			ip := ipFor(i)
			for n := 0; n < iterations; n++ {
				// Errors are expected and benign here (e.g. removing a
				// not-yet-present binding); the test only asserts the absence
				// of data races, so the outcomes are intentionally ignored.
				_, _ = rt.applyBinding(dhcp.BindingRequest{ServerID: spec.ID, MAC: mac, IP: ip})
				_ = rt.removeBinding(dhcp.BindingQuery{ServerID: spec.ID, MAC: mac})
			}
		}(i)
	}

	// Handlers: repeatedly drive DISCOVER and REQUEST for each worker's MAC.
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mac := macFor(i)
			ip := net.IP(ipFor(i).AsSlice())
			for n := 0; n < iterations; n++ {
				discoverReq, discoverResp := dhcpPacketPair(t, dhcpv4.MessageTypeDiscover, mac)
				h(discoverReq, discoverResp)
				requestReq, requestResp := dhcpPacketPair(t, dhcpv4.MessageTypeRequest, mac)
				requestReq.UpdateOption(dhcpv4.OptRequestedIPAddress(ip))
				h(requestReq, requestResp)
			}
		}(i)
	}

	wg.Wait()
}

func handlerRuntime(t *testing.T) (*serverRuntime, dhcp.ServerSpec) {
	t.Helper()
	spec := validServerSpec()
	rt := newServerRuntime(spec)
	rt.mu.Lock()
	rt.state = dhcp.ServerStateReady
	rt.mu.Unlock()
	return rt, spec
}

func bindHandlerLease(t *testing.T, rt *serverRuntime, serverID dhcp.ServerID, mac net.HardwareAddr, ip string) {
	t.Helper()
	if _, err := rt.applyBinding(dhcp.BindingRequest{ServerID: serverID, MAC: mac, IP: netip.MustParseAddr(ip)}); err != nil {
		t.Fatalf("applyBinding returned error: %v", err)
	}
}

func assertReservedLease(t *testing.T, rt *serverRuntime, serverID dhcp.ServerID, mac net.HardwareAddr) {
	t.Helper()
	lease, err := rt.getLease(dhcp.BindingQuery{ServerID: serverID, MAC: mac})
	if err != nil {
		t.Fatalf("getLease returned error: %v", err)
	}
	if lease.State != dhcp.LeaseStateReserved || !lease.ExpiresAt.IsZero() {
		t.Fatalf("lease must remain reserved, got %#v", lease)
	}
}

func dhcpPacketPair(t *testing.T, messageType dhcpv4.MessageType, mac net.HardwareAddr) (*dhcpv4.DHCPv4, *dhcpv4.DHCPv4) {
	t.Helper()
	req, err := dhcpv4.New(dhcpv4.WithHwAddr(mac), dhcpv4.WithMessageType(messageType))
	if err != nil {
		t.Fatalf("building request failed: %v", err)
	}
	respType := dhcpv4.MessageTypeAck
	if messageType == dhcpv4.MessageTypeDiscover {
		respType = dhcpv4.MessageTypeOffer
	}
	resp, err := dhcpv4.NewReplyFromRequest(req, dhcpv4.WithMessageType(respType))
	if err != nil {
		t.Fatalf("building response failed: %v", err)
	}
	return req, resp
}

func assertCommonOptions(t *testing.T, packet *dhcpv4.DHCPv4, spec dhcp.ServerSpec) {
	t.Helper()
	if !packet.ServerIdentifier().Equal(net.IP(spec.ServerAddr.AsSlice())) {
		t.Fatalf("server identifier = %s, want %s", packet.ServerIdentifier(), spec.ServerAddr)
	}
	if got := packet.IPAddressLeaseTime(0); got != spec.LeaseDuration {
		t.Fatalf("lease time = %s, want %s", got, spec.LeaseDuration)
	}
	wantMask := net.CIDRMask(spec.Subnet.Bits(), 32)
	if gotMask := packet.SubnetMask(); gotMask.String() != wantMask.String() {
		t.Fatalf("subnet mask = %s, want %s", gotMask, wantMask)
	}
}
