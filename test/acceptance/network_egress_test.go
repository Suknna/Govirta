//go:build acceptance && linux

package acceptance

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/network"
	"github.com/suknna/govirta/internal/network/netpool"
	"github.com/suknna/govirta/pkg/hostnet/dhcp"
	hostdhcpcore "github.com/suknna/govirta/pkg/hostnet/dhcp/coredhcp"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
	hostfirewalllinux "github.com/suknna/govirta/pkg/hostnet/firewall/linux"
	"github.com/suknna/govirta/pkg/hostnet/link"
	hostlinklinux "github.com/suknna/govirta/pkg/hostnet/link/linux"
	hostroutelinux "github.com/suknna/govirta/pkg/hostnet/route/linux"
	"github.com/suknna/govirta/pkg/virt/qmp"
)

// Govirta-owned logical identities for the egress network under test. They are
// shared between the declarative definitions and the firewall ListRules filters
// used to resolve rule refs for cleanup, so the two never drift apart.
const (
	egressNetwork  = netpool.NetworkName("egress-net")
	egressVM       = netpool.VMID("vm1")
	egressOwner    = firewall.RuleOwner("egress-net")
	egressDHCPID   = dhcp.ServerID("egress-net-dhcp")
	egressNATTable = firewall.TableName("govirta-nat")
	egressMasqChn  = firewall.ChainName("postrouting")
	egressFwdChn   = firewall.ChainName("forward")
	egressBrTable  = firewall.TableName("govirta-bridge")
	egressBrChn    = firewall.ChainName("antispoof")
)

func TestNetworkEgressEndToEnd(t *testing.T) {
	env := requireHostnetAcceptanceEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	bridgeName := link.Name("gvbr0")
	tapName := link.Name("gvtap0")
	guestMAC := mustParseAcceptanceMAC(t, "02:00:00:00:01:02")
	guestIP := netip.MustParseAddr("192.168.100.10")
	gatewayIP := netip.MustParseAddr("192.168.100.1")
	subnet := netip.MustParsePrefix("192.168.100.0/24")
	dnsAddr := netip.MustParseAddr("8.8.8.8")

	egress, err := defaultEgressInterface(ctx)
	if err != nil {
		t.Fatalf("determine egress interface: %v", err)
	}
	t.Logf("egress interface = %q", egress)

	// Real host primitives. Only the firewall manager constructor returns an
	// error; the link, route, and dhcp constructors return a single value.
	linkMgr := hostlinklinux.NewManager()
	routeMgr := hostroutelinux.NewManager()
	firewallMgr, err := hostfirewalllinux.NewManager()
	if err != nil {
		t.Fatalf("new firewall manager: %v", err)
	}
	dhcpMgr := hostdhcpcore.NewManager()

	pools := netpool.NewService(linkMgr, routeMgr, firewallMgr, dhcpMgr)
	netSvc := network.NewNetworkService(pools)
	nicSvc := network.NewNICService(pools)

	netDef := netpool.NetworkDefinition{
		Name:        egressNetwork,
		BridgeName:  bridgeName,
		BridgeMAC:   net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x01, 0x01},
		BridgeMTU:   1500,
		Subnet:      subnet,
		GatewayCIDR: netip.MustParsePrefix("192.168.100.1/24"),
		Pool:        dhcp.AddressRange{Start: guestIP, End: guestIP},
		EgressIface: firewall.InterfaceName(egress),

		DHCPServerID:  egressDHCPID,
		Router:        dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionEnabled, Addrs: []netip.Addr{gatewayIP}},
		DNS:           dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionEnabled, Addrs: []netip.Addr{dnsAddr}},
		LeaseDuration: time.Hour,

		FirewallTable:      egressNATTable,
		MasqueradeChain:    egressMasqChn,
		ForwardChain:       egressFwdChn,
		RuleOwner:          egressOwner,
		MasqueradePriority: firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT),
		ForwardPriority:    firewall.ExplicitPriority(0, firewall.PriorityNameForwardFilter),
	}
	if err := netSvc.RegisterNetwork(netDef); err != nil {
		t.Fatalf("register network: %v", err)
	}
	if _, err := netSvc.EnsureNetwork(ctx, egressNetwork); err != nil {
		logFirewallDiagnostics(t, ctx)
		logRouteDiagnostics(t, ctx, "8.8.8.8", string(bridgeName))
		t.Fatalf("ensure network: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()

		// Exercise the orchestration NIC delete path. The service now resolves
		// the anti-spoofing rule ref live from observed firewall state, so the
		// caller needs no firewall handle.
		if err := nicSvc.DeleteNIC(cleanupCtx, egressNetwork, egressVM); err != nil {
			t.Logf("delete nic: %v", err)
		}

		// Attempt the orchestration network delete to exercise that path. It is
		// expected to return networker.ErrConflict here: DeleteNIC keeps the NIC
		// definition registered and this phase exposes no NIC-unregister API, so
		// the network's NIC count stays non-zero and DeleteNetwork refuses
		// before tearing anything down. The service resolves the masquerade and
		// forward-accept refs internally, so the caller passes only the network
		// name.
		if err := netSvc.DeleteNetwork(cleanupCtx, egressNetwork); err != nil {
			t.Logf("delete network via orchestration (expected ErrConflict while NIC stays registered): %v", err)
		}

		// Direct best-effort teardown of the shared resources the orchestration
		// delete path could not remove while the NIC stays registered. The DHCP
		// server and links are torn down by stable identity (no firewall handle
		// needed); the masquerade and forward-accept rules are left in place and
		// are reconciled idempotently by EnsureNetwork on the next run. All host
		// deletes are idempotent and return nil when the resource is absent.
		if err := dhcpMgr.Stop(cleanupCtx, egressDHCPID); err != nil {
			t.Logf("stop dhcp server: %v", err)
		}
		if err := linkMgr.Delete(cleanupCtx, tapName); err != nil {
			t.Logf("delete tap: %v", err)
		}
		if err := linkMgr.Delete(cleanupCtx, bridgeName); err != nil {
			t.Logf("delete bridge: %v", err)
		}
	})

	nicDef := netpool.NICDefinition{
		NetworkName: egressNetwork,
		VMID:        egressVM,
		TapName:     tapName,
		MAC:         guestMAC,
		IP:          guestIP,
		TapMTU:      1500,
		VNetHeader:  link.VNetHeaderEnabled,
		OwnerUID:    link.ExplicitUID(0),
		OwnerGID:    link.ExplicitGID(0),

		AntiSpoofTable:    egressBrTable,
		AntiSpoofChain:    egressBrChn,
		AntiSpoofPriority: firewall.ExplicitPriority(-200, firewall.PriorityNameBridgeFilter),
	}
	if err := nicSvc.RegisterNIC(nicDef); err != nil {
		t.Fatalf("register nic: %v", err)
	}
	if _, err := nicSvc.EnsureNIC(ctx, egressNetwork, egressVM); err != nil {
		logFirewallDiagnostics(t, ctx)
		logNetworkDiagnostics(t, ctx)
		t.Fatalf("ensure nic: %v", err)
	}

	scratch := t.TempDir()
	diskPath := filepath.Join(scratch, "cirros-root.qcow2")
	qmpPath := shortSocketPath(t, scratch, "qmp.sock")
	serialPath := shortSocketPath(t, scratch, "serial.sock")
	if err := copyFile(env.Cirros, diskPath); err != nil {
		t.Fatalf("copy cirros image: %v", err)
	}

	// Router option is enabled here (egress needs a default route), so suppress
	// CirrOS metadata probing with ds=none to avoid the 169.254.169.254 delay.
	appendLine := "console=ttyAMA0 ds=none"
	args := []string{
		"-machine", "virt,accel=kvm", "-cpu", "host", "-m", "256M", "-smp", "1",
		"-kernel", env.Kernel, "-initrd", env.Initramfs, "-append", appendLine,
		"-drive", "file=" + diskPath + ",if=virtio,format=qcow2",
		"-netdev", "tap,id=net0,ifname=" + string(tapName) + ",script=no,downscript=no,vhost=on",
		"-device", "virtio-net-pci,netdev=net0,mac=" + guestMAC.String() + ",romfile=",
		"-qmp", "unix:" + qmpPath + ",server=on,wait=off",
		"-serial", "unix:" + serialPath + ",server=on,wait=on",
		"-display", "none", "-no-reboot", "-no-shutdown",
	}
	t.Logf("qemu argv: %s %s", env.QEMU, strings.Join(args, " "))

	cmd := exec.Command(env.QEMU, args...)
	qemuStderr, err := startQEMUCommand(cmd)
	if err != nil {
		t.Fatalf("start qemu: %v\nstderr:\n%s", err, qemuStderr.String())
	}
	serialDone := make(chan serialMarkerResult, 1)
	go func() {
		output, err := waitForSerialMarkerGroups(ctx, serialPath, []serialMarkerGroup{
			{Name: "serial login marker", Markers: []string{"login:"}},
		})
		serialDone <- serialMarkerResult{Output: output, Err: err}
	}()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("QEMU stderr before cleanup:\n%s", qemuStderr.String())
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := stopQEMU(cleanupCtx, qmpPath, cmd); err != nil {
			t.Logf("stop qemu: %v", err)
		}
	})

	status := waitForQMPStatus(t, ctx, qmpPath, qmp.StateRunning)
	if !status.Running {
		t.Fatalf("QMP status running = false, state = %q", status.State)
	}

	result := <-serialDone
	if result.Err != nil {
		logFirewallDiagnostics(t, context.Background())
		logRouteDiagnostics(t, context.Background(), "8.8.8.8", string(bridgeName))
		logNetworkDiagnostics(t, context.Background())
		t.Fatalf("waiting for serial login marker: %v\nserial tail:\n%s", result.Err, tailString(result.Output, 8192))
	}

	for _, command := range []string{"cirros", "gocubsgo"} {
		if err := writeSerialCommand(ctx, serialPath, command); err != nil {
			t.Fatalf("write serial login command: %v", err)
		}
		time.Sleep(time.Second)
	}

	// Verify DHCP gave the guest IP + default route.
	addrOutput, err := runSerialCommand(ctx, serialPath, "ip -4 addr show dev eth0")
	if err != nil {
		t.Fatalf("read guest address: %v\noutput:\n%s", err, tailString(addrOutput, 8192))
	}
	if !strings.Contains(addrOutput, guestIP.String()+"/24") {
		t.Fatalf("guest eth0 missing %s/24:\n%s", guestIP, addrOutput)
	}
	routeOutput, err := runSerialCommand(ctx, serialPath, "ip route show")
	if err != nil {
		t.Fatalf("read guest routes: %v\noutput:\n%s", err, tailString(routeOutput, 8192))
	}
	if !strings.Contains(routeOutput, "default via "+gatewayIP.String()) {
		t.Fatalf("guest missing default route via %s:\n%s", gatewayIP, routeOutput)
	}

	// Step 1 (hard core): ping 8.8.8.8 by IP — proves NAT + forward + default route.
	pingIPOutput, err := runSerialCommand(ctx, serialPath, "ping -c 3 -w 10 8.8.8.8")
	if err != nil {
		logFirewallDiagnostics(t, context.Background())
		logRouteDiagnostics(t, context.Background(), "8.8.8.8", string(bridgeName))
		t.Fatalf("guest ping 8.8.8.8 command failed: %v\noutput:\n%s", err, tailString(pingIPOutput, 8192))
	}
	if !guestPingSucceeded(pingIPOutput) {
		logFirewallDiagnostics(t, context.Background())
		logRouteDiagnostics(t, context.Background(), "8.8.8.8", string(bridgeName))
		t.Fatalf("guest could not ping 8.8.8.8 (NAT/forward/default-route path broken):\n%s", pingIPOutput)
	}

	// Step 2: ping a domain — proves DNS option delivery.
	pingDNSOutput, err := runSerialCommand(ctx, serialPath, "ping -c 3 -w 10 one.one.one.one")
	if err != nil {
		t.Fatalf("guest ping domain command failed: %v\noutput:\n%s", err, tailString(pingDNSOutput, 8192))
	}
	if !guestPingSucceeded(pingDNSOutput) {
		t.Fatalf("guest could not ping one.one.one.one (DNS delivery broken):\n%s", pingDNSOutput)
	}
}

// guestPingSucceeded reports whether CirrOS busybox ping output indicates at
// least one received reply.
func guestPingSucceeded(output string) bool {
	return strings.Contains(output, "0% packet loss") ||
		strings.Contains(output, "1 packets received") ||
		strings.Contains(output, "2 packets received") ||
		strings.Contains(output, "3 packets received")
}

// defaultEgressInterface returns the host interface that owns the default route,
// used as the masquerade/forward egress interface.
func defaultEgressInterface(ctx context.Context) (string, error) {
	stdout, _, err := runCommand(ctx, "sh", "-c", "ip -o route show default | awk '{print $5; exit}'")
	if err != nil {
		return "", err
	}
	iface := strings.TrimSpace(string(stdout))
	if iface == "" {
		return "", fmt.Errorf("no default-route egress interface found")
	}
	return iface, nil
}
