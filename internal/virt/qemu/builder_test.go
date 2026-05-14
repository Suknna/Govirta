package qemu

import (
	"reflect"
	"testing"
)

func TestBuilderBuildsRemoteTapBaselineInvocation(t *testing.T) {
	cfg := Config{
		Binary: "/usr/libexec/qemu-kvm",
		Name:   "cirros-dev-tap",
		Machine: MachineConfig{
			Type:        "virt",
			Accelerator: AcceleratorTCG,
		},
		Compute:  ComputeConfig{MemoryMiB: 256, VCPUs: 1, CPUModel: "cortex-a57"},
		Firmware: FirmwareConfig{BIOSPath: "/usr/share/edk2/aarch64/QEMU_EFI.fd"},
		QMP:      QMPConfig{SocketPath: "/root/govirta-qemu-test/run/qmp.sock"},
		Disks: []DiskConfig{{
			ID:        "root",
			Path:      "/root/govirta-qemu-test/images/cirros-aarch64.qcow2",
			Format:    DiskFormatQCOW2,
			Interface: DiskInterfaceVirtio,
		}},
		NICs: []NICConfig{{
			ID:    "net0",
			Model: NICModelVirtioNetPCI,
			MAC:   "52:54:00:12:34:56",
			Tap:   TapBackendConfig{IfName: "gv-tap0"},
		}},
		Console: ConsoleConfig{SerialLogPath: "/root/govirta-qemu-test/run/serial.log"},
		Logging: LoggingConfig{QEMULogPath: "/root/govirta-qemu-test/run/qemu.log"},
		Process: ProcessConfig{PIDFilePath: "/root/govirta-qemu-test/run/qemu.pid"},
	}

	got, err := NewBuilder().Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v, want nil", err)
	}

	want := Invocation{
		Binary:     "/usr/libexec/qemu-kvm",
		StdoutPath: "/root/govirta-qemu-test/run/serial.log",
		Args: []string{
			"-name", "cirros-dev-tap",
			"-machine", "virt,accel=tcg",
			"-cpu", "cortex-a57",
			"-m", "256",
			"-smp", "1",
			"-nographic",
			"-bios", "/usr/share/edk2/aarch64/QEMU_EFI.fd",
			"-qmp", "unix:/root/govirta-qemu-test/run/qmp.sock,server=on,wait=off",
			"-pidfile", "/root/govirta-qemu-test/run/qemu.pid",
			"-D", "/root/govirta-qemu-test/run/qemu.log",
			"-drive", "if=virtio,file=/root/govirta-qemu-test/images/cirros-aarch64.qcow2,format=qcow2",
			"-netdev", "tap,id=net0,ifname=gv-tap0,script=no,downscript=no",
			"-device", "virtio-net-pci,netdev=net0,mac=52:54:00:12:34:56",
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Build() = %#v, want %#v", got, want)
	}
}

func TestBuilderAppendsExtraArgs(t *testing.T) {
	cfg := Config{
		Binary:    "qemu-system-x86_64",
		Name:      "vm",
		Machine:   MachineConfig{Type: "q35", Accelerator: AcceleratorTCG},
		Compute:   ComputeConfig{MemoryMiB: 256, VCPUs: 1},
		QMP:       QMPConfig{SocketPath: ".tmp/qemu/vm/qmp.sock"},
		Disks:     []DiskConfig{{ID: "root", Path: ".tmp/images/root.qcow2", Format: DiskFormatQCOW2, Interface: DiskInterfaceVirtio}},
		ExtraArgs: []string{"-no-reboot", "-snapshot"},
	}

	got, err := NewBuilder().Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v, want nil", err)
	}

	if got.Args[len(got.Args)-2] != "-no-reboot" || got.Args[len(got.Args)-1] != "-snapshot" {
		t.Fatalf("Build() args suffix = %v, want ExtraArgs suffix", got.Args)
	}
}
