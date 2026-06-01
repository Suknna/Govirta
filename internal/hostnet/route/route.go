package route

import (
	"context"
	"net/netip"

	"github.com/suknna/govirta/internal/hostnet/link"
)

type Manager interface {
	GetIPv4Forwarding(ctx context.Context) (IPv4ForwardingInfo, error)
	CheckIPv4Forwarding(ctx context.Context, expected IPv4ForwardingState) (IPv4ForwardingInfo, error)

	AddRoute(ctx context.Context, spec RouteSpec) (RouteInfo, error)
	ReplaceRoute(ctx context.Context, spec RouteSpec) (RouteInfo, error)
	DeleteRoute(ctx context.Context, spec RouteSpec) error
	ListRoutes(ctx context.Context, filter RouteFilter) ([]RouteInfo, error)
	GetRoute(ctx context.Context, query RouteQuery) (RouteInfo, error)
}

type Destination struct {
	Mode DestinationMode
	CIDR netip.Prefix
}

type Gateway struct {
	Mode GatewayMode
	Addr netip.Addr
}

type LinkFilter struct {
	Mode LinkFilterMode
	Name link.Name
}

type RouteSpec struct {
	Family      Family
	Destination Destination
	LinkName    link.Name
	Gateway     Gateway

	Table    RouteTable
	Type     RouteType
	Scope    RouteScope
	Protocol RouteProtocol
	Metric   Metric
}

type RouteFilter struct {
	Family      Family
	Table       RouteTable
	Link        LinkFilter
	Destination Destination
	Gateway     Gateway
	Metric      MetricFilter
}

type RouteQuery struct {
	Family      Family
	Destination netip.Addr
}

type RouteInfo struct {
	Family      Family
	Destination Destination
	LinkName    link.Name
	Gateway     Gateway

	Table    RouteTable
	Type     RouteType
	Scope    RouteScope
	Protocol RouteProtocol
	Metric   Metric
}
