//go:build linux

package linux

import (
	"fmt"

	"github.com/google/nftables"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

func listObservedRules(h handle, filter firewall.RuleFilter) ([]firewall.RuleInfo, error) {
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

	var infos []firewall.RuleInfo
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
			info, recognized, err := observedRuleInfo(table, chain, rule)
			if err != nil {
				return nil, err
			}
			if !recognized || !ruleInfoMatchesFilter(info, filter) {
				continue
			}
			infos = append(infos, info)
		}
	}

	sortRuleInfos(infos)
	return infos, nil
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
