//go:build linux

package linux

import (
	"fmt"
	"net"
	"sort"

	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/link/linkerr"
	"github.com/vishvananda/netlink"
)

type vnetHeaderObserved string

const (
	vnetHeaderObservedEnabled  vnetHeaderObserved = "enabled"
	vnetHeaderObservedDisabled vnetHeaderObserved = "disabled"
	vnetHeaderObservedUnknown  vnetHeaderObserved = "unknown"
)

func observeVNetHeader(nlLink netlink.Link) vnetHeaderObserved {
	tap, ok := nlLink.(*netlink.Tuntap)
	if !ok || tap.Mode != netlink.TUNTAP_MODE_TAP {
		return vnetHeaderObservedUnknown
	}
	if tap.Flags&netlink.TUNTAP_VNET_HDR != 0 {
		return vnetHeaderObservedEnabled
	}

	return vnetHeaderObservedDisabled
}

func kindOf(nlLink netlink.Link) (link.Kind, error) {
	switch typed := nlLink.(type) {
	case *netlink.Bridge:
		return link.KindBridge, nil
	case *netlink.Tuntap:
		if typed.Mode == netlink.TUNTAP_MODE_TAP {
			return link.KindTap, nil
		}
		return "", fmt.Errorf("tuntap mode %v is not TAP: %w", typed.Mode, linkerr.ErrUnsupported)
	default:
		return "", fmt.Errorf("link type %q: %w", nlLink.Type(), linkerr.ErrUnsupported)
	}
}

func linkInfo(h handle, nlLink netlink.Link) (link.LinkInfo, error) {
	attrs := nlLink.Attrs()
	kind, err := kindOf(nlLink)
	if err != nil {
		return link.LinkInfo{}, err
	}
	addresses, err := linkAddresses(h, nlLink)
	if err != nil {
		return link.LinkInfo{}, err
	}
	masterName, err := masterName(h, attrs.MasterIndex)
	if err != nil {
		return link.LinkInfo{}, err
	}

	return link.LinkInfo{
		Name:       link.Name(attrs.Name),
		Kind:       kind,
		Index:      attrs.Index,
		MTU:        attrs.MTU,
		MAC:        cloneHardwareAddr(attrs.HardwareAddr),
		AdminState: adminState(attrs.Flags),
		MasterName: masterName,
		Addresses:  addresses,
	}, nil
}

func linkAddresses(h handle, nlLink netlink.Link) ([]string, error) {
	addrs, err := h.AddrList(nlLink, netlink.FAMILY_ALL)
	if err != nil {
		return nil, translateError("list link addresses", err)
	}
	addresses := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IPNet == nil {
			continue
		}
		addresses = append(addresses, addr.IPNet.String())
	}
	sort.Strings(addresses)

	return addresses, nil
}

func masterName(h handle, masterIndex int) (link.Name, error) {
	if masterIndex == 0 {
		return "", nil
	}
	links, err := h.LinkList()
	if err != nil {
		return "", translateError("list links for master lookup", err)
	}
	for _, candidate := range links {
		attrs := candidate.Attrs()
		if attrs != nil && attrs.Index == masterIndex {
			return link.Name(attrs.Name), nil
		}
	}

	return "", fmt.Errorf("master link index %d: %w", masterIndex, linkerr.ErrNotFound)
}

func adminState(flags net.Flags) link.AdminState {
	if flags&net.FlagUp != 0 {
		return link.AdminStateUp
	}

	return link.AdminStateDown
}

func cloneHardwareAddr(mac net.HardwareAddr) net.HardwareAddr {
	if mac == nil {
		return nil
	}
	clone := make(net.HardwareAddr, len(mac))
	copy(clone, mac)

	return clone
}

func sortLinkInfosByName(infos []link.LinkInfo) {
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Name < infos[j].Name
	})
}
