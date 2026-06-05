//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/blockdev"
	"github.com/suknna/govirta/pkg/virt/qemu/chardev"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
	"github.com/suknna/govirta/pkg/virt/qemu/device"
	"github.com/suknna/govirta/pkg/virt/qemu/display"
	"github.com/suknna/govirta/pkg/virt/qemu/firmware"
	"github.com/suknna/govirta/pkg/virt/qemu/machine"
	"github.com/suknna/govirta/pkg/virt/qemu/monitor"
	"github.com/suknna/govirta/pkg/virt/qemu/serial"
	"github.com/suknna/govirta/pkg/virt/qmp"
)

func TestBootCirrosNoNetworkWithNestedKVM(t *testing.T) {
	env := requireAcceptanceEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	scratch := t.TempDir()
	rootPath := filepath.Join(scratch, "cirros-root.qcow2")
	qmpPath := shortSocketPath(t, scratch, "qmp.sock")
	serialPath := shortSocketPath(t, scratch, "serial.sock")
	pidPath := filepath.Join(scratch, "qemu.pid")

	copyArgs := []string{"convert", "-f", "qcow2", "-O", "qcow2", env.Cirros, rootPath}
	copyOut, copyErrOut, copyErr := runCommand(ctx, env.QEMUImg, copyArgs...)
	if err := commandError(env.QEMUImg, copyArgs, copyOut, copyErrOut, unwrapCommandError(copyErr)); err != nil {
		t.Fatalf("copy cirros image: %v", err)
	}

	vm, err := qemu.NewVM(qemu.ArchAArch64).
		Binary(env.QEMU).
		Name("govirta-acceptance-cirros", qemu.NameDebugThreads(qemu.On)).
		Machine(machine.ProfileAArch64VirtKVM).
		CPU(cpu.ModelHost).
		SMP(qemu.SMP{CPUs: 1, Cores: 1, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(512)).
		BIOS(firmware.BIOS{Path: env.Firmware}).
		AddBlockdev(blockdev.Qcow2{
			NodeName: "root",
			File:     blockdev.FileProtocol{Filename: rootPath},
			Cache:    blockdev.Cache{Direct: qemu.Off},
			AIO:      blockdev.AIOThreads,
		}).
		AddDevice(device.VirtioBlkPCI{
			ID:        "rootdev",
			Drive:     blockdev.Ref("root"),
			BootIndex: qemu.Int(1),
		}).
		AddChardev(chardev.Socket{
			ID:     "qmp0",
			Path:   qmpPath,
			Server: qemu.On,
			Wait:   qemu.Off,
		}).
		Monitor(monitor.Monitor{Chardev: chardev.Ref("qmp0"), Mode: monitor.ModeControl}).
		AddChardev(chardev.Socket{
			ID:     "serial0",
			Path:   serialPath,
			Server: qemu.On,
			Wait:   qemu.On,
		}).
		Serial(serial.Chardev("serial0")).
		NoNIC().
		Display(display.None).
		NoReboot().
		NoShutdown().
		Msg(qemu.Msg{Timestamp: qemu.On, GuestName: qemu.On}).
		PidFile(pidPath).
		Build()
	if err != nil {
		t.Fatalf("build qemu argv: %v", err)
	}

	argv := vm.Argv()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start qemu: %v", err)
	}
	serialDone := make(chan serialMarkerResult, 1)
	go func() {
		output, err := waitForSerialMarkerGroups(ctx, serialPath, []serialMarkerGroup{
			{
				Name: "kernel boot marker",
				Markers: []string{
					"smccc: KVM: hypervisor services detected",
					"Booting Linux on physical CPU",
				},
			},
			{
				Name: "userspace boot marker",
				Markers: []string{
					"checking http://169.254.169.254",
					"login:",
				},
			},
		})
		serialDone <- serialMarkerResult{Output: output, Err: err}
	}()
	t.Cleanup(func() {
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

	select {
	case result := <-serialDone:
		if result.Err != nil {
			t.Fatalf("waiting for serial boot evidence: %v\nserial tail:\n%s", result.Err, tailString(result.Output, 8192))
		}
	case <-ctx.Done():
		t.Fatalf("waiting for serial boot evidence: %v", ctx.Err())
	}
}

func unwrapCommandError(err error) error {
	if err == nil {
		return nil
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		return unwrapped
	}
	return err
}
