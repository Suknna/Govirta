package qemu_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/blockdev"
	"github.com/suknna/govirta/pkg/virt/qemu/chardev"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
	"github.com/suknna/govirta/pkg/virt/qemu/device"
	"github.com/suknna/govirta/pkg/virt/qemu/display"
	"github.com/suknna/govirta/pkg/virt/qemu/firmware"
	"github.com/suknna/govirta/pkg/virt/qemu/machine"
	"github.com/suknna/govirta/pkg/virt/qemu/monitor"
	"github.com/suknna/govirta/pkg/virt/qemu/netdev"
	"github.com/suknna/govirta/pkg/virt/qemu/serial"
)

func TestVMArgvBuildsRequiredQEMUCommand(t *testing.T) {
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
		"-blockdev", "driver=qcow2,node-name=root,file.driver=file,file.filename=/var/lib/vm/root.qcow2,file.cache.direct=off,file.aio=threads",
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

func TestVMArgvRendersExplicitNoNIC(t *testing.T) {
	vm, err := qemu.NewVM(qemu.ArchAArch64).
		Machine(machine.ProfileAArch64VirtKVM).
		CPU(cpu.ModelHost).
		SMP(qemu.SMP{CPUs: 1, Cores: 1, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(256)).
		NoNIC().
		Display(display.None).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	argv := vm.Argv()
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-nic" && argv[i+1] == "none" {
			return
		}
	}
	t.Fatalf("Argv() = %#v, want adjacent -nic none", argv)
}

func TestBuildRejectsNoNICWithExplicitNetworkConfig(t *testing.T) {
	cases := []struct {
		name string
		make func() (qemu.VM, error)
	}{
		{
			name: "netdev",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchAArch64).
					NoNIC().
					AddNetdev(netdev.Tap{ID: "net0", IfName: "tap0"}).
					Build()
			},
		},
		{
			name: "virtio_net_device",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchAArch64).
					NoNIC().
					AddDevice(device.VirtioNetPCI{ID: "nic0", Netdev: netdev.Ref("net0")}).
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
			if !strings.Contains(err.Error(), "NoNIC cannot be combined with explicit network devices") {
				t.Fatalf("Build() error = %q, want NoNIC conflict message", err.Error())
			}
		})
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
			name: "nil_device",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddDevice(nil).
					Build()
			},
		},
		{
			name: "nil_argument",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddArgument(nil).
					Build()
			},
		},
		{
			name: "empty_argument_flag",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddArgument(qemu.Arg("", "value")).
					Build()
			},
		},
		{
			name: "empty_argument_value",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddArgument(qemu.Arg("-rtc", "")).
					Build()
			},
		},
		{
			name: "empty_flag",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddArgument(qemu.Flag("")).
					Build()
			},
		},
		{
			name: "invalid_cpu_model",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					CPU(cpu.Model("bad-cpu")).
					Build()
			},
		},
		{
			name: "invalid_display",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					Display(display.Display("gtk")).
					Build()
			},
		},
		{
			name: "generic_machine_argument",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddArgument(qemu.Arg("-machine", "type=q35")).
					Build()
			},
		},
		{
			name: "generic_machine_alias_argument",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddArgument(qemu.Arg("-M", "type=q35")).
					Build()
			},
		},
		{
			name: "generic_machine_alias_flag",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddArgument(qemu.Flag("-M")).
					Build()
			},
		},
		{
			name: "unknown_machine_profile",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					Machine(machine.Profile("unknown")).
					Build()
			},
		},
		{
			name: "empty_blockdev_fields",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddBlockdev(blockdev.Qcow2{}).
					Build()
			},
		},
		{
			name: "empty_netdev_fields",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddNetdev(netdev.Tap{}).
					Build()
			},
		},
		{
			name: "empty_chardev_fields",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddChardev(chardev.Socket{}).
					Build()
			},
		},
		{
			name: "empty_virtio_blk_drive",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddDevice(device.VirtioBlkPCI{}).
					Build()
			},
		},
		{
			name: "empty_virtio_net_netdev",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddDevice(device.VirtioNetPCI{}).
					Build()
			},
		},
		{
			name: "empty_monitor_fields",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					Monitor(monitor.Monitor{}).
					Build()
			},
		},
		{
			name: "invalid_msg_enum",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					Msg(qemu.Msg{Timestamp: qemu.OnOff("maybe")}).
					Build()
			},
		},
		{
			name: "netdev_injection_value",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					AddNetdev(netdev.Tap{ID: "net0", IfName: "tap0,script=/bad"}).
					Build()
			},
		},
		{
			name: "nil_name_option",
			make: func() (qemu.VM, error) {
				return qemu.NewVM(qemu.ArchX86_64).
					Name("vm", nil).
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

func TestNewVMRejectsUnsupportedArchWithSpecificMessage(t *testing.T) {
	// 回归 F9：未知 arch（如把 aarch64 拼成 arm64）应给出明确的
	// 「unsupported arch」错误，而不是模糊的 「qemu binary is required」。
	cases := []struct {
		name string
		arch qemu.Arch
	}{
		{name: "empty_arch", arch: qemu.Arch("")},
		{name: "common_typo_arm64", arch: qemu.Arch("arm64")},
		{name: "common_typo_amd64", arch: qemu.Arch("amd64")},
		{name: "uppercase", arch: qemu.Arch("X86_64")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := qemu.NewVM(tc.arch).Build()
			if err == nil {
				t.Fatalf("Build() error = nil, want error")
			}
			if !errors.Is(err, qemu.ErrInvalidVM) {
				t.Fatalf("Build() error = %v, want errors.Is(err, qemu.ErrInvalidVM)", err)
			}
			if !strings.Contains(err.Error(), "unsupported arch") {
				t.Fatalf("Build() error = %q, want it to mention 'unsupported arch'", err.Error())
			}
		})
	}
}

func TestNewVMUnsupportedArchClearedByExplicitBinary(t *testing.T) {
	// 回归 F9：远程 acceptance 主机使用 /usr/libexec/qemu-kvm，调用方
	// 可能传未知 arch 然后用 Binary 覆盖；这条路径必须能通过 Build。
	vm, err := qemu.NewVM(qemu.Arch("custom-arch")).
		Binary("/usr/libexec/qemu-kvm").
		Machine(machine.ProfileAArch64VirtKVM).
		CPU(cpu.ModelCortexA57).
		SMP(qemu.SMP{CPUs: 1, Cores: 1, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(256)).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v, want nil after explicit Binary override", err)
	}
	argv := vm.Argv()
	if len(argv) == 0 || argv[0] != "/usr/libexec/qemu-kvm" {
		t.Fatalf("Argv()[0] = %v, want /usr/libexec/qemu-kvm", argv)
	}
}

func TestVMArgvAllowsExplicitBinaryOverride(t *testing.T) {
	vm, err := qemu.NewVM(qemu.ArchAArch64).
		Binary("/usr/libexec/qemu-kvm").
		Name("arm-vm").
		Machine(machine.ProfileAArch64VirtKVM).
		CPU(cpu.ModelCortexA57).
		SMP(qemu.SMP{CPUs: 1, Cores: 1, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(256)).
		BIOS(firmware.BIOS{Path: "/usr/share/edk2/aarch64/QEMU_EFI.fd"}).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	argv := vm.Argv()
	want := []string{
		"/usr/libexec/qemu-kvm",
		"-name", "arm-vm",
		"-machine", "type=virt,accel=kvm",
		"-cpu", "cortex-a57",
		"-smp", "cpus=1,cores=1,threads=1,sockets=1",
		"-m", "size=256",
		"-bios", "/usr/share/edk2/aarch64/QEMU_EFI.fd",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("Argv() = %#v, want %#v", argv, want)
	}
}

type customDevice struct{}

func (customDevice) Arg() (string, error) { return "custom-pci,id=custom0", nil }

type typedNilDevice struct{}

func (*typedNilDevice) Arg() (string, error) { return "typed-nil-device", nil }

func TestBuilderAcceptsDeviceImplementationsWithoutCoreSwitchChanges(t *testing.T) {
	vm, err := qemu.NewVM(qemu.ArchX86_64).
		AddDevice(customDevice{}).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	argv := vm.Argv()
	want := []string{"qemu-system-x86_64", "-device", "custom-pci,id=custom0"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("Argv() = %#v, want %#v", argv, want)
	}
}

func TestBuildRejectsTypedNilDevice(t *testing.T) {
	var d *typedNilDevice

	_, err := qemu.NewVM(qemu.ArchX86_64).
		AddDevice(d).
		Build()
	if err == nil {
		t.Fatalf("Build() error = nil, want error")
	}
	if !errors.Is(err, qemu.ErrInvalidVM) {
		t.Fatalf("Build() error = %v, want errors.Is(err, qemu.ErrInvalidVM)", err)
	}
}

func TestBuilderAcceptsGenericArgumentsWithoutAddingDedicatedMethods(t *testing.T) {
	vm, err := qemu.NewVM(qemu.ArchX86_64).
		AddArgument(qemu.Arg("-rtc", "base=utc")).
		AddArgument(qemu.Flag("-enable-kvm")).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	argv := vm.Argv()
	want := []string{"qemu-system-x86_64", "-rtc", "base=utc", "-enable-kvm"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("Argv() = %#v, want %#v", argv, want)
	}
}

func TestBuildRejectsGenericTypedArgumentBypass(t *testing.T) {
	cases := []struct {
		name string
		arg  qemu.Argument
	}{
		{name: "machine", arg: qemu.Arg("-machine", "type=q35")},
		{name: "machine_alias", arg: qemu.Arg("-M", "type=q35")},
		{name: "name", arg: qemu.Arg("-name", "vm")},
		{name: "cpu", arg: qemu.Arg("-cpu", "host")},
		{name: "smp", arg: qemu.Arg("-smp", "cpus=1")},
		{name: "memory", arg: qemu.Arg("-m", "size=256")},
		{name: "bios", arg: qemu.Arg("-bios", "/usr/share/OVMF/OVMF_CODE.fd")},
		{name: "blockdev", arg: qemu.Arg("-blockdev", "driver=qcow2")},
		{name: "device", arg: qemu.Arg("-device", "virtio-net-pci")},
		{name: "netdev", arg: qemu.Arg("-netdev", "tap,id=net0,ifname=tap0")},
		{name: "chardev", arg: qemu.Arg("-chardev", "socket,id=qmp0")},
		{name: "monitor", arg: qemu.Arg("-mon", "chardev=qmp0,mode=control")},
		{name: "serial", arg: qemu.Arg("-serial", "chardev:serial0")},
		{name: "display", arg: qemu.Arg("-display", "none")},
		{name: "msg", arg: qemu.Arg("-msg", "timestamp=on")},
		{name: "pidfile", arg: qemu.Arg("-pidfile", "/run/vm.pid")},
		{name: "no_reboot", arg: qemu.Flag("-no-reboot")},
		{name: "no_shutdown", arg: qemu.Flag("-no-shutdown")},
		{name: "typedarg_cpu", arg: qemu.TypedArg("-cpu", func() (string, error) { return "max", nil })},
		{name: "typedarg_display", arg: qemu.TypedArg("-display", func() (string, error) { return "gtk", nil })},
		{name: "typedarg_machine", arg: qemu.TypedArg("-machine", func() (string, error) { return "type=q35", nil })},
		{name: "typedarg_bios", arg: qemu.TypedArg("-bios", func() (string, error) { return "/usr/share/OVMF/OVMF_CODE.fd", nil })},
		{name: "typedarg_netdev", arg: qemu.TypedArg("-netdev", func() (string, error) { return "tap,id=net0,ifname=tap0", nil })},
		{name: "enable_kvm_with_value", arg: qemu.Arg("-enable-kvm", "-no-reboot")},
		{name: "typedarg_enable_kvm", arg: qemu.TypedArg("-enable-kvm", func() (string, error) { return "-no-reboot", nil })},
		{name: "bios_without_value", arg: qemu.Flag("-bios")},
		{name: "rtc_without_value", arg: qemu.Flag("-rtc")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := qemu.NewVM(qemu.ArchX86_64).
				AddArgument(tc.arg).
				Build()
			if err == nil {
				t.Fatalf("Build() error = nil, want error")
			}
			if !errors.Is(err, qemu.ErrInvalidVM) {
				t.Fatalf("Build() error = %v, want errors.Is(err, qemu.ErrInvalidVM)", err)
			}
		})
	}
}

func TestBuilderAllowsExternalTypedArgForAllowlistedGenericArgument(t *testing.T) {
	vm, err := qemu.NewVM(qemu.ArchX86_64).
		AddArgument(qemu.TypedArg("-rtc", func() (string, error) {
			return "base=utc", nil
		})).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	argv := vm.Argv()
	want := []string{"qemu-system-x86_64", "-rtc", "base=utc"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("Argv() = %#v, want %#v", argv, want)
	}
}
