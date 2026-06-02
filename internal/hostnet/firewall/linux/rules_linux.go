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
	guard     endpointGuardKind
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
		guard:     guardMasquerade,
		priority:  spec.Priority,
		summary: firewall.RuleSummary{Masquerade: &firewall.MasqueradeSummary{
			GuestCIDR:           spec.GuestCIDR.Masked(),
			EgressInterfaceName: spec.EgressInterfaceName,
			Priority:            spec.Priority,
		}},
	}
}

func desiredEndpointAntiSpoofingRules(spec firewall.EndpointAntiSpoofingSpec) []desiredRule {
	summary := firewall.RuleSummary{EndpointAntiSpoofing: &firewall.EndpointAntiSpoofingSummary{
		BridgeName: spec.BridgeName,
		TapName:    spec.TapName,
		MAC:        netHardwareAddrCopy(spec.MAC),
		IPv4:       spec.IPv4,
		Priority:   spec.Priority,
	}}
	guards := []endpointGuardKind{guardEtherMAC, guardIPv4, guardARPMAC, guardARPIPv4}
	desired := make([]desiredRule, 0, len(guards))
	for _, guard := range guards {
		desired = append(desired, desiredRule{
			family:    firewall.TableFamilyBridge,
			tableName: spec.TableName,
			chainName: spec.ChainName,
			purpose:   firewall.RulePurposeEndpointAntiSpoofing,
			owner:     spec.RuleOwner,
			guard:     guard,
			priority:  spec.Priority,
			summary:   summary,
		})
	}
	return desired
}

func ensureDesiredRule(ctx context.Context, h handle, operation string, desired desiredRule) (firewall.RuleInfo, error) {
	if err := checkContext(ctx); err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	infos, err := listObservedRules(h, desiredRuleFilter(desired))
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	// The Govirta-owned rule set for this owner/purpose/family/table/chain is
	// the single source of truth. More than one managed rule for the same
	// identity is inconsistent state that this ensure path must not silently
	// reconcile (resolving duplicates is an explicit delete-path responsibility).
	switch len(infos) {
	case 0:
		// No managed rule yet; fall through to creation below.
	case 1:
		if ruleSummaryMatchesDesired(infos[0].Summary, desired.summary) {
			return infos[0], nil
		}
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: existing Govirta rule differs from requested state", firewallerr.ErrConflict))
	default:
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: %d Govirta rules already exist for this identity", firewallerr.ErrConflict, len(infos)))
	}

	table, err := ensureTable(h, desired)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	chain, err := ensureChain(h, table, desired)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	exprs, err := ruleExprs(desired)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	userData, err := ruleUserDataForDesired(desired)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	h.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: exprs, UserData: userData})
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

func ensureDesiredRuleGroup(ctx context.Context, h handle, operation string, desired []desiredRule) (firewall.RuleInfo, error) {
	if err := checkContext(ctx); err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	if len(desired) == 0 {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: desired firewall rule group must be non-empty", firewallerr.ErrInvalidRequest))
	}
	base := desired[0]
	if err := validateDesiredEndpointGroup(desired); err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	details, err := listObservedRuleDetails(h, desiredRuleFilter(base))
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	matching, err := matchingEndpointDetails(details, base)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	existingByGuard := map[endpointGuardKind]observedRuleDetail{}
	for _, detail := range matching {
		if _, exists := existingByGuard[detail.guard]; exists {
			return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: duplicate endpoint guard %q exists for TAP %q", firewallerr.ErrConflict, detail.guard, base.summary.EndpointAntiSpoofing.TapName))
		}
		if !endpointGuardMatchesDesired(detail, base) {
			return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: existing endpoint guard differs from requested state", firewallerr.ErrConflict))
		}
		existingByGuard[detail.guard] = detail
	}
	if len(existingByGuard) == len(desired) {
		info, err := logicalEndpointInfo(matching)
		return info, translateError(operation, err)
	}

	table, err := ensureTable(h, base)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	chain, err := ensureChain(h, table, base)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	for _, rule := range desired {
		if _, exists := existingByGuard[rule.guard]; exists {
			continue
		}
		exprs, err := ruleExprs(rule)
		if err != nil {
			return firewall.RuleInfo{}, translateError(operation, err)
		}
		userData, err := ruleUserDataForDesired(rule)
		if err != nil {
			return firewall.RuleInfo{}, translateError(operation, err)
		}
		h.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: exprs, UserData: userData})
	}
	if err := h.Flush(); err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	details, err = listObservedRuleDetails(h, desiredRuleFilter(base))
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: %w", firewallerr.ErrInvalidObservedState, err))
	}
	matching, err = matchingEndpointDetails(details, base)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: %w", firewallerr.ErrInvalidObservedState, err))
	}
	info, err := logicalEndpointInfo(matching)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: %w", firewallerr.ErrInvalidObservedState, err))
	}
	if !ruleSummaryMatchesDesired(info.Summary, base.summary) {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: ensured endpoint anti-spoofing group differs from requested state", firewallerr.ErrInvalidObservedState))
	}
	return info, nil
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
	if desired.EndpointAntiSpoofing != nil {
		return observed.EndpointAntiSpoofing != nil &&
			observed.EndpointAntiSpoofing.BridgeName == desired.EndpointAntiSpoofing.BridgeName &&
			observed.EndpointAntiSpoofing.TapName == desired.EndpointAntiSpoofing.TapName &&
			observed.EndpointAntiSpoofing.IPv4 == desired.EndpointAntiSpoofing.IPv4 &&
			observed.EndpointAntiSpoofing.Priority == desired.EndpointAntiSpoofing.Priority &&
			observed.EndpointAntiSpoofing.MAC.String() == desired.EndpointAntiSpoofing.MAC.String()
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
			if err := validateExistingChain(chain, desired); err != nil {
				return nil, err
			}
			return chain, nil
		}
	}
	chain, err := chainForDesired(table, desired)
	if err != nil {
		return nil, err
	}
	return h.AddChain(chain), nil
}

// validateExistingChain rejects reuse of a chain that shares the desired name
// but is not an effective base chain for the desired purpose. The ensure path
// must not silently attach a managed rule to a chain with the wrong type, hook,
// or priority.
func validateExistingChain(chain *nftables.Chain, desired desiredRule) error {
	switch desired.purpose {
	case firewall.RulePurposeMasquerade:
		if chain.Type != nftables.ChainTypeNAT {
			return fmt.Errorf("%w: existing chain %q is not a NAT chain", firewallerr.ErrConflict, chain.Name)
		}
		if chain.Hooknum == nil || *chain.Hooknum != *nftables.ChainHookPostrouting {
			return fmt.Errorf("%w: existing chain %q is not hooked at postrouting", firewallerr.ErrConflict, chain.Name)
		}
		if chain.Priority == nil || int(*chain.Priority) != desired.priority.Value {
			return fmt.Errorf("%w: existing chain %q priority does not match requested srcnat priority", firewallerr.ErrConflict, chain.Name)
		}
		return nil
	case firewall.RulePurposeEndpointAntiSpoofing:
		if chain.Type != nftables.ChainTypeFilter {
			return fmt.Errorf("%w: existing chain %q is not a filter chain", firewallerr.ErrConflict, chain.Name)
		}
		if chain.Hooknum == nil || *chain.Hooknum != *nftables.ChainHookForward {
			return fmt.Errorf("%w: existing chain %q is not hooked at forward", firewallerr.ErrConflict, chain.Name)
		}
		if chain.Priority == nil || int(*chain.Priority) != desired.priority.Value {
			return fmt.Errorf("%w: existing chain %q priority does not match requested bridge filter priority", firewallerr.ErrConflict, chain.Name)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported desired chain purpose %q", firewallerr.ErrConflict, desired.purpose)
	}
}

func chainForDesired(table *nftables.Table, desired desiredRule) (*nftables.Chain, error) {
	priority := nftables.ChainPriority(desired.priority.Value)
	chain := &nftables.Chain{Table: table, Name: string(desired.chainName), Priority: &priority}
	switch desired.purpose {
	case firewall.RulePurposeMasquerade:
		chain.Type = nftables.ChainTypeNAT
		chain.Hooknum = nftables.ChainHookPostrouting
		return chain, nil
	case firewall.RulePurposeEndpointAntiSpoofing:
		chain.Type = nftables.ChainTypeFilter
		chain.Hooknum = nftables.ChainHookForward
		return chain, nil
	default:
		return nil, fmt.Errorf("%w: unsupported desired chain purpose %q", firewallerr.ErrUnsupported, desired.purpose)
	}
}

func ruleExprs(desired desiredRule) ([]expr.Any, error) {
	switch desired.purpose {
	case firewall.RulePurposeMasquerade:
		if desired.guard != guardMasquerade || desired.summary.Masquerade == nil {
			return nil, fmt.Errorf("%w: invalid masquerade desired rule", firewallerr.ErrInvalidRequest)
		}
		return masqueradeExprs(*desired.summary.Masquerade), nil
	case firewall.RulePurposeEndpointAntiSpoofing:
		if desired.summary.EndpointAntiSpoofing == nil {
			return nil, fmt.Errorf("%w: invalid endpoint anti-spoofing desired rule", firewallerr.ErrInvalidRequest)
		}
		summary := *desired.summary.EndpointAntiSpoofing
		switch desired.guard {
		case guardEtherMAC:
			return endpointEtherMACDropExprs(summary), nil
		case guardIPv4:
			return endpointIPv4DropExprs(summary), nil
		case guardARPMAC:
			return endpointARPMACDropExprs(summary), nil
		case guardARPIPv4:
			return endpointARPIPv4DropExprs(summary), nil
		default:
			return nil, fmt.Errorf("%w: unsupported endpoint guard %q", firewallerr.ErrUnsupported, desired.guard)
		}
	default:
		return nil, fmt.Errorf("%w: unsupported rule purpose %q", firewallerr.ErrUnsupported, desired.purpose)
	}
}

func ruleUserDataForDesired(desired desiredRule) ([]byte, error) {
	switch desired.purpose {
	case firewall.RulePurposeMasquerade:
		if desired.guard != guardMasquerade {
			return nil, fmt.Errorf("%w: invalid masquerade guard %q", firewallerr.ErrInvalidRequest, desired.guard)
		}
		return userDataForRule(desired.owner, desired.purpose, guardMasquerade), nil
	case firewall.RulePurposeEndpointAntiSpoofing:
		switch desired.guard {
		case guardEtherMAC, guardIPv4, guardARPMAC, guardARPIPv4:
			return userDataForRule(desired.owner, desired.purpose, desired.guard), nil
		default:
			return nil, fmt.Errorf("%w: unsupported endpoint guard %q", firewallerr.ErrUnsupported, desired.guard)
		}
	default:
		return nil, fmt.Errorf("%w: unsupported rule purpose %q", firewallerr.ErrUnsupported, desired.purpose)
	}
}

func validateDesiredEndpointGroup(desired []desiredRule) error {
	base := desired[0]
	if base.purpose != firewall.RulePurposeEndpointAntiSpoofing || base.summary.EndpointAntiSpoofing == nil {
		return fmt.Errorf("%w: desired group must contain endpoint anti-spoofing rules", firewallerr.ErrInvalidRequest)
	}
	seen := map[endpointGuardKind]bool{}
	for _, rule := range desired {
		if rule.family != base.family || rule.tableName != base.tableName || rule.chainName != base.chainName || rule.purpose != base.purpose || rule.owner != base.owner || rule.priority != base.priority || !ruleSummaryMatchesDesired(rule.summary, base.summary) {
			return fmt.Errorf("%w: desired endpoint group contains mixed identities", firewallerr.ErrInvalidRequest)
		}
		switch rule.guard {
		case guardEtherMAC, guardIPv4, guardARPMAC, guardARPIPv4:
			if seen[rule.guard] {
				return fmt.Errorf("%w: desired endpoint group contains duplicate guard %q", firewallerr.ErrInvalidRequest, rule.guard)
			}
			seen[rule.guard] = true
		default:
			return fmt.Errorf("%w: unsupported endpoint guard %q", firewallerr.ErrUnsupported, rule.guard)
		}
	}
	if len(seen) != 4 {
		return fmt.Errorf("%w: desired endpoint group must contain four guards", firewallerr.ErrInvalidRequest)
	}
	return nil
}

func matchingEndpointDetails(details []observedRuleDetail, desired desiredRule) ([]observedRuleDetail, error) {
	var matching []observedRuleDetail
	desiredEndpoint := desired.summary.EndpointAntiSpoofing
	for _, detail := range details {
		if detail.info.Ref.Purpose != firewall.RulePurposeEndpointAntiSpoofing || detail.info.Summary.EndpointAntiSpoofing == nil {
			continue
		}
		observed := detail.info.Summary.EndpointAntiSpoofing
		if observed.TapName != desiredEndpoint.TapName {
			continue
		}
		if observed.BridgeName != desiredEndpoint.BridgeName || observed.Priority != desiredEndpoint.Priority {
			return nil, fmt.Errorf("%w: existing endpoint guard has different bridge or priority", firewallerr.ErrConflict)
		}
		matching = append(matching, detail)
	}
	return matching, nil
}

func endpointGuardMatchesDesired(detail observedRuleDetail, desired desiredRule) bool {
	observed := detail.info.Summary.EndpointAntiSpoofing
	want := desired.summary.EndpointAntiSpoofing
	if observed == nil || want == nil || observed.BridgeName != want.BridgeName || observed.TapName != want.TapName || observed.Priority != want.Priority {
		return false
	}
	switch detail.guard {
	case guardEtherMAC, guardARPMAC:
		return observed.MAC.String() == want.MAC.String()
	case guardIPv4, guardARPIPv4:
		return observed.IPv4 == want.IPv4
	default:
		return false
	}
}

func netHardwareAddrCopy(addr []byte) []byte {
	return append([]byte(nil), addr...)
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
