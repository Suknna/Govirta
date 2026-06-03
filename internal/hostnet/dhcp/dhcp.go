package dhcp

import (
	"context"
	"net"
	"net/netip"
	"time"

	"github.com/suknna/govirta/internal/hostnet/link"
)

// Manager owns DHCP server lifecycle and explicit MAC-to-IP bindings.
//
// Implementations must not create bridge/TAP links, modify route or firewall
// state, manage QEMU processes, infer DHCP options, or expose backend-specific
// types through this root package.
type Manager interface {
	// Start starts one explicit DHCP responder instance.
	Start(ctx context.Context, spec ServerSpec) (ServerInfo, error)
	// Stop stops the responder identified by id.
	Stop(ctx context.Context, id ServerID) error

	// ApplyBinding creates or confirms one explicit MAC-to-IP binding.
	ApplyBinding(ctx context.Context, req BindingRequest) (LeaseInfo, error)
	// RemoveBinding removes one explicit MAC-to-IP binding.
	RemoveBinding(ctx context.Context, query BindingQuery) error

	// GetServer returns the observed state for one responder.
	GetServer(ctx context.Context, id ServerID) (ServerInfo, error)
	// GetLease returns the observed lease for one MAC on one responder.
	GetLease(ctx context.Context, query BindingQuery) (LeaseInfo, error)
	// ListLeases returns leases matching filter.
	ListLeases(ctx context.Context, filter LeaseFilter) ([]LeaseInfo, error)
}

// ServerSpec describes the complete DHCP responder configuration.
type ServerSpec struct {
	ID            ServerID
	InterfaceName link.Name
	ListenAddr    netip.Addr
	ListenPort    Port
	ServerAddr    netip.Addr
	Subnet        netip.Prefix
	Pool          AddressRange
	LeaseDuration time.Duration
	Router        DHCPOptionAddrs
	DNS           DHCPOptionAddrs
	BindMode      BindMode
}

// BindingRequest describes one explicit MAC-to-IP binding supplied by the caller.
type BindingRequest struct {
	ServerID ServerID
	MAC      net.HardwareAddr
	IP       netip.Addr
	Hostname BindingHostname
}

// BindingQuery selects one binding by server and MAC address.
type BindingQuery struct {
	ServerID ServerID
	MAC      net.HardwareAddr
}

// LeaseFilter selects leases for one explicit server.
type LeaseFilter struct {
	ServerID ServerID
}

// ServerInfo reports observed DHCP responder state.
type ServerInfo struct {
	ID            ServerID
	InterfaceName link.Name
	ListenAddr    netip.Addr
	ListenPort    Port
	ServerAddr    netip.Addr
	Subnet        netip.Prefix
	Pool          AddressRange
	State         ServerState
	LeaseCount    int
}

// LeaseInfo reports observed lease state for one binding.
type LeaseInfo struct {
	ServerID  ServerID
	MAC       net.HardwareAddr
	IP        netip.Addr
	Hostname  BindingHostname
	State     LeaseState
	ExpiresAt time.Time
}
