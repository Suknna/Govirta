//go:build acceptance && linux

package acceptance

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	hostlink "github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/route"
	routelinux "github.com/suknna/govirta/internal/hostnet/route/linux"
	"github.com/vishvananda/netlink"
)

func TestHostnetRoutePrimitives(t *testing.T) {
	requireHostnetAcceptanceEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	const dummyLinkName = "gvrt0"
	manager := routelinux.NewManager()
	if err := cleanupDummyLink(dummyLinkName); err != nil {
		t.Fatalf("initial cleanup dummy link %q: %v", dummyLinkName, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := cleanupRouteMetrics(cleanupCtx, manager, dummyLinkName, 100, 200); err != nil {
			t.Errorf("cleanup routes for %q: %v", dummyLinkName, err)
		}
		if err := cleanupDummyLink(dummyLinkName); err != nil {
			t.Errorf("cleanup dummy link %q: %v", dummyLinkName, err)
		}
	})

	dummy := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: dummyLinkName}}
	if err := netlink.LinkAdd(dummy); err != nil {
		t.Fatalf("create dummy link %q: %v", dummyLinkName, err)
	}
	observedDummy, err := netlink.LinkByName(dummyLinkName)
	if err != nil {
		t.Fatalf("lookup dummy link %q: %v", dummyLinkName, err)
	}
	if err := netlink.LinkSetUp(observedDummy); err != nil {
		t.Fatalf("set dummy link %q up: %v", dummyLinkName, err)
	}

	if _, err := manager.CheckIPv4Forwarding(ctx, route.IPv4ForwardingEnabled); err != nil {
		logRouteDiagnostics(t, ctx, "198.51.100.10", dummyLinkName)
		t.Fatalf("check IPv4 forwarding enabled: %v", err)
	}

	directRoute := routeSpec(dummyLinkName, 100)
	if _, err := manager.AddRoute(ctx, directRoute); err != nil {
		logRouteDiagnostics(t, ctx, "198.51.100.10", dummyLinkName)
		t.Fatalf("add direct route: %v", err)
	}
	assertRouteCount(t, ctx, manager, routeFilter(dummyLinkName, route.FilterMetric(100)), 1)

	selected, err := manager.GetRoute(ctx, route.RouteQuery{Family: route.FamilyIPv4, Destination: netip.MustParseAddr("198.51.100.10")})
	if err != nil {
		logRouteDiagnostics(t, ctx, "198.51.100.10", dummyLinkName)
		t.Fatalf("get route for probe: %v", err)
	}
	if selected.LinkName != hostlink.Name(dummyLinkName) {
		logRouteDiagnostics(t, ctx, "198.51.100.10", dummyLinkName)
		t.Fatalf("selected route link = %q, want %q", selected.LinkName, dummyLinkName)
	}

	metric200 := routeSpec(dummyLinkName, 200)
	if _, err := manager.ReplaceRoute(ctx, metric200); err != nil {
		logRouteDiagnostics(t, ctx, "198.51.100.10", dummyLinkName)
		t.Fatalf("replace direct route metric: %v", err)
	}
	assertRouteCount(t, ctx, manager, routeFilter(dummyLinkName, route.FilterMetric(100)), 0)
	assertRouteCount(t, ctx, manager, routeFilter(dummyLinkName, route.FilterMetric(200)), 1)

	if err := manager.DeleteRoute(ctx, metric200); err != nil {
		logRouteDiagnostics(t, ctx, "198.51.100.10", dummyLinkName)
		t.Fatalf("delete direct route metric 200: %v", err)
	}
	if err := manager.DeleteRoute(ctx, metric200); err != nil {
		logRouteDiagnostics(t, ctx, "198.51.100.10", dummyLinkName)
		t.Fatalf("delete direct route metric 200 again: %v", err)
	}
	assertRouteCount(t, ctx, manager, routeFilter(dummyLinkName, route.AnyMetric()), 0)
}

func routeSpec(linkName string, metric uint32) route.RouteSpec {
	return route.RouteSpec{
		Family: route.FamilyIPv4,
		Destination: route.Destination{
			Mode: route.DestinationCIDR,
			CIDR: netip.MustParsePrefix("198.51.100.0/24"),
		},
		LinkName: hostlink.Name(linkName),
		Gateway:  route.Gateway{Mode: route.GatewayNone},
		Table:    route.RouteTableMain,
		Type:     route.RouteTypeUnicast,
		Scope:    route.RouteScopeLink,
		Protocol: route.RouteProtocolStatic,
		Metric:   route.ExplicitMetric(metric),
	}
}

func routeFilter(linkName string, metric route.MetricFilter) route.RouteFilter {
	return route.RouteFilter{
		Family:      route.FamilyIPv4,
		Table:       route.RouteTableMain,
		Link:        route.LinkFilter{Mode: route.LinkName, Name: hostlink.Name(linkName)},
		Destination: route.Destination{Mode: route.DestinationCIDR, CIDR: netip.MustParsePrefix("198.51.100.0/24")},
		Gateway:     route.Gateway{Mode: route.GatewayNone},
		Metric:      metric,
	}
}

func assertRouteCount(t *testing.T, ctx context.Context, manager route.Manager, filter route.RouteFilter, want int) {
	t.Helper()

	routes, err := manager.ListRoutes(ctx, filter)
	if err != nil {
		logRouteDiagnostics(t, ctx, "198.51.100.10", string(filter.Link.Name))
		t.Fatalf("list routes with filter %+v: %v", filter, err)
	}
	if len(routes) != want {
		logRouteDiagnostics(t, ctx, "198.51.100.10", string(filter.Link.Name))
		t.Fatalf("route count with filter %+v = %d, want %d: %+v", filter, len(routes), want, routes)
	}
}

func cleanupRouteMetrics(ctx context.Context, manager route.Manager, linkName string, metrics ...uint32) error {
	var cleanupErrs []error
	for _, metric := range metrics {
		if err := manager.DeleteRoute(ctx, routeSpec(linkName, metric)); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	return errors.Join(cleanupErrs...)
}

func cleanupDummyLink(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		var notFound netlink.LinkNotFoundError
		if errors.As(err, &notFound) {
			return nil
		}
		return err
	}

	return netlink.LinkDel(link)
}
