// Package firewall defines Govirta-owned host firewall contracts.
package firewall

// TableFamily identifies the host firewall table family used by a Govirta rule.
type TableFamily string

// TableName identifies a host firewall table owned or selected by Govirta.
type TableName string

// ChainName identifies a host firewall chain owned or selected by Govirta.
type ChainName string

// InterfaceName identifies a host network interface used in firewall matching.
type InterfaceName string

// RuleOwner identifies the Govirta component or resource that owns a firewall rule.
type RuleOwner string

// RuleHandle identifies an observed platform rule handle.
type RuleHandle uint64

// RuleGroupKey identifies the logical rule group an observed rule belongs to.
//
// A single host firewall behavior can be implemented by more than one platform
// rule (for example, endpoint anti-spoofing is four guard rules and
// forward-accept is two). RuleGroupKey is the stable logical discriminator of
// that group (the guarded TAP for endpoint anti-spoofing, the guest CIDR for
// forward-accept), independent of any single rule's platform handle. It is
// empty for single-rule behaviors such as masquerade. Implementations populate
// it on observed RuleInfo so a group delete resolves the group by stable
// identity rather than by a handle that the kernel can renumber.
type RuleGroupKey string

// RulePurpose identifies the Govirta behavior implemented by a firewall rule.
type RulePurpose string

// ChainType identifies the semantic type of a firewall chain.
type ChainType string

// Hook identifies the host packet-processing hook used by a chain.
type Hook string

// PriorityName identifies a named firewall priority used by Govirta.
type PriorityName string

// OwnerFilterMode selects whether a rule owner filter matches any owner or one owner.
type OwnerFilterMode string

// PurposeFilterMode selects whether a rule purpose filter matches any purpose or one purpose.
type PurposeFilterMode string

// FamilyFilterMode selects whether a table family filter matches any family or one family.
type FamilyFilterMode string

// TableFilterMode selects whether a table name filter matches any table or one table.
type TableFilterMode string

// ChainFilterMode selects whether a chain name filter matches any chain or one chain.
type ChainFilterMode string

const (
	// TableFamilyIPv4 selects the IPv4 host firewall family.
	TableFamilyIPv4 TableFamily = "ipv4"
	// TableFamilyBridge selects the bridge host firewall family.
	TableFamilyBridge TableFamily = "bridge"

	// ChainTypeNAT identifies a NAT chain.
	ChainTypeNAT ChainType = "nat"
	// ChainTypeFilter identifies a filter chain.
	ChainTypeFilter ChainType = "filter"

	// HookPostrouting selects the postrouting hook.
	HookPostrouting Hook = "postrouting"
	// HookForward selects the forward hook.
	HookForward Hook = "forward"

	// PriorityNameSrcNAT identifies the source NAT priority used by Govirta.
	PriorityNameSrcNAT PriorityName = "srcnat"
	// PriorityNameBridgeFilter identifies the bridge filter priority used by Govirta.
	PriorityNameBridgeFilter PriorityName = "bridge-filter"
	// PriorityNameForwardFilter identifies the filter-forward priority used by Govirta.
	PriorityNameForwardFilter PriorityName = "forward-filter"

	// RulePurposeMasquerade identifies guest egress masquerade rules.
	RulePurposeMasquerade RulePurpose = "masquerade"
	// RulePurposeEndpointAntiSpoofing identifies endpoint MAC/IP guard rules.
	RulePurposeEndpointAntiSpoofing RulePurpose = "endpoint-anti-spoofing"
	// RulePurposeForwardAccept identifies guest CIDR filter-forward accept rules.
	RulePurposeForwardAccept RulePurpose = "forward-accept"
)

const (
	// OwnerAny matches rules owned by any Govirta owner.
	OwnerAny OwnerFilterMode = "any"
	// OwnerValue matches rules owned by one explicit owner.
	OwnerValue OwnerFilterMode = "value"

	// PurposeAny matches rules with any Govirta purpose.
	PurposeAny PurposeFilterMode = "any"
	// PurposeValue matches rules with one explicit purpose.
	PurposeValue PurposeFilterMode = "value"

	// FamilyAny matches rules in any supported table family.
	FamilyAny FamilyFilterMode = "any"
	// FamilyValue matches rules in one explicit table family.
	FamilyValue FamilyFilterMode = "value"

	// TableAny matches rules in any table.
	TableAny TableFilterMode = "any"
	// TableValue matches rules in one explicit table.
	TableValue TableFilterMode = "value"

	// ChainAny matches rules in any chain.
	ChainAny ChainFilterMode = "any"
	// ChainValue matches rules in one explicit chain.
	ChainValue ChainFilterMode = "value"
)

// Priority selects an explicit platform priority for a Govirta firewall chain.
//
// Set distinguishes an intentional priority value from an omitted priority. Use
// ExplicitPriority even when Value is 0 so zero-priority requests remain
// observable and cannot be confused with missing input.
type Priority struct {
	// Value is the numeric platform priority.
	Value int
	// Name is the Govirta-owned semantic name for Value.
	Name PriorityName
	// Set marks Value and Name as explicitly supplied by the caller.
	Set bool
}

// ExplicitPriority returns a priority marked as caller-supplied.
func ExplicitPriority(value int, name PriorityName) Priority {
	return Priority{Value: value, Name: name, Set: true}
}

// AnyOwner returns an explicit filter that matches any rule owner.
func AnyOwner() OwnerFilter { return OwnerFilter{Mode: OwnerAny} }

// FilterOwner returns an explicit filter that matches one rule owner.
func FilterOwner(owner RuleOwner) OwnerFilter {
	return OwnerFilter{Mode: OwnerValue, Value: owner}
}

// AnyPurpose returns an explicit filter that matches any rule purpose.
func AnyPurpose() PurposeFilter { return PurposeFilter{Mode: PurposeAny} }

// FilterPurpose returns an explicit filter that matches one rule purpose.
func FilterPurpose(purpose RulePurpose) PurposeFilter {
	return PurposeFilter{Mode: PurposeValue, Value: purpose}
}

// AnyFamily returns an explicit filter that matches any table family.
func AnyFamily() FamilyFilter { return FamilyFilter{Mode: FamilyAny} }

// FilterFamily returns an explicit filter that matches one table family.
func FilterFamily(family TableFamily) FamilyFilter {
	return FamilyFilter{Mode: FamilyValue, Value: family}
}

// AnyTable returns an explicit filter that matches any table.
func AnyTable() TableFilter { return TableFilter{Mode: TableAny} }

// FilterTable returns an explicit filter that matches one table.
func FilterTable(table TableName) TableFilter {
	return TableFilter{Mode: TableValue, Value: table}
}

// AnyChain returns an explicit filter that matches any chain.
func AnyChain() ChainFilter { return ChainFilter{Mode: ChainAny} }

// FilterChain returns an explicit filter that matches one chain.
func FilterChain(chain ChainName) ChainFilter {
	return ChainFilter{Mode: ChainValue, Value: chain}
}
