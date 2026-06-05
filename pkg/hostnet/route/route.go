package route

import (
	"context"
	"net/netip"

	"github.com/suknna/govirta/pkg/hostnet/link"
)

// Manager owns host IPv4 forwarding checks and route lifecycle operations.
//
// Implementations must require callers to pass every behavior-affecting
// parameter explicitly through request structs. A nil context is an invalid
// request. A canceled or expired context must be returned as the context error
// before an implementation performs live host networking work. Implementations
// classify invalid requests, unsupported route shapes, missing routes,
// conflicting host state, permission failures, and incomplete enumeration with
// routeerr sentinel errors.
type Manager interface {
	// GetIPv4Forwarding returns the observed host IPv4 forwarding state.
	//
	// Implementations read the host state but must not mutate sysctl or persistent
	// forwarding configuration. A nil context returns routeerr.ErrInvalidRequest;
	// a canceled or expired context is returned directly.
	GetIPv4Forwarding(ctx context.Context) (IPv4ForwardingInfo, error)

	// CheckIPv4Forwarding returns the observed IPv4 forwarding state and verifies
	// it matches expected.
	//
	// Implementations return routeerr.ErrInvalidRequest for an invalid expected
	// state and routeerr.ErrNotReady when host forwarding is configured to a
	// different valid state.
	CheckIPv4Forwarding(ctx context.Context, expected IPv4ForwardingState) (IPv4ForwardingInfo, error)

	// AddRoute creates a host IPv4 route that exactly matches spec.
	//
	// RouteSpec must provide explicit family, destination, link, gateway, table,
	// type, scope, protocol, and metric values. Implementations return observed
	// host route state after success rather than echoing spec.
	AddRoute(ctx context.Context, spec RouteSpec) (RouteInfo, error)

	// ReplaceRoute creates or replaces a host IPv4 route to match spec.
	//
	// Implementations use the same validation and observed-state return contract as
	// AddRoute, but update an existing kernel route when one already matches the
	// destination selection.
	ReplaceRoute(ctx context.Context, spec RouteSpec) (RouteInfo, error)

	// DeleteRoute removes the host IPv4 route selected by spec.
	//
	// Implementations return nil when the route is absent after the operation, and
	// return context or host errors when removal cannot be completed.
	DeleteRoute(ctx context.Context, spec RouteSpec) error

	// ListRoutes returns observed host IPv4 routes that match filter.
	//
	// Filters must use explicit modes for destination, gateway, link, and metric
	// matching. When the platform reports incomplete enumeration, implementations
	// return routeerr.ErrIncompleteList instead of returning a partial success.
	ListRoutes(ctx context.Context, filter RouteFilter) ([]RouteInfo, error)

	// GetRoute returns the kernel path selection for query.Destination.
	//
	// This follows ip route get semantics: it asks the kernel how traffic to an IP
	// would be routed. It is not an exact route-entry lookup; callers that need to
	// find a specific configured route entry should use ListRoutes and match the
	// returned RouteInfo values.
	GetRoute(ctx context.Context, query RouteQuery) (RouteInfo, error)
}

// Destination selects a route destination.
//
// Mode must be explicit. DestinationCIDR requires CIDR to be a valid IPv4
// prefix. DestinationDefault requires CIDR to be zero and represents the IPv4
// default route. DestinationAny is valid only in RouteFilter and must not be
// used in RouteSpec.
type Destination struct {
	Mode DestinationMode
	CIDR netip.Prefix
}

// Gateway selects gateway matching or direct-link routing semantics.
//
// Mode must be explicit. GatewayNone describes a direct route with no gateway.
// GatewayIPv4 requires Addr to be a valid IPv4 gateway address. GatewayAny is
// valid only in RouteFilter and must not be used in RouteSpec.
type Gateway struct {
	Mode GatewayMode
	Addr netip.Addr
}

// LinkFilter selects route output links for ListRoutes.
//
// Mode must be explicitly set to LinkAny or LinkName. LinkName requires Name to
// identify the host link to match.
type LinkFilter struct {
	Mode LinkFilterMode
	Name link.Name
}

// RouteSpec describes the complete desired route mutation state.
//
// Family, Destination, LinkName, Gateway, Table, Type, Scope, Protocol, and
// Metric must all be explicitly set. Metric.Set must be true, including when the
// desired Linux metric value is 0. DestinationAny and GatewayAny are filter-only
// modes and are invalid in RouteSpec.
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

// RouteFilter selects which observed host routes ListRoutes returns.
//
// Family and Table must be explicitly set. All behavior-affecting filter
// dimensions must be explicit. DestinationAny, GatewayAny, LinkAny, and
// MetricAny select all values for their dimension; concrete modes select only
// matching observed route fields.
type RouteFilter struct {
	Family      Family
	Table       RouteTable
	Link        LinkFilter
	Destination Destination
	Gateway     Gateway
	Metric      MetricFilter
}

// RouteQuery asks the kernel how an IPv4 destination would be routed.
//
// Family must be explicitly set to FamilyIPv4. Destination must be a valid IPv4
// address. This type is for GetRoute path selection and does not identify an
// exact configured route entry.
type RouteQuery struct {
	Family      Family
	Destination netip.Addr
}

// RouteInfo reports observed host route state.
//
// Implementations populate RouteInfo from actual host state after translating
// platform route data into Govirta-owned types. RouteInfo must not be a blind
// echo of the requested RouteSpec.
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
