package dhcp_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/suknna/govirta/pkg/hostnet/dhcp"
	"github.com/suknna/govirta/pkg/hostnet/dhcp/dhcperr"
)

func TestNoopManagerRejectsNilContext(t *testing.T) {
	manager := dhcp.NewNoopManager()

	_, err := manager.Start(nil, dhcp.ServerSpec{})
	if !errors.Is(err, dhcperr.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestNoopManagerReturnsUnsupported(t *testing.T) {
	manager := dhcp.NewNoopManager()

	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "Start",
			run: func(ctx context.Context) error {
				_, err := manager.Start(ctx, dhcp.ServerSpec{})
				return err
			},
		},
		{
			name: "Stop",
			run:  func(ctx context.Context) error { return manager.Stop(ctx, dhcp.ServerID("dhcp-a")) },
		},
		{
			name: "ApplyBinding",
			run: func(ctx context.Context) error {
				_, err := manager.ApplyBinding(ctx, dhcp.BindingRequest{})
				return err
			},
		},
		{
			name: "RemoveBinding",
			run:  func(ctx context.Context) error { return manager.RemoveBinding(ctx, dhcp.BindingQuery{}) },
		},
		{
			name: "GetServer",
			run: func(ctx context.Context) error {
				_, err := manager.GetServer(ctx, dhcp.ServerID("dhcp-a"))
				return err
			},
		},
		{
			name: "GetLease",
			run: func(ctx context.Context) error {
				_, err := manager.GetLease(ctx, dhcp.BindingQuery{})
				return err
			},
		},
		{
			name: "ListLeases",
			run: func(ctx context.Context) error {
				_, err := manager.ListLeases(ctx, dhcp.LeaseFilter{})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(context.Background())
			if !errors.Is(err, dhcperr.ErrUnsupported) {
				t.Fatalf("expected ErrUnsupported, got %v", err)
			}
		})
	}
}

func TestNoopManagerReturnsCanceledContext(t *testing.T) {
	manager := dhcp.NewNoopManager()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "write method",
			run: func(ctx context.Context) error {
				_, err := manager.ApplyBinding(ctx, dhcp.BindingRequest{})
				return err
			},
		},
		{
			name: "read method",
			run: func(ctx context.Context) error {
				_, err := manager.GetServer(ctx, dhcp.ServerID("dhcp-a"))
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled, got %v", err)
			}
		})
	}
}

func TestExplicitDHCPOptionModes(t *testing.T) {
	enabled := dhcp.DHCPOptionAddrs{
		Mode:  dhcp.DHCPOptionEnabled,
		Addrs: []netip.Addr{netip.MustParseAddr("192.168.100.1")},
	}
	disabled := dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionDisabled}

	if enabled.Mode == disabled.Mode {
		t.Fatalf("expected distinct option modes")
	}
}
