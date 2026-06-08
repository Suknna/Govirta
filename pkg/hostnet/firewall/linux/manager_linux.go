//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/nftables"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
	"github.com/suknna/govirta/pkg/hostnet/firewall/firewallerr"
)

type Manager struct {
	handle handle
}

var _ firewall.Manager = (*Manager)(nil)

func NewManager() (*Manager, error) {
	h, err := newRealHandle()
	if err != nil {
		return nil, translateError("create nftables handle", err)
	}
	return NewManagerWithHandle(h), nil
}

func NewManagerWithHandle(h handle) *Manager {
	return &Manager{handle: h}
}

func (m *Manager) firewallHandle() handle {
	return m.handle
}

func (m *Manager) EnsureMasquerade(ctx context.Context, spec firewall.MasqueradeSpec) (firewall.RuleInfo, error) {
	if err := validateMasqueradeSpec(ctx, spec); err != nil {
		return firewall.RuleInfo{}, translateError("ensure masquerade", err)
	}
	return ensureDesiredRule(ctx, m.firewallHandle(), "ensure masquerade", desiredMasqueradeRule(spec))
}

func (m *Manager) DeleteMasquerade(ctx context.Context, ref firewall.RuleRef) error {
	if err := validateRuleRef(ctx, ref, firewall.RulePurposeMasquerade); err != nil {
		return translateError("delete masquerade", err)
	}
	return translateError("delete masquerade", deleteObservedRule(m.firewallHandle(), ref))
}

func (m *Manager) EnsureEndpointAntiSpoofing(ctx context.Context, spec firewall.EndpointAntiSpoofingSpec) (firewall.RuleInfo, error) {
	if err := validateEndpointAntiSpoofingSpec(ctx, spec); err != nil {
		return firewall.RuleInfo{}, translateError("ensure endpoint anti-spoofing", err)
	}
	return ensureDesiredRuleGroup(ctx, m.firewallHandle(), "ensure endpoint anti-spoofing", desiredEndpointAntiSpoofingRules(spec))
}

func (m *Manager) DeleteEndpointAntiSpoofing(ctx context.Context, ref firewall.RuleRef) error {
	if err := validateGroupDeleteRef(ctx, ref, firewall.RulePurposeEndpointAntiSpoofing); err != nil {
		return translateError("delete endpoint anti-spoofing", err)
	}
	return translateError("delete endpoint anti-spoofing", deleteObservedEndpointGroup(m.firewallHandle(), ref))
}

func (m *Manager) EnsureForwardAccept(ctx context.Context, spec firewall.ForwardAcceptSpec) (firewall.RuleInfo, error) {
	if err := validateForwardAcceptSpec(ctx, spec); err != nil {
		return firewall.RuleInfo{}, translateError("ensure forward-accept", err)
	}
	return ensureDesiredForwardGroup(ctx, m.firewallHandle(), "ensure forward-accept", desiredForwardAcceptRules(spec))
}

func (m *Manager) DeleteForwardAccept(ctx context.Context, ref firewall.RuleRef) error {
	if err := validateGroupDeleteRef(ctx, ref, firewall.RulePurposeForwardAccept); err != nil {
		return translateError("delete forward-accept", err)
	}
	return translateError("delete forward-accept", deleteObservedForwardGroup(m.firewallHandle(), ref))
}

func (m *Manager) GetRule(ctx context.Context, query firewall.RuleQuery) (firewall.RuleInfo, error) {
	if err := validateRuleQuery(ctx, query); err != nil {
		return firewall.RuleInfo{}, translateError("get firewall rule", err)
	}
	info, err := getObservedRule(m.firewallHandle(), query)
	if err != nil {
		return firewall.RuleInfo{}, translateError("get firewall rule", err)
	}
	return info, nil
}

func deleteObservedEndpointGroup(h handle, ref firewall.RuleRef) error {
	details, err := listObservedRuleDetails(h, firewall.RuleFilter{
		Owner:   firewall.FilterOwner(ref.Owner),
		Purpose: firewall.FilterPurpose(ref.Purpose),
		Family:  firewall.FilterFamily(ref.Family),
		Table:   firewall.FilterTable(ref.TableName),
		Chain:   firewall.FilterChain(ref.ChainName),
	})
	if err != nil {
		return err
	}
	groups := map[firewall.InterfaceName][]observedRuleDetail{}
	for _, detail := range details {
		if detail.info.Summary.EndpointAntiSpoofing == nil {
			continue
		}
		groups[detail.info.Summary.EndpointAntiSpoofing.TapName] = append(groups[detail.info.Summary.EndpointAntiSpoofing.TapName], detail)
	}

	// Resolve the group by its stable logical key (the guarded TAP) rather than
	// by lowest-handle equality: the kernel can renumber handles and an
	// out-of-band rule change can shift which handle is lowest, but the TAP
	// identity is stable, so a group delete stays correct under either.
	selected := groups[firewall.InterfaceName(ref.GroupKey)]
	if len(selected) == 0 {
		// Group already gone: still reclaim the now-empty dedicated chain so a
		// retried delete (rules deleted on a prior pass) leaves no empty chain.
		return deleteChainIfEmpty(h, ref.Family, ref.TableName, ref.ChainName)
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

	details, err = listObservedRuleDetails(h, firewall.RuleFilter{
		Owner:   firewall.FilterOwner(ref.Owner),
		Purpose: firewall.FilterPurpose(ref.Purpose),
		Family:  firewall.FilterFamily(ref.Family),
		Table:   firewall.FilterTable(ref.TableName),
		Chain:   firewall.FilterChain(ref.ChainName),
	})
	if err != nil {
		return err
	}
	for _, detail := range details {
		for _, deleted := range selected {
			if detail.info.Ref.Handle == deleted.info.Ref.Handle {
				return fmt.Errorf("%w: deleted endpoint anti-spoofing guard still observed", firewallerr.ErrConflict)
			}
		}
	}
	// Rules confirmed gone: reclaim the dedicated chain if now empty.
	return deleteChainIfEmpty(h, ref.Family, ref.TableName, ref.ChainName)
}

func (m *Manager) ListRules(ctx context.Context, filter firewall.RuleFilter) ([]firewall.RuleInfo, error) {
	if err := validateRuleFilter(ctx, filter); err != nil {
		return nil, translateError("list firewall rules", err)
	}
	infos, err := listObservedRules(m.firewallHandle(), filter)
	if err != nil {
		return nil, translateError("list firewall rules", err)
	}
	return infos, nil
}

func deleteObservedRule(h handle, ref firewall.RuleRef) error {
	if _, err := getObservedRule(h, firewall.RuleQuery{Ref: ref}); err != nil {
		if errors.Is(err, firewallerr.ErrNotFound) {
			// Rule already gone: still reclaim the now-empty dedicated chain so a
			// retried delete (rule deleted on a prior pass) leaves no empty chain.
			return deleteChainIfEmpty(h, ref.Family, ref.TableName, ref.ChainName)
		}
		return err
	}

	table := &nftables.Table{Family: nftFamily(ref.Family), Name: string(ref.TableName)}
	chain := &nftables.Chain{Table: table, Name: string(ref.ChainName)}
	if err := h.DelRule(&nftables.Rule{Table: table, Chain: chain, Handle: uint64(ref.Handle)}); err != nil {
		return err
	}
	if err := h.Flush(); err != nil {
		return err
	}

	if _, err := getObservedRule(h, firewall.RuleQuery{Ref: ref}); err != nil {
		if errors.Is(err, firewallerr.ErrNotFound) {
			// Rule confirmed gone: reclaim the dedicated chain if now empty.
			return deleteChainIfEmpty(h, ref.Family, ref.TableName, ref.ChainName)
		}
		return err
	}
	return fmt.Errorf("%w: deleted firewall rule still observed", firewallerr.ErrConflict)
}

// deleteChainIfEmpty removes a Govirta-owned per-resource base chain once its
// last rule is gone, so a torn-down network/NIC leaves no empty
// gv-masq-/gv-fwd-/gv-as- chain behind (上下一致: Ensure* creates the dedicated
// chain, Delete* must reclaim it). It is deliberately conservative and
// idempotent:
//
//   - chain absent → nothing to do (idempotent on a re-driven delete).
//   - chain still holds ANY rule → left untouched. This guards the project iron
//     rule that the firewall layer never removes a chain that still carries
//     rules; only a verified-empty dedicated chain is reclaimed, so an
//     out-of-band or not-yet-deleted rule always prevents chain removal.
//
// It must be called only for the per-resource base chains the firewall layer
// itself creates, never for shared/built-in chains.
func deleteChainIfEmpty(h handle, family firewall.TableFamily, tableName firewall.TableName, chainName firewall.ChainName) error {
	chains, err := h.GetChains()
	if err != nil {
		return err
	}
	var target *nftables.Chain
	for _, chain := range chains {
		if chain != nil && chain.Table != nil &&
			chain.Table.Family == nftFamily(family) && chain.Table.Name == string(tableName) &&
			chain.Name == string(chainName) {
			target = chain
			break
		}
	}
	if target == nil {
		// Chain already gone: nothing to reclaim (idempotent).
		return nil
	}

	rules, err := h.GetRules(target.Table, target)
	if err != nil {
		return err
	}
	if len(rules) > 0 {
		// Chain still carries rules: never remove a non-empty chain.
		return nil
	}

	h.DelChain(target)
	return h.Flush()
}
