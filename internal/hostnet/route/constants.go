package route

type Family string
type RouteTable string
type RouteType string
type RouteScope string
type RouteProtocol string
type DestinationMode string
type GatewayMode string
type LinkFilterMode string
type MetricFilterMode string

const (
	FamilyIPv4 Family = "ipv4"

	RouteTableMain RouteTable = "main"

	RouteTypeUnicast RouteType = "unicast"

	RouteScopeGlobal RouteScope = "global"
	RouteScopeLink   RouteScope = "link"
	RouteScopeHost   RouteScope = "host"

	RouteProtocolStatic RouteProtocol = "static"
	RouteProtocolKernel RouteProtocol = "kernel"
	RouteProtocolBoot   RouteProtocol = "boot"
	RouteProtocolDHCP   RouteProtocol = "dhcp"

	DestinationCIDR    DestinationMode = "cidr"
	DestinationDefault DestinationMode = "default"
	DestinationAny     DestinationMode = "any"

	GatewayNone GatewayMode = "none"
	GatewayIPv4 GatewayMode = "ipv4"
	GatewayAny  GatewayMode = "any"

	LinkAny  LinkFilterMode = "any"
	LinkName LinkFilterMode = "name"

	MetricAny   MetricFilterMode = "any"
	MetricValue MetricFilterMode = "value"
)

type Metric struct {
	Value uint32
	Set   bool
}

type MetricFilter struct {
	Mode  MetricFilterMode
	Value uint32
}

func ExplicitMetric(value uint32) Metric {
	return Metric{Value: value, Set: true}
}

func AnyMetric() MetricFilter {
	return MetricFilter{Mode: MetricAny}
}

func FilterMetric(value uint32) MetricFilter {
	return MetricFilter{Mode: MetricValue, Value: value}
}
