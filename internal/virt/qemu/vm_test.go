package qemu_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

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

func TestVMArgvBuildsRequiredQEMUCommand(t *testing.T) {
	vm, err := qemu.NewVM(qemu.ArchX86_64).
		Name("prod-vm", qemu.NameDebugThreads(qemu.On)).
		Machine(machine.TypeQ35,
			machine.WithAccel(machine.AccelKVM),
			machine.WithKernelIRQChip(machine.IRQChipSplit),
		).
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
		t.Fatalf("Build() error = %v", err)
	}

	argv := vm.Argv()

	want := []string{
		"qemu-system-x86_64",
		"-name", "prod-vm,debug-threads=on",
		"-machine", "type=q35,accel=kvm,kernel-irqchip=split",
		"-cpu", "host",
		"-smp", "cpus=4,cores=4,threads=1,sockets=1",
		"-m", "size=8192",
		"-blockdev", "driver=qcow2,node-name=root,file.driver=file,file.filename=/var/lib/vm/root.qcow2,cache.direct=off,aio=threads",
		"-device", "virtio-blk-pci,drive=root,bootindex=1,id=rootdev",
		"-netdev", "tap,id=net0,ifname=tap0,script=no,downscript=no,vhost=on",
		"-device", "virtio-net-pci,netdev=net0,mac=52:54:00:12:34:56,id=nic0",
		"-chardev", "socket,id=qmp0,path=/run/vm/prod.qmp,server=on,wait=off",
		"-mon", "chardev=qmp0,mode=control",
		"-chardev", "socket,id=serial0,path=/run/vm/prod.console,server=on,wait=off",
		"-serial", "chardev:serial0",
		"-display", "none",
		"-no-reboot",
		"-no-shutdown",
		"-msg", "timestamp=on,guest-name=on",
		"-pidfile", "/run/vm/prod.pid",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("Argv() =\n%s\nwant\n%s", strings.Join(argv, " "), strings.Join(want, " "))
	}
}

func TestBuildRejectsInvalidConfig(t *testing.T) {
	cases := []struct {
		name string
		make func() (qemu.VM, error)
	}{
		{
			name: "empty_binary",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.Arch("")).Build()
			},
		},
		{
			name: "smp_cpus_zero",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					SMP(qemu.SMP{CPUs: 0, Cores: 1, Threads: 1, Sockets: 1}).
					Build()
			},
		},
		{
			name: "smp_cores_zero",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					SMP(qemu.SMP{CPUs: 1, Cores: 0, Threads: 1, Sockets: 1}).
					Build()
			},
		},
		{
			name: "smp_threads_zero",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					SMP(qemu.SMP{CPUs: 1, Cores: 1, Threads: 0, Sockets: 1}).
					Build()
			},
		},
		{
			name: "smp_sockets_zero",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					SMP(qemu.SMP{CPUs: 1, Cores: 1, Threads: 1, Sockets: 0}).
					Build()
			},
		},
		{
			name: "memory_zero",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					Memory(qemu.MiB(0)).
					Build()
			},
		},
		{
			name: "memory_negative",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					Memory(qemu.MiB(-1)).
					Build()
			},
		},
		{
			name: "unsupported_device",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddDevice("not-a-device").
					Build()
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.make()
			if err == nil {
				t.Fatalf("Build() error = nil, want error")
			}
			if !errors.Is(err, qemu.ErrInvalidVM) {
				t.Fatalf("Build() error = %v, want errors.Is(err, qemu.ErrInvalidVM)", err)
			}
		})
	}
}

func TestVMArgvAllowsExplicitBinaryOverride(t *testing.T) {
	vm, err := qemu.NewVM(qemu.ArchAArch64).
		Binary("/usr/libexec/qemu-kvm").
		Name("arm-vm").
		Machine(machine.TypeVirt).
		CPU(cpu.ModelCortexA57).
		SMP(qemu.SMP{CPUs: 1, Cores: 1, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(256)).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	argv := vm.Argv()
	if argv[0] != "/usr/libexec/qemu-kvm" {
		t.Fatalf("argv[0] = %q, want /usr/libexec/qemu-kvm", argv[0])
	}
}
