// Package netpool registers declarative logical network definitions and
// orchestrates the host networking primitives (link, route, firewall, dhcp)
// that realize them. It never caches drift-prone observed resource state: the
// authoritative state is always the live host resource, read on demand through
// the injected primitive managers. The in-memory index holds only declarative
// intent that the control plane re-registers (replays) after restart.
package netpool

import (
	"net"
	"net/netip"
	"time"

	"github.com/suknna/govirta/internal/hostnet/dhcp"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/link"
)

// NetworkName is the explicit, caller-provided identifier for a logical network.
type NetworkName string

// VMID is the explicit, caller-provided identifier for a VM owning a NIC.
type VMID string

// NetworkDefinition is the declarative logical intent for one shared network
// segment. It describes only what the network should look like; it holds no
// observed resource state. Every behavior-affecting field is explicit and
// caller-provided: the orchestration layer never generates, infers, or defaults
// names, addresses, MACs, or firewall identities.
type NetworkDefinition struct {
	Name        NetworkName
	BridgeName  link.Name
	BridgeMAC   net.HardwareAddr
	BridgeMTU   int
	Subnet      netip.Prefix
	GatewayCIDR netip.Prefix
	Pool        dhcp.AddressRange

	EgressIface firewall.InterfaceName

	DHCPServerID  dhcp.ServerID
	Router        dhcp.DHCPOptionAddrs
	DNS           dhcp.DHCPOptionAddrs
	LeaseDuration time.Duration

	// Govirta-owned firewall identities for this network's IPv4 rules.
	FirewallTable      firewall.TableName
	MasqueradeChain    firewall.ChainName
	ForwardChain       firewall.ChainName
	RuleOwner          firewall.RuleOwner
	MasqueradePriority firewall.Priority
	ForwardPriority    firewall.Priority
}

// NICDefinition is the declarative logical intent for one VM NIC. MAC is the
// guest NIC MAC, supplied by the control plane and threaded unchanged to the
// TAP, the DHCP binding, and the anti-spoofing guard. The orchestration layer
// never generates a MAC.
type NICDefinition struct {
	NetworkName NetworkName
	VMID        VMID
	TapName     link.Name
	MAC         net.HardwareAddr
	IP          netip.Addr
	TapMTU      int
	VNetHeader  link.VNetHeaderMode
	OwnerUID    link.UID
	OwnerGID    link.GID
	Hostname    dhcp.BindingHostname

	// Govirta-owned bridge-family anti-spoofing identities for this NIC.
	AntiSpoofTable    firewall.TableName
	AntiSpoofChain    firewall.ChainName
	AntiSpoofPriority firewall.Priority
}

// networkRecord is the service-owned stored form of a registered network plus
// its registered NIC definitions, keyed by VMID (one NIC per VM per network in
// this phase).
type networkRecord struct {
	def  NetworkDefinition
	nics map[VMID]NICDefinition
}

func (r *networkRecord) clone() *networkRecord {
	cloned := &networkRecord{
		def:  cloneNetworkDefinition(r.def),
		nics: make(map[VMID]NICDefinition, len(r.nics)),
	}
	for id, nic := range r.nics {
		cloned.nics[id] = cloneNICDefinition(nic)
	}
	return cloned
}

func cloneNetworkDefinition(def NetworkDefinition) NetworkDefinition {
	def.BridgeMAC = cloneHardwareAddr(def.BridgeMAC)
	def.Router = cloneDHCPOptionAddrs(def.Router)
	def.DNS = cloneDHCPOptionAddrs(def.DNS)
	return def
}

func cloneNICDefinition(nic NICDefinition) NICDefinition {
	nic.MAC = cloneHardwareAddr(nic.MAC)
	return nic
}

func cloneHardwareAddr(addr net.HardwareAddr) net.HardwareAddr {
	if addr == nil {
		return nil
	}
	cloned := make(net.HardwareAddr, len(addr))
	copy(cloned, addr)
	return cloned
}

func cloneDHCPOptionAddrs(opt dhcp.DHCPOptionAddrs) dhcp.DHCPOptionAddrs {
	if opt.Addrs == nil {
		return opt
	}
	cloned := make([]netip.Addr, len(opt.Addrs))
	copy(cloned, opt.Addrs)
	opt.Addrs = cloned
	return opt
}
