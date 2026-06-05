//go:build linux

package linux

import (
	"fmt"
	"net/netip"

	"github.com/google/nftables"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

func listObservedRules(h handle, filter firewall.RuleFilter) ([]firewall.RuleInfo, error) {
	details, err := listObservedRuleDetails(h, filter)
	if err != nil {
		return nil, err
	}
	infos, err := compactObservedRules(details)
	if err != nil {
		return nil, err
	}
	sortRuleInfos(infos)
	return infos, nil
}

func listObservedRuleDetails(h handle, filter firewall.RuleFilter) ([]observedRuleDetail, error) {
	tables, err := h.GetTables()
	if err != nil {
		return nil, err
	}
	chains, err := h.GetChains()
	if err != nil {
		return nil, err
	}

	tableByChain := make(map[*nftables.Chain]*nftables.Table, len(chains))
	for _, chain := range chains {
		if chain == nil || chain.Table == nil {
			continue
		}
		for _, table := range tables {
			if table == nil {
				continue
			}
			if sameTable(table, chain.Table) {
				tableByChain[chain] = table
				break
			}
		}
	}

	var details []observedRuleDetail
	for _, chain := range chains {
		table := tableByChain[chain]
		if table == nil || !tableChainMatchesFilter(table, chain, filter) {
			continue
		}

		rules, err := h.GetRules(table, chain)
		if err != nil {
			return nil, err
		}
		for _, rule := range rules {
			detail, recognized, err := observedRuleDetailFor(table, chain, rule)
			if err != nil {
				return nil, err
			}
			if !recognized || !ruleInfoMatchesFilter(detail.info, filter) {
				continue
			}
			details = append(details, detail)
		}
	}

	return details, nil
}

func compactObservedRules(details []observedRuleDetail) ([]firewall.RuleInfo, error) {
	var infos []firewall.RuleInfo
	endpointGroups := map[endpointGroupKey][]observedRuleDetail{}
	forwardGroups := map[forwardGroupKey][]observedRuleDetail{}
	for _, detail := range details {
		switch detail.info.Ref.Purpose {
		case firewall.RulePurposeEndpointAntiSpoofing:
			summary := detail.info.Summary.EndpointAntiSpoofing
			if summary == nil {
				return nil, fmt.Errorf("%w: endpoint anti-spoofing rule has no endpoint summary", firewallerr.ErrInvalidObservedState)
			}
			key := endpointGroupKey{
				owner:     detail.info.Ref.Owner,
				family:    detail.info.Ref.Family,
				tableName: detail.info.Ref.TableName,
				chainName: detail.info.Ref.ChainName,
				tapName:   summary.TapName,
			}
			endpointGroups[key] = append(endpointGroups[key], detail)
		case firewall.RulePurposeForwardAccept:
			summary := detail.info.Summary.ForwardAccept
			if summary == nil {
				return nil, fmt.Errorf("%w: forward-accept rule has no summary", firewallerr.ErrInvalidObservedState)
			}
			key := forwardGroupKey{
				owner:     detail.info.Ref.Owner,
				family:    detail.info.Ref.Family,
				tableName: detail.info.Ref.TableName,
				chainName: detail.info.Ref.ChainName,
				guestCIDR: summary.GuestCIDR,
			}
			forwardGroups[key] = append(forwardGroups[key], detail)
		default:
			infos = append(infos, detail.info)
		}
	}
	for _, group := range endpointGroups {
		info, err := logicalEndpointInfo(group)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	for _, group := range forwardGroups {
		info, err := logicalForwardInfo(group)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

type endpointGroupKey struct {
	owner     firewall.RuleOwner
	family    firewall.TableFamily
	tableName firewall.TableName
	chainName firewall.ChainName
	tapName   firewall.InterfaceName
}

type forwardGroupKey struct {
	owner     firewall.RuleOwner
	family    firewall.TableFamily
	tableName firewall.TableName
	chainName firewall.ChainName
	guestCIDR netip.Prefix
}

func logicalEndpointInfo(details []observedRuleDetail) (firewall.RuleInfo, error) {
	if len(details) == 0 {
		return firewall.RuleInfo{}, fmt.Errorf("%w: endpoint anti-spoofing group is empty", firewallerr.ErrInvalidObservedState)
	}
	base := details[0]
	baseSummary := base.info.Summary.EndpointAntiSpoofing
	if baseSummary == nil {
		return firewall.RuleInfo{}, fmt.Errorf("%w: endpoint anti-spoofing rule has no endpoint summary", firewallerr.ErrInvalidObservedState)
	}
	ref := base.info.Ref
	// GroupKey is the stable logical identity of this endpoint guard group (the
	// guarded TAP), so a group delete resolves by identity rather than by a
	// handle the kernel can renumber.
	ref.GroupKey = firewall.RuleGroupKey(baseSummary.TapName)
	merged := firewall.EndpointAntiSpoofingSummary{
		BridgeName: baseSummary.BridgeName,
		TapName:    baseSummary.TapName,
		Priority:   baseSummary.Priority,
	}
	seen := map[endpointGuardKind]bool{}
	for _, detail := range details {
		summary := detail.info.Summary.EndpointAntiSpoofing
		if summary == nil {
			return firewall.RuleInfo{}, fmt.Errorf("%w: endpoint anti-spoofing rule has no endpoint summary", firewallerr.ErrInvalidObservedState)
		}
		if detail.info.Ref.Owner != ref.Owner || detail.info.Ref.Purpose != ref.Purpose || detail.info.Ref.Family != ref.Family || detail.info.Ref.TableName != ref.TableName || detail.info.Ref.ChainName != ref.ChainName || summary.BridgeName != merged.BridgeName || summary.TapName != merged.TapName || summary.Priority != merged.Priority {
			return firewall.RuleInfo{}, fmt.Errorf("%w: endpoint anti-spoofing group has mixed identity", firewallerr.ErrInvalidObservedState)
		}
		if seen[detail.guard] {
			return firewall.RuleInfo{}, fmt.Errorf("%w: endpoint anti-spoofing group has duplicate guard %q", firewallerr.ErrInvalidObservedState, detail.guard)
		}
		seen[detail.guard] = true
		if detail.info.Ref.Handle < ref.Handle {
			ref.Handle = detail.info.Ref.Handle
		}
		switch detail.guard {
		case guardEtherMAC, guardARPMAC:
			if len(summary.MAC) != 6 {
				return firewall.RuleInfo{}, fmt.Errorf("%w: endpoint anti-spoofing MAC guard is incomplete", firewallerr.ErrInvalidObservedState)
			}
			if len(merged.MAC) == 0 {
				merged.MAC = netHardwareAddrCopy(summary.MAC)
			} else if merged.MAC.String() != summary.MAC.String() {
				return firewall.RuleInfo{}, fmt.Errorf("%w: endpoint anti-spoofing MAC guards disagree", firewallerr.ErrInvalidObservedState)
			}
		case guardIPv4, guardARPIPv4:
			if !summary.IPv4.IsValid() || !summary.IPv4.Is4() {
				return firewall.RuleInfo{}, fmt.Errorf("%w: endpoint anti-spoofing IPv4 guard is incomplete", firewallerr.ErrInvalidObservedState)
			}
			if !merged.IPv4.IsValid() {
				merged.IPv4 = summary.IPv4
			} else if merged.IPv4 != summary.IPv4 {
				return firewall.RuleInfo{}, fmt.Errorf("%w: endpoint anti-spoofing IPv4 guards disagree", firewallerr.ErrInvalidObservedState)
			}
		default:
			return firewall.RuleInfo{}, fmt.Errorf("%w: unsupported endpoint guard %q", firewallerr.ErrInvalidObservedState, detail.guard)
		}
	}
	for _, required := range []endpointGuardKind{guardEtherMAC, guardIPv4, guardARPMAC, guardARPIPv4} {
		if !seen[required] {
			return firewall.RuleInfo{}, fmt.Errorf("%w: endpoint anti-spoofing group is missing guard %q", firewallerr.ErrInvalidObservedState, required)
		}
	}
	return firewall.RuleInfo{Ref: ref, Summary: firewall.RuleSummary{EndpointAntiSpoofing: &merged}}, nil
}

func getObservedRule(h handle, query firewall.RuleQuery) (firewall.RuleInfo, error) {
	ref := query.Ref
	infos, err := listObservedRules(h, firewall.RuleFilter{
		Owner:   firewall.FilterOwner(ref.Owner),
		Purpose: firewall.FilterPurpose(ref.Purpose),
		Family:  firewall.FilterFamily(ref.Family),
		Table:   firewall.FilterTable(ref.TableName),
		Chain:   firewall.FilterChain(ref.ChainName),
	})
	if err != nil {
		return firewall.RuleInfo{}, err
	}
	for _, info := range infos {
		if ruleRefForInfo(info).Handle == ref.Handle {
			return info, nil
		}
	}
	return firewall.RuleInfo{}, fmt.Errorf("%w: %s/%s/%s handle %d", firewallerr.ErrNotFound, ref.Family, ref.TableName, ref.ChainName, ref.Handle)
}

func tableChainMatchesFilter(table *nftables.Table, chain *nftables.Chain, filter firewall.RuleFilter) bool {
	family, ok := firewallFamily(table.Family)
	if !ok {
		return false
	}
	if filter.Family.Mode == firewall.FamilyValue && family != filter.Family.Value {
		return false
	}
	if filter.Table.Mode == firewall.TableValue && firewall.TableName(table.Name) != filter.Table.Value {
		return false
	}
	if filter.Chain.Mode == firewall.ChainValue && firewall.ChainName(chain.Name) != filter.Chain.Value {
		return false
	}
	return true
}

func sameTable(left, right *nftables.Table) bool {
	return left != nil && right != nil && left.Family == right.Family && left.Name == right.Name
}
