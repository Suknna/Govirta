//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/suknna/govirta/internal/hostnet/route"
	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
	"github.com/vishvananda/netlink"
)

// Manager owns Linux host route prerequisites and route lifecycle operations.
type Manager struct {
	handle     handle
	forwarding forwardingReader
}

var _ route.Manager = (*Manager)(nil)

// NewManager returns a Linux route manager backed by procfs forwarding state.
func NewManager() *Manager {
	return NewManagerWithHandle(nil, nil)
}

func NewManagerWithHandle(h handle, forwarding forwardingReader) *Manager {
	if h == nil {
		h = realHandle{}
	}
	if forwarding == nil {
		forwarding = procForwardingReader{}
	}

	return &Manager{handle: h, forwarding: forwarding}
}

func (m *Manager) routeHandle() handle {
	if m == nil || m.handle == nil {
		return realHandle{}
	}

	return m.handle
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

// AddRoute creates a Linux IPv4 route and returns observed kernel state.
func (m *Manager) AddRoute(ctx context.Context, spec route.RouteSpec) (route.RouteInfo, error) {
	return applyRoute(ctx, m.routeHandle(), "add route", spec, func(nlRoute *netlink.Route) error {
		return m.routeHandle().RouteAdd(nlRoute)
	})
}

// ReplaceRoute creates or replaces a Linux IPv4 route and returns observed kernel state.
func (m *Manager) ReplaceRoute(ctx context.Context, spec route.RouteSpec) (route.RouteInfo, error) {
	h := m.routeHandle()
	return applyRoute(ctx, h, "replace route", spec, func(nlRoute *netlink.Route) error {
		if err := h.RouteReplace(nlRoute); err != nil {
			return err
		}
		return cleanupStaleRoutesAfterReplace(h, spec)
	})
}

// DeleteRoute removes the exact Linux IPv4 route selected by spec.
func (m *Manager) DeleteRoute(ctx context.Context, spec route.RouteSpec) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := validateRouteSpec(spec); err != nil {
		return translateError("delete route", err)
	}

	nlRoute, err := netlinkRouteForSpec(m.routeHandle(), spec)
	if err != nil {
		return err
	}
	if err := m.routeHandle().RouteDel(&nlRoute); err != nil {
		translated := translateError("delete route", err)
		if errors.Is(translated, routeerr.ErrNotFound) {
			return nil
		}
		return translated
	}

	return nil
}

// ListRoutes returns observed Linux IPv4 routes matching filter.
func (m *Manager) ListRoutes(ctx context.Context, filter route.RouteFilter) ([]route.RouteInfo, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := validateRouteFilter(filter); err != nil {
		return nil, translateError("list routes", err)
	}

	nlFilter, mask, err := netlinkFilterForRouteFilter(m.routeHandle(), filter)
	if err != nil {
		return nil, err
	}
	nlRoutes, err := m.routeHandle().RouteListFiltered(netlink.FAMILY_V4, &nlFilter, mask)
	if err != nil {
		return nil, translateError("list routes", err)
	}

	infos := make([]route.RouteInfo, 0, len(nlRoutes))
	for _, nlRoute := range nlRoutes {
		info, err := observedRouteInfo(m.routeHandle(), nlRoute)
		if err != nil {
			return nil, translateError("list routes", err)
		}
		if routeInfoMatchesFilter(info, filter) {
			infos = append(infos, info)
		}
	}
	sortRouteInfos(infos)

	return infos, nil
}

// GetRoute asks the Linux kernel for path selection to query.Destination.
func (m *Manager) GetRoute(ctx context.Context, query route.RouteQuery) (route.RouteInfo, error) {
	if err := checkContext(ctx); err != nil {
		return route.RouteInfo{}, err
	}
	if err := validateRouteQuery(query); err != nil {
		return route.RouteInfo{}, translateError("get route", err)
	}

	destination := query.Destination.As4()
	nlRoutes, err := m.routeHandle().RouteGet(net.IPv4(destination[0], destination[1], destination[2], destination[3]))
	if err != nil {
		return route.RouteInfo{}, translateError("get route", err)
	}
	if len(nlRoutes) == 0 {
		return route.RouteInfo{}, translateError("get route", routeerr.ErrNotFound)
	}

	info, err := observedRouteInfo(m.routeHandle(), nlRoutes[0])
	if err != nil {
		return route.RouteInfo{}, translateError("get route", err)
	}

	return info, nil
}
