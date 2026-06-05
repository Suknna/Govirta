//go:build linux

package linux

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/suknna/govirta/pkg/hostnet/link"
	"github.com/suknna/govirta/pkg/hostnet/route"
	"github.com/suknna/govirta/pkg/hostnet/route/routeerr"
	"github.com/vishvananda/netlink"
)

func TestAddRouteDirectRouteReturnsObservedRouteInfo(t *testing.T) {
	manager, fake := newRouteTestManager()
	spec := validRouteSpec()

	info, err := manager.AddRoute(context.Background(), spec)
	if err != nil {
		t.Fatalf("AddRoute error = %v, want nil", err)
	}
	assertRouteInfoMatchesSpec(t, info, spec)
	if fake.lastAddedRouteFamily != netlink.FAMILY_V4 {
		t.Fatalf("netlink route family = %d, want FAMILY_V4", fake.lastAddedRouteFamily)
	}
	if info.Family != route.FamilyIPv4 {
		t.Fatalf("observed family = %q, want ipv4", info.Family)
	}
}

func TestAddRouteGatewayRouteReturnsObservedRouteInfo(t *testing.T) {
	manager, _ := newRouteTestManager()
	spec := gatewayRouteSpec()

	info, err := manager.AddRoute(context.Background(), spec)
	if err != nil {
		t.Fatalf("AddRoute error = %v, want nil", err)
	}
	assertRouteInfoMatchesSpec(t, info, spec)
}

func TestAddRouteDefaultRouteReturnsDestinationDefault(t *testing.T) {
	manager, _ := newRouteTestManager()
	spec := defaultRouteSpec()

	info, err := manager.AddRoute(context.Background(), spec)
	if err != nil {
		t.Fatalf("AddRoute error = %v, want nil", err)
	}
	if info.Destination.Mode != route.DestinationDefault {
		t.Fatalf("destination mode = %q, want default", info.Destination.Mode)
	}
	assertRouteInfoMatchesSpec(t, info, spec)
}

func TestAddRouteDuplicateReturnsAlreadyExists(t *testing.T) {
	manager, _ := newRouteTestManager()
	spec := validRouteSpec()
	if _, err := manager.AddRoute(context.Background(), spec); err != nil {
		t.Fatalf("first AddRoute error = %v, want nil", err)
	}

	_, err := manager.AddRoute(context.Background(), spec)
	if !errors.Is(err, routeerr.ErrAlreadyExists) {
		t.Fatalf("second AddRoute error = %v, want ErrAlreadyExists", err)
	}
}

func TestReplaceRouteMissingAddsRoute(t *testing.T) {
	manager, fake := newRouteTestManager()
	spec := validRouteSpec()

	info, err := manager.ReplaceRoute(context.Background(), spec)
	if err != nil {
		t.Fatalf("ReplaceRoute error = %v, want nil", err)
	}
	assertRouteInfoMatchesSpec(t, info, spec)
	if len(fake.routes) != 1 {
		t.Fatalf("route count = %d, want 1", len(fake.routes))
	}
}

func TestReplaceRouteExistingReplacesMetric(t *testing.T) {
	manager, fake := newRouteTestManager()
	spec := validRouteSpec()
	if _, err := manager.AddRoute(context.Background(), spec); err != nil {
		t.Fatalf("AddRoute error = %v, want nil", err)
	}
	replacement := spec
	replacement.Metric = route.ExplicitMetric(200)

	info, err := manager.ReplaceRoute(context.Background(), replacement)
	if err != nil {
		t.Fatalf("ReplaceRoute error = %v, want nil", err)
	}
	assertRouteInfoMatchesSpec(t, info, replacement)
	if len(fake.routes) != 1 {
		t.Fatalf("route count = %d, want 1", len(fake.routes))
	}
	if fake.routes[0].Priority != 200 {
		t.Fatalf("remaining metric = %d, want 200", fake.routes[0].Priority)
	}
	if !slices.Contains(fake.calls, "RouteDel") {
		t.Fatalf("calls = %#v, want manager cleanup to call RouteDel", fake.calls)
	}
	if len(fake.addedRoutes) < 2 || fake.addedRoutes[len(fake.addedRoutes)-2].Priority != 100 || fake.addedRoutes[len(fake.addedRoutes)-1].Priority != 200 {
		t.Fatalf("added routes = %#v, want fake to preserve replace target before manager cleanup", fake.addedRoutes)
	}
}

func TestReplaceRouteCleanupFailureReturnsError(t *testing.T) {
	manager, fake := newRouteTestManager()
	spec := validRouteSpec()
	if _, err := manager.AddRoute(context.Background(), spec); err != nil {
		t.Fatalf("AddRoute error = %v, want nil", err)
	}
	fake.routeDelErr = errors.New("cleanup failed")
	replacement := spec
	replacement.Metric = route.ExplicitMetric(200)

	_, err := manager.ReplaceRoute(context.Background(), replacement)
	if err == nil || !errors.Is(err, fake.routeDelErr) {
		t.Fatalf("ReplaceRoute error = %v, want cleanup failure", err)
	}
}

func TestDeleteRouteDeletesExactRoute(t *testing.T) {
	manager, fake := newRouteTestManager()
	spec := validRouteSpec()
	if _, err := manager.AddRoute(context.Background(), spec); err != nil {
		t.Fatalf("AddRoute error = %v, want nil", err)
	}

	if err := manager.DeleteRoute(context.Background(), spec); err != nil {
		t.Fatalf("DeleteRoute error = %v, want nil", err)
	}
	if len(fake.routes) != 0 {
		t.Fatalf("route count = %d, want 0", len(fake.routes))
	}
}

func TestDeleteRouteMissingReturnsNil(t *testing.T) {
	manager, _ := newRouteTestManager()

	if err := manager.DeleteRoute(context.Background(), validRouteSpec()); err != nil {
		t.Fatalf("DeleteRoute error = %v, want nil", err)
	}
}

func TestDeleteRouteDoesNotDeleteSameDestinationDifferentMetric(t *testing.T) {
	manager, fake := newRouteTestManager()
	spec := validRouteSpec()
	stored := spec
	stored.Metric = route.ExplicitMetric(200)
	if _, err := manager.AddRoute(context.Background(), stored); err != nil {
		t.Fatalf("AddRoute error = %v, want nil", err)
	}

	if err := manager.DeleteRoute(context.Background(), spec); err != nil {
		t.Fatalf("DeleteRoute error = %v, want nil", err)
	}
	if len(fake.routes) != 1 || fake.routes[0].Priority != 200 {
		t.Fatalf("routes = %#v, want metric 200 preserved", fake.routes)
	}
}

func TestDeleteRouteDoesNotDeleteDifferentExactIdentityFields(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, manager *Manager, fake *fakeHandle, spec route.RouteSpec)
	}{
		{name: "gateway", setup: func(t *testing.T, manager *Manager, _ *fakeHandle, _ route.RouteSpec) {
			t.Helper()
			stored := gatewayRouteSpec()
			stored.Gateway = route.Gateway{Mode: route.GatewayIPv4, Addr: netip.MustParseAddr("192.168.100.254")}
			if _, err := manager.AddRoute(context.Background(), stored); err != nil {
				t.Fatalf("AddRoute error = %v, want nil", err)
			}
		}},
		{name: "link", setup: func(t *testing.T, manager *Manager, _ *fakeHandle, spec route.RouteSpec) {
			t.Helper()
			stored := spec
			stored.LinkName = link.Name("gvbr0")
			if _, err := manager.AddRoute(context.Background(), stored); err != nil {
				t.Fatalf("AddRoute error = %v, want nil", err)
			}
		}},
		{name: "scope", setup: func(t *testing.T, _ *Manager, fake *fakeHandle, spec route.RouteSpec) {
			t.Helper()
			nlRoute, err := netlinkRouteForSpec(fake, spec)
			if err != nil {
				t.Fatalf("netlinkRouteForSpec error = %v, want nil", err)
			}
			nlRoute.Scope = netlink.SCOPE_UNIVERSE
			fake.routes = append(fake.routes, nlRoute)
		}},
		{name: "protocol", setup: func(t *testing.T, _ *Manager, fake *fakeHandle, spec route.RouteSpec) {
			t.Helper()
			nlRoute, err := netlinkRouteForSpec(fake, spec)
			if err != nil {
				t.Fatalf("netlinkRouteForSpec error = %v, want nil", err)
			}
			nlRoute.Protocol = netlink.RouteProtocol(2)
			fake.routes = append(fake.routes, nlRoute)
		}},
		{name: "destination", setup: func(t *testing.T, manager *Manager, _ *fakeHandle, spec route.RouteSpec) {
			t.Helper()
			stored := spec
			stored.Destination = route.Destination{Mode: route.DestinationCIDR, CIDR: netip.MustParsePrefix("203.0.113.0/24")}
			if _, err := manager.AddRoute(context.Background(), stored); err != nil {
				t.Fatalf("AddRoute error = %v, want nil", err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, fake := newRouteTestManager()
			deleteSpec := validRouteSpec()
			if tt.name == "gateway" {
				deleteSpec = gatewayRouteSpec()
			}
			tt.setup(t, manager, fake, deleteSpec)

			if err := manager.DeleteRoute(context.Background(), deleteSpec); err != nil {
				t.Fatalf("DeleteRoute error = %v, want nil", err)
			}
			if len(fake.routes) != 1 {
				t.Fatalf("route count = %d, want stored route preserved", len(fake.routes))
			}
		})
	}
}

func TestAddRouteLinkByNameMissingReturnsNotFound(t *testing.T) {
	fake := newFakeHandle()
	manager := NewManagerWithHandle(fake, fakeForwardingReader{value: "1\n"})

	_, err := manager.AddRoute(context.Background(), validRouteSpec())
	if !errors.Is(err, routeerr.ErrNotFound) {
		t.Fatalf("AddRoute error = %v, want ErrNotFound", err)
	}
}

func newRouteTestManager() (*Manager, *fakeHandle) {
	fake := newFakeHandle()
	fake.addLink("gvrt0", 10)
	fake.addLink("gvbr0", 11)
	fake.addLink("eth0", 12)
	return NewManagerWithHandle(fake, fakeForwardingReader{value: "1\n"}), fake
}

func gatewayRouteSpec() route.RouteSpec {
	spec := validRouteSpec()
	spec.Destination = route.Destination{Mode: route.DestinationCIDR, CIDR: netip.MustParsePrefix("10.0.0.0/8")}
	spec.LinkName = link.Name("gvbr0")
	spec.Gateway = route.Gateway{Mode: route.GatewayIPv4, Addr: netip.MustParseAddr("192.168.100.1")}
	spec.Scope = route.RouteScopeGlobal
	return spec
}

func defaultRouteSpec() route.RouteSpec {
	spec := gatewayRouteSpec()
	spec.Destination = route.Destination{Mode: route.DestinationDefault}
	spec.LinkName = link.Name("eth0")
	spec.Gateway = route.Gateway{Mode: route.GatewayIPv4, Addr: netip.MustParseAddr("192.168.1.1")}
	return spec
}

func assertRouteInfoMatchesSpec(t *testing.T, info route.RouteInfo, spec route.RouteSpec) {
	t.Helper()
	if !exactRouteMatch(info, spec) {
		t.Fatalf("route info = %#v, want spec %#v", info, spec)
	}
}
