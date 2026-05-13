package scheduler

import (
	"context"

	"github.com/suknna/govirta/internal/types"
)

// Scheduler defines the VM placement boundary.
type Scheduler interface {
	Schedule(ctx context.Context, vm types.VirtualMachine, nodes []types.Node) (types.Node, error)
}

// NoopScheduler returns the first available node without applying policy.
type NoopScheduler struct{}

// NewNoopScheduler creates a scheduler for the initial skeleton.
func NewNoopScheduler() *NoopScheduler {
	return &NoopScheduler{}
}

// Schedule validates context cancellation and returns the first node when present.
func (s *NoopScheduler) Schedule(ctx context.Context, vm types.VirtualMachine, nodes []types.Node) (types.Node, error) {
	_ = vm

	select {
	case <-ctx.Done():
		return types.Node{}, ctx.Err()
	default:
	}

	if len(nodes) == 0 {
		return types.Node{}, nil
	}

	return nodes[0], nil
}
