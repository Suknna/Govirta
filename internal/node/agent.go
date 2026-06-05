package node

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/network"
	"github.com/suknna/govirta/internal/network/netpool"
	hostdhcp "github.com/suknna/govirta/pkg/hostnet/dhcp"
	hostfirewall "github.com/suknna/govirta/pkg/hostnet/firewall"
	hostlink "github.com/suknna/govirta/pkg/hostnet/link"
	hostroute "github.com/suknna/govirta/pkg/hostnet/route"
	"github.com/suknna/govirta/pkg/virt/qmp"
)

// Agent coordinates compute-node local virtualization dependencies.
type Agent struct {
	qmpClient      qmp.Client
	networkService *network.NetworkService
	nicService     *network.NICService
}

// NewAgent creates a node agent with no-op dependencies.
//
// The network services share one netpool core wired with no-op host primitives;
// real netlink/nftables/CoreDHCP managers are injected by the compute node at a
// later integration step.
func NewAgent() *Agent {
	pools := netpool.NewService(
		hostlink.NewNoopManager(),
		hostroute.NewNoopManager(),
		hostfirewall.NewNoopManager(),
		hostdhcp.NewNoopManager(),
	)
	return &Agent{
		qmpClient:      qmp.NewNoopClient(),
		networkService: network.NewNetworkService(pools),
		nicService:     network.NewNICService(pools),
	}
}

// Run starts the node agent skeleton.
func (a *Agent) Run(ctx context.Context) error {
	logger := zerolog.Ctx(ctx).With().
		Str("component", "node").
		Str("qmp_client", a.qmpClient.Name()).
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
