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

func assertNoHandleCalls(t *testing.T, fh *fakeHandle) {
	t.Helper()
	if len(fh.calls) != 0 {
		t.Fatalf("handle calls = %v, want none", fh.calls)
	}
}
