//go:build !linux

package node

import (
	"github.com/suknna/govirta/internal/vmm/proc"
	"github.com/suknna/govirta/pkg/hostnet/dhcp"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
	"github.com/suknna/govirta/pkg/hostnet/link"
	"github.com/suknna/govirta/pkg/hostnet/route"
)

// buildHostManagers wires no-op execution-plane primitives on non-Linux
// platforms. The real netlink / nftables / CoreDHCP / exec implementations are
// Linux-only (//go:build linux), so on darwin the agent assembles and the unit
// tests run with no-op stand-ins (memory 700: keep the assembly cross-platform
// compilable). A node started on a non-Linux host cannot actually serve guests;
// it exists only so the package builds and tests run off-Linux. proc's
// NewLinuxController is itself build-tagged: the Linux file is the real exec
// controller, the other file is the unsupported stand-in.
func buildHostManagers() (hostManagers, error) {
	return hostManagers{
		link:     link.NewNoopManager(),
		route:    route.NewNoopManager(),
		firewall: firewall.NewNoopManager(),
		dhcp:     dhcp.NewNoopManager(),
		proc:     proc.NewLinuxController(),
	}, nil
}
