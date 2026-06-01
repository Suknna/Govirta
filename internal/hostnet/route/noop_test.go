package route

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
)

func TestExplicitMetricAllowsZeroValue(t *testing.T) {
	metric := ExplicitMetric(0)
	if !metric.Set {
		t.Fatal("ExplicitMetric(0) must mark the metric as explicitly set")
	}
	if metric.Value != 0 {
		t.Fatalf("metric value = %d, want 0", metric.Value)
	}
}

func TestMetricFilterAllowsExplicitZeroValue(t *testing.T) {
	filter := FilterMetric(0)
	if filter.Mode != MetricValue {
		t.Fatalf("filter mode = %q, want %q", filter.Mode, MetricValue)
	}
	if filter.Value != 0 {
		t.Fatalf("filter value = %d, want 0", filter.Value)
	}
}

func TestNoopManagerRejectsNilContext(t *testing.T) {
	manager := NewNoopManager()
	_, err := manager.GetIPv4Forwarding(nil)
	if !errors.Is(err, routeerr.ErrInvalidRequest) {
		t.Fatalf("GetIPv4Forwarding(nil) error = %v, want ErrInvalidRequest", err)
	}
}

func TestNoopManagerReturnsCanceledContext(t *testing.T) {
	manager := NewNoopManager()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := manager.GetRoute(ctx, RouteQuery{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GetRoute canceled error = %v, want context.Canceled", err)
	}
}

func TestNoopManagerReturnsUnsupportedForLiveOperations(t *testing.T) {
	manager := NewNoopManager()
	ctx := context.Background()
	operations := []struct {
		name string
		run  func() error
	}{
		{"GetIPv4Forwarding", func() error { _, err := manager.GetIPv4Forwarding(ctx); return err }},
		{"CheckIPv4Forwarding", func() error { _, err := manager.CheckIPv4Forwarding(ctx, IPv4ForwardingEnabled); return err }},
		{"AddRoute", func() error { _, err := manager.AddRoute(ctx, RouteSpec{}); return err }},
		{"ReplaceRoute", func() error { _, err := manager.ReplaceRoute(ctx, RouteSpec{}); return err }},
		{"DeleteRoute", func() error { return manager.DeleteRoute(ctx, RouteSpec{}) }},
		{"ListRoutes", func() error { _, err := manager.ListRoutes(ctx, RouteFilter{}); return err }},
		{"GetRoute", func() error { _, err := manager.GetRoute(ctx, RouteQuery{}); return err }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); !errors.Is(err, routeerr.ErrUnsupported) {
				t.Fatalf("error = %v, want ErrUnsupported", err)
			}
		})
	}
}
