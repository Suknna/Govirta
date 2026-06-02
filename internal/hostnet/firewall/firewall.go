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
	EnsureEndpointAntiSpoofing(ctx context.Context, spec EndpointAntiSpoofingSpec) (RuleInfo, error)

	// DeleteEndpointAntiSpoofing removes the endpoint anti-spoofing rule selected by ref.
	DeleteEndpointAntiSpoofing(ctx context.Context, ref RuleRef) error

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
type RuleRef struct {
	Owner     RuleOwner
	Purpose   RulePurpose
	Family    TableFamily
	TableName TableName
	ChainName ChainName
	Handle    RuleHandle
}

// RuleQuery selects a single observed firewall rule.
type RuleQuery struct {
	Ref RuleRef
}

// RuleFilter selects observed firewall rules for enumeration.
//
// All behavior-affecting filter dimensions are explicit. Empty values match the
// implementation-defined validation contract of the concrete manager.
type RuleFilter struct {
	Owner     RuleOwner
	Purpose   RulePurpose
	Family    TableFamily
	TableName TableName
	ChainName ChainName
}

// RuleSummary contains purpose-specific observed firewall rule details.
type RuleSummary struct {
	Masquerade           *MasqueradeSummary
	EndpointAntiSpoofing *EndpointAntiSpoofingSummary
}

// MasqueradeSummary reports observed source NAT rule details.
type MasqueradeSummary struct {
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
// Implementations populate RuleInfo from actual host firewall state after
// translating platform data into Govirta-owned types. RuleInfo must not be a
// blind echo of a requested spec.
type RuleInfo struct {
	Ref       RuleRef
	Family    TableFamily
	TableName TableName
	ChainName ChainName
	Purpose   RulePurpose
	Owner     RuleOwner
	Handle    RuleHandle
	Summary   RuleSummary
}
