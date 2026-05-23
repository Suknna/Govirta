// Command qemucli renders a typed QEMU command line and prints it to stdout.
// It does not start a QEMU process; consumers redirect or copy the output
// into their own runner.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/suknna/govirta/internal/virt/qemu"
	"github.com/suknna/govirta/internal/virt/qemu/blockdev"
	"github.com/suknna/govirta/internal/virt/qemu/chardev"
	"github.com/suknna/govirta/internal/virt/qemu/cpu"
	"github.com/suknna/govirta/internal/virt/qemu/device"
	"github.com/suknna/govirta/internal/virt/qemu/display"
	"github.com/suknna/govirta/internal/virt/qemu/machine"
	"github.com/suknna/govirta/internal/virt/qemu/monitor"
	"github.com/suknna/govirta/internal/virt/qemu/netdev"
	"github.com/suknna/govirta/internal/virt/qemu/serial"
)

func main() {
	argv, err := buildDefaultArgv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(strings.Join(argv, " "))
}

// buildDefaultArgv constructs a representative QEMU argv using the typed
// builder API. It mirrors the golden test in internal/virt/qemu so the CLI
// output matches the canonical flag set.
func buildDefaultArgv() ([]string, error) {
	vm, err := qemu.NewVM(qemu.ArchX86_64).
		Name("prod-vm", qemu.NameDebugThreads(qemu.On)).
		Machine(machine.ProfileX86_64Q35KVM).
		CPU(cpu.ModelHost).
		SMP(qemu.SMP{CPUs: 4, Cores: 4, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(8192)).
		AddBlockdev(blockdev.Qcow2{
			NodeName: "root",
			File:     blockdev.FileProtocol{Filename: "/var/lib/vm/root.qcow2"},
			Cache:    blockdev.Cache{Direct: qemu.Off},
			AIO:      blockdev.AIOThreads,
		}).
		AddDevice(device.VirtioBlkPCI{
			ID: "rootdev", Drive: blockdev.Ref("root"), BootIndex: qemu.Int(1),
		}).
		AddNetdev(netdev.Tap{
			ID: "net0", IfName: "tap0",
			Script: netdev.ScriptNo, DownScript: netdev.ScriptNo,
			Vhost: qemu.On,
		}).
		AddDevice(device.VirtioNetPCI{
			ID: "nic0", Netdev: netdev.Ref("net0"),
			Mac: device.MAC("52:54:00:12:34:56"),
		}).
		AddChardev(chardev.Socket{
			ID: "qmp0", Path: "/run/vm/prod.qmp",
			Server: qemu.On, Wait: qemu.Off,
		}).
		Monitor(monitor.Monitor{Chardev: chardev.Ref("qmp0"), Mode: monitor.ModeControl}).
		AddChardev(chardev.Socket{
			ID: "serial0", Path: "/run/vm/prod.console",
			Server: qemu.On, Wait: qemu.Off,
		}).
		Serial(serial.Chardev("serial0")).
		Display(display.None).
		NoReboot().NoShutdown().
		Msg(qemu.Msg{Timestamp: qemu.On, GuestName: qemu.On}).
		PidFile("/run/vm/prod.pid").
		Build()
	if err != nil {
		return nil, err
	}
	return vm.Argv(), nil
}
