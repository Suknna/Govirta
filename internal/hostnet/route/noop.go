package route

import (
	"context"

	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
)

// NoopManager is a route Manager implementation for composition tests.
//
// It validates only nil or canceled contexts and reports routeerr.ErrUnsupported
// for live host route operations.
type NoopManager struct{}

var _ Manager = NoopManager{}

// NewNoopManager returns a no-op host route manager.
func NewNoopManager() NoopManager { return NoopManager{} }

func (NoopManager) GetIPv4Forwarding(ctx context.Context) (IPv4ForwardingInfo, error) {
	if err := noopRouteOperationError(ctx); err != nil {
		return IPv4ForwardingInfo{}, err
	}

	return IPv4ForwardingInfo{}, routeerr.ErrUnsupported
}

func (NoopManager) CheckIPv4Forwarding(ctx context.Context, _ IPv4ForwardingState) (IPv4ForwardingInfo, error) {
	if err := noopRouteOperationError(ctx); err != nil {
		return IPv4ForwardingInfo{}, err
	}

	return IPv4ForwardingInfo{}, routeerr.ErrUnsupported
}

func (NoopManager) AddRoute(ctx context.Context, _ RouteSpec) (RouteInfo, error) {
	if err := noopRouteOperationError(ctx); err != nil {
		return RouteInfo{}, err
	}

	return RouteInfo{}, routeerr.ErrUnsupported
}

func (NoopManager) ReplaceRoute(ctx context.Context, _ RouteSpec) (RouteInfo, error) {
	if err := noopRouteOperationError(ctx); err != nil {
		return RouteInfo{}, err
	}

	return RouteInfo{}, routeerr.ErrUnsupported
}

func (NoopManager) DeleteRoute(ctx context.Context, _ RouteSpec) error {
	if err := noopRouteOperationError(ctx); err != nil {
		return err
	}

	return routeerr.ErrUnsupported
}

func (NoopManager) ListRoutes(ctx context.Context, _ RouteFilter) ([]RouteInfo, error) {
	if err := noopRouteOperationError(ctx); err != nil {
		return nil, err
	}

	return nil, routeerr.ErrUnsupported
}

func (NoopManager) GetRoute(ctx context.Context, _ RouteQuery) (RouteInfo, error) {
	if err := noopRouteOperationError(ctx); err != nil {
		return RouteInfo{}, err
	}

	return RouteInfo{}, routeerr.ErrUnsupported
}

func noopRouteOperationError(ctx context.Context) error {
	if ctx == nil {
		return routeerr.ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	return nil
}
