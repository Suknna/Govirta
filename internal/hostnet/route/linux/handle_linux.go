//go:build linux

package linux

import (
	"net"

	"github.com/vishvananda/netlink"
)

type handle interface {
	LinkByIndex(index int) (netlink.Link, error)
	LinkByName(name string) (netlink.Link, error)
	RouteAdd(route *netlink.Route) error
	RouteReplace(route *netlink.Route) error
	RouteDel(route *netlink.Route) error
	RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error)
	RouteGet(destination net.IP) ([]netlink.Route, error)
}

type realHandle struct{}

func (realHandle) LinkByIndex(index int) (netlink.Link, error) {
	return netlink.LinkByIndex(index)
}

func (realHandle) LinkByName(name string) (netlink.Link, error) {
	return netlink.LinkByName(name)
}

func (realHandle) RouteAdd(route *netlink.Route) error {
	return netlink.RouteAdd(route)
}

func (realHandle) RouteReplace(route *netlink.Route) error {
	return netlink.RouteReplace(route)
}

func (realHandle) RouteDel(route *netlink.Route) error {
	return netlink.RouteDel(route)
}

func (realHandle) RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error) {
	return netlink.RouteListFiltered(family, filter, filterMask)
}

func (realHandle) RouteGet(destination net.IP) ([]netlink.Route, error) {
	return netlink.RouteGet(destination)
}
