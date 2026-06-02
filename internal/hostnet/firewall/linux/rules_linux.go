//go:build linux

package linux

import (
	"sort"

	"github.com/google/nftables"
	"github.com/suknna/govirta/internal/hostnet/firewall"
)

func ruleRefForInfo(info firewall.RuleInfo) firewall.RuleRef {
	return info.Ref
}

func ruleInfoMatchesFilter(info firewall.RuleInfo, filter firewall.RuleFilter) bool {
	ref := ruleRefForInfo(info)
	if filter.Owner.Mode == firewall.OwnerValue && ref.Owner != filter.Owner.Value {
		return false
	}
	if filter.Purpose.Mode == firewall.PurposeValue && ref.Purpose != filter.Purpose.Value {
		return false
	}
	if filter.Family.Mode == firewall.FamilyValue && ref.Family != filter.Family.Value {
		return false
	}
	if filter.Table.Mode == firewall.TableValue && ref.TableName != filter.Table.Value {
		return false
	}
	if filter.Chain.Mode == firewall.ChainValue && ref.ChainName != filter.Chain.Value {
		return false
	}
	return true
}

func nftFamily(family firewall.TableFamily) nftables.TableFamily {
	switch family {
	case firewall.TableFamilyIPv4:
		return nftables.TableFamilyIPv4
	case firewall.TableFamilyBridge:
		return nftables.TableFamilyBridge
	default:
		return nftables.TableFamilyUnspecified
	}
}

func firewallFamily(family nftables.TableFamily) (firewall.TableFamily, bool) {
	switch family {
	case nftables.TableFamilyIPv4:
		return firewall.TableFamilyIPv4, true
	case nftables.TableFamilyBridge:
		return firewall.TableFamilyBridge, true
	default:
		return "", false
	}
}

func sortRuleInfos(infos []firewall.RuleInfo) {
	sort.Slice(infos, func(i, j int) bool {
		left := ruleRefForInfo(infos[i])
		right := ruleRefForInfo(infos[j])
		if left.Family != right.Family {
			return left.Family < right.Family
		}
		if left.TableName != right.TableName {
			return left.TableName < right.TableName
		}
		if left.ChainName != right.ChainName {
			return left.ChainName < right.ChainName
		}
		if left.Purpose != right.Purpose {
			return left.Purpose < right.Purpose
		}
		if left.Owner != right.Owner {
			return left.Owner < right.Owner
		}
		return left.Handle < right.Handle
	})
}
