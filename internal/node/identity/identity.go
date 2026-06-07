// Package identity derives deterministic low-level kernel identities from the
// stable identities of Govirta resources.
//
// The API spec deliberately does not carry kernel identities (nftables table
// and chain names, chain priorities, TAP interface names, VNetHeader mode).
// Controllers compute them from stable resource identity (a network name, a VM
// uid) and then fill them into the execution-plane definitions. The functions
// here are pure: same input always yields the same output, they read no etcd
// state, and they perform no side effects.
//
// TAP owner UID/GID are intentionally not derived here. They are the identity
// of the OS user that runs QEMU and must be injected by cmd from configuration,
// so the pure derivation never hard-codes root or any other principal.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/suknna/govirta/pkg/hostnet/firewall"
	"github.com/suknna/govirta/pkg/hostnet/link"
)

const (
	// firewallTableName is the single project-owned nftables table that holds
	// every Govirta-managed rule. nftables keys tables by (family, name), so the
	// same name is reused across the ipv4 and bridge families; the family is
	// chosen by the execution plane, not by this pure derivation.
	firewallTableName firewall.TableName = "govirta"

	// masqueradeChainPrefix prefixes the per-network source NAT chain.
	masqueradeChainPrefix = "gv-masq-"
	// forwardChainPrefix prefixes the per-network filter-forward accept chain.
	forwardChainPrefix = "gv-fwd-"
	// antiSpoofChainPrefix prefixes the per-NIC anti-spoofing chain.
	antiSpoofChainPrefix = "gv-as-"

	// ruleOwnerPrefix prefixes the per-network rule owner identity. It must use
	// only firewall-safe bytes ([a-zA-Z0-9_.-]): the host firewall's
	// validateSafeName rejects "/", so a "govirta/" prefix made every masquerade
	// ensure fail and the network controller spin re-reconciling forever. The
	// "-" separator matches the chain-name prefixes above and stays safe.
	ruleOwnerPrefix = "govirta-"

	// tapNamePrefix prefixes every derived TAP interface name.
	tapNamePrefix = "gv"
	// tapHashHexLen is the number of hex characters of the VM-uid hash embedded
	// in a TAP name. Four bytes render to eight hex characters; combined with the
	// "gv" prefix, the "." separator, and a small NIC index this keeps the name
	// within the Linux IFNAMSIZ-1 (15 byte) interface name limit.
	tapHashHexLen = 8
)

// NetworkIdentity is the deterministic set of kernel firewall identities a
// network resource maps to.
type NetworkIdentity struct {
	// FirewallTable is the project-owned nftables table that holds the network's
	// rules.
	FirewallTable firewall.TableName
	// MasqueradeChain is the chain that holds the network's source NAT rule.
	MasqueradeChain firewall.ChainName
	// ForwardChain is the chain that holds the network's filter-forward accept
	// rules.
	ForwardChain firewall.ChainName
	// RuleOwner is the owner identity stamped on the network's firewall rules.
	RuleOwner firewall.RuleOwner
	// MasqueradePriority is the explicit priority for the masquerade chain.
	MasqueradePriority firewall.Priority
	// ForwardPriority is the explicit priority for the forward-accept chain.
	ForwardPriority firewall.Priority
}

// DeriveNetworkIdentity derives the kernel firewall identity for networkName.
//
// The derivation is deterministic: the same networkName always yields the same
// table, chains, owner, and priorities. Distinct names yield distinct chains
// and owners while sharing the single project-owned table.
func DeriveNetworkIdentity(networkName string) NetworkIdentity {
	return NetworkIdentity{
		FirewallTable:      firewallTableName,
		MasqueradeChain:    firewall.ChainName(masqueradeChainPrefix + networkName),
		ForwardChain:       firewall.ChainName(forwardChainPrefix + networkName),
		RuleOwner:          firewall.RuleOwner(ruleOwnerPrefix + networkName),
		MasqueradePriority: firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT),
		ForwardPriority:    firewall.ExplicitPriority(0, firewall.PriorityNameForwardFilter),
	}
}

// NICIdentity is the deterministic set of kernel identities a single virtual
// NIC of a VM maps to.
type NICIdentity struct {
	// TapName is the host TAP interface name backing the NIC. It is guaranteed to
	// fit the Linux interface name limit (IFNAMSIZ-1, 15 bytes).
	TapName link.Name
	// AntiSpoofTable is the project-owned nftables table that holds the NIC's
	// anti-spoofing rules.
	AntiSpoofTable firewall.TableName
	// AntiSpoofChain is the chain that holds the NIC's anti-spoofing rules.
	AntiSpoofChain firewall.ChainName
	// AntiSpoofPriority is the explicit priority for the anti-spoofing chain.
	AntiSpoofPriority firewall.Priority
	// VNetHeader is the virtio net-header mode for the TAP.
	VNetHeader link.VNetHeaderMode
}

// DeriveNICIdentity derives the kernel identity for the nicIndex-th NIC of the
// VM identified by vmUID.
//
// The TAP name is "gv" + the first eight hex characters of the SHA-256 hash of
// vmUID + "." + nicIndex. Hashing the uid bounds the variable-length input to a
// fixed width, which keeps the interface name within the Linux IFNAMSIZ-1 limit
// for any uid. The derivation is deterministic: the same (vmUID, nicIndex)
// always yields the same identity.
func DeriveNICIdentity(vmUID string, nicIndex int) NICIdentity {
	tapName := link.Name(tapNamePrefix + shortHash(vmUID) + "." + strconv.Itoa(nicIndex))
	return NICIdentity{
		TapName:           tapName,
		AntiSpoofTable:    firewallTableName,
		AntiSpoofChain:    firewall.ChainName(antiSpoofChainPrefix + string(tapName)),
		AntiSpoofPriority: firewall.ExplicitPriority(-200, firewall.PriorityNameBridgeFilter),
		VNetHeader:        link.VNetHeaderEnabled,
	}
}

// shortHash returns a stable, fixed-width lowercase hex digest of input.
//
// SHA-256 gives a uniform, collision-resistant digest; truncating to the first
// few bytes yields a short stable identifier suitable for embedding in a
// length-constrained interface name.
func shortHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:tapHashHexLen]
}
