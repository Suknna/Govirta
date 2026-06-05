package firewall_test

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/pkg/hostnet/firewall"
	"github.com/suknna/govirta/pkg/hostnet/firewall/firewallerr"
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

func TestRuleFilterConstructorsSetExplicitModes(t *testing.T) {
	tests := []struct {
		name string
		got  firewall.RuleFilter
		want firewall.RuleFilter
	}{
		{
			name: "any",
			got: firewall.RuleFilter{
				Owner:   firewall.AnyOwner(),
				Purpose: firewall.AnyPurpose(),
				Family:  firewall.AnyFamily(),
				Table:   firewall.AnyTable(),
				Chain:   firewall.AnyChain(),
			},
			want: firewall.RuleFilter{
				Owner:   firewall.OwnerFilter{Mode: firewall.OwnerAny},
				Purpose: firewall.PurposeFilter{Mode: firewall.PurposeAny},
				Family:  firewall.FamilyFilter{Mode: firewall.FamilyAny},
				Table:   firewall.TableFilter{Mode: firewall.TableAny},
				Chain:   firewall.ChainFilter{Mode: firewall.ChainAny},
			},
		},
		{
			name: "value",
			got: firewall.RuleFilter{
				Owner:   firewall.FilterOwner("vm-1"),
				Purpose: firewall.FilterPurpose(firewall.RulePurposeMasquerade),
				Family:  firewall.FilterFamily(firewall.TableFamilyIPv4),
				Table:   firewall.FilterTable("govirta"),
				Chain:   firewall.FilterChain("postrouting"),
			},
			want: firewall.RuleFilter{
				Owner:   firewall.OwnerFilter{Mode: firewall.OwnerValue, Value: "vm-1"},
				Purpose: firewall.PurposeFilter{Mode: firewall.PurposeValue, Value: firewall.RulePurposeMasquerade},
				Family:  firewall.FamilyFilter{Mode: firewall.FamilyValue, Value: firewall.TableFamilyIPv4},
				Table:   firewall.TableFilter{Mode: firewall.TableValue, Value: "govirta"},
				Chain:   firewall.ChainFilter{Mode: firewall.ChainValue, Value: "postrouting"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("filter = %#v, want %#v", tt.got, tt.want)
			}
		})
	}
}

func TestNoopManagerReportsUnsupported(t *testing.T) {
	manager := firewall.NewNoopManager()
	ctx := context.Background()
	filter := firewall.RuleFilter{
		Owner:   firewall.AnyOwner(),
		Purpose: firewall.AnyPurpose(),
		Family:  firewall.AnyFamily(),
		Table:   firewall.AnyTable(),
		Chain:   firewall.AnyChain(),
	}
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
		{name: "ListRules", run: func() error { _, err := manager.ListRules(ctx, filter); return err }},
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

func TestNoopManagerEnsureForwardAccept(t *testing.T) {
	mgr := firewall.NewNoopManager()

	if _, err := mgr.EnsureForwardAccept(context.Background(), firewall.ForwardAcceptSpec{}); !errors.Is(err, firewallerr.ErrUnsupported) {
		t.Fatalf("EnsureForwardAccept on background context = %v, want ErrUnsupported", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mgr.EnsureForwardAccept(canceled, firewall.ForwardAcceptSpec{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureForwardAccept on canceled context = %v, want context.Canceled", err)
	}

	//nolint:staticcheck // explicitly testing nil-context rejection
	if _, err := mgr.EnsureForwardAccept(nil, firewall.ForwardAcceptSpec{}); !errors.Is(err, firewallerr.ErrInvalidRequest) {
		t.Fatalf("EnsureForwardAccept on nil context = %v, want ErrInvalidRequest", err)
	}
}

func TestNoopManagerDeleteForwardAccept(t *testing.T) {
	mgr := firewall.NewNoopManager()

	if err := mgr.DeleteForwardAccept(context.Background(), firewall.RuleRef{}); !errors.Is(err, firewallerr.ErrUnsupported) {
		t.Fatalf("DeleteForwardAccept on background context = %v, want ErrUnsupported", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mgr.DeleteForwardAccept(canceled, firewall.RuleRef{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteForwardAccept on canceled context = %v, want context.Canceled", err)
	}

	//nolint:staticcheck // explicitly testing nil-context rejection
	if err := mgr.DeleteForwardAccept(nil, firewall.RuleRef{}); !errors.Is(err, firewallerr.ErrInvalidRequest) {
		t.Fatalf("DeleteForwardAccept on nil context = %v, want ErrInvalidRequest", err)
	}
}
