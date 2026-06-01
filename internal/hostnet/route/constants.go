package route

// Family identifies the address family a route operation targets.
type Family string

// RouteTable identifies the routing table a route operation targets.
type RouteTable string

// RouteType identifies the kernel route type represented by RouteSpec or RouteInfo.
type RouteType string

// RouteScope identifies the kernel route scope represented by RouteSpec or RouteInfo.
type RouteScope string

// RouteProtocol identifies the route protocol represented by RouteSpec or RouteInfo.
type RouteProtocol string

// DestinationMode describes how a Destination value should be interpreted.
type DestinationMode string

// GatewayMode describes how a Gateway value should be interpreted.
type GatewayMode string

// LinkFilterMode describes how a LinkFilter value should match route links.
type LinkFilterMode string

// MetricFilterMode describes how a MetricFilter value should match route metrics.
type MetricFilterMode string

const (
	// FamilyIPv4 selects IPv4 route operations.
	FamilyIPv4 Family = "ipv4"

	// RouteTableMain selects the main routing table.
	RouteTableMain RouteTable = "main"

	// RouteTypeUnicast selects ordinary unicast routes.
	RouteTypeUnicast RouteType = "unicast"

	// RouteScopeGlobal selects globally scoped routes.
	RouteScopeGlobal RouteScope = "global"
	// RouteScopeLink selects routes scoped to the output link.
	RouteScopeLink RouteScope = "link"
	// RouteScopeHost selects host-scoped observed routes.
	RouteScopeHost RouteScope = "host"

	// RouteProtocolStatic selects routes intentionally configured by Govirta callers.
	RouteProtocolStatic RouteProtocol = "static"
	// RouteProtocolKernel reports routes installed by the kernel.
	RouteProtocolKernel RouteProtocol = "kernel"
	// RouteProtocolBoot reports boot-time routes.
	RouteProtocolBoot RouteProtocol = "boot"
	// RouteProtocolDHCP reports routes installed from DHCP configuration.
	RouteProtocolDHCP RouteProtocol = "dhcp"

	// DestinationCIDR selects an explicit IPv4 prefix destination.
	DestinationCIDR DestinationMode = "cidr"
	// DestinationDefault selects the IPv4 default route.
	DestinationDefault DestinationMode = "default"
	// DestinationAny selects all destinations in RouteFilter only.
	DestinationAny DestinationMode = "any"

	// GatewayNone selects direct routes with no gateway.
	GatewayNone GatewayMode = "none"
	// GatewayIPv4 selects routes with an IPv4 gateway.
	GatewayIPv4 GatewayMode = "ipv4"
	// GatewayAny selects all gateway modes in RouteFilter only.
	GatewayAny GatewayMode = "any"

	// LinkAny selects routes on any output link in RouteFilter.
	LinkAny LinkFilterMode = "any"
	// LinkName selects routes on one named output link in RouteFilter.
	LinkName LinkFilterMode = "name"

	// MetricAny selects all route metrics in RouteFilter.
	MetricAny MetricFilterMode = "any"
	// MetricValue selects one explicit route metric in RouteFilter.
	MetricValue MetricFilterMode = "value"
)

// Metric describes an explicitly provided route metric.
//
// Set must be true anywhere a route metric affects behavior. Value 0 is valid
// only when Set is true, because Linux metric 0 is meaningful and must not be
// confused with an omitted Go zero value.
type Metric struct {
	Value uint32
	Set   bool
}

// MetricFilter selects route metrics for ListRoutes.
//
// Mode must be explicitly set to MetricAny or MetricValue. MetricValue with
// Value 0 is valid and matches the explicit Linux metric 0.
type MetricFilter struct {
	Mode  MetricFilterMode
	Value uint32
}

// ExplicitMetric returns a route metric marked as caller-provided.
func ExplicitMetric(value uint32) Metric {
	return Metric{Value: value, Set: true}
}

// AnyMetric returns a metric filter that matches every route metric.
func AnyMetric() MetricFilter {
	return MetricFilter{Mode: MetricAny}
}

// FilterMetric returns a metric filter for one explicit metric value.
func FilterMetric(value uint32) MetricFilter {
	return MetricFilter{Mode: MetricValue, Value: value}
}
