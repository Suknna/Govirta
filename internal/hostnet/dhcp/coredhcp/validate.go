package coredhcp

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/dhcp/dhcperr"
)

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", dhcperr.ErrInvalidRequest)
	}
	return ctx.Err()
}

func validateServerSpec(spec dhcp.ServerSpec) error {
	if err := validateServerID(spec.ID); err != nil {
		return err
	}
	if spec.InterfaceName == "" {
		return fmt.Errorf("%w: interface name is empty", dhcperr.ErrInvalidRequest)
	}
	if !isIPv4(spec.ListenAddr) {
		return fmt.Errorf("%w: listen address must be IPv4", dhcperr.ErrInvalidRequest)
	}
	if spec.ListenPort == 0 {
		return fmt.Errorf("%w: listen port is zero", dhcperr.ErrInvalidRequest)
	}
	if !isIPv4(spec.ServerAddr) {
		return fmt.Errorf("%w: server address must be IPv4", dhcperr.ErrInvalidRequest)
	}
	if !spec.Subnet.IsValid() || !spec.Subnet.Addr().Is4() {
		return fmt.Errorf("%w: subnet must be IPv4", dhcperr.ErrInvalidRequest)
	}
	if !spec.Subnet.Contains(spec.ServerAddr) {
		return fmt.Errorf("%w: server address must be inside subnet", dhcperr.ErrInvalidRequest)
	}
	if err := validateAddressRange(spec.Subnet, spec.Pool); err != nil {
		return err
	}
	if spec.LeaseDuration <= 0 {
		return fmt.Errorf("%w: lease duration must be positive", dhcperr.ErrInvalidRequest)
	}
	if err := validateOptionAddrs("router", spec.Router); err != nil {
		return err
	}
	if err := validateOptionAddrs("dns", spec.DNS); err != nil {
		return err
	}
	switch spec.BindMode {
	case "":
		return fmt.Errorf("%w: bind mode is empty", dhcperr.ErrInvalidRequest)
	case dhcp.BindModeInterfaceZone:
		return nil
	default:
		return fmt.Errorf("%w: bind mode %q", dhcperr.ErrUnsupported, spec.BindMode)
	}
}

func validateOptionAddrs(name string, opt dhcp.DHCPOptionAddrs) error {
	switch opt.Mode {
	case "":
		return fmt.Errorf("%w: %s option mode is empty", dhcperr.ErrInvalidRequest, name)
	case dhcp.DHCPOptionDisabled:
		if len(opt.Addrs) != 0 {
			return fmt.Errorf("%w: %s option disabled with addresses", dhcperr.ErrInvalidRequest, name)
		}
		return nil
	case dhcp.DHCPOptionEnabled:
		if len(opt.Addrs) == 0 {
			return fmt.Errorf("%w: %s option enabled without addresses", dhcperr.ErrInvalidRequest, name)
		}
		for _, addr := range opt.Addrs {
			if !isIPv4(addr) {
				return fmt.Errorf("%w: %s option address must be IPv4", dhcperr.ErrInvalidRequest, name)
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: %s option mode %q", dhcperr.ErrInvalidRequest, name, opt.Mode)
	}
}

func validateAddressRange(subnet netip.Prefix, pool dhcp.AddressRange) error {
	if !isIPv4(pool.Start) || !isIPv4(pool.End) {
		return fmt.Errorf("%w: pool addresses must be IPv4", dhcperr.ErrInvalidRequest)
	}
	if !subnet.Contains(pool.Start) || !subnet.Contains(pool.End) {
		return fmt.Errorf("%w: pool addresses must be inside subnet", dhcperr.ErrInvalidRequest)
	}
	if pool.Start.Compare(pool.End) > 0 {
		return fmt.Errorf("%w: pool start is after end", dhcperr.ErrInvalidRequest)
	}
	return nil
}

func validateServerID(id dhcp.ServerID) error {
	if id == "" {
		return fmt.Errorf("%w: server ID is empty", dhcperr.ErrInvalidRequest)
	}
	return nil
}

func validateMAC(mac net.HardwareAddr) error {
	if len(mac) != 6 {
		return fmt.Errorf("%w: MAC must be 6 bytes", dhcperr.ErrInvalidRequest)
	}
	if mac[0]&1 != 0 {
		return fmt.Errorf("%w: MAC must be unicast", dhcperr.ErrInvalidRequest)
	}
	return nil
}

func validateLeaseFilter(filter dhcp.LeaseFilter) error {
	return validateServerID(filter.ServerID)
}

func validateBindingIPInPool(spec dhcp.ServerSpec, ip netip.Addr) error {
	if !isIPv4(ip) {
		return fmt.Errorf("%w: binding IP must be IPv4", dhcperr.ErrInvalidRequest)
	}
	if ip.Compare(spec.Pool.Start) < 0 || ip.Compare(spec.Pool.End) > 0 {
		return fmt.Errorf("%w: binding IP %s is outside pool", dhcperr.ErrInvalidRequest, ip)
	}
	return nil
}

func validateHostname(hostname dhcp.BindingHostname) error {
	if !hostname.Set {
		return nil
	}
	value := strings.TrimSpace(hostname.Value)
	if value == "" || value != hostname.Value {
		return fmt.Errorf("%w: hostname must be non-empty without surrounding spaces", dhcperr.ErrInvalidRequest)
	}
	if len(value) > 253 {
		return fmt.Errorf("%w: hostname is too long", dhcperr.ErrInvalidRequest)
	}
	for label := range strings.SplitSeq(value, ".") {
		if !validHostnameLabel(label) {
			return fmt.Errorf("%w: hostname %q is invalid", dhcperr.ErrInvalidRequest, hostname.Value)
		}
	}
	return nil
}

func validHostnameLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	for i, r := range label {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		digit := r >= '0' && r <= '9'
		hyphen := r == '-'
		if !letter && !digit && !hyphen {
			return false
		}
		if hyphen && (i == 0 || i == len(label)-1) {
			return false
		}
	}
	return true
}

func isIPv4(addr netip.Addr) bool {
	return addr.IsValid() && addr.Is4()
}
