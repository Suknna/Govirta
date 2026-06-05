//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"

	linkpkg "github.com/suknna/govirta/pkg/hostnet/link"
	"github.com/suknna/govirta/pkg/hostnet/route"
	"github.com/suknna/govirta/pkg/hostnet/route/routeerr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const ipv4Bits = 32

func netlinkRouteForSpec(h handle, spec route.RouteSpec) (netlink.Route, error) {
	observedLink, err := h.LinkByName(string(spec.LinkName))
	if err != nil {
		return netlink.Route{}, translateError("lookup route link", err)
	}

	destination, err := destinationIPNet(spec.Destination)
	if err != nil {
		return netlink.Route{}, err
	}

	return netlink.Route{
		Family:    netlink.FAMILY_V4,
		LinkIndex: observedLink.Attrs().Index,
		Dst:       destination,
		Gw:        gatewayIP(spec.Gateway),
		Table:     routeTableToNetlink(spec.Table),
		Type:      routeTypeToNetlink(spec.Type),
		Scope:     routeScopeToNetlink(spec.Scope),
		Protocol:  routeProtocolToNetlink(spec.Protocol),
		Priority:  int(spec.Metric.Value),
	}, nil
}

func observedRouteInfo(h handle, observed netlink.Route) (route.RouteInfo, error) {
	info, err := netlinkRouteInfo(h, observed)
	if err != nil {
		return route.RouteInfo{}, err
	}

	return info, nil
}

func applyRoute(ctx context.Context, h handle, operation string, spec route.RouteSpec, mutate func(*netlink.Route) error) (route.RouteInfo, error) {
	if err := checkContext(ctx); err != nil {
		return route.RouteInfo{}, err
	}
	if err := validateRouteSpec(spec); err != nil {
		return route.RouteInfo{}, translateError(operation, err)
	}

	nlRoute, err := netlinkRouteForSpec(h, spec)
	if err != nil {
		return route.RouteInfo{}, err
	}
	if err := mutate(&nlRoute); err != nil {
		return route.RouteInfo{}, translateError(operation, err)
	}

	return observedRouteForSpec(h, operation, spec)
}

func observedRouteForSpec(h handle, operation string, spec route.RouteSpec) (route.RouteInfo, error) {
	nlFilter, err := netlinkRouteForSpec(h, spec)
	if err != nil {
		return route.RouteInfo{}, err
	}
	nlRoutes, err := h.RouteListFiltered(netlink.FAMILY_V4, &nlFilter, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_OIF|netlink.RT_FILTER_DST|netlink.RT_FILTER_GW|netlink.RT_FILTER_PRIORITY)
	if err != nil {
		return route.RouteInfo{}, translateError(operation, err)
	}
	for _, nlRoute := range nlRoutes {
		info, err := observedRouteInfo(h, nlRoute)
		if err != nil {
			return route.RouteInfo{}, translateError(operation, err)
		}
		if exactRouteMatch(info, spec) {
			return info, nil
		}
	}

	return route.RouteInfo{}, translateError(operation, routeerr.ErrNotFound)
}

func cleanupStaleRoutesAfterReplace(h handle, spec route.RouteSpec) error {
	nlFilter, err := netlinkRouteForSpec(h, spec)
	if err != nil {
		return err
	}
	nlRoutes, err := h.RouteListFiltered(netlink.FAMILY_V4, &nlFilter, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_OIF|netlink.RT_FILTER_DST|netlink.RT_FILTER_GW)
	if err != nil {
		return translateError("replace route cleanup", err)
	}

	var cleanupErrs []error
	for _, nlRoute := range nlRoutes {
		if !staleManagedNetlinkRoute(nlRoute, nlFilter) {
			continue
		}
		candidate := cloneNetlinkRoute(nlRoute)
		if err := h.RouteDel(&candidate); err != nil {
			cleanupErrs = append(cleanupErrs, translateError("replace route cleanup", err))
		}
	}

	return errors.Join(cleanupErrs...)
}

func staleManagedNetlinkRoute(candidate, target netlink.Route) bool {
	return candidate.Family == target.Family &&
		candidate.Table == target.Table &&
		candidate.LinkIndex == target.LinkIndex &&
		sameNetIPNet(candidate.Dst, target.Dst) &&
		candidate.Gw.Equal(target.Gw) &&
		candidate.Type == unix.RTN_UNICAST &&
		candidate.Protocol == unix.RTPROT_STATIC &&
		candidate.Scope == target.Scope &&
		!sameNetlinkRouteIdentity(candidate, target)
}

func cloneNetlinkRoute(route netlink.Route) netlink.Route {
	cloned := route
	if route.Dst != nil {
		cloned.Dst = &net.IPNet{IP: append(net.IP(nil), route.Dst.IP...), Mask: append(net.IPMask(nil), route.Dst.Mask...)}
	}
	if route.Gw != nil {
		cloned.Gw = append(net.IP(nil), route.Gw...)
	}
	return cloned
}

func sameNetlinkRouteIdentity(left, right netlink.Route) bool {
	return sameNetlinkRouteSelector(left, right) &&
		left.Type == right.Type &&
		left.Scope == right.Scope &&
		left.Protocol == right.Protocol &&
		left.Priority == right.Priority
}

func sameNetlinkRouteSelector(left, right netlink.Route) bool {
	return left.Family == right.Family &&
		left.Table == right.Table &&
		left.LinkIndex == right.LinkIndex &&
		sameNetIPNet(left.Dst, right.Dst) &&
		left.Gw.Equal(right.Gw)
}

func sameNetIPNet(left, right *net.IPNet) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.IP.Equal(right.IP) && string(left.Mask) == string(right.Mask)
}

func netlinkFilterForRouteFilter(h handle, filter route.RouteFilter) (netlink.Route, uint64, error) {
	nlFilter := netlink.Route{Family: netlink.FAMILY_V4, Table: routeTableToNetlink(filter.Table)}
	mask := uint64(netlink.RT_FILTER_TABLE)

	if filter.Link.Mode == route.LinkName {
		link, err := h.LinkByName(string(filter.Link.Name))
		if err != nil {
			return netlink.Route{}, 0, translateError("lookup route filter link", err)
		}
		nlFilter.LinkIndex = link.Attrs().Index
		mask |= netlink.RT_FILTER_OIF
	}

	if filter.Destination.Mode == route.DestinationCIDR || filter.Destination.Mode == route.DestinationDefault {
		destination, err := destinationIPNet(filter.Destination)
		if err != nil {
			return netlink.Route{}, 0, err
		}
		nlFilter.Dst = destination
		mask |= netlink.RT_FILTER_DST
	}

	if filter.Gateway.Mode == route.GatewayIPv4 {
		nlFilter.Gw = gatewayIP(filter.Gateway)
		mask |= netlink.RT_FILTER_GW
	}

	return nlFilter, mask, nil
}

func destinationIPNet(destination route.Destination) (*net.IPNet, error) {
	switch destination.Mode {
	case route.DestinationDefault:
		return &net.IPNet{IP: net.IPv4(0, 0, 0, 0), Mask: net.CIDRMask(0, ipv4Bits)}, nil
	case route.DestinationCIDR:
		prefix := destination.CIDR.Masked()
		addr := prefix.Addr().As4()
		return &net.IPNet{IP: net.IPv4(addr[0], addr[1], addr[2], addr[3]), Mask: net.CIDRMask(prefix.Bits(), ipv4Bits)}, nil
	default:
		return nil, routeerr.ErrInvalidRequest
	}
}

func netlinkRouteInfo(h handle, observed netlink.Route) (route.RouteInfo, error) {
	observedLink, err := h.LinkByIndex(observed.LinkIndex)
	if err != nil {
		return route.RouteInfo{}, translateError("lookup observed route link", err)
	}

	table, err := routeTableFromNetlink(observed.Table)
	if err != nil {
		return route.RouteInfo{}, err
	}
	routeType, err := routeTypeFromNetlink(observed.Type)
	if err != nil {
		return route.RouteInfo{}, err
	}
	scope, err := routeScopeFromNetlink(observed.Scope)
	if err != nil {
		return route.RouteInfo{}, err
	}
	protocol, err := routeProtocolFromNetlink(observed.Protocol)
	if err != nil {
		return route.RouteInfo{}, err
	}
	destination, err := destinationFromNetlink(observed.Dst)
	if err != nil {
		return route.RouteInfo{}, err
	}
	gateway, err := gatewayFromNetlink(observed.Gw)
	if err != nil {
		return route.RouteInfo{}, err
	}
	if observed.Priority < 0 {
		return route.RouteInfo{}, routeerr.ErrInvalidObservedState
	}

	return route.RouteInfo{
		Family:      route.FamilyIPv4,
		Destination: destination,
		LinkName:    linkpkg.Name(observedLink.Attrs().Name),
		Gateway:     gateway,
		Table:       table,
		Type:        routeType,
		Scope:       scope,
		Protocol:    protocol,
		Metric:      route.ExplicitMetric(uint32(observed.Priority)),
	}, nil
}

func routeProtocolFromNetlink(protocol netlink.RouteProtocol) (route.RouteProtocol, error) {
	switch protocol {
	case 0:
		return route.RouteProtocolUnspecified, nil
	case unix.RTPROT_STATIC:
		return route.RouteProtocolStatic, nil
	case unix.RTPROT_KERNEL:
		return route.RouteProtocolKernel, nil
	case unix.RTPROT_BOOT:
		return route.RouteProtocolBoot, nil
	case unix.RTPROT_DHCP:
		return route.RouteProtocolDHCP, nil
	default:
		return "", fmt.Errorf("observed route protocol %d: %w", protocol, routeerr.ErrInvalidObservedState)
	}
}

func routeScopeFromNetlink(scope netlink.Scope) (route.RouteScope, error) {
	switch scope {
	case netlink.SCOPE_UNIVERSE:
		return route.RouteScopeGlobal, nil
	case netlink.SCOPE_LINK:
		return route.RouteScopeLink, nil
	case netlink.SCOPE_HOST:
		return route.RouteScopeHost, nil
	default:
		return "", fmt.Errorf("observed route scope %d: %w", scope, routeerr.ErrInvalidObservedState)
	}
}

func routeTypeFromNetlink(routeType int) (route.RouteType, error) {
	switch routeType {
	case unix.RTN_UNICAST:
		return route.RouteTypeUnicast, nil
	case unix.RTN_BLACKHOLE, unix.RTN_UNREACHABLE, unix.RTN_PROHIBIT, unix.RTN_THROW, unix.RTN_NAT, unix.RTN_LOCAL, unix.RTN_BROADCAST, unix.RTN_ANYCAST, unix.RTN_MULTICAST, unix.RTN_XRESOLVE:
		return "", fmt.Errorf("observed route type %d: %w", routeType, routeerr.ErrUnsupported)
	default:
		return "", fmt.Errorf("observed route type %d: %w", routeType, routeerr.ErrInvalidObservedState)
	}
}

func metricFitsNativeInt(value uint32) bool {
	return metricFitsInt(value, strconv.IntSize)
}

func metricFitsInt(value uint32, intSize int) bool {
	if intSize >= 64 {
		return true
	}
	return value <= uint32(maxIntForSize(intSize))
}

func maxIntForSize(intSize int) int64 {
	return int64(1)<<(intSize-1) - 1
}

func exactRouteMatch(info route.RouteInfo, spec route.RouteSpec) bool {
	return info.Family == spec.Family &&
		info.Destination == spec.Destination &&
		info.LinkName == spec.LinkName &&
		info.Gateway == spec.Gateway &&
		info.Table == spec.Table &&
		info.Type == spec.Type &&
		info.Scope == spec.Scope &&
		info.Protocol == spec.Protocol &&
		info.Metric == spec.Metric
}

func routeInfoMatchesFilter(info route.RouteInfo, filter route.RouteFilter) bool {
	if info.Family != filter.Family || info.Table != filter.Table {
		return false
	}
	if filter.Link.Mode == route.LinkName && info.LinkName != filter.Link.Name {
		return false
	}
	if filter.Destination.Mode != route.DestinationAny && info.Destination != filter.Destination {
		return false
	}
	if filter.Gateway.Mode != route.GatewayAny && info.Gateway != filter.Gateway {
		return false
	}
	if filter.Metric.Mode == route.MetricValue && info.Metric.Value != filter.Metric.Value {
		return false
	}

	return true
}

func sortRouteInfos(infos []route.RouteInfo) {
	sort.Slice(infos, func(i, j int) bool {
		return routeInfoSortKey(infos[i]) < routeInfoSortKey(infos[j])
	})
}

func routeInfoSortKey(info route.RouteInfo) string {
	destination := string(info.Destination.Mode)
	if info.Destination.Mode == route.DestinationCIDR {
		destination += "/" + info.Destination.CIDR.String()
	}
	gateway := string(info.Gateway.Mode)
	if info.Gateway.Mode == route.GatewayIPv4 {
		gateway += "/" + info.Gateway.Addr.String()
	}
	return fmt.Sprintf("%s|%s|%s|%s|%010d|%s|%s", info.Table, destination, info.LinkName, gateway, info.Metric.Value, info.Scope, info.Protocol)
}

func destinationFromNetlink(dst *net.IPNet) (route.Destination, error) {
	if dst == nil {
		return route.Destination{Mode: route.DestinationDefault}, nil
	}
	ones, bits := dst.Mask.Size()
	if bits != ipv4Bits || dst.IP.To4() == nil {
		return route.Destination{}, routeerr.ErrInvalidObservedState
	}
	if ones == 0 && dst.IP.To4().Equal(net.IPv4(0, 0, 0, 0)) {
		return route.Destination{Mode: route.DestinationDefault}, nil
	}
	addr, ok := netip.AddrFromSlice(dst.IP.To4())
	if !ok {
		return route.Destination{}, routeerr.ErrInvalidObservedState
	}

	return route.Destination{Mode: route.DestinationCIDR, CIDR: netip.PrefixFrom(addr, ones)}, nil
}

func gatewayFromNetlink(gateway net.IP) (route.Gateway, error) {
	if gateway == nil {
		return route.Gateway{Mode: route.GatewayNone}, nil
	}
	addr, ok := netip.AddrFromSlice(gateway.To4())
	if !ok {
		return route.Gateway{}, routeerr.ErrInvalidObservedState
	}

	return route.Gateway{Mode: route.GatewayIPv4, Addr: addr}, nil
}

func gatewayIP(gateway route.Gateway) net.IP {
	if gateway.Mode != route.GatewayIPv4 {
		return nil
	}
	addr := gateway.Addr.As4()
	return net.IPv4(addr[0], addr[1], addr[2], addr[3])
}

func routeTableToNetlink(table route.RouteTable) int {
	if table == route.RouteTableMain {
		return unix.RT_TABLE_MAIN
	}
	return 0
}

func routeTableFromNetlink(table int) (route.RouteTable, error) {
	if table != unix.RT_TABLE_MAIN {
		return "", fmt.Errorf("observed route table %d: %w", table, routeerr.ErrUnsupported)
	}
	return route.RouteTableMain, nil
}

func routeTypeToNetlink(routeType route.RouteType) int {
	if routeType == route.RouteTypeUnicast {
		return unix.RTN_UNICAST
	}
	return 0
}

func routeScopeToNetlink(scope route.RouteScope) netlink.Scope {
	switch scope {
	case route.RouteScopeGlobal:
		return netlink.SCOPE_UNIVERSE
	case route.RouteScopeLink:
		return netlink.SCOPE_LINK
	case route.RouteScopeHost:
		return netlink.SCOPE_HOST
	default:
		return netlink.SCOPE_NOWHERE
	}
}

func routeProtocolToNetlink(protocol route.RouteProtocol) netlink.RouteProtocol {
	if protocol == route.RouteProtocolStatic {
		return unix.RTPROT_STATIC
	}
	return 0
}
