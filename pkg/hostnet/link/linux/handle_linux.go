//go:build linux

package linux

import (
	"net"

	"github.com/vishvananda/netlink"
)

type handle interface {
	LinkByName(name string) (netlink.Link, error)
	LinkAdd(link netlink.Link) error
	LinkDel(link netlink.Link) error
	LinkSetUp(link netlink.Link) error
	LinkSetMTU(link netlink.Link, mtu int) error
	LinkSetHardwareAddr(link netlink.Link, hwaddr net.HardwareAddr) error
	LinkSetMaster(link netlink.Link, master netlink.Link) error
	AddrReplace(link netlink.Link, addr *netlink.Addr) error
	AddrDel(link netlink.Link, addr *netlink.Addr) error
	LinkList() ([]netlink.Link, error)
	AddrList(link netlink.Link, family int) ([]netlink.Addr, error)
}

type realHandle struct{}

func (realHandle) LinkByName(name string) (netlink.Link, error) {
	return netlink.LinkByName(name)
}

func (realHandle) LinkAdd(link netlink.Link) error {
	return netlink.LinkAdd(link)
}

func (realHandle) LinkDel(link netlink.Link) error {
	return netlink.LinkDel(link)
}

func (realHandle) LinkSetUp(link netlink.Link) error {
	return netlink.LinkSetUp(link)
}

func (realHandle) LinkSetMTU(link netlink.Link, mtu int) error {
	return netlink.LinkSetMTU(link, mtu)
}

func (realHandle) LinkSetHardwareAddr(link netlink.Link, hwaddr net.HardwareAddr) error {
	return netlink.LinkSetHardwareAddr(link, hwaddr)
}

func (realHandle) LinkSetMaster(link netlink.Link, master netlink.Link) error {
	return netlink.LinkSetMaster(link, master)
}

func (realHandle) AddrReplace(link netlink.Link, addr *netlink.Addr) error {
	return netlink.AddrReplace(link, addr)
}

func (realHandle) AddrDel(link netlink.Link, addr *netlink.Addr) error {
	return netlink.AddrDel(link, addr)
}

func (realHandle) LinkList() ([]netlink.Link, error) {
	return netlink.LinkList()
}

func (realHandle) AddrList(link netlink.Link, family int) ([]netlink.Addr, error) {
	return netlink.AddrList(link, family)
}
