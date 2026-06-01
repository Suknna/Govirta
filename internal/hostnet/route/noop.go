package route

import (
	"context"
	"fmt"

	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
)

type NoopManager struct{}

func NewNoopManager() NoopManager { return NoopManager{} }

func (NoopManager) GetIPv4Forwarding(ctx context.Context) (IPv4ForwardingInfo, error) {
	if err := noopRouteOperationError(ctx, "get IPv4 forwarding"); err != nil {
		return IPv4ForwardingInfo{}, err
	}
	return IPv4ForwardingInfo{}, nil
}

func (NoopManager) CheckIPv4Forwarding(ctx context.Context, _ IPv4ForwardingState) (IPv4ForwardingInfo, error) {
	if err := noopRouteOperationError(ctx, "check IPv4 forwarding"); err != nil {
		return IPv4ForwardingInfo{}, err
	}
	return IPv4ForwardingInfo{}, nil
}

func (NoopManager) AddRoute(ctx context.Context, _ RouteSpec) (RouteInfo, error) {
	if err := noopRouteOperationError(ctx, "add route"); err != nil {
		return RouteInfo{}, err
	}
	return RouteInfo{}, nil
}

func (NoopManager) ReplaceRoute(ctx context.Context, _ RouteSpec) (RouteInfo, error) {
	if err := noopRouteOperationError(ctx, "replace route"); err != nil {
		return RouteInfo{}, err
	}
	return RouteInfo{}, nil
}

func (NoopManager) DeleteRoute(ctx context.Context, _ RouteSpec) error {
	return noopRouteOperationError(ctx, "delete route")
}

func (NoopManager) ListRoutes(ctx context.Context, _ RouteFilter) ([]RouteInfo, error) {
	if err := noopRouteOperationError(ctx, "list routes"); err != nil {
		return nil, err
	}
	return nil, nil
}

func (NoopManager) GetRoute(ctx context.Context, _ RouteQuery) (RouteInfo, error) {
	if err := noopRouteOperationError(ctx, "get route"); err != nil {
		return RouteInfo{}, err
	}
	return RouteInfo{}, nil
}

func noopRouteOperationError(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s: %w", operation, routeerr.ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%s: %w", operation, routeerr.ErrUnsupported)
}
