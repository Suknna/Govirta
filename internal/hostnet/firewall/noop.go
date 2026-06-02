package firewall

import (
	"context"

	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

// NoopManager is a firewall Manager implementation for composition tests.
//
// It validates only nil or canceled contexts and reports
// firewallerr.ErrUnsupported for live host firewall operations.
type NoopManager struct{}

var _ Manager = NoopManager{}

// NewNoopManager returns a no-op host firewall manager.
func NewNoopManager() NoopManager { return NoopManager{} }

// EnsureMasquerade validates nil or canceled context and otherwise returns ErrUnsupported.
func (NoopManager) EnsureMasquerade(ctx context.Context, _ MasqueradeSpec) (RuleInfo, error) {
	if err := noopFirewallOperationError(ctx); err != nil {
		return RuleInfo{}, err
	}

	return RuleInfo{}, firewallerr.ErrUnsupported
}

// DeleteMasquerade validates nil or canceled context and otherwise returns ErrUnsupported.
func (NoopManager) DeleteMasquerade(ctx context.Context, _ RuleRef) error {
	if err := noopFirewallOperationError(ctx); err != nil {
		return err
	}

	return firewallerr.ErrUnsupported
}

// EnsureEndpointAntiSpoofing validates nil or canceled context and otherwise returns ErrUnsupported.
func (NoopManager) EnsureEndpointAntiSpoofing(ctx context.Context, _ EndpointAntiSpoofingSpec) (RuleInfo, error) {
	if err := noopFirewallOperationError(ctx); err != nil {
		return RuleInfo{}, err
	}

	return RuleInfo{}, firewallerr.ErrUnsupported
}

// DeleteEndpointAntiSpoofing validates nil or canceled context and otherwise returns ErrUnsupported.
func (NoopManager) DeleteEndpointAntiSpoofing(ctx context.Context, _ RuleRef) error {
	if err := noopFirewallOperationError(ctx); err != nil {
		return err
	}

	return firewallerr.ErrUnsupported
}

// GetRule validates nil or canceled context and otherwise returns ErrUnsupported.
func (NoopManager) GetRule(ctx context.Context, _ RuleQuery) (RuleInfo, error) {
	if err := noopFirewallOperationError(ctx); err != nil {
		return RuleInfo{}, err
	}

	return RuleInfo{}, firewallerr.ErrUnsupported
}

// ListRules validates nil or canceled context and otherwise returns ErrUnsupported.
func (NoopManager) ListRules(ctx context.Context, _ RuleFilter) ([]RuleInfo, error) {
	if err := noopFirewallOperationError(ctx); err != nil {
		return nil, err
	}

	return nil, firewallerr.ErrUnsupported
}

func noopFirewallOperationError(ctx context.Context) error {
	if ctx == nil {
		return firewallerr.ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
