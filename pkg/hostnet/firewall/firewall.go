package firewall

import (
	"context"
	"net"
	"net/netip"
)

// Manager owns host firewall rule lifecycle operations.
//
// Implementations must require callers to pass every behavior-affecting
// parameter explicitly through request structs. Ensure operations must return
// observed firewall state after success rather than echoing requested inputs.
// A nil context is an invalid request; a canceled or expired context must be
// returned as the context error before an implementation performs live host
// firewall work.
type Manager interface {
	// EnsureMasquerade creates or reconciles the explicit source NAT rule in spec.
	//
	// Implementations return observed firewall state after success rather than a
	// blind echo of spec.
	EnsureMasquerade(ctx context.Context, spec MasqueradeSpec) (RuleInfo, error)

	// DeleteMasquerade removes the masquerade rule selected by ref.
	DeleteMasquerade(ctx context.Context, ref RuleRef) error

	// EnsureEndpointAntiSpoofing creates or reconciles the explicit endpoint guard in spec.
	//
	// Implementations return observed firewall state after success rather than a
	// blind echo of spec.
	//
	// Scope: the guard covers untagged IPv4 (EtherType 0x0800) and ARP
	// (EtherType 0x0806) frames only. VLAN-tagged frames (0x8100) and IPv6
	// frames are out of scope and are not matched by the guard, so under an
	// accept default policy they bypass source-MAC/IPv4 enforcement. Callers
	// that require non-IPv4 or tagged isolation must enforce it separately.
	EnsureEndpointAntiSpoofing(ctx context.Context, spec EndpointAntiSpoofingSpec) (RuleInfo, error)

	// DeleteEndpointAntiSpoofing removes the endpoint anti-spoofing rule group
	// selected by ref. ref must carry the GroupKey from the observed RuleInfo
	// returned by Ensure/List/Get; a missing GroupKey is rejected with an
	// invalid-request error.
	DeleteEndpointAntiSpoofing(ctx context.Context, ref RuleRef) error

	// EnsureForwardAccept creates or reconciles the explicit filter-forward
	// accept rule group that allows guest CIDR egress traffic and its conntrack
	// established/related return traffic across the egress interface.
	//
	// Implementations return observed firewall state after success rather than a
	// blind echo of spec.
	EnsureForwardAccept(ctx context.Context, spec ForwardAcceptSpec) (RuleInfo, error)

	// DeleteForwardAccept removes the forward-accept rule group selected by ref.
	// ref must carry the GroupKey from the observed RuleInfo returned by
	// Ensure/List/Get; a missing GroupKey is rejected with an invalid-request
	// error.
	DeleteForwardAccept(ctx context.Context, ref RuleRef) error

	// GetRule returns the observed firewall rule selected by query.
	GetRule(ctx context.Context, query RuleQuery) (RuleInfo, error)

	// ListRules returns observed firewall rules that match filter.
	ListRules(ctx context.Context, filter RuleFilter) ([]RuleInfo, error)
}

// MasqueradeSpec describes the complete desired source NAT rule state.
//
// TableName, ChainName, RuleOwner, GuestCIDR, EgressInterfaceName, and Priority
// are all behavior-affecting fields and must be explicitly supplied by callers.
type MasqueradeSpec struct {
	TableName           TableName
	ChainName           ChainName
	RuleOwner           RuleOwner
	GuestCIDR           netip.Prefix
	EgressInterfaceName InterfaceName
	Priority            Priority
}

// ForwardAcceptSpec describes the complete desired filter-forward accept state.
//
// TableName, ChainName, RuleOwner, GuestCIDR, EgressInterfaceName, and Priority
// are all behavior-affecting fields and must be explicitly supplied by callers.
// The implementation creates a two-rule group (egress accept plus a conntrack
// established/related return accept) under one logical RuleInfo; callers must
// not pass a bridge name because forward-accept matches by guest CIDR and egress
// interface only, symmetric with MasqueradeSpec.
type ForwardAcceptSpec struct {
	TableName           TableName
	ChainName           ChainName
	RuleOwner           RuleOwner
	GuestCIDR           netip.Prefix
	EgressInterfaceName InterfaceName
	Priority            Priority
}

// EndpointAntiSpoofingSpec describes the complete desired endpoint guard rule state.
//
// TableName, ChainName, RuleOwner, BridgeName, TapName, MAC, IPv4, and Priority
// are all behavior-affecting fields and must be explicitly supplied by callers.
type EndpointAntiSpoofingSpec struct {
	TableName  TableName
	ChainName  ChainName
	RuleOwner  RuleOwner
	BridgeName InterfaceName
	TapName    InterfaceName
	MAC        net.HardwareAddr
	IPv4       netip.Addr
	Priority   Priority
}

// RuleRef identifies an existing firewall rule selected by explicit owner,
// purpose, family, table, chain, and platform rule handle.
//
// GroupKey is the stable logical discriminator for behaviors implemented by a
// multi-rule group (endpoint anti-spoofing, forward-accept); implementations
// populate it on observed RuleInfo and callers round-trip it back into the
// matching Delete operation so the group is resolved by stable identity rather
// than by a platform handle the kernel can renumber. It is empty for
// single-rule behaviors such as masquerade.
type RuleRef struct {
	Owner     RuleOwner
	Purpose   RulePurpose
	Family    TableFamily
	TableName TableName
	ChainName ChainName
	Handle    RuleHandle
	GroupKey  RuleGroupKey
}

// RuleQuery selects a single observed firewall rule.
type RuleQuery struct {
	Ref RuleRef
}

// OwnerFilter selects owner matching for ListRules.
//
// Mode must be explicitly set to OwnerAny or OwnerValue. OwnerValue requires
// Value to identify the owner to match.
type OwnerFilter struct {
	Mode  OwnerFilterMode
	Value RuleOwner
}

// PurposeFilter selects purpose matching for ListRules.
//
// Mode must be explicitly set to PurposeAny or PurposeValue. PurposeValue
// requires Value to identify the rule purpose to match.
type PurposeFilter struct {
	Mode  PurposeFilterMode
	Value RulePurpose
}

// FamilyFilter selects table-family matching for ListRules.
//
// Mode must be explicitly set to FamilyAny or FamilyValue. FamilyValue requires
// Value to identify the table family to match.
type FamilyFilter struct {
	Mode  FamilyFilterMode
	Value TableFamily
}

// TableFilter selects table matching for ListRules.
//
// Mode must be explicitly set to TableAny or TableValue. TableValue requires
// Value to identify the table to match.
type TableFilter struct {
	Mode  TableFilterMode
	Value TableName
}

// ChainFilter selects chain matching for ListRules.
//
// Mode must be explicitly set to ChainAny or ChainValue. ChainValue requires
// Value to identify the chain to match.
type ChainFilter struct {
	Mode  ChainFilterMode
	Value ChainName
}

// RuleFilter selects observed firewall rules for enumeration.
//
// All behavior-affecting filter dimensions must use explicit modes. Any modes
// select all values for their dimension; value modes select only matching
// observed rule fields.
type RuleFilter struct {
	Owner   OwnerFilter
	Purpose PurposeFilter
	Family  FamilyFilter
	Table   TableFilter
	Chain   ChainFilter
}

// RuleSummary contains purpose-specific observed firewall rule details.
//
// Exactly one summary pointer must be populated. The populated summary must
// match RuleInfo.Ref.Purpose.
type RuleSummary struct {
	Masquerade           *MasqueradeSummary
	EndpointAntiSpoofing *EndpointAntiSpoofingSummary
	ForwardAccept        *ForwardAcceptSummary
}

// MasqueradeSummary reports observed source NAT rule details.
type MasqueradeSummary struct {
	GuestCIDR           netip.Prefix
	EgressInterfaceName InterfaceName
	Priority            Priority
}

// ForwardAcceptSummary reports observed forward-accept rule group details.
//
// The two underlying rules (egress accept and conntrack return accept) are
// compacted into one logical summary.
type ForwardAcceptSummary struct {
	GuestCIDR           netip.Prefix
	EgressInterfaceName InterfaceName
	Priority            Priority
}

// EndpointAntiSpoofingSummary reports observed endpoint guard rule details.
type EndpointAntiSpoofingSummary struct {
	BridgeName InterfaceName
	TapName    InterfaceName
	MAC        net.HardwareAddr
	IPv4       netip.Addr
	Priority   Priority
}

// RuleInfo reports observed host firewall rule state.
//
// Ref is the single source of identity for the observed rule. Summary carries
// purpose-specific observed details and must match Ref.Purpose.
// Implementations populate RuleInfo from actual host firewall state after
// translating platform data into Govirta-owned types. RuleInfo must not be a
// blind echo of a requested spec.
type RuleInfo struct {
	Ref     RuleRef
	Summary RuleSummary
}
