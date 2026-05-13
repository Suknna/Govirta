package bridge

import "context"

// Manager defines the Linux bridge management boundary.
type Manager interface {
	Name() string
	Ensure(ctx context.Context, bridgeName string) error
}

// NoopManager is a non-operational bridge manager for the initial skeleton.
type NoopManager struct{}

// NewNoopManager creates a bridge manager that does not modify host networking.
func NewNoopManager() *NoopManager {
	return &NoopManager{}
}

// Name returns the manager name.
func (m *NoopManager) Name() string {
	return "bridge-noop"
}

// Ensure validates context cancellation and performs no host networking operation.
func (m *NoopManager) Ensure(ctx context.Context, bridgeName string) error {
	_ = bridgeName

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
