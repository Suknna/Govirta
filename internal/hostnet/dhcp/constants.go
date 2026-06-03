package dhcp

import "net/netip"

// ServerID identifies one explicit DHCP responder instance.
type ServerID string

// Port is an explicit UDP listen port for a DHCP responder.
type Port uint16

// BindMode selects how a DHCP responder binds to host networking.
type BindMode string

// DHCPOptionMode controls whether an optional DHCP option is emitted.
type DHCPOptionMode string

// ServerState reports the observed DHCP responder lifecycle state.
type ServerState string

// LeaseState reports the observed DHCP lease lifecycle state.
type LeaseState string

const (
	BindModeInterfaceZone BindMode = "interface-zone"

	DHCPOptionDisabled DHCPOptionMode = "disabled"
	DHCPOptionEnabled  DHCPOptionMode = "enabled"

	ServerStateStarting ServerState = "starting"
	ServerStateReady    ServerState = "ready"
	ServerStateStopping ServerState = "stopping"
	ServerStateStopped  ServerState = "stopped"

	LeaseStateReserved LeaseState = "reserved"
	LeaseStateBound    LeaseState = "bound"
)

// DHCPOptionAddrs makes option emission an explicit caller choice.
type DHCPOptionAddrs struct {
	Mode  DHCPOptionMode
	Addrs []netip.Addr
}

// AddressRange describes an explicit IPv4 address range.
type AddressRange struct {
	Start netip.Addr
	End   netip.Addr
}

// BindingHostname carries an optional hostname without inferring one from MAC or IP.
type BindingHostname struct {
	Value string
	Set   bool
}
