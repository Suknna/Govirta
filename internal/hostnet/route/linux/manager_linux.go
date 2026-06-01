//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/suknna/govirta/internal/hostnet/route"
	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
)

// Manager reads Linux host route prerequisites.
//
// Route mutation, list, and query methods are intentionally left to the route
// mutation task; this task only owns IPv4 forwarding observations.
type Manager struct {
	forwarding forwardingReader
}

// NewManager returns a Linux route manager backed by procfs forwarding state.
func NewManager() *Manager {
	return &Manager{forwarding: procForwardingReader{}}
}

func (m *Manager) forwardingReader() forwardingReader {
	if m == nil || m.forwarding == nil {
		return procForwardingReader{}
	}

	return m.forwarding
}

// GetIPv4Forwarding returns the observed Linux IPv4 forwarding state.
func (m *Manager) GetIPv4Forwarding(ctx context.Context) (route.IPv4ForwardingInfo, error) {
	if err := checkContext(ctx); err != nil {
		return route.IPv4ForwardingInfo{}, err
	}

	value, err := m.forwardingReader().ReadIPv4Forwarding(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return route.IPv4ForwardingInfo{}, err
		}
		if errors.Is(err, os.ErrNotExist) {
			return route.IPv4ForwardingInfo{}, fmt.Errorf("get IPv4 forwarding: %w: %w", routeerr.ErrUnsupported, err)
		}
		return route.IPv4ForwardingInfo{}, translateError("get IPv4 forwarding", err)
	}

	trimmed := strings.TrimSpace(value)
	switch trimmed {
	case "1":
		return route.IPv4ForwardingInfo{State: route.IPv4ForwardingEnabled, Path: ipv4ForwardingPath}, nil
	case "0":
		return route.IPv4ForwardingInfo{State: route.IPv4ForwardingDisabled, Path: ipv4ForwardingPath}, nil
	default:
		return route.IPv4ForwardingInfo{}, fmt.Errorf("get IPv4 forwarding: observed %q: %w", trimmed, routeerr.ErrInvalidObservedState)
	}
}

// CheckIPv4Forwarding verifies the observed Linux IPv4 forwarding state.
func (m *Manager) CheckIPv4Forwarding(ctx context.Context, expected route.IPv4ForwardingState) (route.IPv4ForwardingInfo, error) {
	if err := checkContext(ctx); err != nil {
		return route.IPv4ForwardingInfo{}, err
	}
	if err := validateForwardingState(expected); err != nil {
		return route.IPv4ForwardingInfo{}, translateError("check IPv4 forwarding", err)
	}

	info, err := m.GetIPv4Forwarding(ctx)
	if err != nil {
		return route.IPv4ForwardingInfo{}, err
	}
	if info.State != expected {
		return info, fmt.Errorf("check IPv4 forwarding: expected %s observed %s; configure IPv4 forwarding with node installation or operations tooling, not internal/hostnet/route: %w", expected, info.State, routeerr.ErrNotReady)
	}

	return info, nil
}
