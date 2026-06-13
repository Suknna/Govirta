// Package node assembles govirtlet's controller-manager: it wires the master
// apiserver client, the streaming watch source, the local execution-plane
// services (storage / network / VM process), the node-local image cache, and
// the first-class controllers into one runnable Agent. The Agent watches the
// master and reconciles each kind onto the local node.
package node

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/network"
	"github.com/suknna/govirta/internal/network/netpool"
	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/node/controllers"
	"github.com/suknna/govirta/internal/storage"
	"github.com/suknna/govirta/internal/storage/pool"
	"github.com/suknna/govirta/internal/vmm"
	"github.com/suknna/govirta/internal/vmm/proc"
	"github.com/suknna/govirta/pkg/hostnet/dhcp"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
	"github.com/suknna/govirta/pkg/hostnet/link"
	"github.com/suknna/govirta/pkg/hostnet/route"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
	"github.com/suknna/govirta/pkg/virt/qmp"
)

// Config holds every behavior-affecting input for a compute node agent. Every
// field is supplied explicitly by cmd/govirtlet (显式铁律): the package defaults
// no master address, node identity, runtime location, image source root, or TAP
// owner.
//
// Note (plan deviation): the plan sketched StorageRoot and EgressIface fields,
// but neither is an agent-level input. The block/file pool root comes from each
// StoragePool object's own spec.storageRoot, and the egress interface comes from
// each Network object's spec.egressInterface; carrying them on the agent would
// be dead config. They are omitted here and read from the object specs instead.
type Config struct {
	// MasterURL is the master apiserver root (scheme://host[:port]) the node
	// watches and reports status to.
	MasterURL string
	// NodeName is this node's identity; the watch source sends it so the master
	// streams only objects bound to this node.
	NodeName string
	// RuntimeRoot is the directory under which the VM process manager keeps each
	// guest's runtime state (vm.json, pidfile, QMP socket).
	RuntimeRoot string
	// ImageCacheRoot is the explicit node-local root for cached image bytes.
	ImageCacheRoot string
	// OwnerUID / OwnerGID is the OS user/group that owns the guest TAP devices
	// (the user QEMU runs as); threaded to the NIC controller, never defaulted.
	OwnerUID link.UID
	OwnerGID link.GID
	// GuestCPU is the QEMU CPU model the node runs guests with (e.g. host).
	GuestCPU cpu.Model
	// QEMUBinary is the absolute path to this node's QEMU executable (e.g.
	// /usr/libexec/qemu-kvm). Required; the node never falls back to a PATH
	// default. This is node-level runtime environment, not per-VM config.
	QEMUBinary string
	// Firmware is the guest firmware image path this node boots guests with
	// (aarch64 virt disk-boot needs edk2, memory 868). Explicitly optional: an
	// empty string renders no -bios (x86_64 q35 ships SeaBIOS).
	Firmware string
}

// hostManagers bundles the host-network primitive managers plus the VM process
// controller — the platform-specific execution-plane dependencies. On Linux they
// are the real netlink / nftables / CoreDHCP / exec implementations; on other
// platforms they are no-op stand-ins so the agent still compiles and the unit
// tests run cross-platform (memory 700). buildHostManagers is defined per-platform
// in hostdeps_linux.go / hostdeps_other.go.
type hostManagers struct {
	link     link.Manager
	route    route.Manager
	firewall firewall.Manager
	dhcp     dhcp.Manager
	proc     proc.ProcessController
}

// Agent runs the node's controller-manager: it watches the master apiserver and
// reconciles each first-class kind onto the local execution plane.
type Agent struct {
	manager         *controller.Manager
	controllerKinds []string
}

// NewAgent assembles a production node agent from cfg. It builds the master
// client + watch source, the platform-specific host primitive managers, the real
// execution-plane services (pool / volume / network / NIC / VM process), the
// node image cache, TaskController, the cache-backed VolumeController, and the
// controller manager.
//
// It returns an error if a host manager or the VM process service cannot be
// constructed (e.g. the nftables handle on Linux, or missing runtime config).
func NewAgent(cfg Config) (*Agent, error) {
	hm, err := buildHostManagers()
	if err != nil {
		return nil, fmt.Errorf("node: build host managers: %w", err)
	}

	master := client.New(cfg.MasterURL, nil)
	source := client.NewWatchSource(cfg.MasterURL, nil, cfg.NodeName)

	poolSvc := pool.NewService()
	volumeSvc := storage.NewVolumeService(poolSvc)

	netpoolSvc := netpool.NewService(hm.link, hm.route, hm.firewall, hm.dhcp)
	networkSvc := network.NewNetworkService(netpoolSvc)
	nicSvc := network.NewNICService(netpoolSvc)

	vmmSvc, err := vmm.NewVMMService(cfg.RuntimeRoot, hm.proc, qmpFactory, vmm.NodeEnv{
		QEMUBinary: cfg.QEMUBinary,
		Firmware:   cfg.Firmware,
	})
	if err != nil {
		return nil, fmt.Errorf("node: build vmm service: %w", err)
	}
	imageCache, err := controllers.NewImageCache(cfg.ImageCacheRoot)
	if err != nil {
		return nil, fmt.Errorf("node: build image cache: %w", err)
	}

	list := []controller.Controller{
		controllers.NewTaskControllerWithImageCache(cfg.NodeName, master, imageCache, nil),
		controllers.NewStoragePoolController(poolSvc, master),
		controllers.NewVolumeController(volumeSvc, imageCache.Root(), vmmSvc, master),
		controllers.NewNetworkController(networkSvc, master),
		controllers.NewNICController(nicSvc, master, cfg.OwnerUID, cfg.OwnerGID),
		controllers.NewVMController(vmmSvc, master, cfg.GuestCPU),
		controllers.NewSnapshotController(volumeSvc, vmmSvc, master),
	}

	return newAgentWithDeps(source, list), nil
}

// newAgentWithDeps is the internal constructor that wires an Agent from an
// already-built event source and controller set. It is the seam unit tests use
// to inject a fake source + fake controllers without touching real services.
func newAgentWithDeps(source controller.EventSource, list []controller.Controller) *Agent {
	kinds := make([]string, 0, len(list))
	for _, c := range list {
		kinds = append(kinds, c.Kind())
	}
	return &Agent{manager: controller.NewManager(source, list), controllerKinds: kinds}
}

// qmpFactory builds a per-socket QMP client on demand. It is cross-platform (a
// unix-socket client), so it lives here rather than in the build-tagged host
// dependencies. vmm calls it lazily per VM and does not hold the client.
func qmpFactory(socketPath string) (qmp.Client, error) {
	c, err := qmp.NewSocketClient(qmp.Config{SocketPath: socketPath})
	if err != nil {
		return nil, fmt.Errorf("node: build qmp client for %q: %w", socketPath, err)
	}
	return c, nil
}

// Run starts the controller manager and blocks until ctx is cancelled or a
// controller's watch fails. It returns ctx.Err() on cancellation.
func (a *Agent) Run(ctx context.Context) error {
	logger := zerolog.Ctx(ctx).With().Str("component", "node").Logger()
	ctx = logger.WithContext(ctx)
	zerolog.Ctx(ctx).Info().Msg("starting node agent")
	return a.manager.Run(ctx)
}
