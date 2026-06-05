//go:build linux

package linux

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/suknna/govirta/pkg/hostnet/link"
	"github.com/suknna/govirta/pkg/hostnet/route"
	"github.com/suknna/govirta/pkg/hostnet/route/routeerr"
)

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return routeerr.ErrInvalidRequest
	}

	return ctx.Err()
}

func validateForwardingState(state route.IPv4ForwardingState) error {
	switch state {
	case route.IPv4ForwardingEnabled, route.IPv4ForwardingDisabled:
		return nil
	default:
		return fmt.Errorf("IPv4 forwarding state %q: %w", state, routeerr.ErrInvalidRequest)
	}
}

func validateRouteSpec(spec route.RouteSpec) error {
	if err := validateFamily(spec.Family); err != nil {
		return err
	}
	if err := validateMainTable(spec.Table); err != nil {
		return err
	}
	if spec.Type == "" || spec.Protocol == "" || spec.Scope == "" {
		return routeerr.ErrInvalidRequest
	}
	if spec.Type != route.RouteTypeUnicast || spec.Protocol != route.RouteProtocolStatic {
		return routeerr.ErrUnsupported
	}
	if !spec.Metric.Set {
		return routeerr.ErrInvalidRequest
	}
	if !metricFitsNativeInt(spec.Metric.Value) {
		return routeerr.ErrInvalidRequest
	}
	if err := validateLinkName(spec.LinkName); err != nil {
		return err
	}
	if err := validateSpecDestination(spec.Destination); err != nil {
		return err
	}
	if err := validateSpecGateway(spec.Gateway); err != nil {
		return err
	}

	return validateRouteShape(spec)
}

func validateRouteFilter(filter route.RouteFilter) error {
	if err := validateFamily(filter.Family); err != nil {
		return err
	}
	if err := validateMainTable(filter.Table); err != nil {
		return err
	}
	if err := validateFilterDestination(filter.Destination); err != nil {
		return err
	}
	if err := validateFilterGateway(filter.Gateway); err != nil {
		return err
	}
	if err := validateLinkFilter(filter.Link); err != nil {
		return err
	}
	switch filter.Metric.Mode {
	case route.MetricAny:
		return nil
	case route.MetricValue:
		if !metricFitsNativeInt(filter.Metric.Value) {
			return routeerr.ErrInvalidRequest
		}
		return nil
	default:
		return routeerr.ErrInvalidRequest
	}
}

func validateRouteQuery(query route.RouteQuery) error {
	if err := validateFamily(query.Family); err != nil {
		return err
	}
	if !query.Destination.IsValid() || !query.Destination.Is4() {
		return routeerr.ErrInvalidRequest
	}

	return nil
}

func validateFamily(family route.Family) error {
	switch family {
	case route.FamilyIPv4:
		return nil
	case "":
		return routeerr.ErrInvalidRequest
	default:
		return routeerr.ErrUnsupported
	}
}

func validateMainTable(table route.RouteTable) error {
	switch table {
	case route.RouteTableMain:
		return nil
	case "":
		return routeerr.ErrInvalidRequest
	default:
		return routeerr.ErrUnsupported
	}
}

func validateLinkName(name link.Name) error {
	if name == "" || len(string(name)) > link.MaxInterfaceNameLength {
		return routeerr.ErrInvalidRequest
	}

	return nil
}

func validateLinkFilter(filter route.LinkFilter) error {
	switch filter.Mode {
	case route.LinkAny:
		if filter.Name != "" {
			return routeerr.ErrInvalidRequest
		}
		return nil
	case route.LinkName:
		return validateLinkName(filter.Name)
	default:
		return routeerr.ErrInvalidRequest
	}
}

func validateSpecDestination(destination route.Destination) error {
	switch destination.Mode {
	case route.DestinationCIDR:
		return validateIPv4Prefix(destination.CIDR)
	case route.DestinationDefault:
		if destination.CIDR != (netip.Prefix{}) {
			return routeerr.ErrInvalidRequest
		}
		return nil
	case route.DestinationAny:
		return routeerr.ErrInvalidRequest
	default:
		return routeerr.ErrInvalidRequest
	}
}

func validateFilterDestination(destination route.Destination) error {
	switch destination.Mode {
	case route.DestinationAny:
		if destination.CIDR != (netip.Prefix{}) {
			return routeerr.ErrInvalidRequest
		}
		return nil
	case route.DestinationCIDR, route.DestinationDefault:
		return validateSpecDestination(destination)
	default:
		return routeerr.ErrInvalidRequest
	}
}

func validateIPv4Prefix(prefix netip.Prefix) error {
	if !prefix.IsValid() || !prefix.Addr().Is4() || prefix.Bits() == 0 {
		return routeerr.ErrInvalidRequest
	}
	// Require canonical form (no host bits set). destinationIPNet masks the
	// prefix for the kernel, but exactRouteMatch / routeInfoMatchesFilter compare
	// the raw spec.Destination against the canonical observed destination, so a
	// non-canonical prefix (e.g. 198.51.100.5/24) would never match its own
	// observed route and silently fail re-read, list, and delete.
	if prefix != prefix.Masked() {
		return routeerr.ErrInvalidRequest
	}

	return nil
}

func validateSpecGateway(gateway route.Gateway) error {
	switch gateway.Mode {
	case route.GatewayNone:
		if gateway.Addr != (netip.Addr{}) {
			return routeerr.ErrInvalidRequest
		}
		return nil
	case route.GatewayIPv4:
		if !gateway.Addr.IsValid() || !gateway.Addr.Is4() || gateway.Addr.IsUnspecified() || gateway.Addr.IsMulticast() {
			return routeerr.ErrInvalidRequest
		}
		return nil
	case route.GatewayAny:
		return routeerr.ErrInvalidRequest
	default:
		return routeerr.ErrInvalidRequest
	}
}

func validateFilterGateway(gateway route.Gateway) error {
	switch gateway.Mode {
	case route.GatewayAny:
		if gateway.Addr != (netip.Addr{}) {
			return routeerr.ErrInvalidRequest
		}
		return nil
	case route.GatewayNone, route.GatewayIPv4:
		return validateSpecGateway(gateway)
	default:
		return routeerr.ErrInvalidRequest
	}
}

func validateRouteShape(spec route.RouteSpec) error {
	switch {
	case spec.Destination.Mode == route.DestinationCIDR && spec.Gateway.Mode == route.GatewayNone && spec.Scope == route.RouteScopeLink:
		return nil
	case spec.Destination.Mode == route.DestinationCIDR && spec.Gateway.Mode == route.GatewayIPv4 && spec.Scope == route.RouteScopeGlobal:
		return nil
	case spec.Destination.Mode == route.DestinationDefault && spec.Gateway.Mode == route.GatewayIPv4 && spec.Scope == route.RouteScopeGlobal:
		return nil
	case spec.Scope == route.RouteScopeHost:
		return routeerr.ErrUnsupported
	default:
		return routeerr.ErrInvalidRequest
	}
}
