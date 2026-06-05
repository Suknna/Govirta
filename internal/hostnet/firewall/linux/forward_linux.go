//go:build linux

package linux

import (
	"context"
	"fmt"

	"github.com/google/nftables"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

// forward-accept rule-group guards. Each guard maps to exactly one rule in the
// two-rule forward-accept group, mirroring the anti-spoofing guard mechanism.
const (
	guardForwardEgress endpointGuardKind = "forward-egress"
	guardForwardReturn endpointGuardKind = "forward-return"
)

// desiredForwardAcceptRules expands a ForwardAcceptSpec into the two-rule group
// (egress accept + conntrack established/related return accept).
func desiredForwardAcceptRules(spec firewall.ForwardAcceptSpec) []desiredRule {
	summary := firewall.RuleSummary{ForwardAccept: &firewall.ForwardAcceptSummary{
		GuestCIDR:           spec.GuestCIDR.Masked(),
		EgressInterfaceName: spec.EgressInterfaceName,
		Priority:            spec.Priority,
	}}
	guards := []endpointGuardKind{guardForwardEgress, guardForwardReturn}
	desired := make([]desiredRule, 0, len(guards))
	for _, guard := range guards {
		desired = append(desired, desiredRule{
			family:    firewall.TableFamilyIPv4,
			tableName: spec.TableName,
			chainName: spec.ChainName,
			purpose:   firewall.RulePurposeForwardAccept,
			owner:     spec.RuleOwner,
			guard:     guard,
			priority:  spec.Priority,
			summary:   summary,
		})
	}
	return desired
}

// ensureDesiredForwardGroup reconciles the forward-accept two-rule group. It
// mirrors ensureDesiredRuleGroup but groups by guest CIDR + egress rather than
// by TAP name.
func ensureDesiredForwardGroup(ctx context.Context, h handle, operation string, desired []desiredRule) (firewall.RuleInfo, error) {
	if err := checkContext(ctx); err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	if len(desired) == 0 {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: desired forward-accept group must be non-empty", firewallerr.ErrInvalidRequest))
	}
	base := desired[0]
	if err := validateDesiredForwardGroup(desired); err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	details, err := listObservedRuleDetails(h, desiredRuleFilter(base))
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}
	matching, err := matchingForwardDetails(details, base)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, err)
	}

	existingByGuard := map[endpointGuardKind]observedRuleDetail{}
	for _, detail := range matching {
		if _, exists := existingByGuard[detail.guard]; exists {
			return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: duplicate forward-accept guard %q exists for guest CIDR %s", firewallerr.ErrConflict, detail.guard, base.summary.ForwardAccept.GuestCIDR))
		}
		if !forwardGuardMatchesDesired(detail, base) {
			return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: existing forward-accept guard differs from requested state", firewallerr.ErrConflict))
		}
		existingByGuard[detail.guard] = detail
	}
	if len(existingByGuard) == len(desired) {
		info, err := logicalForwardInfo(matching)
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
	matching, err = matchingForwardDetails(details, base)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: %w", firewallerr.ErrInvalidObservedState, err))
	}
	info, err := logicalForwardInfo(matching)
	if err != nil {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: %w", firewallerr.ErrInvalidObservedState, err))
	}
	if !ruleSummaryMatchesDesired(info.Summary, base.summary) {
		return firewall.RuleInfo{}, translateError(operation, fmt.Errorf("%w: ensured forward-accept group differs from requested state", firewallerr.ErrInvalidObservedState))
	}
	return info, nil
}

// deleteObservedForwardGroup removes the forward-accept group whose stable
// GroupKey (the guest CIDR) equals ref.GroupKey, mirroring
// deleteObservedEndpointGroup.
func deleteObservedForwardGroup(h handle, ref firewall.RuleRef) error {
	filter := firewall.RuleFilter{
		Owner:   firewall.FilterOwner(ref.Owner),
		Purpose: firewall.FilterPurpose(ref.Purpose),
		Family:  firewall.FilterFamily(ref.Family),
		Table:   firewall.FilterTable(ref.TableName),
		Chain:   firewall.FilterChain(ref.ChainName),
	}
	details, err := listObservedRuleDetails(h, filter)
	if err != nil {
		return err
	}
	groups := map[firewall.RuleGroupKey][]observedRuleDetail{}
	for _, detail := range details {
		if detail.info.Summary.ForwardAccept == nil {
			continue
		}
		key := firewall.RuleGroupKey(detail.info.Summary.ForwardAccept.GuestCIDR.String())
		groups[key] = append(groups[key], detail)
	}

	// Resolve the group by its stable logical key (the guest CIDR) rather than
	// by lowest-handle equality: the kernel can renumber handles and an
	// out-of-band rule change can shift which handle is lowest, but the guest
	// CIDR identity is stable, so a group delete stays correct under either.
	selected := groups[ref.GroupKey]
	if len(selected) == 0 {
		return nil
	}

	table := &nftables.Table{Family: nftFamily(ref.Family), Name: string(ref.TableName)}
	chain := &nftables.Chain{Table: table, Name: string(ref.ChainName)}
	for _, detail := range selected {
		if err := h.DelRule(&nftables.Rule{Table: table, Chain: chain, Handle: uint64(detail.info.Ref.Handle)}); err != nil {
			return err
		}
	}
	if err := h.Flush(); err != nil {
		return err
	}

	details, err = listObservedRuleDetails(h, filter)
	if err != nil {
		return err
	}
	for _, detail := range details {
		for _, deleted := range selected {
			if detail.info.Ref.Handle == deleted.info.Ref.Handle {
				return fmt.Errorf("%w: deleted forward-accept guard still observed", firewallerr.ErrConflict)
			}
		}
	}
	return nil
}

// matchingForwardDetails selects observed forward-accept rules whose guest CIDR
// matches the desired group, rejecting egress/priority conflicts under the same
// guest CIDR.
func matchingForwardDetails(details []observedRuleDetail, desired desiredRule) ([]observedRuleDetail, error) {
	var matching []observedRuleDetail
	want := desired.summary.ForwardAccept
	for _, detail := range details {
		if detail.info.Ref.Purpose != firewall.RulePurposeForwardAccept || detail.info.Summary.ForwardAccept == nil {
			continue
		}
		observed := detail.info.Summary.ForwardAccept
		if observed.GuestCIDR != want.GuestCIDR {
			continue
		}
		if observed.EgressInterfaceName != want.EgressInterfaceName || observed.Priority != want.Priority {
			return nil, fmt.Errorf("%w: existing forward-accept guard has different egress or priority", firewallerr.ErrConflict)
		}
		matching = append(matching, detail)
	}
	return matching, nil
}

func forwardGuardMatchesDesired(detail observedRuleDetail, desired desiredRule) bool {
	observed := detail.info.Summary.ForwardAccept
	want := desired.summary.ForwardAccept
	if observed == nil || want == nil {
		return false
	}
	return observed.GuestCIDR == want.GuestCIDR &&
		observed.EgressInterfaceName == want.EgressInterfaceName &&
		observed.Priority == want.Priority
}

func validateDesiredForwardGroup(desired []desiredRule) error {
	base := desired[0]
	if base.purpose != firewall.RulePurposeForwardAccept || base.summary.ForwardAccept == nil {
		return fmt.Errorf("%w: desired group must contain forward-accept rules", firewallerr.ErrInvalidRequest)
	}
	seen := map[endpointGuardKind]bool{}
	for _, rule := range desired {
		if rule.family != base.family || rule.tableName != base.tableName || rule.chainName != base.chainName || rule.purpose != base.purpose || rule.owner != base.owner || rule.priority != base.priority || !ruleSummaryMatchesDesired(rule.summary, base.summary) {
			return fmt.Errorf("%w: desired forward-accept group contains mixed identities", firewallerr.ErrInvalidRequest)
		}
		switch rule.guard {
		case guardForwardEgress, guardForwardReturn:
			if seen[rule.guard] {
				return fmt.Errorf("%w: desired forward-accept group contains duplicate guard %q", firewallerr.ErrInvalidRequest, rule.guard)
			}
			seen[rule.guard] = true
		default:
			return fmt.Errorf("%w: unsupported forward-accept guard %q", firewallerr.ErrUnsupported, rule.guard)
		}
	}
	if len(seen) != 2 {
		return fmt.Errorf("%w: desired forward-accept group must contain two guards", firewallerr.ErrInvalidRequest)
	}
	return nil
}

// logicalForwardInfo compacts the observed forward-accept guard rules into a
// single logical RuleInfo whose Ref.Handle is the lowest guard handle and whose
// Ref.GroupKey (the guest CIDR) is the stable delete selector, mirroring
// logicalEndpointInfo. It enforces the group invariants: no duplicate guards,
// consistent identity across every grouped rule, and presence of both
// forward-accept guards before returning success.
func logicalForwardInfo(details []observedRuleDetail) (firewall.RuleInfo, error) {
	if len(details) == 0 {
		return firewall.RuleInfo{}, fmt.Errorf("%w: forward-accept group is empty", firewallerr.ErrInvalidObservedState)
	}
	base := details[0]
	baseSummary := base.info.Summary.ForwardAccept
	if baseSummary == nil {
		return firewall.RuleInfo{}, fmt.Errorf("%w: forward-accept rule has no summary", firewallerr.ErrInvalidObservedState)
	}
	ref := base.info.Ref
	// GroupKey is the stable logical identity of this forward-accept group (the
	// guest CIDR), so a group delete resolves by identity rather than by a
	// handle the kernel can renumber.
	ref.GroupKey = firewall.RuleGroupKey(baseSummary.GuestCIDR.String())
	merged := firewall.ForwardAcceptSummary{
		GuestCIDR:           baseSummary.GuestCIDR,
		EgressInterfaceName: baseSummary.EgressInterfaceName,
		Priority:            baseSummary.Priority,
	}
	seen := map[endpointGuardKind]bool{}
	for _, detail := range details {
		summary := detail.info.Summary.ForwardAccept
		if summary == nil {
			return firewall.RuleInfo{}, fmt.Errorf("%w: forward-accept rule has no summary", firewallerr.ErrInvalidObservedState)
		}
		if detail.info.Ref.Owner != ref.Owner || detail.info.Ref.Purpose != ref.Purpose || detail.info.Ref.Family != ref.Family || detail.info.Ref.TableName != ref.TableName || detail.info.Ref.ChainName != ref.ChainName || summary.GuestCIDR != merged.GuestCIDR || summary.EgressInterfaceName != merged.EgressInterfaceName || summary.Priority != merged.Priority {
			return firewall.RuleInfo{}, fmt.Errorf("%w: forward-accept group has mixed identity", firewallerr.ErrInvalidObservedState)
		}
		if seen[detail.guard] {
			return firewall.RuleInfo{}, fmt.Errorf("%w: forward-accept group has duplicate guard %q", firewallerr.ErrInvalidObservedState, detail.guard)
		}
		seen[detail.guard] = true
		if detail.info.Ref.Handle < ref.Handle {
			ref.Handle = detail.info.Ref.Handle
		}
		switch detail.guard {
		case guardForwardEgress, guardForwardReturn:
		default:
			return firewall.RuleInfo{}, fmt.Errorf("%w: unsupported forward-accept guard %q", firewallerr.ErrInvalidObservedState, detail.guard)
		}
	}
	for _, required := range []endpointGuardKind{guardForwardEgress, guardForwardReturn} {
		if !seen[required] {
			return firewall.RuleInfo{}, fmt.Errorf("%w: forward-accept group is missing guard %q", firewallerr.ErrInvalidObservedState, required)
		}
	}
	return firewall.RuleInfo{Ref: ref, Summary: firewall.RuleSummary{ForwardAccept: &merged}}, nil
}
