package qemu

import (
	"fmt"
	"strconv"
)

type Invocation struct {
	Binary     string
	Args       []string
	StdoutPath string
}

type Builder struct{}

func NewBuilder() Builder {
	return Builder{}
}

func (b Builder) Build(cfg Config) (Invocation, error) {
	cfg = cfg.WithRuntimeDefaults()
	if err := cfg.Validate(); err != nil {
		return Invocation{}, err
	}

	args := []string{
		"-name", cfg.Name,
		"-machine", cfg.Machine.Type + ",accel=" + cfg.Machine.Accelerator,
	}

	if cfg.Compute.CPUModel != "" {
		args = append(args, "-cpu", cfg.Compute.CPUModel)
	}

	args = append(args,
		"-m", strconv.Itoa(cfg.Compute.MemoryMiB),
		"-smp", strconv.Itoa(cfg.Compute.VCPUs),
		"-nographic",
	)

	if cfg.Firmware.BIOSPath != "" {
		args = append(args, "-bios", cfg.Firmware.BIOSPath)
	}

	args = append(args,
		"-qmp", "unix:"+cfg.QMP.SocketPath+",server=on,wait=off",
	)

	if cfg.Process.PIDFilePath != "" {
		args = append(args, "-pidfile", cfg.Process.PIDFilePath)
	}
	if cfg.Logging.QEMULogPath != "" {
		args = append(args, "-D", cfg.Logging.QEMULogPath)
	}
	if cfg.Boot.Order != "" {
		args = append(args, "-boot", "order="+cfg.Boot.Order)
	}

	for _, disk := range cfg.Disks {
		args = append(args, "-drive", formatDrive(disk))
	}
	for _, nic := range cfg.NICs {
		args = append(args,
			"-netdev", formatTapNetdev(nic),
			"-device", formatNICDevice(nic),
		)
	}

	args = append(args, cfg.ExtraArgs...)

	return Invocation{Binary: cfg.Binary, Args: args, StdoutPath: cfg.Console.SerialLogPath}, nil
}

func formatDrive(disk DiskConfig) string {
	return fmt.Sprintf("if=%s,file=%s,format=%s", disk.Interface, disk.Path, disk.Format)
}

func formatTapNetdev(nic NICConfig) string {
	return fmt.Sprintf("tap,id=%s,ifname=%s,script=no,downscript=no", nic.ID, nic.Tap.IfName)
}

func formatNICDevice(nic NICConfig) string {
	return fmt.Sprintf("%s,netdev=%s,mac=%s", nic.Model, nic.ID, nic.MAC)
}
