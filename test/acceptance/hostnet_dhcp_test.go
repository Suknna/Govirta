//go:build acceptance && linux

package acceptance

import (
	"context"
	"net"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"

	hostdhcp "github.com/suknna/govirta/pkg/hostnet/dhcp"
	"github.com/suknna/govirta/pkg/hostnet/dhcp/coredhcp"
	hostlink "github.com/suknna/govirta/pkg/hostnet/link"
	linklinux "github.com/suknna/govirta/pkg/hostnet/link/linux"
	"github.com/suknna/govirta/pkg/virt/qmp"
)

func TestHostnetDHCPBindingEndToEnd(t *testing.T) {
	env := requireHostnetAcceptanceEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// Hostnet acceptance reuses fixed gvbr0/gvtap0/192.168.100.0/24
	// resources, so keep these tests serial unless the resources become unique.
	manager := linklinux.NewManager()
	bridgeName := hostlink.Name("gvbr0")
	tapName := hostlink.Name("gvtap0")
	if err := cleanupHostLinks(ctx, manager, tapName, bridgeName); err != nil {
		t.Fatalf("initial cleanup host links tap=%q bridge=%q: %v", tapName, bridgeName, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := cleanupHostLinks(cleanupCtx, manager, tapName, bridgeName); err != nil {
			t.Errorf("cleanup host links tap=%q bridge=%q: %v", tapName, bridgeName, err)
		}
	})

	guestMAC := mustParseAcceptanceMAC(t, "02:00:00:00:01:02")
	guestIP := netip.MustParseAddr("192.168.100.10")
	gatewayIP := netip.MustParseAddr("192.168.100.1")
	subnet := netip.MustParsePrefix("192.168.100.0/24")
	if _, err := manager.EnsureBridge(ctx, hostlink.BridgeSpec{
		Name:        bridgeName,
		GatewayCIDR: gatewayIP.String() + "/24",
		MTU:         1500,
		MAC:         net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x01, 0x01},
	}); err != nil {
		t.Fatalf("ensure bridge %q: %v", bridgeName, err)
	}
	if _, err := manager.EnsureTap(ctx, hostlink.TapSpec{
		Name:       tapName,
		BridgeName: bridgeName,
		OwnerUID:   hostlink.ExplicitUID(0),
		OwnerGID:   hostlink.ExplicitGID(0),
		MTU:        1500,
		MAC:        guestMAC,
		VNetHeader: hostlink.VNetHeaderEnabled,
	}); err != nil {
		t.Fatalf("ensure tap %q: %v", tapName, err)
	}

	dhcpManager := coredhcp.NewManager()
	serverID := hostdhcp.ServerID("acceptance-gvbr0-dhcp")
	serverSpec := hostdhcp.ServerSpec{
		ID:            serverID,
		InterfaceName: bridgeName,
		ListenAddr:    netip.MustParseAddr("0.0.0.0"),
		ListenPort:    hostdhcp.Port(dhcpv4.ServerPort),
		ServerAddr:    gatewayIP,
		Subnet:        subnet,
		Pool:          hostdhcp.AddressRange{Start: guestIP, End: guestIP},
		LeaseDuration: time.Hour,
		Router:        hostdhcp.DHCPOptionAddrs{Mode: hostdhcp.DHCPOptionDisabled},
		DNS:           hostdhcp.DHCPOptionAddrs{Mode: hostdhcp.DHCPOptionDisabled},
		BindMode:      hostdhcp.BindModeInterfaceZone,
	}
	serverInfo, err := dhcpManager.Start(ctx, serverSpec)
	if err != nil {
		t.Fatalf("start DHCP server: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := dhcpManager.Stop(cleanupCtx, serverID); err != nil {
			t.Errorf("stop DHCP server %q: %v", serverID, err)
		}
	})
	lease, err := dhcpManager.ApplyBinding(ctx, hostdhcp.BindingRequest{ServerID: serverID, MAC: guestMAC, IP: guestIP})
	if err != nil {
		t.Fatalf("apply DHCP binding: %v", err)
	}
	if lease.State != hostdhcp.LeaseStateReserved {
		t.Fatalf("initial lease state = %q, want %q", lease.State, hostdhcp.LeaseStateReserved)
	}

	scratch := t.TempDir()
	diskPath := filepath.Join(scratch, "cirros-root.qcow2")
	qmpPath := shortSocketPath(t, scratch, "qmp.sock")
	serialPath := shortSocketPath(t, scratch, "serial.sock")
	if err := copyFile(env.Cirros, diskPath); err != nil {
		t.Fatalf("copy cirros image: %v", err)
	}

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
	t.Logf("DHCP server info: %+v", serverInfo)
	t.Logf("guestIP=%s gatewayIP=%s append=%q qemu argv=%s %s", guestIP, gatewayIP, appendLine, env.QEMU, strings.Join(args, " "))

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
			if cmd.ProcessState != nil {
				t.Logf("qemu process state: %s", cmd.ProcessState.String())
			} else if cmd.Process != nil {
				t.Logf("qemu process pid=%d has no process state before cleanup", cmd.Process.Pid)
			} else {
				t.Logf("qemu process was not started")
			}
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
	serialOutput := result.Output
	if result.Err != nil {
		logHostnetDHCPDiagnostics(t, context.Background(), manager, dhcpManager, bridgeName, tapName, serverID, qmpPath, args, env.QEMU, qemuStderr.String(), result.Output)
		t.Fatalf("waiting for serial login marker: %v\nserial tail:\n%s", result.Err, tailString(result.Output, 8192))
	}

	for _, command := range []string{"cirros", "gocubsgo"} {
		if err := writeSerialCommand(ctx, serialPath, command); err != nil {
			t.Fatalf("write serial login command: %v", err)
		}
		time.Sleep(time.Second)
	}

	boundLease := waitForDHCPLeaseState(t, ctx, dhcpManager, serverID, guestMAC, hostdhcp.LeaseStateBound)
	if boundLease.IP != guestIP {
		t.Fatalf("bound lease IP = %s, want %s", boundLease.IP, guestIP)
	}

	addrOutput, err := runSerialCommand(ctx, serialPath, "ip -4 addr show dev eth0")
	if err != nil {
		t.Fatalf("read guest IPv4 address: %v\noutput:\n%s", err, tailString(addrOutput, 8192))
	}
	if !strings.Contains(addrOutput, guestIP.String()+"/24") {
		t.Fatalf("guest eth0 address output missing %s/24:\n%s", guestIP, addrOutput)
	}
	routeOutput, err := runSerialCommand(ctx, serialPath, "ip route show")
	if err != nil {
		t.Fatalf("read guest routes: %v\noutput:\n%s", err, tailString(routeOutput, 8192))
	}
	t.Logf("guest route output after DHCP without router option:\n%s", routeOutput)

	if pingUntilSuccess(t, ctx, guestIP.String()) {
		return
	}

	logHostnetDHCPDiagnostics(t, context.Background(), manager, dhcpManager, bridgeName, tapName, serverID, qmpPath, args, env.QEMU, qemuStderr.String(), serialOutput)
	t.Fatalf("host could not ping DHCP guest %s via bridge %q and tap %q", guestIP, bridgeName, tapName)
}

func logHostnetDHCPDiagnostics(t *testing.T, parent context.Context, linkManager hostlink.Manager, dhcpManager hostdhcp.Manager, bridgeName hostlink.Name, tapName hostlink.Name, serverID hostdhcp.ServerID, qmpPath string, args []string, qemuBinary string, qemuStderr string, serialOutput string) {
	t.Helper()

	diagCtx, diagCancel := context.WithTimeout(parent, 15*time.Second)
	defer diagCancel()
	bridgeInfo, bridgeErr := linkManager.Get(diagCtx, bridgeName)
	tapInfo, tapErr := linkManager.Get(diagCtx, tapName)
	serverInfo, serverErr := dhcpManager.GetServer(diagCtx, serverID)
	leases, leasesErr := dhcpManager.ListLeases(diagCtx, hostdhcp.LeaseFilter{ServerID: serverID})
	qmpStatus, qmpErr := queryQMPStatusOnce(diagCtx, qmpPath)
	t.Logf("bridge %q info: %+v err=%v", bridgeName, bridgeInfo, bridgeErr)
	t.Logf("tap %q info: %+v err=%v", tapName, tapInfo, tapErr)
	t.Logf("DHCP server info: %+v err=%v", serverInfo, serverErr)
	t.Logf("DHCP leases: %+v err=%v", leases, leasesErr)
	t.Logf("qemu argv: %s %s", qemuBinary, strings.Join(args, " "))
	t.Logf("QMP query status: %+v err=%v", qmpStatus, qmpErr)
	t.Logf("serial tail:\n%s", tailString(serialOutput, 8192))
	t.Logf("QEMU stderr:\n%s", qemuStderr)
	logNetworkDiagnostics(t, diagCtx)
}

func waitForDHCPLeaseState(t *testing.T, ctx context.Context, manager hostdhcp.Manager, serverID hostdhcp.ServerID, mac net.HardwareAddr, want hostdhcp.LeaseState) hostdhcp.LeaseInfo {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)
	var lastLease hostdhcp.LeaseInfo
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("waiting for DHCP lease %s state %q: %v", mac, want, err)
		}
		lease, err := manager.GetLease(ctx, hostdhcp.BindingQuery{ServerID: serverID, MAC: mac})
		if err == nil {
			lastLease = lease
			if lease.State == want {
				return lease
			}
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			t.Fatalf("waiting for DHCP lease %s state %q: %v", mac, want, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for DHCP lease %s state %q, last lease=%+v last err=%v", mac, want, lastLease, lastErr)
	return hostdhcp.LeaseInfo{}
}

func mustParseAcceptanceMAC(t *testing.T, value string) net.HardwareAddr {
	t.Helper()
	mac, err := net.ParseMAC(value)
	if err != nil {
		t.Fatalf("parse MAC %q: %v", value, err)
	}
	return mac
}
