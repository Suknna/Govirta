package coredhcp

import (
	"slices"
	"strings"

	"github.com/suknna/govirta/pkg/hostnet/dhcp"
)

func serverInfo(rt *serverRuntime) dhcp.ServerInfo {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	return dhcp.ServerInfo{
		ID:            rt.spec.ID,
		InterfaceName: rt.spec.InterfaceName,
		ListenAddr:    rt.spec.ListenAddr,
		ListenPort:    rt.spec.ListenPort,
		ServerAddr:    rt.spec.ServerAddr,
		Subnet:        rt.spec.Subnet,
		Pool:          rt.spec.Pool,
		State:         rt.state,
		LeaseCount:    len(rt.bindingsByMAC),
	}
}

func leaseInfo(record *leaseRecord) dhcp.LeaseInfo {
	return dhcp.LeaseInfo{
		ServerID:  record.serverID,
		MAC:       append([]byte(nil), record.mac...),
		IP:        record.ip,
		Hostname:  record.hostname,
		State:     record.state,
		ExpiresAt: record.expiresAt,
	}
}

func sortedLeaseInfos(records []*leaseRecord) []dhcp.LeaseInfo {
	slices.SortFunc(records, func(a, b *leaseRecord) int {
		if cmp := strings.Compare(string(a.serverID), string(b.serverID)); cmp != 0 {
			return cmp
		}
		if cmp := strings.Compare(a.mac.String(), b.mac.String()); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.ip.String(), b.ip.String())
	})

	infos := make([]dhcp.LeaseInfo, 0, len(records))
	for _, record := range records {
		infos = append(infos, leaseInfo(record))
	}
	return infos
}
