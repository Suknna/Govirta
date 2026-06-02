//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/nftables"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
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
	if err := validateRuleRef(ctx, ref, firewall.RulePurposeEndpointAntiSpoofing); err != nil {
		return translateError("delete endpoint anti-spoofing", err)
	}
	return translateError("delete endpoint anti-spoofing", deleteObservedEndpointGroup(m.firewallHandle(), ref))
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

	var selected []observedRuleDetail
	for _, group := range groups {
		if endpointGroupLowestHandle(group) == ref.Handle {
			selected = group
			break
		}
	}
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
	return nil
}

func endpointGroupLowestHandle(details []observedRuleDetail) firewall.RuleHandle {
	var lowest firewall.RuleHandle
	for _, detail := range details {
		if lowest == 0 || detail.info.Ref.Handle < lowest {
			lowest = detail.info.Ref.Handle
		}
	}
	return lowest
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
			return nil
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
			return nil
		}
		return err
	}
	return fmt.Errorf("%w: deleted firewall rule still observed", firewallerr.ErrConflict)
}
