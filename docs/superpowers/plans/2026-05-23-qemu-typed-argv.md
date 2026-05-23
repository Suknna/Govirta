# QEMU Typed Argv Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the old `internal/virt/qemu` process-oriented implementation with a typed Go API that converts QEMU flag structs into deterministic `[]string` argv, and add a tiny `cmd/qemucli` printer.

**Architecture:** The root `internal/virt/qemu` package owns fluent VM composition and final argv rendering. Domain subpackages (`machine`, `cpu`, `blockdev`, `device`, `netdev`, `chardev`, `monitor`, `serial`, `display`) own typed flag structs and constants. A leaf `internal/virt/qemu/qflag` package holds cross-cutting primitive types (`OnOff`, `OptionalInt`) so domain packages do not import the root package and create Go import cycles; root `qemu` re-exports aliases as `qemu.On`, `qemu.Off`, and `qemu.Int`.

**Tech Stack:** Go, standard library only, QEMU System Emulator invocation model, existing project test baseline `go test ./...`.

---

## Evidence and Constraints

- Approved spec: `docs/superpowers/specs/2026-05-23-qemu-typed-argv-design.md`.
- Existing QEMU implementation to remove: `internal/virt/qemu/config.go`, `builder.go`, `driver.go`, `runner.go`, `signal_unix.go`, `signal_windows.go`, and their old tests.
- Current compile dependency: `internal/node/agent.go -> internal/virt/qemu.Driver`. This must be removed; do not preserve `qemu.Driver` as a compatibility shim.
- CLI scope from product owner: `cmd/qemucli` is not the重点; it may simply print generated argv/command and must not execute QEMU.
- No QEMU process execution path may remain in `internal/virt/qemu`.
- No architecture parsing or `runtime.GOARCH` default inference in the new QEMU package.
- External resources such as TAP, disk image, QMP socket path, PID path, and console socket path are caller-provided and are not existence-checked.
- Official documentation basis: QEMU System Emulator Invocation docs, especially `-name`, `-machine`, `-cpu`, `-smp`, `-m`, `-blockdev`, `-device`, `-netdev`, `-chardev`, `-mon`, `-serial`, `-display`, `-no-reboot`, `-no-shutdown`, `-msg`, `-pidfile`.

## File Structure

Create or replace these files:

```text
internal/virt/qemu/qflag/qflag.go            // primitive reusable flag value types; no root qemu import
internal/virt/qemu/machine/machine.go        // -machine type=q35,accel=kvm,kernel-irqchip=split
internal/virt/qemu/cpu/cpu.go                // -cpu host
internal/virt/qemu/blockdev/blockdev.go      // -blockdev qcow2-over-file rendering
internal/virt/qemu/netdev/netdev.go          // -netdev tap rendering
internal/virt/qemu/device/device.go          // virtio-blk-pci and virtio-net-pci rendering
internal/virt/qemu/chardev/chardev.go        // -chardev socket rendering
internal/virt/qemu/monitor/monitor.go        // -mon chardev=...,mode=control
internal/virt/qemu/serial/serial.go          // -serial chardev:...
internal/virt/qemu/display/display.go        // -display none
internal/virt/qemu/vm.go                     // root aliases, Builder, VM, Argv
internal/virt/qemu/vm_test.go                // golden argv and validation tests
internal/node/agent.go                       // remove qemu.Driver dependency from skeleton agent
internal/node/agent_test.go                  // update log contract after qemu driver removal
cmd/qemucli/main.go                          // small argv printer, no exec.Command
cmd/qemucli/main_test.go                     // optional smoke test for printed command
```

Delete these obsolete files after new failing tests are in place:

```text
internal/virt/qemu/config.go
internal/virt/qemu/config_test.go
internal/virt/qemu/builder.go
internal/virt/qemu/builder_test.go
internal/virt/qemu/driver.go
internal/virt/qemu/driver_test.go
internal/virt/qemu/runner.go
internal/virt/qemu/runner_test.go
internal/virt/qemu/signal_unix.go
internal/virt/qemu/signal_windows.go
internal/virt/qemu/integration_test.go
```

---

### Task 1: Remove node's dependency on the old QEMU driver contract

**Files:**
- Modify: `internal/node/agent_test.go`
- Modify: `internal/node/agent.go`

- [ ] **Step 1: Write the failing node contract test**

Replace `TestAgentRunLogsDependencyNames` in `internal/node/agent_test.go` with this version, which no longer expects a `qemu_driver` log field:

```go
func TestAgentRunLogsDependencyNames(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	ctx := logger.WithContext(context.Background())

	agent := NewAgent()
	if err := agent.Run(ctx); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	var event map[string]any
	if err := json.Unmarshal(buf.Bytes(), &event); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	assertLogField(t, event, "component", "node")
	assertLogField(t, event, "qmp_client", "qmp-noop")
	assertLogField(t, event, "bridge_manager", "bridge-noop")
	assertLogField(t, event, "message", "starting node agent")
	if _, ok := event["qemu_driver"]; ok {
		t.Fatalf("unexpected qemu_driver log field after qemu.Driver removal")
	}
}
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
go test ./internal/node -run TestAgentRunLogsDependencyNames -count=1
```

Expected: FAIL with `unexpected qemu_driver log field after qemu.Driver removal`.

- [ ] **Step 3: Remove qemu.Driver from node agent**

Update `internal/node/agent.go` to remove the `internal/virt/qemu` import, `qemuDriver` field, `qemu.NewNoopDriver()` initialization, and `.Str("qemu_driver", ...)` log field. The resulting file should be:

```go
package node

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/network/bridge"
	"github.com/suknna/govirta/internal/virt/qmp"
)

// Agent coordinates compute-node local virtualization dependencies.
type Agent struct {
	qmpClient     qmp.Client
	bridgeManager bridge.Manager
}

// NewAgent creates a node agent with no-op dependencies.
func NewAgent() *Agent {
	return &Agent{
		qmpClient:     qmp.NewNoopClient(),
		bridgeManager: bridge.NewNoopManager(),
	}
}

// Run starts the node agent skeleton.
func (a *Agent) Run(ctx context.Context) error {
	logger := zerolog.Ctx(ctx).With().
		Str("component", "node").
		Str("qmp_client", a.qmpClient.Name()).
		Str("bridge_manager", a.bridgeManager.Name()).
		Logger()

	ctx = logger.WithContext(ctx)
	zerolog.Ctx(ctx).Info().Msg("starting node agent")

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
```

- [ ] **Step 4: Run node tests and verify they pass**

Run:

```bash
go test ./internal/node -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the node boundary change**

Run:

```bash
git add internal/node/agent.go internal/node/agent_test.go
git commit -m "refactor(node): drop qemu driver dependency"
```

---

### Task 2: Write the typed QEMU golden argv test before implementation

**Files:**
- Delete: obsolete `internal/virt/qemu/*_test.go` files listed in File Structure
- Create: `internal/virt/qemu/vm_test.go`

- [ ] **Step 1: Remove old qemu tests that encode the deleted architecture**

Run:

```bash
rm internal/virt/qemu/config_test.go \
   internal/virt/qemu/builder_test.go \
   internal/virt/qemu/driver_test.go \
   internal/virt/qemu/runner_test.go \
   internal/virt/qemu/integration_test.go
```

These tests describe the old `Config/Builder/Driver/Runner` contract and are no longer valid business behavior.

- [ ] **Step 2: Write the new failing golden argv test**

Create `internal/virt/qemu/vm_test.go`:

```go
package qemu_test

import (
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

	argv, err := vm.Argv()
	if err != nil {
		t.Fatalf("Argv() error = %v", err)
	}

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

func TestBuildRejectsMissingRequiredFields(t *testing.T) {
	_, err := qemu.NewVM(qemu.ArchX86_64).
		SMP(qemu.SMP{CPUs: 0, Cores: 4, Threads: 1, Sockets: 1}).
		Build()
	if err == nil {
		t.Fatalf("Build() error = nil, want missing required field error")
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

	argv, err := vm.Argv()
	if err != nil {
		t.Fatalf("Argv() error = %v", err)
	}
	if argv[0] != "/usr/libexec/qemu-kvm" {
		t.Fatalf("argv[0] = %q, want /usr/libexec/qemu-kvm", argv[0])
	}
}
```

- [ ] **Step 3: Run the focused QEMU test and verify it fails for missing packages/types**

Run:

```bash
go test ./internal/virt/qemu -run TestVMArgvBuildsRequiredQEMUCommand -count=1
```

Expected: FAIL with missing packages such as `internal/virt/qemu/blockdev` or missing symbols such as `NewVM`.

- [ ] **Step 4: Commit the failing test if the team wants red commits, otherwise keep it uncommitted**

Default for this repository: do not commit a known failing state. Keep this test uncommitted until Task 3 makes it pass.

---

### Task 3: Implement typed QEMU domain renderers and VM argv builder

**Files:**
- Delete: obsolete production files listed in File Structure
- Create: all new `internal/virt/qemu/**.go` files listed in File Structure

- [ ] **Step 1: Delete obsolete production files**

Run:

```bash
rm internal/virt/qemu/config.go \
   internal/virt/qemu/builder.go \
   internal/virt/qemu/driver.go \
   internal/virt/qemu/runner.go \
   internal/virt/qemu/signal_unix.go \
   internal/virt/qemu/signal_windows.go
```

- [ ] **Step 2: Create `qflag` primitives**

Create `internal/virt/qemu/qflag/qflag.go`:

```go
// Package qflag defines primitive QEMU flag value types shared by qemu packages.
package qflag

// OnOff represents QEMU's common on/off option values.
type OnOff string

const (
	// On renders as "on".
	On OnOff = "on"
	// Off renders as "off".
	Off OnOff = "off"
)

// OptionalInt represents an integer option that may be absent.
type OptionalInt struct {
	value int
	set   bool
}

// Int returns a present optional integer value.
func Int(v int) OptionalInt {
	return OptionalInt{value: v, set: true}
}

// IsSet reports whether the option should be rendered.
func (v OptionalInt) IsSet() bool { return v.set }

// Value returns the integer value.
func (v OptionalInt) Value() int { return v.value }
```

- [ ] **Step 3: Create domain packages**

Create the following files with render methods returning only the right-hand flag value; root `qemu` will add the flag names.

`internal/virt/qemu/machine/machine.go`:

```go
package machine

import "strings"

type Type string

const (
	TypeQ35  Type = "q35"
	TypeVirt Type = "virt"
)

type Accel string

const AccelKVM Accel = "kvm"

type IRQChip string

const IRQChipSplit IRQChip = "split"

type Config struct {
	Type          Type
	Accel         Accel
	KernelIRQChip IRQChip
}

type Option func(*Config)

func WithAccel(v Accel) Option { return func(c *Config) { c.Accel = v } }
func WithKernelIRQChip(v IRQChip) Option { return func(c *Config) { c.KernelIRQChip = v } }

func New(t Type, opts ...Option) Config {
	c := Config{Type: t}
	for _, opt := range opts { opt(&c) }
	return c
}

func (c Config) Arg() string {
	parts := []string{"type=" + string(c.Type)}
	if c.Accel != "" { parts = append(parts, "accel="+string(c.Accel)) }
	if c.KernelIRQChip != "" { parts = append(parts, "kernel-irqchip="+string(c.KernelIRQChip)) }
	return strings.Join(parts, ",")
}
```

`internal/virt/qemu/cpu/cpu.go`:

```go
package cpu

type Model string

const (
	ModelHost      Model = "host"
	ModelCortexA57 Model = "cortex-a57"
)
```

`internal/virt/qemu/blockdev/blockdev.go`:

```go
package blockdev

import "github.com/suknna/govirta/internal/virt/qemu/qflag"

type Ref string

type AIO string

const AIOThreads AIO = "threads"

type FileProtocol struct { Filename string }
type Cache struct { Direct qflag.OnOff }

type Qcow2 struct {
	NodeName string
	File     FileProtocol
	Cache    Cache
	AIO      AIO
}

func (d Qcow2) Arg() string {
	arg := "driver=qcow2,node-name=" + d.NodeName + ",file.driver=file,file.filename=" + d.File.Filename
	if d.Cache.Direct != "" { arg += ",cache.direct=" + string(d.Cache.Direct) }
	if d.AIO != "" { arg += ",aio=" + string(d.AIO) }
	return arg
}
```

`internal/virt/qemu/netdev/netdev.go`:

```go
package netdev

import "github.com/suknna/govirta/internal/virt/qemu/qflag"

type Ref string
type Script string
const ScriptNo Script = "no"

type Tap struct {
	ID         string
	IfName     string
	Script     Script
	DownScript Script
	Vhost      qflag.OnOff
}

func (n Tap) Arg() string {
	arg := "tap,id=" + n.ID + ",ifname=" + n.IfName
	if n.Script != "" { arg += ",script=" + string(n.Script) }
	if n.DownScript != "" { arg += ",downscript=" + string(n.DownScript) }
	if n.Vhost != "" { arg += ",vhost=" + string(n.Vhost) }
	return arg
}
```

`internal/virt/qemu/device/device.go`:

```go
package device

import (
	"strconv"

	"github.com/suknna/govirta/internal/virt/qemu/blockdev"
	"github.com/suknna/govirta/internal/virt/qemu/netdev"
	"github.com/suknna/govirta/internal/virt/qemu/qflag"
)

type MAC string

type VirtioBlkPCI struct {
	ID        string
	Drive     blockdev.Ref
	BootIndex qflag.OptionalInt
}

func (d VirtioBlkPCI) Arg() string {
	arg := "virtio-blk-pci,drive=" + string(d.Drive)
	if d.BootIndex.IsSet() { arg += ",bootindex=" + strconv.Itoa(d.BootIndex.Value()) }
	if d.ID != "" { arg += ",id=" + d.ID }
	return arg
}

type VirtioNetPCI struct {
	ID     string
	Netdev netdev.Ref
	Mac    MAC
}

func (d VirtioNetPCI) Arg() string {
	arg := "virtio-net-pci,netdev=" + string(d.Netdev)
	if d.Mac != "" { arg += ",mac=" + string(d.Mac) }
	if d.ID != "" { arg += ",id=" + d.ID }
	return arg
}
```

`internal/virt/qemu/chardev/chardev.go`:

```go
package chardev

import "github.com/suknna/govirta/internal/virt/qemu/qflag"

type Ref string

type Socket struct {
	ID     string
	Path   string
	Server qflag.OnOff
	Wait   qflag.OnOff
}

func (c Socket) Arg() string {
	arg := "socket,id=" + c.ID + ",path=" + c.Path
	if c.Server != "" { arg += ",server=" + string(c.Server) }
	if c.Wait != "" { arg += ",wait=" + string(c.Wait) }
	return arg
}
```

`internal/virt/qemu/monitor/monitor.go`:

```go
package monitor

import "github.com/suknna/govirta/internal/virt/qemu/chardev"

type Mode string
const ModeControl Mode = "control"

type Monitor struct {
	Chardev chardev.Ref
	Mode    Mode
}

func (m Monitor) Arg() string { return "chardev=" + string(m.Chardev) + ",mode=" + string(m.Mode) }
```

`internal/virt/qemu/serial/serial.go`:

```go
package serial

type Serial struct { arg string }

func Chardev(id string) Serial { return Serial{arg: "chardev:" + id} }
func (s Serial) Arg() string { return s.arg }
```

`internal/virt/qemu/display/display.go`:

```go
package display

type Display string
const None Display = "none"
```

- [ ] **Step 4: Create the root VM builder**

Create `internal/virt/qemu/vm.go` with root aliases, typed builder methods, validation, and deterministic argv rendering. The implementation must import domain packages but domain packages must not import root `qemu`.

Use this complete starting implementation:

```go
package qemu

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/suknna/govirta/internal/virt/qemu/blockdev"
	"github.com/suknna/govirta/internal/virt/qemu/chardev"
	"github.com/suknna/govirta/internal/virt/qemu/cpu"
	"github.com/suknna/govirta/internal/virt/qemu/device"
	"github.com/suknna/govirta/internal/virt/qemu/display"
	"github.com/suknna/govirta/internal/virt/qemu/machine"
	"github.com/suknna/govirta/internal/virt/qemu/monitor"
	"github.com/suknna/govirta/internal/virt/qemu/netdev"
	"github.com/suknna/govirta/internal/virt/qemu/qflag"
	"github.com/suknna/govirta/internal/virt/qemu/serial"
)

var ErrInvalidVM = errors.New("invalid qemu vm")

type Arch string

const (
	ArchX86_64  Arch = "x86_64"
	ArchAArch64 Arch = "aarch64"
)

type OnOff = qflag.OnOff
type OptionalInt = qflag.OptionalInt

const (
	On  = qflag.On
	Off = qflag.Off
)

func Int(v int) OptionalInt { return qflag.Int(v) }

type Memory struct{ MiB int }

func MiB(v int) Memory { return Memory{MiB: v} }

type SMP struct {
	CPUs    int
	Cores   int
	Threads int
	Sockets int
}

type Msg struct {
	Timestamp OnOff
	GuestName OnOff
}

type NameOption func(*nameConfig)

type nameConfig struct {
	value        string
	debugThreads OnOff
}

func NameDebugThreads(v OnOff) NameOption {
	return func(c *nameConfig) { c.debugThreads = v }
}

type argvEntry struct {
	flag  string
	value string
}

type Builder struct {
	binary     string
	name       *nameConfig
	machine    *machine.Config
	cpu        cpu.Model
	smp        *SMP
	memory     *Memory
	ordered    []argvEntry
	monitor    *monitor.Monitor
	serial     *serial.Serial
	display    display.Display
	noReboot   bool
	noShutdown bool
	msg        *Msg
	pidFile    string
}

type VM struct{ builder Builder }

func NewVM(arch Arch) *Builder { return &Builder{binary: binaryForArch(arch)} }

func binaryForArch(arch Arch) string {
	switch arch {
	case ArchX86_64:
		return "qemu-system-x86_64"
	case ArchAArch64:
		return "qemu-system-aarch64"
	default:
		return ""
	}
}

func (b *Builder) Binary(path string) *Builder { b.binary = path; return b }

func (b *Builder) Name(name string, opts ...NameOption) *Builder {
	c := nameConfig{value: name}
	for _, opt := range opts {
		opt(&c)
	}
	b.name = &c
	return b
}

func (b *Builder) Machine(t machine.Type, opts ...machine.Option) *Builder {
	c := machine.New(t, opts...)
	b.machine = &c
	return b
}

func (b *Builder) CPU(model cpu.Model) *Builder { b.cpu = model; return b }

func (b *Builder) SMP(v SMP) *Builder { b.smp = &v; return b }

func (b *Builder) Memory(v Memory) *Builder { b.memory = &v; return b }

func (b *Builder) AddBlockdev(v blockdev.Qcow2) *Builder {
	b.ordered = append(b.ordered, argvEntry{flag: "-blockdev", value: v.Arg()})
	return b
}

func (b *Builder) AddDevice(v any) *Builder {
	switch d := v.(type) {
	case device.VirtioBlkPCI:
		b.ordered = append(b.ordered, argvEntry{flag: "-device", value: d.Arg()})
	case device.VirtioNetPCI:
		b.ordered = append(b.ordered, argvEntry{flag: "-device", value: d.Arg()})
	default:
		b.ordered = append(b.ordered, argvEntry{flag: "-device", value: fmt.Sprintf("unsupported-device:%T", v)})
	}
	return b
}

func (b *Builder) AddNetdev(v netdev.Tap) *Builder {
	b.ordered = append(b.ordered, argvEntry{flag: "-netdev", value: v.Arg()})
	return b
}

func (b *Builder) AddChardev(v chardev.Socket) *Builder {
	b.ordered = append(b.ordered, argvEntry{flag: "-chardev", value: v.Arg()})
	return b
}

func (b *Builder) Monitor(v monitor.Monitor) *Builder { b.monitor = &v; return b }
func (b *Builder) Serial(v serial.Serial) *Builder { b.serial = &v; return b }
func (b *Builder) Display(v display.Display) *Builder { b.display = v; return b }
func (b *Builder) NoReboot() *Builder { b.noReboot = true; return b }
func (b *Builder) NoShutdown() *Builder { b.noShutdown = true; return b }
func (b *Builder) Msg(v Msg) *Builder { b.msg = &v; return b }
func (b *Builder) PidFile(path string) *Builder { b.pidFile = path; return b }

func (b *Builder) Build() (VM, error) {
	if b.binary == "" {
		return VM{}, fmt.Errorf("%w: qemu binary is required", ErrInvalidVM)
	}
	if b.smp != nil && (b.smp.CPUs <= 0 || b.smp.Cores <= 0 || b.smp.Threads <= 0 || b.smp.Sockets <= 0) {
		return VM{}, fmt.Errorf("%w: smp values must be positive", ErrInvalidVM)
	}
	if b.memory != nil && b.memory.MiB <= 0 {
		return VM{}, fmt.Errorf("%w: memory must be positive", ErrInvalidVM)
	}
	for _, entry := range b.ordered {
		if strings.Contains(entry.value, "unsupported-device:") {
			return VM{}, fmt.Errorf("%w: %s", ErrInvalidVM, entry.value)
		}
		if entry.value == "" {
			return VM{}, fmt.Errorf("%w: empty value for %s", ErrInvalidVM, entry.flag)
		}
	}
	return VM{builder: *b}, nil
}

func (v VM) Argv() ([]string, error) {
	b := v.builder
	argv := []string{b.binary}
	if b.name != nil {
		argv = append(argv, "-name", b.name.arg())
	}
	if b.machine != nil {
		argv = append(argv, "-machine", b.machine.Arg())
	}
	if b.cpu != "" {
		argv = append(argv, "-cpu", string(b.cpu))
	}
	if b.smp != nil {
		argv = append(argv, "-smp", b.smp.arg())
	}
	if b.memory != nil {
		argv = append(argv, "-m", "size="+strconv.Itoa(b.memory.MiB))
	}
	for _, entry := range b.ordered {
		argv = append(argv, entry.flag, entry.value)
	}
	if b.monitor != nil {
		argv = append(argv, "-mon", b.monitor.Arg())
	}
	if b.serial != nil {
		argv = append(argv, "-serial", b.serial.Arg())
	}
	if b.display != "" {
		argv = append(argv, "-display", string(b.display))
	}
	if b.noReboot {
		argv = append(argv, "-no-reboot")
	}
	if b.noShutdown {
		argv = append(argv, "-no-shutdown")
	}
	if b.msg != nil {
		argv = append(argv, "-msg", b.msg.arg())
	}
	if b.pidFile != "" {
		argv = append(argv, "-pidfile", b.pidFile)
	}
	return argv, nil
}

func (c nameConfig) arg() string {
	arg := c.value
	if c.debugThreads != "" {
		arg += ",debug-threads=" + string(c.debugThreads)
	}
	return arg
}

func (s SMP) arg() string {
	return "cpus=" + strconv.Itoa(s.CPUs) +
		",cores=" + strconv.Itoa(s.Cores) +
		",threads=" + strconv.Itoa(s.Threads) +
		",sockets=" + strconv.Itoa(s.Sockets)
}

func (m Msg) arg() string {
	parts := make([]string, 0, 2)
	if m.Timestamp != "" {
		parts = append(parts, "timestamp="+string(m.Timestamp))
	}
	if m.GuestName != "" {
		parts = append(parts, "guest-name="+string(m.GuestName))
	}
	return strings.Join(parts, ",")
}
```

Use a private ordered option type so `AddBlockdev`, `AddNetdev`, `AddDevice`, and `AddChardev` preserve call ordering exactly where the user placed them.

- [ ] **Step 5: Run QEMU package tests and fix only compile/test failures in scope**

Run:

```bash
go test ./internal/virt/qemu -count=1
```

Expected after implementation: PASS.

- [ ] **Step 6: Commit typed QEMU builder implementation**

Run:

```bash
git add internal/virt/qemu
git commit -m "feat(qemu): add typed argv builder"
```

---

### Task 4: Add minimal `cmd/qemucli` command printer

**Files:**
- Create: `cmd/qemucli/main.go`
- Create: `cmd/qemucli/main_test.go`

- [ ] **Step 1: Write a CLI smoke test**

Create `cmd/qemucli/main_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestBuildDefaultArgvPrintsQEMUCommand(t *testing.T) {
	argv, err := buildDefaultArgv()
	if err != nil {
		t.Fatalf("buildDefaultArgv() error = %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"qemu-system-x86_64", "-name prod-vm", "-blockdev", "-netdev tap"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("command %q does not contain %q", joined, want)
		}
	}
}
```

- [ ] **Step 2: Run the CLI test and verify it fails**

Run:

```bash
go test ./cmd/qemucli -count=1
```

Expected: FAIL because package or `buildDefaultArgv` does not exist.

- [ ] **Step 3: Implement the minimal printer**

Create `cmd/qemucli/main.go` with a hard-coded example using the same typed API as the golden test, then print `strings.Join(argv, " ")`. Do not import `os/exec` and do not start QEMU.

Required structure:

```go
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

func buildDefaultArgv() ([]string, error) {
	vm, err := qemu.NewVM(qemu.ArchX86_64).
		Name("prod-vm", qemu.NameDebugThreads(qemu.On)).
		Machine(machine.TypeQ35, machine.WithAccel(machine.AccelKVM), machine.WithKernelIRQChip(machine.IRQChipSplit)).
		CPU(cpu.ModelHost).
		SMP(qemu.SMP{CPUs: 4, Cores: 4, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(8192)).
		AddBlockdev(blockdev.Qcow2{NodeName: "root", File: blockdev.FileProtocol{Filename: "/var/lib/vm/root.qcow2"}, Cache: blockdev.Cache{Direct: qemu.Off}, AIO: blockdev.AIOThreads}).
		AddDevice(device.VirtioBlkPCI{ID: "rootdev", Drive: blockdev.Ref("root"), BootIndex: qemu.Int(1)}).
		AddNetdev(netdev.Tap{ID: "net0", IfName: "tap0", Script: netdev.ScriptNo, DownScript: netdev.ScriptNo, Vhost: qemu.On}).
		AddDevice(device.VirtioNetPCI{ID: "nic0", Netdev: netdev.Ref("net0"), Mac: device.MAC("52:54:00:12:34:56")} ).
		AddChardev(chardev.Socket{ID: "qmp0", Path: "/run/vm/prod.qmp", Server: qemu.On, Wait: qemu.Off}).
		Monitor(monitor.Monitor{Chardev: chardev.Ref("qmp0"), Mode: monitor.ModeControl}).
		AddChardev(chardev.Socket{ID: "serial0", Path: "/run/vm/prod.console", Server: qemu.On, Wait: qemu.Off}).
		Serial(serial.Chardev("serial0")).
		Display(display.None).
		NoReboot().NoShutdown().
		Msg(qemu.Msg{Timestamp: qemu.On, GuestName: qemu.On}).
		PidFile("/run/vm/prod.pid").
		Build()
	if err != nil { return nil, err }
	return vm.Argv()
}
```

After pasting, run `gofmt` because the compact chained call is intentionally dense in the plan.

- [ ] **Step 4: Run CLI tests and gofmt**

Run:

```bash
gofmt -w cmd/qemucli/main.go cmd/qemucli/main_test.go
go test ./cmd/qemucli -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit CLI printer**

Run:

```bash
git add cmd/qemucli
git commit -m "feat(qemucli): print typed qemu argv"
```

---

### Task 5: Full verification, documentation alignment, and final review

**Files:**
- Inspect: all changed files
- Modify: only if verification exposes a real mismatch

- [ ] **Step 1: Run formatting across changed Go files**

Run:

```bash
gofmt -w internal/node/agent.go internal/node/agent_test.go internal/virt/qemu/**/*.go cmd/qemucli/*.go
```

If the shell does not expand `**`, use:

```bash
find internal/virt/qemu cmd/qemucli -name '*.go' -print0 | xargs -0 gofmt -w
gofmt -w internal/node/agent.go internal/node/agent_test.go
```

- [ ] **Step 2: Run full test baseline**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Confirm CLI does not execute QEMU**

Run:

```bash
grep -R "os/exec\|exec.Command" -n cmd/qemucli internal/virt/qemu || true
```

Expected: no output.

- [ ] **Step 4: Confirm old qemu driver/runner symbols are gone**

Run:

```bash
grep -R "type Driver\|NewNoopDriver\|ProcessRunner\|DefaultConfigForRuntime" -n internal/virt/qemu internal/node || true
```

Expected: no output.

- [ ] **Step 5: Inspect git diff for scope discipline**

Run:

```bash
git status --short
```

Expected: diff only covers the approved QEMU typed argv refactor, `qemucli`, and the direct `internal/node` compile-boundary update.

- [ ] **Step 6: Commit any final verification fixes**

If Step 2–5 required fixes, commit them with:

```bash
git add internal/virt/qemu cmd/qemucli internal/node
git commit -m "test(qemu): verify typed argv refactor"
```

If no fixes were required, do not create an empty commit.

## Final Acceptance Checklist

- [ ] `internal/virt/qemu` contains no process runner, signal handling, QMP connection, or architecture runtime inference.
- [ ] Required QEMU command flags from the spec are represented by typed structs/constants and covered by the golden argv test.
- [ ] `qemu.On`, `qemu.Off`, and `qemu.Int` work in caller code without causing a Go import cycle.
- [ ] `cmd/qemucli` prints argv/command text and never calls `exec.Command`.
- [ ] `internal/node` no longer imports `internal/virt/qemu` only to obtain a no-op driver.
- [ ] `go test ./...` passes.
