# QEMU Entrypoints Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build typed, testable Go package boundaries for `qemu-system-*` / `qemu-kvm` and `qemu-img`, then verify the QEMU package can start CirrOS with a TAP-backed NIC on `192.168.139.206`.

**Architecture:** `internal/virt/qemu` owns VM launch config, deterministic argv building, and process lifecycle. `internal/virt/qemuimg` owns offline image operations. Unit tests use fake runners and do not require QEMU, TAP, or CirrOS; integration tests are opt-in and run on the remote Linux validation host.

**Tech Stack:** Go 1.26, standard library `os/exec`, `runtime.GOARCH`, `encoding/json`, QEMU 6.2.0 baseline on Rocky Linux aarch64, `qemu-img`, TAP networking.

---

## Evidence and Constraints

- Spec: `docs/superpowers/specs/2026-05-13-qemu-entrypoints-design.md`.
- Current QEMU package: `internal/virt/qemu/driver.go` has only `Driver.Start(ctx, vmName string)` and `NoopDriver`; this interface is too narrow and may be replaced.
- Current QMP package: `internal/virt/qmp/client.go` is a no-op boundary; this plan does not implement a full QMP client, but integration tests may perform minimal QMP socket checks internally.
- Remote acceptance host baseline:
  - `root@192.168.139.206`
  - Rocky Linux 8.10, `aarch64`
  - QEMU system binary: `/usr/libexec/qemu-kvm`
  - qemu-img: `/usr/bin/qemu-img`
  - firmware: `/usr/share/edk2/aarch64/QEMU_EFI.fd`
  - bridge: `govirta0`
  - TAP: `gv-tap0`
  - image: `/root/govirta-qemu-test/images/cirros-aarch64.qcow2`
- Final acceptance must use Govirta packages to build and execute QEMU commands. Manual shell commands are only baseline evidence.
- Unit tests must pass without real QEMU binaries or TAP devices.
- No production code may create orphan contexts with `context.Background()` or `context.TODO()`.
- Commands must be binary plus `[]string`; do not build shell command strings.
- Temporary integration artifacts must be under `.tmp/` for local runs or `/root/govirta-qemu-test/run` on the remote host.

## File Structure

Create or modify these files:

```text
internal/virt/qemu/config.go          // typed VM config, runtime defaults, validation
internal/virt/qemu/config_test.go     // defaults and validation tests
internal/virt/qemu/builder.go         // Config -> Invocation{Binary, Args}
internal/virt/qemu/builder_test.go    // deterministic argv tests
internal/virt/qemu/runner.go          // ProcessRunner, OS runner, process handle
internal/virt/qemu/runner_test.go     // fake-command and cancellation-safe tests
internal/virt/qemu/driver.go          // driver API using Config, replacing narrow Start(vmName)
internal/virt/qemu/driver_test.go     // driver uses builder + runner
internal/virt/qemu/integration_test.go // gated CirrOS TAP acceptance
internal/virt/qemuimg/client.go       // qemu-img client, runner, requests, ImageInfo
internal/virt/qemuimg/client_test.go  // create/info/resize argv and JSON parsing tests
internal/node/agent.go                // adjust only if qemu.Driver signature breaks compile
configs/govirtlet.example.yaml        // show configurable binary/firmware/tap examples
docs/superpowers/specs/2026-05-13-qemu-entrypoints-design.md // already updated; only fix if implementation reveals mismatch
```

---

### Task 1: Define QEMU Config, Defaults, and Validation

**Files:**
- Create: `internal/virt/qemu/config.go`
- Create: `internal/virt/qemu/config_test.go`

- [ ] **Step 1: Write failing defaults and validation tests**

Create `internal/virt/qemu/config_test.go` with:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/virt/qemu
```

Expected: FAIL with undefined symbols such as `DefaultConfigForRuntime`, `Config`, and `ErrInvalidConfig`.

- [ ] **Step 3: Implement config types, constants, defaults, and validation**

Create `internal/virt/qemu/config.go` with:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
go test ./internal/virt/qemu
```

Expected: PASS.

- [ ] **Step 5: Checkpoint**

Do not create a git commit unless the user explicitly asks. Record changed files for later:

```text
internal/virt/qemu/config.go
internal/virt/qemu/config_test.go
```

---

### Task 2: Build Deterministic qemu-system argv

**Files:**
- Create: `internal/virt/qemu/builder.go`
- Create: `internal/virt/qemu/builder_test.go`

- [ ] **Step 1: Write failing builder tests**

Create `internal/virt/qemu/builder_test.go` with:

```go
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
		Compute: ComputeConfig{MemoryMiB: 256, VCPUs: 1, CPUModel: "cortex-a57"},
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
		Binary: "qemu-system-x86_64",
		Name:   "vm",
		Machine: MachineConfig{Type: "q35", Accelerator: AcceleratorTCG},
		Compute: ComputeConfig{MemoryMiB: 256, VCPUs: 1},
		QMP:     QMPConfig{SocketPath: ".tmp/qemu/vm/qmp.sock"},
		Disks: []DiskConfig{{ID: "root", Path: ".tmp/images/root.qcow2", Format: DiskFormatQCOW2, Interface: DiskInterfaceVirtio}},
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/virt/qemu -run 'TestBuilder'
```

Expected: FAIL with undefined `Invocation`, `NewBuilder`, or `Build`.

- [ ] **Step 3: Implement builder**

Create `internal/virt/qemu/builder.go` with:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
go test ./internal/virt/qemu -run 'TestBuilder|TestConfig'
```

Expected: PASS.

- [ ] **Step 5: Checkpoint**

Do not commit unless requested. Record changed files:

```text
internal/virt/qemu/builder.go
internal/virt/qemu/builder_test.go
```

---

### Task 3: Implement QEMU Process Runner and Driver

**Files:**
- Create: `internal/virt/qemu/runner.go`
- Modify: `internal/virt/qemu/driver.go`
- Modify: `internal/virt/qemu/driver_test.go`

- [ ] **Step 1: Replace driver tests with runner-backed driver tests**

Replace `internal/virt/qemu/driver_test.go` with:

```go
package qemu

import (
	"context"
	"reflect"
	"testing"
)

type fakeProcessRunner struct {
	binary string
	args   []string
}

func (r *fakeProcessRunner) Start(ctx context.Context, inv Invocation) (Process, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	r.binary = inv.Binary
	r.args = append([]string(nil), inv.Args...)
	return fakeProcess{}, nil
}

type fakeProcess struct{}

func (fakeProcess) PID() int { return 1234 }
func (fakeProcess) Wait() error { return nil }
func (fakeProcess) Stop(ctx context.Context) error { return ctx.Err() }

func TestDriverStartUsesBuilderAndRunner(t *testing.T) {
	runner := &fakeProcessRunner{}
	driver := NewDriver(NewBuilder(), runner)
	cfg := Config{
		Binary: "qemu-system-x86_64",
		Name:   "vm",
		Machine: MachineConfig{Type: "q35", Accelerator: AcceleratorTCG},
		Compute: ComputeConfig{MemoryMiB: 256, VCPUs: 1},
		QMP:     QMPConfig{SocketPath: ".tmp/qemu/vm/qmp.sock"},
		Disks: []DiskConfig{{ID: "root", Path: ".tmp/images/root.qcow2", Format: DiskFormatQCOW2, Interface: DiskInterfaceVirtio}},
	}

	proc, err := driver.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}
	if proc.PID() != 1234 {
		t.Fatalf("PID() = %d, want 1234", proc.PID())
	}
	if runner.binary != "qemu-system-x86_64" {
		t.Fatalf("runner binary = %q, want qemu-system-x86_64", runner.binary)
	}
	if !reflect.DeepEqual(runner.args[:2], []string{"-name", "vm"}) {
		t.Fatalf("runner args prefix = %v, want [-name vm]", runner.args[:2])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/virt/qemu -run 'TestDriver'
```

Expected: FAIL because `NewDriver`, `ProcessRunner`, and `Process` are not defined, and the existing `Driver` interface still uses `Start(ctx, vmName string)`.

- [ ] **Step 3: Implement runner and update driver**

Create `internal/virt/qemu/runner.go` with:

```go
package qemu

import (
	"context"
	"os"
	"os/exec"
	"sync"
)

type ProcessRunner interface {
	Start(ctx context.Context, inv Invocation) (Process, error)
}

type Process interface {
	PID() int
	Wait() error
	Stop(ctx context.Context) error
}

type OSProcessRunner struct{}

func NewOSProcessRunner() OSProcessRunner {
	return OSProcessRunner{}
}

func (r OSProcessRunner) Start(ctx context.Context, inv Invocation) (Process, error) {
	cmd := exec.CommandContext(ctx, inv.Binary, inv.Args...)
	var output *os.File
	if inv.StdoutPath != "" {
		file, err := os.OpenFile(inv.StdoutPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		output = file
		cmd.Stdout = file
		cmd.Stderr = file
	}
	if err := cmd.Start(); err != nil {
		if output != nil {
			_ = output.Close()
		}
		return nil, err
	}
	return &osProcess{cmd: cmd, output: output}, nil
}

type osProcess struct {
	cmd       *exec.Cmd
	output    *os.File
	closeOnce sync.Once
}

func (p *osProcess) PID() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *osProcess) Wait() error {
	err := p.cmd.Wait()
	p.closeOutput()
	return err
}

func (p *osProcess) Stop(ctx context.Context) error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Signal(execSignalTerm()); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		p.closeOutput()
		return ctx.Err()
	case err := <-done:
		p.closeOutput()
		return err
	}
}

func (p *osProcess) closeOutput() {
	p.closeOnce.Do(func() {
		if p.output != nil {
			_ = p.output.Close()
		}
	})
}
```

Create `internal/virt/qemu/signal_unix.go` with:

```go
//go:build !windows

package qemu

import (
	"os"
	"syscall"
)

func execSignalTerm() os.Signal {
	return syscall.SIGTERM
}
```

Create `internal/virt/qemu/signal_windows.go` with:

```go
//go:build windows

package qemu

import "os"

func execSignalTerm() os.Signal {
	return os.Kill
}
```

Replace `internal/virt/qemu/driver.go` with:

```go
package qemu

import "context"

type Driver interface {
	Name() string
	Start(ctx context.Context, cfg Config) (Process, error)
}

type RealDriver struct {
	builder Builder
	runner  ProcessRunner
}

func NewDriver(builder Builder, runner ProcessRunner) *RealDriver {
	return &RealDriver{builder: builder, runner: runner}
}

func NewDefaultDriver() *RealDriver {
	return NewDriver(NewBuilder(), NewOSProcessRunner())
}

func (d *RealDriver) Name() string {
	return "qemu"
}

func (d *RealDriver) Start(ctx context.Context, cfg Config) (Process, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	inv, err := d.builder.Build(cfg)
	if err != nil {
		return nil, err
	}
	return d.runner.Start(ctx, inv)
}

type NoopDriver struct{}

func NewNoopDriver() *NoopDriver {
	return &NoopDriver{}
}

func (d *NoopDriver) Name() string {
	return "qemu-noop"
}

func (d *NoopDriver) Start(ctx context.Context, cfg Config) (Process, error) {
	_ = cfg
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return fakeNoopProcess{}, nil
	}
}

type fakeNoopProcess struct{}

func (fakeNoopProcess) PID() int { return 0 }
func (fakeNoopProcess) Wait() error { return nil }
func (fakeNoopProcess) Stop(ctx context.Context) error { return ctx.Err() }
```

- [ ] **Step 4: Fix node compile break**

Modify `internal/node/agent.go` so `Run` only calls `Name()` and does not call the old `Stop` method. No additional change is needed if it only uses `Name()`.

If compile fails because interface usage differs, update only the minimum lines required in `internal/node/agent.go` to keep the skeleton compiling.

- [ ] **Step 5: Run tests**

Run:

```bash
go test ./internal/virt/qemu ./internal/node
```

Expected: PASS.

- [ ] **Step 6: Checkpoint**

Do not commit unless requested. Record changed files:

```text
internal/virt/qemu/runner.go
internal/virt/qemu/signal_unix.go
internal/virt/qemu/signal_windows.go
internal/virt/qemu/driver.go
internal/virt/qemu/driver_test.go
internal/node/agent.go
```

---

### Task 4: Implement qemu-img Client

**Files:**
- Create: `internal/virt/qemuimg/client.go`
- Create: `internal/virt/qemuimg/client_test.go`

- [ ] **Step 1: Write failing qemu-img tests**

Create `internal/virt/qemuimg/client_test.go` with:

```go
package qemuimg

import (
	"context"
	"reflect"
	"testing"
)

type fakeRunner struct {
	binary string
	args   []string
	result CommandResult
}

func (r *fakeRunner) Run(ctx context.Context, binary string, args []string) (CommandResult, error) {
	select {
	case <-ctx.Done():
		return CommandResult{}, ctx.Err()
	default:
	}
	r.binary = binary
	r.args = append([]string(nil), args...)
	return r.result, nil
}

func TestClientCreateBuildsArgs(t *testing.T) {
	runner := &fakeRunner{}
	client := NewClient("qemu-img", runner)
	err := client.Create(context.Background(), CreateRequest{
		Path:      ".tmp/images/root.qcow2",
		Format:    FormatQCOW2,
		SizeBytes: 117440512,
	})
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	want := []string{"create", "-f", "qcow2", ".tmp/images/root.qcow2", "117440512"}
	if runner.binary != "qemu-img" || !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("runner = %q %v, want qemu-img %v", runner.binary, runner.args, want)
	}
}

func TestClientInfoParsesJSON(t *testing.T) {
	runner := &fakeRunner{result: CommandResult{Stdout: `{"virtual-size":117440512,"filename":"cirros.qcow2","format":"qcow2","actual-size":25169920}`}}
	client := NewClient("qemu-img", runner)
	info, err := client.Info(context.Background(), "cirros.qcow2")
	if err != nil {
		t.Fatalf("Info() error = %v, want nil", err)
	}
	if info.Format != "qcow2" || info.VirtualSize != 117440512 || info.ActualSize != 25169920 {
		t.Fatalf("Info() = %#v, want parsed qcow2 sizes", info)
	}
	want := []string{"info", "--output=json", "cirros.qcow2"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("Info() args = %v, want %v", runner.args, want)
	}
}

func TestClientResizeBuildsArgs(t *testing.T) {
	runner := &fakeRunner{}
	client := NewClient("qemu-img", runner)
	err := client.Resize(context.Background(), ResizeRequest{Path: "root.qcow2", SizeBytes: 234881024})
	if err != nil {
		t.Fatalf("Resize() error = %v, want nil", err)
	}
	want := []string{"resize", "root.qcow2", "234881024"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("Resize() args = %v, want %v", runner.args, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/virt/qemuimg
```

Expected: FAIL because package/types do not exist.

- [ ] **Step 3: Implement qemu-img client**

Create `internal/virt/qemuimg/client.go` with:

```go
package qemuimg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const (
	FormatQCOW2 = "qcow2"
	FormatRaw   = "raw"
)

var ErrInvalidRequest = errors.New("invalid qemu-img request")

type CommandRunner interface {
	Run(ctx context.Context, binary string, args []string) (CommandResult, error)
}

type CommandResult struct {
	Stdout string
	Stderr string
}

type OSRunner struct{}

func (r OSRunner) Run(ctx context.Context, binary string, args []string) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return CommandResult{Stdout: string(stdout), Stderr: string(exitErr.Stderr)}, err
		}
		return CommandResult{Stdout: string(stdout)}, err
	}
	return CommandResult{Stdout: string(stdout)}, nil
}

type Client interface {
	Create(ctx context.Context, req CreateRequest) error
	Info(ctx context.Context, path string) (ImageInfo, error)
	Resize(ctx context.Context, req ResizeRequest) error
}

type ExecClient struct {
	binary string
	runner CommandRunner
}

func NewClient(binary string, runner CommandRunner) *ExecClient {
	return &ExecClient{binary: binary, runner: runner}
}

func NewDefaultClient() *ExecClient {
	return NewClient("qemu-img", OSRunner{})
}

type CreateRequest struct {
	Path          string
	Format        string
	SizeBytes     int64
	BackingFile   string
	BackingFormat string
}

type ResizeRequest struct {
	Path      string
	SizeBytes int64
}

type ImageInfo struct {
	Filename        string
	Format          string
	VirtualSize     int64
	ActualSize      int64
	BackingFilename string
}

func (c *ExecClient) Create(ctx context.Context, req CreateRequest) error {
	if err := validateCreate(req); err != nil {
		return err
	}
	args := []string{"create", "-f", req.Format}
	if req.BackingFile != "" {
		args = append(args, "-b", req.BackingFile)
		if req.BackingFormat != "" {
			args = append(args, "-F", req.BackingFormat)
		}
	}
	args = append(args, req.Path, strconv.FormatInt(req.SizeBytes, 10))
	_, err := c.runner.Run(ctx, c.binary, args)
	return err
}

func (c *ExecClient) Info(ctx context.Context, path string) (ImageInfo, error) {
	if strings.TrimSpace(path) == "" {
		return ImageInfo{}, invalidRequest("path is required")
	}
	result, err := c.runner.Run(ctx, c.binary, []string{"info", "--output=json", path})
	if err != nil {
		return ImageInfo{}, err
	}
	return parseInfo(result.Stdout)
}

func (c *ExecClient) Resize(ctx context.Context, req ResizeRequest) error {
	if strings.TrimSpace(req.Path) == "" {
		return invalidRequest("path is required")
	}
	if req.SizeBytes <= 0 {
		return invalidRequest("size_bytes must be positive")
	}
	_, err := c.runner.Run(ctx, c.binary, []string{"resize", req.Path, strconv.FormatInt(req.SizeBytes, 10)})
	return err
}

func validateCreate(req CreateRequest) error {
	if strings.TrimSpace(req.Path) == "" {
		return invalidRequest("path is required")
	}
	if req.Format != FormatQCOW2 && req.Format != FormatRaw {
		return invalidRequest("unsupported format %q", req.Format)
	}
	if req.SizeBytes <= 0 {
		return invalidRequest("size_bytes must be positive")
	}
	if req.BackingFile != "" && req.BackingFormat == "" {
		return invalidRequest("backing_format is required when backing_file is set")
	}
	return nil
}

func parseInfo(raw string) (ImageInfo, error) {
	var payload struct {
		Filename        string `json:"filename"`
		Format          string `json:"format"`
		VirtualSize     int64  `json:"virtual-size"`
		ActualSize      int64  `json:"actual-size"`
		BackingFilename string `json:"backing-filename"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ImageInfo{}, err
	}
	return ImageInfo{
		Filename:        payload.Filename,
		Format:          payload.Format,
		VirtualSize:     payload.VirtualSize,
		ActualSize:      payload.ActualSize,
		BackingFilename: payload.BackingFilename,
	}, nil
}

func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/virt/qemuimg
```

Expected: PASS.

- [ ] **Step 5: Checkpoint**

Do not commit unless requested. Record changed files:

```text
internal/virt/qemuimg/client.go
internal/virt/qemuimg/client_test.go
```

---

### Task 5: Add Gated Integration Acceptance Test

**Files:**
- Create: `internal/virt/qemu/integration_test.go`

- [ ] **Step 1: Write gated integration test**

Create `internal/virt/qemu/integration_test.go` with:

```go
package qemu

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIntegrationCirrOSWithTap(t *testing.T) {
	if os.Getenv("GOVIRTA_QEMU_INTEGRATION") != "1" {
		t.Skip("set GOVIRTA_QEMU_INTEGRATION=1 to run QEMU integration test")
	}

	image := requiredEnv(t, "GOVIRTA_CIRROS_IMAGE")
	binary := requiredEnv(t, "GOVIRTA_QEMU_BINARY")
	firmware := os.Getenv("GOVIRTA_QEMU_FIRMWARE")
	tapName := requiredEnv(t, "GOVIRTA_QEMU_TAP")

	runDir := filepath.Join(".tmp", "qemu", "integration")
	if remoteRunDir := os.Getenv("GOVIRTA_QEMU_RUN_DIR"); remoteRunDir != "" {
		runDir = remoteRunDir
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", runDir, err)
	}

	cfg := Config{
		Binary: binary,
		Name:   "cirros-dev-tap",
		Machine: MachineConfig{Type: "virt", Accelerator: AcceleratorTCG},
		Compute: ComputeConfig{MemoryMiB: 256, VCPUs: 1, CPUModel: "cortex-a57"},
		Firmware: FirmwareConfig{BIOSPath: firmware},
		QMP: QMPConfig{SocketPath: filepath.Join(runDir, "qmp.sock")},
		Disks: []DiskConfig{{ID: "root", Path: image, Format: DiskFormatQCOW2, Interface: DiskInterfaceVirtio}},
		NICs: []NICConfig{{ID: "net0", Model: NICModelVirtioNetPCI, MAC: "52:54:00:12:34:56", Tap: TapBackendConfig{IfName: tapName}}},
		Logging: LoggingConfig{QEMULogPath: filepath.Join(runDir, "qemu.log")},
		Process: ProcessConfig{PIDFilePath: filepath.Join(runDir, "qemu.pid")},
	}

	serialPath := filepath.Join(runDir, "serial.log")
	cfg.Console.SerialLogPath = serialPath

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proc, err := NewDefaultDriver().Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = proc.Stop(stopCtx)
	}()

	waitForQMP(t, cfg.QMP.SocketPath, 30*time.Second)
	waitForSerialMarker(t, serialPath, "eth0: carrier acquired", 180*time.Second)
}

func requiredEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is required", key)
	}
	return value
}

func waitForQMP(t *testing.T, socketPath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, time.Second)
		if err == nil {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			buf := make([]byte, 4096)
			if _, err := conn.Read(buf); err != nil {
				t.Fatalf("read QMP greeting error = %v", err)
			}
			if err := json.NewEncoder(conn).Encode(map[string]string{"execute": "qmp_capabilities"}); err != nil {
				t.Fatalf("write qmp_capabilities error = %v", err)
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("QMP socket %q not ready within %s", socketPath, timeout)
}

func waitForSerialMarker(t *testing.T, path string, marker string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), marker) {
			return
		}
		time.Sleep(time.Second)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("serial marker %q not found within %s; serial tail: %s", marker, timeout, tailString(string(data), 2000))
}

func tailString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
```

- [ ] **Step 2: Run default test to verify skip**

Run:

```bash
go test ./internal/virt/qemu -run TestIntegrationCirrOSWithTap -v
```

Expected: PASS with skip message `set GOVIRTA_QEMU_INTEGRATION=1 to run QEMU integration test`.

- [ ] **Step 3: Run remote acceptance manually after implementation**

On `192.168.139.206`, run from a copied Govirta workspace or copied test binary with working directory that can create `.tmp`, or set `GOVIRTA_QEMU_RUN_DIR=/root/govirta-qemu-test/run`:

```bash
GOVIRTA_QEMU_INTEGRATION=1 \
GOVIRTA_CIRROS_IMAGE=/root/govirta-qemu-test/images/cirros-aarch64.qcow2 \
GOVIRTA_QEMU_BINARY=/usr/libexec/qemu-kvm \
GOVIRTA_QEMU_FIRMWARE=/usr/share/edk2/aarch64/QEMU_EFI.fd \
GOVIRTA_QEMU_TAP=gv-tap0 \
GOVIRTA_QEMU_RUN_DIR=/root/govirta-qemu-test/run \
go test ./internal/virt/qemu -run TestIntegrationCirrOSWithTap -v
```

Expected: PASS after serial log contains `eth0: carrier acquired`.

- [ ] **Step 4: Checkpoint**

Do not commit unless requested. Record changed file:

```text
internal/virt/qemu/integration_test.go
```

---

### Task 6: Update Example Config and Run Full Verification

**Files:**
- Modify: `configs/govirtlet.example.yaml`
- Use: `scripts/verify.sh`

- [ ] **Step 1: Update config example**

Modify `configs/govirtlet.example.yaml` to:

```yaml
node:
  name: local-dev-node

qemu:
  binary: qemu-system-x86_64
  machine:
    type: q35
    accelerator: tcg
  cpuModel: ""
  firmware:
    biosPath: ""
  runtime:
    qmpSocket: .tmp/qemu/cirros/qmp.sock
    pidFile: .tmp/qemu/cirros/qemu.pid
    qemuLog: .tmp/qemu/cirros/qemu.log
    serialLog: .tmp/qemu/cirros/serial.log

qmp:
  socketDir: .tmp/qemu

network:
  bridge: govirta0
  tap: gv-tap0
```

- [ ] **Step 2: Run formatting**

Run:

```bash
gofmt -w internal/virt/qemu internal/virt/qemuimg internal/node
```

Expected: no output.

- [ ] **Step 3: Run baseline verification**

Run:

```bash
./scripts/verify.sh
```

Expected:

```text
verification passed
```

If the script does not print `verification passed`, expected result is exit code 0 with no `gofmt` file list and no `go test` / `go build` failures.

- [ ] **Step 4: Run race tests**

Run:

```bash
go test -race ./...
```

Expected: PASS.

- [ ] **Step 5: Run remote acceptance**

Ensure remote network baseline exists:

```bash
ssh root@192.168.139.206 'ip -brief link | grep -E "govirta0|gv-tap0" && bridge link show | grep gv-tap0'
```

Expected output includes:

```text
govirta0
gv-tap0
gv-tap0: ... master govirta0
```

Then copy or run the repository on `192.168.139.206` and execute the integration command from Task 5 Step 3.

- [ ] **Step 6: Final report**

Report these facts:

```text
Affected call relationships:
cmd/govirtlet/main.go -> internal/node.Agent.Run -> internal/virt/qemu.Driver.Name
internal/virt/qemu.Driver.Start -> internal/virt/qemu.Builder.Build -> internal/virt/qemu.ProcessRunner.Start -> os/exec.CommandContext
internal/virt/qemuimg.ExecClient.Info/Create/Resize -> internal/virt/qemuimg.CommandRunner.Run -> os/exec.CommandContext

Verification:
./scripts/verify.sh: PASS
go test -race ./...: PASS
remote TAP integration on 192.168.139.206: PASS or explicitly not run with reason
```

---

## Self-Review

Spec coverage:

- `qemu-system-*` / `qemu-kvm` typed config: Task 1.
- Runtime architecture defaults via `runtime.GOARCH`: Task 1.
- Firmware `-bios` for arm64: Tasks 1 and 2.
- TAP-only NIC argv: Tasks 1, 2, and 5.
- QMP socket argv and readiness check: Tasks 2 and 5.
- qemu-img `create/info/resize`: Task 4.
- Unit tests without real QEMU/TAP/CirrOS: Tasks 1-4.
- Remote acceptance through Govirta package boundary: Task 5.
- Example config and full verification: Task 6.

No known scope gaps remain for the approved first phase.
