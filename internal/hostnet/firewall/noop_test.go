package firewall_test

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

func TestExplicitPriorityAllowsZeroValue(t *testing.T) {
	priority := firewall.ExplicitPriority(0, firewall.PriorityNameSrcNAT)
	if !priority.Set {
		t.Fatal("ExplicitPriority(0) must mark the priority as explicitly set")
	}
	if priority.Value != 0 {
		t.Fatalf("priority value = %d, want 0", priority.Value)
	}
	if priority.Name != firewall.PriorityNameSrcNAT {
		t.Fatalf("priority name = %q, want %q", priority.Name, firewall.PriorityNameSrcNAT)
	}
}

func TestNoopManagerReportsUnsupported(t *testing.T) {
	manager := firewall.NewNoopManager()
	ctx := context.Background()
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "EnsureMasquerade", run: func() error { _, err := manager.EnsureMasquerade(ctx, firewall.MasqueradeSpec{}); return err }},
		{name: "DeleteMasquerade", run: func() error { return manager.DeleteMasquerade(ctx, firewall.RuleRef{}) }},
		{name: "EnsureEndpointAntiSpoofing", run: func() error {
			_, err := manager.EnsureEndpointAntiSpoofing(ctx, firewall.EndpointAntiSpoofingSpec{})
			return err
		}},
		{name: "DeleteEndpointAntiSpoofing", run: func() error { return manager.DeleteEndpointAntiSpoofing(ctx, firewall.RuleRef{}) }},
		{name: "GetRule", run: func() error { _, err := manager.GetRule(ctx, firewall.RuleQuery{}); return err }},
		{name: "ListRules", run: func() error { _, err := manager.ListRules(ctx, firewall.RuleFilter{}); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, firewallerr.ErrUnsupported) {
				t.Fatalf("error = %v, want ErrUnsupported", err)
			}
		})
	}
}

func TestNoopManagerRejectsNilContext(t *testing.T) {
	manager := firewall.NewNoopManager()
	var nilCtx context.Context
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "EnsureMasquerade", run: func() error { _, err := manager.EnsureMasquerade(nilCtx, firewall.MasqueradeSpec{}); return err }},
		{name: "DeleteMasquerade", run: func() error { return manager.DeleteMasquerade(nilCtx, firewall.RuleRef{}) }},
		{name: "EnsureEndpointAntiSpoofing", run: func() error {
			_, err := manager.EnsureEndpointAntiSpoofing(nilCtx, firewall.EndpointAntiSpoofingSpec{})
			return err
		}},
		{name: "DeleteEndpointAntiSpoofing", run: func() error { return manager.DeleteEndpointAntiSpoofing(nilCtx, firewall.RuleRef{}) }},
		{name: "GetRule", run: func() error { _, err := manager.GetRule(nilCtx, firewall.RuleQuery{}); return err }},
		{name: "ListRules", run: func() error { _, err := manager.ListRules(nilCtx, firewall.RuleFilter{}); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, firewallerr.ErrInvalidRequest) {
				t.Fatalf("error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestNoopManagerReturnsCanceledContext(t *testing.T) {
	manager := firewall.NewNoopManager()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "EnsureMasquerade", run: func() error { _, err := manager.EnsureMasquerade(ctx, firewall.MasqueradeSpec{}); return err }},
		{name: "DeleteMasquerade", run: func() error { return manager.DeleteMasquerade(ctx, firewall.RuleRef{}) }},
		{name: "EnsureEndpointAntiSpoofing", run: func() error {
			_, err := manager.EnsureEndpointAntiSpoofing(ctx, firewall.EndpointAntiSpoofingSpec{})
			return err
		}},
		{name: "DeleteEndpointAntiSpoofing", run: func() error { return manager.DeleteEndpointAntiSpoofing(ctx, firewall.RuleRef{}) }},
		{name: "GetRule", run: func() error { _, err := manager.GetRule(ctx, firewall.RuleQuery{}); return err }},
		{name: "ListRules", run: func() error { _, err := manager.ListRules(ctx, firewall.RuleFilter{}); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}
