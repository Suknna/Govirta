package firewall

type TableFamily string
type TableName string
type ChainName string
type InterfaceName string
type RuleOwner string
type RuleHandle uint64
type RulePurpose string
type ChainType string
type Hook string
type PriorityName string

const (
	TableFamilyIPv4   TableFamily = "ipv4"
	TableFamilyBridge TableFamily = "bridge"

	ChainTypeNAT    ChainType = "nat"
	ChainTypeFilter ChainType = "filter"

	HookPostrouting Hook = "postrouting"
	HookForward     Hook = "forward"

	PriorityNameSrcNAT       PriorityName = "srcnat"
	PriorityNameBridgeFilter PriorityName = "bridge-filter"

	RulePurposeMasquerade           RulePurpose = "masquerade"
	RulePurposeEndpointAntiSpoofing RulePurpose = "endpoint-anti-spoofing"
)

type Priority struct {
	Value int
	Name  PriorityName
	Set   bool
}

func ExplicitPriority(value int, name PriorityName) Priority {
	return Priority{Value: value, Name: name, Set: true}
}
