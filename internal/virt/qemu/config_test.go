package qemu

import (
	"errors"
	"runtime"
	"testing"
)

func TestDefaultConfigForRuntime(t *testing.T) {
	cfg := DefaultConfigForRuntime()

	if cfg.Machine.Accelerator != AcceleratorTCG {
		t.Fatalf("DefaultConfigForRuntime().Machine.Accelerator = %q, want %q", cfg.Machine.Accelerator, AcceleratorTCG)
	}

	switch runtime.GOARCH {
	case "amd64":
		if cfg.Binary != "qemu-system-x86_64" {
			t.Fatalf("amd64 default binary = %q, want qemu-system-x86_64", cfg.Binary)
		}
		if cfg.Machine.Type != "q35" {
			t.Fatalf("amd64 machine = %q, want q35", cfg.Machine.Type)
		}
	case "arm64":
		if cfg.Binary != "qemu-system-aarch64" {
			t.Fatalf("arm64 default binary = %q, want qemu-system-aarch64", cfg.Binary)
		}
		if cfg.Machine.Type != "virt" {
			t.Fatalf("arm64 machine = %q, want virt", cfg.Machine.Type)
		}
		if cfg.Compute.CPUModel == "" {
			t.Fatalf("arm64 CPUModel is empty, want non-empty runtime default")
		}
	default:
		if cfg.Binary != "qemu-system-"+runtime.GOARCH {
			t.Fatalf("fallback binary = %q, want qemu-system-%s", cfg.Binary, runtime.GOARCH)
		}
	}
}

func TestConfigValidateRejectsMissingRequiredFields(t *testing.T) {
	cfg := Config{}
	err := cfg.Validate()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Validate() error = %v, want ErrInvalidConfig", err)
	}
}

func TestConfigValidateAcceptsRemoteTapBaseline(t *testing.T) {
	cfg := Config{
		Binary: "/usr/libexec/qemu-kvm",
		Name:   "cirros-dev-tap",
		Machine: MachineConfig{
			Type:        "virt",
			Accelerator: AcceleratorTCG,
		},
		Compute: ComputeConfig{
			MemoryMiB: 256,
			VCPUs:     1,
			CPUModel:  "cortex-a57",
		},
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

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestConfigValidateRejectsNICWithoutTap(t *testing.T) {
	cfg := Config{
		Binary: "qemu-system-x86_64",
		Name:   "vm",
		Machine: MachineConfig{
			Type:        "q35",
			Accelerator: AcceleratorTCG,
		},
		Compute: ComputeConfig{MemoryMiB: 256, VCPUs: 1},
		QMP:     QMPConfig{SocketPath: ".tmp/qemu/vm/qmp.sock"},
		Disks: []DiskConfig{{
			ID:        "root",
			Path:      ".tmp/images/root.qcow2",
			Format:    DiskFormatQCOW2,
			Interface: DiskInterfaceVirtio,
		}},
		NICs: []NICConfig{{
			ID:    "net0",
			Model: NICModelVirtioNetPCI,
			MAC:   "52:54:00:12:34:56",
		}},
	}

	err := cfg.Validate()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Validate() error = %v, want ErrInvalidConfig", err)
	}
}
