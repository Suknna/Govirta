//go:build linux

package linux

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/route"
	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
)

func TestAddRouteDirectRouteReturnsObservedRouteInfo(t *testing.T) {
	manager, _ := newRouteTestManager()
	spec := validRouteSpec()

	info, err := manager.AddRoute(context.Background(), spec)
	if err != nil {
		t.Fatalf("AddRoute error = %v, want nil", err)
	}
	assertRouteInfoMatchesSpec(t, info, spec)
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
