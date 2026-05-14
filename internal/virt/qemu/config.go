package qemu

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
)

var ErrInvalidConfig = errors.New("invalid qemu config")

const (
	AcceleratorTCG = "tcg"
	AcceleratorKVM = "kvm"

	DiskFormatQCOW2 = "qcow2"
	DiskFormatRaw   = "raw"

	DiskInterfaceVirtio = "virtio"

	NICModelVirtioNetPCI = "virtio-net-pci"
)

type Config struct {
	Binary    string
	Name      string
	Machine   MachineConfig
	Compute   ComputeConfig
	Firmware  FirmwareConfig
	QMP       QMPConfig
	Disks     []DiskConfig
	NICs      []NICConfig
	Console   ConsoleConfig
	Logging   LoggingConfig
	Process   ProcessConfig
	Boot      BootConfig
	ExtraArgs []string
}

type MachineConfig struct {
	Type        string
	Accelerator string
}

type ComputeConfig struct {
	MemoryMiB int
	VCPUs     int
	CPUModel  string
}

type FirmwareConfig struct {
	BIOSPath string
}

type QMPConfig struct {
	SocketPath string
}

type DiskConfig struct {
	ID        string
	Path      string
	Format    string
	Interface string
}

type NICConfig struct {
	ID    string
	Model string
	MAC   string
	Tap   TapBackendConfig
}

type TapBackendConfig struct {
	IfName string
}

type ConsoleConfig struct {
	SerialLogPath string
}

type LoggingConfig struct {
	QEMULogPath string
}

type ProcessConfig struct {
	PIDFilePath string
}

type BootConfig struct {
	Order string
}

func DefaultConfigForRuntime() Config {
	cfg := Config{
		Machine: MachineConfig{Accelerator: AcceleratorTCG},
	}

	switch runtime.GOARCH {
	case "amd64":
		cfg.Binary = "qemu-system-x86_64"
		cfg.Machine.Type = "q35"
	case "arm64":
		cfg.Binary = "qemu-system-aarch64"
		cfg.Machine.Type = "virt"
		cfg.Compute.CPUModel = "cortex-a57"
	default:
		cfg.Binary = "qemu-system-" + runtime.GOARCH
		cfg.Machine.Type = "virt"
	}

	return cfg
}

func (c Config) WithRuntimeDefaults() Config {
	defaults := DefaultConfigForRuntime()

	if c.Binary == "" {
		c.Binary = defaults.Binary
	}
	if c.Machine.Type == "" {
		c.Machine.Type = defaults.Machine.Type
	}
	if c.Machine.Accelerator == "" {
		c.Machine.Accelerator = defaults.Machine.Accelerator
	}
	if c.Compute.CPUModel == "" {
		c.Compute.CPUModel = defaults.Compute.CPUModel
	}

	return c
}

func (c Config) Validate() error {
	c = c.WithRuntimeDefaults()

	if strings.TrimSpace(c.Binary) == "" {
		return invalidConfig("binary is required")
	}
	if strings.TrimSpace(c.Name) == "" {
		return invalidConfig("name is required")
	}
	if strings.TrimSpace(c.Machine.Type) == "" {
		return invalidConfig("machine type is required")
	}
	if c.Machine.Accelerator != AcceleratorTCG && c.Machine.Accelerator != AcceleratorKVM {
		return invalidConfig("unsupported accelerator %q", c.Machine.Accelerator)
	}
	if c.Compute.MemoryMiB <= 0 {
		return invalidConfig("memory_mib must be positive")
	}
	if c.Compute.VCPUs <= 0 {
		return invalidConfig("vcpus must be positive")
	}
	if firmwareRequired(c) && strings.TrimSpace(c.Firmware.BIOSPath) == "" {
		return invalidConfig("firmware bios path is required on arm64")
	}
	if strings.TrimSpace(c.QMP.SocketPath) == "" {
		return invalidConfig("qmp socket path is required")
	}
	if len(c.Disks) == 0 {
		return invalidConfig("at least one disk is required")
	}
	for _, disk := range c.Disks {
		if err := validateDisk(disk); err != nil {
			return err
		}
	}
	for _, nic := range c.NICs {
		if err := validateNIC(nic); err != nil {
			return err
		}
	}

	return nil
}

func validateDisk(disk DiskConfig) error {
	if strings.TrimSpace(disk.ID) == "" {
		return invalidConfig("disk id is required")
	}
	if strings.TrimSpace(disk.Path) == "" {
		return invalidConfig("disk path is required")
	}
	if disk.Format != DiskFormatQCOW2 && disk.Format != DiskFormatRaw {
		return invalidConfig("unsupported disk format %q", disk.Format)
	}
	if disk.Interface != DiskInterfaceVirtio {
		return invalidConfig("unsupported disk interface %q", disk.Interface)
	}
	return nil
}

func firmwareRequired(c Config) bool {
	if c.Machine.Type == "virt" {
		return true
	}
	if strings.Contains(c.Binary, "aarch64") {
		return true
	}
	if runtime.GOARCH == "arm64" && strings.Contains(c.Binary, "qemu-kvm") {
		return true
	}
	return false
}

func validateNIC(nic NICConfig) error {
	if strings.TrimSpace(nic.ID) == "" {
		return invalidConfig("nic id is required")
	}
	if nic.Model != NICModelVirtioNetPCI {
		return invalidConfig("unsupported nic model %q", nic.Model)
	}
	if strings.TrimSpace(nic.MAC) == "" {
		return invalidConfig("nic mac is required")
	}
	if strings.TrimSpace(nic.Tap.IfName) == "" {
		return invalidConfig("nic tap ifname is required")
	}
	return nil
}

func invalidConfig(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}
