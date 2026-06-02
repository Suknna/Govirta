//go:build linux

package linux

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

func TestEnsureMasqueradeRejectsInvalidRequestsBeforeHandleUse(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	valid := validMasqueradeSpec()
	cases := []struct {
		name    string
		ctx     context.Context
		mutate  func(*firewall.MasqueradeSpec)
		wantErr error
	}{
		{name: "nil context", ctx: nil, wantErr: firewallerr.ErrInvalidRequest},
		{name: "canceled context", ctx: canceledCtx, wantErr: context.Canceled},
		{name: "empty table", mutate: func(spec *firewall.MasqueradeSpec) { spec.TableName = "" }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "unsafe table", mutate: func(spec *firewall.MasqueradeSpec) { spec.TableName = "bad/name" }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "dot relative owner", mutate: func(spec *firewall.MasqueradeSpec) { spec.RuleOwner = ".." }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "invalid guest CIDR", mutate: func(spec *firewall.MasqueradeSpec) { spec.GuestCIDR = netip.Prefix{} }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "IPv6 guest CIDR", mutate: func(spec *firewall.MasqueradeSpec) { spec.GuestCIDR = netip.MustParsePrefix("2001:db8::/64") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "zero guest CIDR", mutate: func(spec *firewall.MasqueradeSpec) { spec.GuestCIDR = netip.MustParsePrefix("0.0.0.0/0") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "empty egress interface", mutate: func(spec *firewall.MasqueradeSpec) { spec.EgressInterfaceName = "" }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "missing priority", mutate: func(spec *firewall.MasqueradeSpec) { spec.Priority = firewall.Priority{} }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "priority name mismatch", mutate: func(spec *firewall.MasqueradeSpec) {
			spec.Priority = firewall.ExplicitPriority(100, firewall.PriorityNameBridgeFilter)
		}, wantErr: firewallerr.ErrInvalidRequest},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			spec := valid
			if tt.mutate != nil {
				tt.mutate(&spec)
			}
			ctx := tt.ctx
			if ctx == nil && tt.name != "nil context" {
				ctx = context.Background()
			}
			fh := &fakeHandle{}
			manager := NewManagerWithHandle(fh)
			_, err := manager.EnsureMasquerade(ctx, spec)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("EnsureMasquerade error = %v, want %v", err, tt.wantErr)
			}
			assertNoHandleCalls(t, fh)
		})
	}
}

func TestEnsureEndpointAntiSpoofingRejectsInvalidRequestsBeforeHandleUse(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	valid := validEndpointAntiSpoofingSpec()
	cases := []struct {
		name    string
		ctx     context.Context
		mutate  func(*firewall.EndpointAntiSpoofingSpec)
		wantErr error
	}{
		{name: "nil context", ctx: nil, wantErr: firewallerr.ErrInvalidRequest},
		{name: "canceled context", ctx: canceledCtx, wantErr: context.Canceled},
		{name: "empty bridge", mutate: func(spec *firewall.EndpointAntiSpoofingSpec) { spec.BridgeName = "" }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "empty TAP", mutate: func(spec *firewall.EndpointAntiSpoofingSpec) { spec.TapName = "" }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "unsafe TAP", mutate: func(spec *firewall.EndpointAntiSpoofingSpec) { spec.TapName = "tap/name" }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "multicast MAC", mutate: func(spec *firewall.EndpointAntiSpoofingSpec) {
			spec.MAC = net.HardwareAddr{0x01, 0x00, 0x5e, 0x00, 0x00, 0x01}
		}, wantErr: firewallerr.ErrInvalidRequest},
		{name: "short MAC", mutate: func(spec *firewall.EndpointAntiSpoofingSpec) {
			spec.MAC = net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x01}
		}, wantErr: firewallerr.ErrInvalidRequest},
		{name: "zero IPv4", mutate: func(spec *firewall.EndpointAntiSpoofingSpec) { spec.IPv4 = netip.MustParseAddr("0.0.0.0") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "limited broadcast IPv4", mutate: func(spec *firewall.EndpointAntiSpoofingSpec) { spec.IPv4 = netip.MustParseAddr("255.255.255.255") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "IPv6 address", mutate: func(spec *firewall.EndpointAntiSpoofingSpec) { spec.IPv4 = netip.MustParseAddr("2001:db8::1") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "missing priority", mutate: func(spec *firewall.EndpointAntiSpoofingSpec) { spec.Priority = firewall.Priority{} }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "priority value mismatch", mutate: func(spec *firewall.EndpointAntiSpoofingSpec) {
			spec.Priority = firewall.ExplicitPriority(-199, firewall.PriorityNameBridgeFilter)
		}, wantErr: firewallerr.ErrInvalidRequest},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			spec := valid
			if tt.mutate != nil {
				tt.mutate(&spec)
			}
			ctx := tt.ctx
			if ctx == nil && tt.name != "nil context" {
				ctx = context.Background()
			}
			fh := &fakeHandle{}
			manager := NewManagerWithHandle(fh)
			_, err := manager.EnsureEndpointAntiSpoofing(ctx, spec)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("EnsureEndpointAntiSpoofing error = %v, want %v", err, tt.wantErr)
			}
			assertNoHandleCalls(t, fh)
		})
	}
}

func TestDeleteMasqueradeRejectsInvalidRequestsBeforeHandleUse(t *testing.T) {
	valid := validMasqueradeRef()
	cases := []struct {
		name    string
		ctx     context.Context
		mutate  func(*firewall.RuleRef)
		wantErr error
	}{
		{name: "nil context", ctx: nil, wantErr: firewallerr.ErrInvalidRequest},
		{name: "canceled context", ctx: canceledContext(), wantErr: context.Canceled},
		{name: "purpose mismatch", mutate: func(ref *firewall.RuleRef) { ref.Purpose = firewall.RulePurposeEndpointAntiSpoofing }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "invalid owner", mutate: func(ref *firewall.RuleRef) { ref.Owner = "bad/name" }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "invalid table", mutate: func(ref *firewall.RuleRef) { ref.TableName = "." }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "invalid chain", mutate: func(ref *firewall.RuleRef) { ref.ChainName = "bad/name" }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "family mismatch", mutate: func(ref *firewall.RuleRef) { ref.Family = firewall.TableFamilyBridge }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "zero handle", mutate: func(ref *firewall.RuleRef) { ref.Handle = 0 }, wantErr: firewallerr.ErrInvalidRequest},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ref := valid
			if tt.mutate != nil {
				tt.mutate(&ref)
			}
			fh := &fakeHandle{}
			manager := NewManagerWithHandle(fh)
			err := manager.DeleteMasquerade(contextForCase(tt.ctx, tt.name), ref)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("DeleteMasquerade error = %v, want %v", err, tt.wantErr)
			}
			assertNoHandleCalls(t, fh)
		})
	}
}

func TestDeleteEndpointAntiSpoofingRejectsInvalidRequestsBeforeHandleUse(t *testing.T) {
	valid := validEndpointAntiSpoofingRef()
	cases := []struct {
		name    string
		ctx     context.Context
		mutate  func(*firewall.RuleRef)
		wantErr error
	}{
		{name: "nil context", ctx: nil, wantErr: firewallerr.ErrInvalidRequest},
		{name: "canceled context", ctx: canceledContext(), wantErr: context.Canceled},
		{name: "purpose mismatch", mutate: func(ref *firewall.RuleRef) { ref.Purpose = firewall.RulePurposeMasquerade }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "invalid owner", mutate: func(ref *firewall.RuleRef) { ref.Owner = "bad/name" }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "invalid table", mutate: func(ref *firewall.RuleRef) { ref.TableName = "." }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "invalid chain", mutate: func(ref *firewall.RuleRef) { ref.ChainName = "bad/name" }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "family mismatch", mutate: func(ref *firewall.RuleRef) { ref.Family = firewall.TableFamilyIPv4 }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "zero handle", mutate: func(ref *firewall.RuleRef) { ref.Handle = 0 }, wantErr: firewallerr.ErrInvalidRequest},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ref := valid
			if tt.mutate != nil {
				tt.mutate(&ref)
			}
			fh := &fakeHandle{}
			manager := NewManagerWithHandle(fh)
			err := manager.DeleteEndpointAntiSpoofing(contextForCase(tt.ctx, tt.name), ref)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("DeleteEndpointAntiSpoofing error = %v, want %v", err, tt.wantErr)
			}
			assertNoHandleCalls(t, fh)
		})
	}
}

func TestGetRuleRejectsInvalidRequestsBeforeHandleUse(t *testing.T) {
	valid := firewall.RuleQuery{Ref: validMasqueradeRef()}
	cases := []struct {
		name    string
		ctx     context.Context
		mutate  func(*firewall.RuleQuery)
		wantErr error
	}{
		{name: "context error visible", ctx: canceledContext(), wantErr: context.Canceled},
		{name: "unsupported purpose", mutate: func(query *firewall.RuleQuery) { query.Ref.Purpose = firewall.RulePurpose("unsupported") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "invalid ref", mutate: func(query *firewall.RuleQuery) { query.Ref.Handle = 0 }, wantErr: firewallerr.ErrInvalidRequest},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			query := valid
			if tt.mutate != nil {
				tt.mutate(&query)
			}
			fh := &fakeHandle{}
			manager := NewManagerWithHandle(fh)
			_, err := manager.GetRule(contextForCase(tt.ctx, tt.name), query)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("GetRule error = %v, want %v", err, tt.wantErr)
			}
			assertNoHandleCalls(t, fh)
		})
	}
}

func TestListRulesRejectsInvalidRequestsBeforeHandleUse(t *testing.T) {
	valid := validRuleFilter()
	cases := []struct {
		name    string
		ctx     context.Context
		mutate  func(*firewall.RuleFilter)
		wantErr error
	}{
		{name: "nil context", ctx: nil, wantErr: firewallerr.ErrInvalidRequest},
		{name: "context error visible", ctx: canceledContext(), wantErr: context.Canceled},
		{name: "missing owner mode", mutate: func(filter *firewall.RuleFilter) { filter.Owner = firewall.OwnerFilter{} }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "unsupported owner mode", mutate: func(filter *firewall.RuleFilter) { filter.Owner.Mode = firewall.OwnerFilterMode("unsupported") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "owner value mode empty", mutate: func(filter *firewall.RuleFilter) { filter.Owner = firewall.FilterOwner("") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "owner value mode illegal", mutate: func(filter *firewall.RuleFilter) { filter.Owner = firewall.FilterOwner("bad/name") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "owner any carries value", mutate: func(filter *firewall.RuleFilter) {
			filter.Owner = firewall.OwnerFilter{Mode: firewall.OwnerAny, Value: "vm-1"}
		}, wantErr: firewallerr.ErrInvalidRequest},
		{name: "missing purpose mode", mutate: func(filter *firewall.RuleFilter) { filter.Purpose = firewall.PurposeFilter{} }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "unsupported purpose mode", mutate: func(filter *firewall.RuleFilter) { filter.Purpose.Mode = firewall.PurposeFilterMode("unsupported") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "purpose value mode empty", mutate: func(filter *firewall.RuleFilter) { filter.Purpose = firewall.FilterPurpose("") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "purpose value mode illegal", mutate: func(filter *firewall.RuleFilter) { filter.Purpose = firewall.FilterPurpose("unsupported") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "purpose any carries value", mutate: func(filter *firewall.RuleFilter) {
			filter.Purpose = firewall.PurposeFilter{Mode: firewall.PurposeAny, Value: firewall.RulePurposeMasquerade}
		}, wantErr: firewallerr.ErrInvalidRequest},
		{name: "missing family mode", mutate: func(filter *firewall.RuleFilter) { filter.Family = firewall.FamilyFilter{} }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "unsupported family mode", mutate: func(filter *firewall.RuleFilter) { filter.Family.Mode = firewall.FamilyFilterMode("unsupported") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "family value mode empty", mutate: func(filter *firewall.RuleFilter) { filter.Family = firewall.FilterFamily("") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "family value mode illegal", mutate: func(filter *firewall.RuleFilter) { filter.Family = firewall.FilterFamily("inet") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "family any carries value", mutate: func(filter *firewall.RuleFilter) {
			filter.Family = firewall.FamilyFilter{Mode: firewall.FamilyAny, Value: firewall.TableFamilyIPv4}
		}, wantErr: firewallerr.ErrInvalidRequest},
		{name: "missing table mode", mutate: func(filter *firewall.RuleFilter) { filter.Table = firewall.TableFilter{} }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "unsupported table mode", mutate: func(filter *firewall.RuleFilter) { filter.Table.Mode = firewall.TableFilterMode("unsupported") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "table value mode empty", mutate: func(filter *firewall.RuleFilter) { filter.Table = firewall.FilterTable("") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "table value mode illegal", mutate: func(filter *firewall.RuleFilter) { filter.Table = firewall.FilterTable("bad/name") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "table any carries value", mutate: func(filter *firewall.RuleFilter) {
			filter.Table = firewall.TableFilter{Mode: firewall.TableAny, Value: "govirta_nat"}
		}, wantErr: firewallerr.ErrInvalidRequest},
		{name: "missing chain mode", mutate: func(filter *firewall.RuleFilter) { filter.Chain = firewall.ChainFilter{} }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "unsupported chain mode", mutate: func(filter *firewall.RuleFilter) { filter.Chain.Mode = firewall.ChainFilterMode("unsupported") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "chain value mode empty", mutate: func(filter *firewall.RuleFilter) { filter.Chain = firewall.FilterChain("") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "chain value mode illegal", mutate: func(filter *firewall.RuleFilter) { filter.Chain = firewall.FilterChain("bad/name") }, wantErr: firewallerr.ErrInvalidRequest},
		{name: "chain any carries value", mutate: func(filter *firewall.RuleFilter) {
			filter.Chain = firewall.ChainFilter{Mode: firewall.ChainAny, Value: "postrouting"}
		}, wantErr: firewallerr.ErrInvalidRequest},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			filter := valid
			if tt.mutate != nil {
				tt.mutate(&filter)
			}
			fh := &fakeHandle{}
			manager := NewManagerWithHandle(fh)
			_, err := manager.ListRules(contextForCase(tt.ctx, tt.name), filter)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ListRules error = %v, want %v", err, tt.wantErr)
			}
			assertNoHandleCalls(t, fh)
		})
	}
}

func validMasqueradeSpec() firewall.MasqueradeSpec {
	return firewall.MasqueradeSpec{
		TableName:           "govirta_nat",
		ChainName:           "postrouting",
		RuleOwner:           "vm-1",
		GuestCIDR:           netip.MustParsePrefix("192.0.2.0/24"),
		EgressInterfaceName: "eth0",
		Priority:            firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT),
	}
}

func validEndpointAntiSpoofingSpec() firewall.EndpointAntiSpoofingSpec {
	return firewall.EndpointAntiSpoofingSpec{
		TableName:  "govirta_bridge",
		ChainName:  "forward",
		RuleOwner:  "vm-1",
		BridgeName: "br0",
		TapName:    "tap0",
		MAC:        net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01},
		IPv4:       netip.MustParseAddr("192.0.2.10"),
		Priority:   firewall.ExplicitPriority(-200, firewall.PriorityNameBridgeFilter),
	}
}

func validMasqueradeRef() firewall.RuleRef {
	return firewall.RuleRef{
		Owner:     "vm-1",
		Purpose:   firewall.RulePurposeMasquerade,
		Family:    firewall.TableFamilyIPv4,
		TableName: "govirta_nat",
		ChainName: "postrouting",
		Handle:    1,
	}
}

func validEndpointAntiSpoofingRef() firewall.RuleRef {
	return firewall.RuleRef{
		Owner:     "vm-1",
		Purpose:   firewall.RulePurposeEndpointAntiSpoofing,
		Family:    firewall.TableFamilyBridge,
		TableName: "govirta_bridge",
		ChainName: "forward",
		Handle:    1,
	}
}

func validRuleFilter() firewall.RuleFilter {
	return firewall.RuleFilter{
		Owner:   firewall.AnyOwner(),
		Purpose: firewall.AnyPurpose(),
		Family:  firewall.AnyFamily(),
		Table:   firewall.AnyTable(),
		Chain:   firewall.AnyChain(),
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func contextForCase(ctx context.Context, name string) context.Context {
	if ctx == nil && name != "nil context" {
		return context.Background()
	}
	return ctx
}

func assertNoHandleCalls(t *testing.T, fh *fakeHandle) {
	t.Helper()
	if len(fh.calls) != 0 {
		t.Fatalf("handle calls = %v, want none", fh.calls)
	}
}
