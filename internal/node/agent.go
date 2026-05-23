package node

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/network/bridge"
	"github.com/suknna/govirta/internal/virt/qmp"
)

// Agent coordinates compute-node local virtualization dependencies.
type Agent struct {
	qmpClient     qmp.Client
	bridgeManager bridge.Manager
}

// NewAgent creates a node agent with no-op dependencies.
func NewAgent() *Agent {
	return &Agent{
		qmpClient:     qmp.NewNoopClient(),
		bridgeManager: bridge.NewNoopManager(),
	}
}

// Run starts the node agent skeleton.
func (a *Agent) Run(ctx context.Context) error {
	logger := zerolog.Ctx(ctx).With().
		Str("component", "node").
		Str("qmp_client", a.qmpClient.Name()).
		Str("bridge_manager", a.bridgeManager.Name()).
		Logger()

	ctx = logger.WithContext(ctx)
	zerolog.Ctx(ctx).Info().Msg("starting node agent")

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
