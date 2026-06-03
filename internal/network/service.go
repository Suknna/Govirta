// Package network is the VM-facing network orchestration layer. It mirrors the
// internal/storage layering: NetworkService/NICService are the VM-facing API,
// netpool.Service is the shared registration core, and the internal/hostnet/*
// primitives are the driver layer. The registration core stores declarative
// logical intent only; observed resource state always comes from the primitives.
package network

import (
	"context"

	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/network/netpool"
)

// NetworkService is the VM-facing API for shared network segments.
type NetworkService struct {
	pools *netpool.Service
}

// NewNetworkService creates a VM-facing network service backed by an explicit
// netpool service.
func NewNetworkService(pools *netpool.Service) *NetworkService {
	return &NetworkService{pools: pools}
}

// RegisterNetwork registers one logical network definition without touching the
// kernel. Callers replay registrations after restart.
func (s *NetworkService) RegisterNetwork(def netpool.NetworkDefinition) error {
	return s.pools.RegisterNetwork(def)
}

// EnsureNetwork reconciles the host primitives for one registered network.
func (s *NetworkService) EnsureNetwork(ctx context.Context, name netpool.NetworkName) (netpool.NetworkStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NetworkStatus{}, err
	}
	return s.pools.EnsureNetwork(ctx, name)
}

// DeleteNetwork tears down one registered network; it fails if NICs remain. The
// masquerade and forward rule references identify the firewall rules to remove
// and are forwarded to the netpool core unchanged.
func (s *NetworkService) DeleteNetwork(ctx context.Context, name netpool.NetworkName, masqueradeRef firewall.RuleRef, forwardRef firewall.RuleRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.pools.DeleteNetwork(ctx, name, masqueradeRef, forwardRef)
}

// GetNetworkStatus returns the observed live state aggregated from primitives.
func (s *NetworkService) GetNetworkStatus(ctx context.Context, name netpool.NetworkName) (netpool.NetworkStatus, error) {
	if err := ctx.Err(); err != nil {
		return netpool.NetworkStatus{}, err
	}
	return s.pools.GetNetworkStatus(ctx, name)
}

// ListNetworks returns registered network definitions (clones).
func (s *NetworkService) ListNetworks(ctx context.Context) ([]netpool.NetworkDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.pools.ListNetworks(ctx)
}
