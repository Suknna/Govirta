package coredhcp

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/coredhcp/coredhcp/handler"
	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/suknna/govirta/pkg/hostnet/dhcp"
	"github.com/suknna/govirta/pkg/hostnet/dhcp/dhcperr"
)

func setupHandler4(args ...string) (handler.Handler4, error) {
	if len(args) != 1 || args[0] == "" {
		return nil, fmt.Errorf("%w: Govirta CoreDHCP plugin requires server ID", dhcperr.ErrInvalidRequest)
	}
	rt, err := lookupRuntime(dhcp.ServerID(args[0]))
	if err != nil {
		return nil, err
	}
	return newHandler4(rt), nil
}

func newHandler4(rt *serverRuntime) handler.Handler4 {
	return func(req, resp *dhcpv4.DHCPv4) (*dhcpv4.DHCPv4, bool) {
		if rt == nil || req == nil || resp == nil {
			return nil, true
		}

		spec, state, record, ok := rt.bindingForMAC(req.ClientHWAddr)
		if !ok || state == dhcp.ServerStateStopping || state == dhcp.ServerStateStopped {
			return nil, true
		}

		switch req.MessageType() {
		case dhcpv4.MessageTypeDiscover:
			applyDHCPOptions(resp, spec, record)
			return resp, false
		case dhcpv4.MessageTypeRequest:
			if !requestMatchesBinding(req, spec, record.ip) {
				return nil, true
			}
			spec, record, ok = rt.bindLease(req.ClientHWAddr)
			if !ok {
				return nil, true
			}
			applyDHCPOptions(resp, spec, record)
			return resp, false
		default:
			return nil, true
		}
	}
}

func applyDHCPOptions(resp *dhcpv4.DHCPv4, spec dhcp.ServerSpec, record leaseRecord) {
	resp.YourIPAddr = net.IP(record.ip.AsSlice())
	resp.UpdateOption(dhcpv4.OptServerIdentifier(net.IP(spec.ServerAddr.AsSlice())))
	resp.UpdateOption(dhcpv4.OptIPAddressLeaseTime(spec.LeaseDuration))
	resp.UpdateOption(dhcpv4.OptSubnetMask(net.CIDRMask(spec.Subnet.Bits(), 32)))

	resp.DeleteOption(dhcpv4.OptionRouter)
	if spec.Router.Mode == dhcp.DHCPOptionEnabled {
		resp.UpdateOption(dhcpv4.OptRouter(optionIPs(spec.Router.Addrs)...))
	}

	resp.DeleteOption(dhcpv4.OptionDomainNameServer)
	if spec.DNS.Mode == dhcp.DHCPOptionEnabled {
		resp.UpdateOption(dhcpv4.OptDNS(optionIPs(spec.DNS.Addrs)...))
	}
}

func optionIPs(addrs []netip.Addr) []net.IP {
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, net.IP(addr.AsSlice()))
	}
	return ips
}

func requestMatchesBinding(req *dhcpv4.DHCPv4, spec dhcp.ServerSpec, binding netip.Addr) bool {
	serverID, hasServerID, ok := rawIPv4Option(req, dhcpv4.OptionServerIdentifier)
	if !ok {
		return false
	}
	if hasServerID && !serverID.Equal(net.IP(spec.ServerAddr.AsSlice())) {
		return false
	}
	requestedIP, hasRequestedIP, ok := rawIPv4Option(req, dhcpv4.OptionRequestedIPAddress)
	if !ok {
		return false
	}
	clientIP := req.ClientIPAddr
	if hasRequestedIP && !requestedIP.Equal(net.IP(binding.AsSlice())) {
		return false
	}
	if explicitDifferentIP(clientIP, binding) {
		return false
	}
	return hasRequestedIP || explicitMatchingIP(clientIP, binding)
}

func rawIPv4Option(req *dhcpv4.DHCPv4, code dhcpv4.OptionCode) (net.IP, bool, bool) {
	if !req.Options.Has(code) {
		return nil, false, true
	}
	raw := req.Options.Get(code)
	if len(raw) != net.IPv4len {
		return nil, true, false
	}
	return net.IP(raw), true, true
}

func explicitDifferentIP(ip net.IP, binding netip.Addr) bool {
	if ip == nil || ip.IsUnspecified() {
		return false
	}
	return !ip.Equal(net.IP(binding.AsSlice()))
}

func explicitMatchingIP(ip net.IP, binding netip.Addr) bool {
	if ip == nil || ip.IsUnspecified() {
		return false
	}
	return ip.Equal(net.IP(binding.AsSlice()))
}
