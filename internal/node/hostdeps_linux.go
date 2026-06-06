//go:build linux

package node

import (
	"fmt"

	"github.com/suknna/govirta/internal/vmm/proc"
	"github.com/suknna/govirta/pkg/hostnet/dhcp/coredhcp"
	firewalllinux "github.com/suknna/govirta/pkg/hostnet/firewall/linux"
	linklinux "github.com/suknna/govirta/pkg/hostnet/link/linux"
	routelinux "github.com/suknna/govirta/pkg/hostnet/route/linux"
)

// buildHostManagers wires the real Linux execution-plane primitives: netlink
// bridge/TAP and route managers, an nftables firewall manager, a CoreDHCP static
// binding manager, and the os/exec process controller. The nftables handle can
// fail to open, so this returns an error.
func buildHostManagers() (hostManagers, error) {
	fw, err := firewalllinux.NewManager()
	if err != nil {
		return hostManagers{}, fmt.Errorf("node: build firewall manager: %w", err)
	}
	return hostManagers{
		link:     linklinux.NewManager(),
		route:    routelinux.NewManager(),
		firewall: fw,
		dhcp:     coredhcp.NewManager(),
		proc:     proc.NewLinuxController(),
	}, nil
}
