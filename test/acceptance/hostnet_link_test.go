//go:build acceptance && linux

package acceptance

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostlink "github.com/suknna/govirta/pkg/hostnet/link"
	linklinux "github.com/suknna/govirta/pkg/hostnet/link/linux"
	"github.com/suknna/govirta/pkg/virt/qmp"
)

func TestHostnetLinkBridgeTapEndToEnd(t *testing.T) {
	env := requireHostnetAcceptanceEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

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

	if _, err := manager.EnsureBridge(ctx, hostlink.BridgeSpec{
		Name:        bridgeName,
		GatewayCIDR: "192.168.100.1/24",
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
		MAC:        net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x01, 0x02},
		VNetHeader: hostlink.VNetHeaderEnabled,
	}); err != nil {
		t.Fatalf("ensure tap %q: %v", tapName, err)
	}
	tapInfo, err := manager.Get(ctx, tapName)
	if err != nil {
		t.Fatalf("get tap %q: %v", tapName, err)
	}
	if tapInfo.MasterName != bridgeName {
		t.Fatalf("tap %q master = %q, want %q", tapName, tapInfo.MasterName, bridgeName)
	}

	scratch := t.TempDir()
	diskPath := filepath.Join(scratch, "cirros-root.qcow2")
	qmpPath := shortSocketPath(t, scratch, "qmp.sock")
	serialPath := shortSocketPath(t, scratch, "serial.sock")
	if err := copyFile(env.Cirros, diskPath); err != nil {
		t.Fatalf("copy cirros image: %v", err)
	}

	guestIP := "192.168.100.10"
	gatewayIP := "192.168.100.1"
	appendLine := "console=ttyAMA0 ds=none"
	args := []string{
		"-machine", "virt,accel=kvm", "-cpu", "host", "-m", "256M", "-smp", "1",
		"-kernel", env.Kernel, "-initrd", env.Initramfs, "-append", appendLine,
		"-drive", "file=" + diskPath + ",if=virtio,format=qcow2",
		"-netdev", "tap,id=net0,ifname=gvtap0,script=no,downscript=no,vhost=on",
		"-device", "virtio-net-pci,netdev=net0,mac=02:00:00:00:01:02,romfile=",
		"-qmp", "unix:" + qmpPath + ",server=on,wait=off",
		"-serial", "unix:" + serialPath + ",server=on,wait=on",
		"-display", "none", "-no-reboot", "-no-shutdown",
	}
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

	var serialOutput string
	select {
	case result := <-serialDone:
		serialOutput = result.Output
		if result.Err != nil {
			t.Fatalf("waiting for serial login marker: %v\nserial tail:\n%s", result.Err, tailString(result.Output, 8192))
		}
	case <-ctx.Done():
		t.Fatalf("waiting for serial login marker: %v", ctx.Err())
	}

	// CirrOS dhcpcd overrides kernel ip= config; configure static IP via serial after boot.
	// First log in: CirrOS default user is "cirros" with password "gocubsgo".
	loginCommands := []string{
		"cirros",
		"gocubsgo",
	}
	for _, command := range loginCommands {
		if err := writeSerialCommand(ctx, serialPath, command); err != nil {
			t.Fatalf("write serial login command: %v", err)
		}
		time.Sleep(1 * time.Second)
	}
	staticIPCommands := []string{
		"sudo ip addr flush dev eth0",
		"sudo ip addr add 192.168.100.10/24 dev eth0",
		"sudo ip link set eth0 up",
		"sudo ip route add default via 192.168.100.1",
	}
	for _, command := range staticIPCommands {
		if err := writeSerialCommand(ctx, serialPath, command); err != nil {
			t.Fatalf("write serial command %q: %v", command, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("configured guest static IP %s/24 via serial", guestIP)

	if pingUntilSuccess(t, ctx, guestIP) {
		return
	}

	diagCtx, diagCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer diagCancel()
	bridgeInfo, bridgeErr := manager.Get(diagCtx, bridgeName)
	tapInfo, tapErr := manager.Get(diagCtx, tapName)
	qmpStatus, qmpErr := queryQMPStatusOnce(diagCtx, qmpPath)
	t.Logf("bridge %q info: %+v err=%v", bridgeName, bridgeInfo, bridgeErr)
	t.Logf("tap %q info: %+v err=%v", tapName, tapInfo, tapErr)
	t.Logf("qemu argv: %s %s", env.QEMU, strings.Join(args, " "))
	t.Logf("QMP query status: %+v err=%v", qmpStatus, qmpErr)
	t.Logf("serial tail:\n%s", tailString(serialOutput, 8192))
	t.Logf("QEMU stderr:\n%s", qemuStderr.String())
	logNetworkDiagnostics(t, diagCtx)
	t.Fatalf("host could not ping guest %s via bridge %q and tap %q", guestIP, bridgeName, tapName)
}

func cleanupHostLinks(ctx context.Context, manager hostlink.Manager, tapName hostlink.Name, bridgeName hostlink.Name) error {
	return errors.Join(manager.Delete(ctx, tapName), manager.Delete(ctx, bridgeName))
}

func copyFile(sourcePath, targetPath string) error {
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}

	return os.WriteFile(targetPath, contents, 0600)
}
