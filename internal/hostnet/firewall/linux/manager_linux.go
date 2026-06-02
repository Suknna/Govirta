//go:build linux

package linux

import (
	"context"

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

func (m *Manager) EnsureMasquerade(ctx context.Context, spec firewall.MasqueradeSpec) (firewall.RuleInfo, error) {
	if err := validateMasqueradeSpec(ctx, spec); err != nil {
		return firewall.RuleInfo{}, translateError("ensure masquerade", err)
	}
	return firewall.RuleInfo{}, translateError("ensure masquerade", firewallerr.ErrUnsupported)
}

func (m *Manager) DeleteMasquerade(ctx context.Context, ref firewall.RuleRef) error {
	if err := validateRuleRef(ctx, ref, firewall.RulePurposeMasquerade); err != nil {
		return translateError("delete masquerade", err)
	}
	return translateError("delete masquerade", firewallerr.ErrUnsupported)
}

func (m *Manager) EnsureEndpointAntiSpoofing(ctx context.Context, spec firewall.EndpointAntiSpoofingSpec) (firewall.RuleInfo, error) {
	if err := validateEndpointAntiSpoofingSpec(ctx, spec); err != nil {
		return firewall.RuleInfo{}, translateError("ensure endpoint anti-spoofing", err)
	}
	return firewall.RuleInfo{}, translateError("ensure endpoint anti-spoofing", firewallerr.ErrUnsupported)
}

func (m *Manager) DeleteEndpointAntiSpoofing(ctx context.Context, ref firewall.RuleRef) error {
	if err := validateRuleRef(ctx, ref, firewall.RulePurposeEndpointAntiSpoofing); err != nil {
		return translateError("delete endpoint anti-spoofing", err)
	}
	return translateError("delete endpoint anti-spoofing", firewallerr.ErrUnsupported)
}

func (m *Manager) GetRule(ctx context.Context, query firewall.RuleQuery) (firewall.RuleInfo, error) {
	if err := validateRuleQuery(ctx, query); err != nil {
		return firewall.RuleInfo{}, translateError("get firewall rule", err)
	}
	return firewall.RuleInfo{}, translateError("get firewall rule", firewallerr.ErrUnsupported)
}

func (m *Manager) ListRules(ctx context.Context, filter firewall.RuleFilter) ([]firewall.RuleInfo, error) {
	if err := validateRuleFilter(ctx, filter); err != nil {
		return nil, translateError("list firewall rules", err)
	}
	return nil, translateError("list firewall rules", firewallerr.ErrUnsupported)
}
