package network

import (
	"context"

	"github.com/suknna/govirta/internal/network/netpool"
)

// NICService is the VM-facing API for per-VM network interfaces. It shares the
// same netpool registration core as NetworkService.
type NICService struct {
	pools *netpool.Service
}

// NewNICService creates a VM-facing NIC service backed by an explicit netpool
// service. It shares the same registration core as NetworkService.
func NewNICService(pools *netpool.Service) *NICService {
	return &NICService{pools: pools}
}

// RegisterNIC registers one logical NIC definition without touching the kernel.
func (s *NICService) RegisterNIC(def netpool.NICDefinition) error {
	return s.pools.RegisterNIC(def)
}

// EnsureNIC reconciles the host primitives for one registered NIC.
func (s *NICService) EnsureNIC(ctx context.Context, networkName netpool.NetworkName, vmID netpool.VMID) (netpool.NICStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NICStatus{}, err
	}
	return s.pools.EnsureNIC(ctx, networkName, vmID)
}

// DeleteNIC tears down one registered NIC in reverse dependency order. The
// anti-spoofing rule ref is resolved live inside the netpool core, so callers
// need no firewall handle to drive teardown.
func (s *NICService) DeleteNIC(ctx context.Context, networkName netpool.NetworkName, vmID netpool.VMID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.pools.DeleteNIC(ctx, networkName, vmID)
}

// GetNICStatus returns the observed live NIC state aggregated from primitives.
func (s *NICService) GetNICStatus(ctx context.Context, networkName netpool.NetworkName, vmID netpool.VMID) (netpool.NICStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NICStatus{}, err
	}
	return s.pools.GetNICStatus(ctx, networkName, vmID)
}
