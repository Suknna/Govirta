//go:build linux

package linux

import (
	"fmt"
	"net"

	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
	"github.com/vishvananda/netlink"
)

type fakeHandle struct {
	linksByName  map[string]netlink.Link
	linksByIndex map[int]netlink.Link
	routes       []netlink.Route
	routeGet     []netlink.Route
	calls        []string

	linkByNameErr           error
	linkByIndexErr          error
	routeAddErr             error
	routeReplaceErr         error
	routeDelErr             error
	routeListFilteredErr    error
	routeGetErr             error
	lastRouteListFilterMask uint64
}

func newFakeHandle() *fakeHandle {
	return &fakeHandle{
		linksByName:  make(map[string]netlink.Link),
		linksByIndex: make(map[int]netlink.Link),
	}
}

func (f *fakeHandle) addLink(name string, index int) {
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name, Index: index}}
	f.linksByName[name] = link
	f.linksByIndex[index] = link
}

func (f *fakeHandle) LinkByIndex(index int) (netlink.Link, error) {
	f.calls = append(f.calls, fmt.Sprintf("LinkByIndex:%d", index))
	if f.linkByIndexErr != nil {
		return nil, f.linkByIndexErr
	}
	link, ok := f.linksByIndex[index]
	if !ok {
		return nil, fmt.Errorf("link index %d: %w", index, routeerr.ErrNotFound)
	}
	return link, nil
}

func (f *fakeHandle) LinkByName(name string) (netlink.Link, error) {
	f.calls = append(f.calls, "LinkByName:"+name)
	if f.linkByNameErr != nil {
		return nil, f.linkByNameErr
	}
	link, ok := f.linksByName[name]
	if !ok {
		return nil, fmt.Errorf("link name %s: %w", name, routeerr.ErrNotFound)
	}
	return link, nil
}

func (f *fakeHandle) RouteAdd(route *netlink.Route) error {
	f.calls = append(f.calls, "RouteAdd")
	if f.routeAddErr != nil {
		return f.routeAddErr
	}
	for _, existing := range f.routes {
		if sameRouteIdentity(existing, *route) {
			return fmt.Errorf("route exists: %w", routeerr.ErrAlreadyExists)
		}
	}
	f.routes = append(f.routes, cloneRoute(*route))
	return nil
}

func (f *fakeHandle) RouteReplace(route *netlink.Route) error {
	f.calls = append(f.calls, "RouteReplace")
	if f.routeReplaceErr != nil {
		return f.routeReplaceErr
	}
	next := f.routes[:0]
	for _, existing := range f.routes {
		if sameRouteSelector(existing, *route) {
			continue
		}
		next = append(next, existing)
	}
	f.routes = append(next, cloneRoute(*route))
	return nil
}

func (f *fakeHandle) RouteDel(route *netlink.Route) error {
	f.calls = append(f.calls, "RouteDel")
	if f.routeDelErr != nil {
		return f.routeDelErr
	}
	next := f.routes[:0]
	deleted := false
	for _, existing := range f.routes {
		if sameRouteIdentity(existing, *route) {
			deleted = true
			continue
		}
		next = append(next, existing)
	}
	f.routes = next
	if !deleted {
		return fmt.Errorf("route missing: %w", routeerr.ErrNotFound)
	}
	return nil
}

func (f *fakeHandle) RouteListFiltered(_ int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error) {
	f.calls = append(f.calls, "RouteListFiltered")
	f.lastRouteListFilterMask = filterMask
	if f.routeListFilteredErr != nil {
		return nil, f.routeListFilteredErr
	}
	var out []netlink.Route
	for _, candidate := range f.routes {
		if routeMatchesNetlinkFilter(candidate, *filter, filterMask) {
			out = append(out, cloneRoute(candidate))
		}
	}
	return out, nil
}

func (f *fakeHandle) RouteGet(destination net.IP) ([]netlink.Route, error) {
	f.calls = append(f.calls, "RouteGet:"+destination.String())
	if f.routeGetErr != nil {
		return nil, f.routeGetErr
	}
	if f.routeGet != nil {
		out := make([]netlink.Route, 0, len(f.routeGet))
		for _, route := range f.routeGet {
			out = append(out, cloneRoute(route))
		}
		return out, nil
	}
	return nil, nil
}

func cloneRoute(route netlink.Route) netlink.Route {
	cloned := route
	if route.Dst != nil {
		cloned.Dst = &net.IPNet{IP: append(net.IP(nil), route.Dst.IP...), Mask: append(net.IPMask(nil), route.Dst.Mask...)}
	}
	if route.Gw != nil {
		cloned.Gw = append(net.IP(nil), route.Gw...)
	}
	return cloned
}

func routeMatchesNetlinkFilter(candidate, filter netlink.Route, mask uint64) bool {
	if mask&netlink.RT_FILTER_TABLE != 0 && candidate.Table != filter.Table {
		return false
	}
	if mask&netlink.RT_FILTER_OIF != 0 && candidate.LinkIndex != filter.LinkIndex {
		return false
	}
	if mask&netlink.RT_FILTER_DST != 0 && !sameIPNet(candidate.Dst, filter.Dst) {
		return false
	}
	if mask&netlink.RT_FILTER_GW != 0 && !candidate.Gw.Equal(filter.Gw) {
		return false
	}
	if mask&netlink.RT_FILTER_PRIORITY != 0 && candidate.Priority != filter.Priority {
		return false
	}
	return true
}

func sameRouteSelector(left, right netlink.Route) bool {
	return left.Table == right.Table &&
		left.LinkIndex == right.LinkIndex &&
		sameIPNet(left.Dst, right.Dst) &&
		left.Gw.Equal(right.Gw)
}

func sameRouteIdentity(left, right netlink.Route) bool {
	return sameRouteSelector(left, right) &&
		left.Type == right.Type &&
		left.Scope == right.Scope &&
		left.Protocol == right.Protocol &&
		left.Priority == right.Priority
}

func sameIPNet(left, right *net.IPNet) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.IP.Equal(right.IP) && string(left.Mask) == string(right.Mask)
}
