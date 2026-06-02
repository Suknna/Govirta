//go:build linux

package linux

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

type desiredRule struct {
	family    firewall.TableFamily
	tableName firewall.TableName
	chainName firewall.ChainName
	purpose   firewall.RulePurpose
	owner     firewall.RuleOwner
	priority  firewall.Priority
	summary   firewall.RuleSummary
}

func desiredMasqueradeRule(spec firewall.MasqueradeSpec) desiredRule {
	return desiredRule{
		family:    firewall.TableFamilyIPv4,
		tableName: spec.TableName,
		chainName: spec.ChainName,
		purpose:   firewall.RulePurposeMasquerade,
		owner:     spec.RuleOwner,
		priority:  spec.Priority,
		summary: firewall.RuleSummary{Masquerade: &firewall.MasqueradeSummary{
			GuestCIDR:           spec.GuestCIDR.Masked(),
			EgressInterfaceName: spec.EgressInterfaceName,
			Priority:            spec.Priority,
		}},
	}
}

func ensureDesiredRule(ctx context.Context, h handle, operation string, desired desiredRule) (firewall.RuleInfo, error) {
	if err := checkContext(ctx); err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	infos, err := listObservedRules(h, desiredRuleFilter(desired))
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	if info, ok := findEquivalentRule(infos, desired); ok {
		return info, nil
	}
	if len(infos) > 0 {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: existing Govirta rule differs from requested state", firewallerr.ErrConflict))
	}

	table, err := ensureTable(h, desired)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	chain, err := ensureChain(h, table, desired)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	h.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: ruleExprs(desired), UserData: ruleUserDataForDesired(desired)})
	if err := h.Flush(); err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	infos, err = listObservedRules(h, desiredRuleFilter(desired))
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: %w", firewallerr.ErrInvalidObservedState, err))
	}
	if info, ok := findEquivalentRule(infos, desired); ok {
		return info, nil
	}
	return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: ensured firewall rule was not observed", firewallerr.ErrInvalidObservedState))
}

func desiredRuleFilter(desired desiredRule) firewall.RuleFilter {
	return firewall.RuleFilter{
		Owner:   firewall.FilterOwner(desired.owner),
		Purpose: firewall.FilterPurpose(desired.purpose),
		Family:  firewall.FilterFamily(desired.family),
		Table:   firewall.FilterTable(desired.tableName),
		Chain:   firewall.FilterChain(desired.chainName),
	}
}

func findEquivalentRule(infos []firewall.RuleInfo, desired desiredRule) (firewall.RuleInfo, bool) {
	for _, info := range infos {
		if ruleSummaryMatchesDesired(info.Summary, desired.summary) {
			return info, true
		}
	}
	return firewall.RuleInfo{}, false
}

func ruleSummaryMatchesDesired(observed firewall.RuleSummary, desired firewall.RuleSummary) bool {
	if desired.Masquerade != nil {
		return observed.Masquerade != nil &&
			observed.Masquerade.GuestCIDR == desired.Masquerade.GuestCIDR &&
			observed.Masquerade.EgressInterfaceName == desired.Masquerade.EgressInterfaceName &&
			observed.Masquerade.Priority == desired.Masquerade.Priority
	}
	return false
}

func ensureTable(h handle, desired desiredRule) (*nftables.Table, error) {
	tables, err := h.GetTables()
	if err != nil {
		return nil, err
	}
	for _, table := range tables {
		if table != nil && table.Family == nftFamily(desired.family) && table.Name == string(desired.tableName) {
			return table, nil
		}
	}
	return h.AddTable(&nftables.Table{Family: nftFamily(desired.family), Name: string(desired.tableName)}), nil
}

func ensureChain(h handle, table *nftables.Table, desired desiredRule) (*nftables.Chain, error) {
	chains, err := h.GetChains()
	if err != nil {
		return nil, err
	}
	for _, chain := range chains {
		if chain != nil && sameTable(chain.Table, table) && chain.Name == string(desired.chainName) {
			return chain, nil
		}
	}
	return h.AddChain(chainForDesired(table, desired)), nil
}

func chainForDesired(table *nftables.Table, desired desiredRule) *nftables.Chain {
	priority := nftables.ChainPriority(desired.priority.Value)
	chain := &nftables.Chain{Table: table, Name: string(desired.chainName), Priority: &priority}
	switch desired.purpose {
	case firewall.RulePurposeMasquerade:
		chain.Type = nftables.ChainTypeNAT
		chain.Hooknum = nftables.ChainHookPostrouting
	}
	return chain
}

func ruleExprs(desired desiredRule) []expr.Any {
	switch desired.purpose {
	case firewall.RulePurposeMasquerade:
		return masqueradeExprs(*desired.summary.Masquerade)
	default:
		return nil
	}
}

func ruleUserDataForDesired(desired desiredRule) []byte {
	switch desired.purpose {
	case firewall.RulePurposeMasquerade:
		return userDataForRule(desired.owner, desired.purpose, guardMasquerade)
	default:
		return nil
	}
}

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
