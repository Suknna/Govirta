//go:build linux

package linux

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/route"
	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestListRoutesReturnsSortedRoutesForAnyFilters(t *testing.T) {
	manager, _ := managerWithListRoutes(t)

	infos, err := manager.ListRoutes(context.Background(), anyRouteFilter())
	if err != nil {
		t.Fatalf("ListRoutes error = %v, want nil", err)
	}
	got := routeInfoKeys(infos)
	want := []string{
		"main|cidr/10.0.0.0/8|gvbr0|ipv4/192.168.100.1|0000000050|global|static",
		"main|cidr/198.51.100.0/24|gvrt0|none|0000000000|link|static",
		"main|default|eth0|ipv4/192.168.1.1|0000000100|global|static",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("route keys = %#v, want %#v", got, want)
	}
}

func TestListRoutesAppliesFilters(t *testing.T) {
	manager, _ := managerWithListRoutes(t)
	tests := []struct {
		name   string
		filter route.RouteFilter
		want   int
	}{
		{name: "destination cidr", filter: filterWith(func(f *route.RouteFilter) {
			f.Destination = route.Destination{Mode: route.DestinationCIDR, CIDR: netip.MustParsePrefix("198.51.100.0/24")}
		}), want: 1},
		{name: "destination default", filter: filterWith(func(f *route.RouteFilter) { f.Destination = route.Destination{Mode: route.DestinationDefault} }), want: 1},
		{name: "gateway none", filter: filterWith(func(f *route.RouteFilter) { f.Gateway = route.Gateway{Mode: route.GatewayNone} }), want: 1},
		{name: "gateway ipv4", filter: filterWith(func(f *route.RouteFilter) {
			f.Gateway = route.Gateway{Mode: route.GatewayIPv4, Addr: netip.MustParseAddr("192.168.100.1")}
		}), want: 1},
		{name: "metric zero", filter: filterWith(func(f *route.RouteFilter) { f.Metric = route.FilterMetric(0) }), want: 1},
		{name: "metric hundred", filter: filterWith(func(f *route.RouteFilter) { f.Metric = route.FilterMetric(100) }), want: 1},
		{name: "link name", filter: filterWith(func(f *route.RouteFilter) { f.Link = route.LinkFilter{Mode: route.LinkName, Name: link.Name("gvbr0")} }), want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			infos, err := manager.ListRoutes(context.Background(), tt.filter)
			if err != nil {
				t.Fatalf("ListRoutes error = %v, want nil", err)
			}
			if len(infos) != tt.want {
				t.Fatalf("route count = %d, want %d: %#v", len(infos), tt.want, infos)
			}
		})
	}
}

func TestObservedProtocolMappings(t *testing.T) {
	tests := []struct {
		protocol netlink.RouteProtocol
		want     route.RouteProtocol
		wantErr  error
	}{
		{protocol: 0, want: route.RouteProtocolUnspecified},
		{protocol: unix.RTPROT_KERNEL, want: route.RouteProtocolKernel},
		{protocol: unix.RTPROT_BOOT, want: route.RouteProtocolBoot},
		{protocol: unix.RTPROT_DHCP, want: route.RouteProtocolDHCP},
		{protocol: netlink.RouteProtocol(254), wantErr: routeerr.ErrInvalidObservedState},
	}
	for _, tt := range tests {
		got, err := routeProtocolFromNetlink(tt.protocol)
		if !errors.Is(err, tt.wantErr) {
			t.Fatalf("protocol %d error = %v, want %v", tt.protocol, err, tt.wantErr)
		}
		if err == nil && got != tt.want {
			t.Fatalf("protocol %d = %q, want %q", tt.protocol, got, tt.want)
		}
	}
}

func TestObservedScopeMappings(t *testing.T) {
	tests := []struct {
		scope   netlink.Scope
		want    route.RouteScope
		wantErr error
	}{
		{scope: netlink.SCOPE_UNIVERSE, want: route.RouteScopeGlobal},
		{scope: netlink.SCOPE_LINK, want: route.RouteScopeLink},
		{scope: netlink.SCOPE_HOST, want: route.RouteScopeHost},
		{scope: netlink.Scope(99), wantErr: routeerr.ErrInvalidObservedState},
	}
	for _, tt := range tests {
		got, err := routeScopeFromNetlink(tt.scope)
		if !errors.Is(err, tt.wantErr) {
			t.Fatalf("scope %d error = %v, want %v", tt.scope, err, tt.wantErr)
		}
		if err == nil && got != tt.want {
			t.Fatalf("scope %d = %q, want %q", tt.scope, got, tt.want)
		}
	}
}

func TestObservedTypeMappings(t *testing.T) {
	tests := []struct {
		routeType int
		want      route.RouteType
		wantErr   error
	}{
		{routeType: unix.RTN_UNICAST, want: route.RouteTypeUnicast},
		{routeType: unix.RTN_BLACKHOLE, wantErr: routeerr.ErrUnsupported},
		{routeType: unix.RTN_UNREACHABLE, wantErr: routeerr.ErrUnsupported},
		{routeType: 250, wantErr: routeerr.ErrInvalidObservedState},
	}
	for _, tt := range tests {
		got, err := routeTypeFromNetlink(tt.routeType)
		if !errors.Is(err, tt.wantErr) {
			t.Fatalf("type %d error = %v, want %v", tt.routeType, err, tt.wantErr)
		}
		if err == nil && got != tt.want {
			t.Fatalf("type %d = %q, want %q", tt.routeType, got, tt.want)
		}
	}
}

func TestRouteListFilteredDumpInterruptedReturnsIncompleteList(t *testing.T) {
	manager, fake := newRouteTestManager()
	fake.routeListFilteredErr = netlink.ErrDumpInterrupted

	_, err := manager.ListRoutes(context.Background(), anyRouteFilter())
	if !errors.Is(err, routeerr.ErrIncompleteList) {
		t.Fatalf("ListRoutes error = %v, want ErrIncompleteList", err)
	}
}

func TestGetRouteReturnsFirstRouteWithResolvedLinkName(t *testing.T) {
	manager, fake := managerWithListRoutes(t)
	fake.routeGet = []netlink.Route{fake.routes[2], fake.routes[0]}

	info, err := manager.GetRoute(context.Background(), route.RouteQuery{Family: route.FamilyIPv4, Destination: netip.MustParseAddr("8.8.8.8")})
	if err != nil {
		t.Fatalf("GetRoute error = %v, want nil", err)
	}
	if info.LinkName != link.Name("eth0") {
		t.Fatalf("link name = %q, want eth0", info.LinkName)
	}
}

func TestGetRouteNoRoutesReturnsNotFound(t *testing.T) {
	manager, _ := newRouteTestManager()

	_, err := manager.GetRoute(context.Background(), route.RouteQuery{Family: route.FamilyIPv4, Destination: netip.MustParseAddr("8.8.8.8")})
	if !errors.Is(err, routeerr.ErrNotFound) {
		t.Fatalf("GetRoute error = %v, want ErrNotFound", err)
	}
}

func TestGetRouteLinkByIndexFailureReturnsError(t *testing.T) {
	manager, fake := managerWithListRoutes(t)
	fake.routeGet = []netlink.Route{fake.routes[0]}
	fake.linkByIndexErr = errors.New("boom")

	_, err := manager.GetRoute(context.Background(), route.RouteQuery{Family: route.FamilyIPv4, Destination: netip.MustParseAddr("8.8.8.8")})
	if err == nil || !errors.Is(err, fake.linkByIndexErr) {
		t.Fatalf("GetRoute error = %v, want link failure", err)
	}
}

func managerWithListRoutes(t *testing.T) (*Manager, *fakeHandle) {
	t.Helper()
	manager, _ := newRouteTestManager()
	for _, spec := range []route.RouteSpec{validRouteSpec(), gatewayRouteSpec(), defaultRouteSpec()} {
		if spec.LinkName == "gvbr0" {
			spec.Metric = route.ExplicitMetric(50)
		}
		if spec.LinkName == "gvrt0" {
			spec.Metric = route.ExplicitMetric(0)
		}
		if _, err := manager.AddRoute(context.Background(), spec); err != nil {
			t.Fatalf("AddRoute(%s) error = %v, want nil", spec.LinkName, err)
		}
	}
	return manager, manager.routeHandle().(*fakeHandle)
}

func anyRouteFilter() route.RouteFilter {
	return route.RouteFilter{
		Family:      route.FamilyIPv4,
		Table:       route.RouteTableMain,
		Link:        route.LinkFilter{Mode: route.LinkAny},
		Destination: route.Destination{Mode: route.DestinationAny},
		Gateway:     route.Gateway{Mode: route.GatewayAny},
		Metric:      route.AnyMetric(),
	}
}

func filterWith(edit func(*route.RouteFilter)) route.RouteFilter {
	filter := anyRouteFilter()
	edit(&filter)
	return filter
}

func routeInfoKeys(infos []route.RouteInfo) []string {
	keys := make([]string, 0, len(infos))
	for _, info := range infos {
		keys = append(keys, routeInfoSortKey(info))
	}
	return keys
}

func TestDestinationNilGatewayMappings(t *testing.T) {
	fake := newFakeHandle()
	fake.addLink("eth0", 12)
	info, err := netlinkRouteInfo(fake, netlink.Route{
		LinkIndex: 12,
		Dst:       nil,
		Gw:        nil,
		Table:     unix.RT_TABLE_MAIN,
		Type:      unix.RTN_UNICAST,
		Scope:     netlink.SCOPE_LINK,
		Protocol:  unix.RTPROT_STATIC,
		Priority:  0,
	})
	if err != nil {
		t.Fatalf("netlinkRouteInfo error = %v, want nil", err)
	}
	if info.Destination.Mode != route.DestinationDefault || info.Gateway.Mode != route.GatewayNone {
		t.Fatalf("info = %#v, want default destination and no gateway", info)
	}
}

func TestNetlinkFilterMaskIncludesExpressibleFields(t *testing.T) {
	fake := newFakeHandle()
	fake.addLink("gvrt0", 10)
	filter := route.RouteFilter{
		Family:      route.FamilyIPv4,
		Table:       route.RouteTableMain,
		Link:        route.LinkFilter{Mode: route.LinkName, Name: link.Name("gvrt0")},
		Destination: route.Destination{Mode: route.DestinationCIDR, CIDR: netip.MustParsePrefix("198.51.100.0/24")},
		Gateway:     route.Gateway{Mode: route.GatewayIPv4, Addr: netip.MustParseAddr("192.168.1.1")},
		Metric:      route.FilterMetric(100),
	}

	_, mask, err := netlinkFilterForRouteFilter(fake, filter)
	if err != nil {
		t.Fatalf("netlinkFilterForRouteFilter error = %v, want nil", err)
	}
	want := uint64(netlink.RT_FILTER_TABLE | netlink.RT_FILTER_OIF | netlink.RT_FILTER_DST | netlink.RT_FILTER_GW)
	if mask != want {
		t.Fatalf("mask = %d, want %d", mask, want)
	}
}

func TestMetricFilterUsesGoSideFiltering(t *testing.T) {
	manager, fake := managerWithListRoutes(t)

	infos, err := manager.ListRoutes(context.Background(), filterWith(func(f *route.RouteFilter) { f.Metric = route.FilterMetric(100) }))
	if err != nil {
		t.Fatalf("ListRoutes error = %v, want nil", err)
	}
	if fake.lastRouteListFilterMask&netlink.RT_FILTER_PRIORITY != 0 {
		t.Fatalf("filter mask = %d, want no RT_FILTER_PRIORITY", fake.lastRouteListFilterMask)
	}
	if len(infos) != 1 || infos[0].Metric.Value != 100 {
		t.Fatalf("infos = %#v, want one route with metric 100", infos)
	}
}

func TestGatewayIPv6ObservedReturnsInvalidObservedState(t *testing.T) {
	_, err := gatewayFromNetlink(net.ParseIP("2001:db8::1"))
	if !errors.Is(err, routeerr.ErrInvalidObservedState) {
		t.Fatalf("gatewayFromNetlink error = %v, want ErrInvalidObservedState", err)
	}
}
