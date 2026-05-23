# QEMU Typed Argv Design

## Status

- Date: 2026-05-23
- Scope: design specification only
- Approved approach: package-oriented typed model under `internal/virt/qemu`

## Goal

Replace the current `internal/virt/qemu` implementation with a narrow, typed QEMU argv builder. The package's core responsibility is converting typed Go structs into stable `qemu-system-*` command arguments. It must not infer the current host architecture, create host resources, connect to QMP, or own process lifecycle.

The immediate required command shape is:

```text
qemu-system-x86_64 \
  -name prod-vm,debug-threads=on \
  -machine type=q35,accel=kvm,kernel-irqchip=split \
  -cpu host \
  -smp cpus=4,cores=4,threads=1,sockets=1 \
  -m size=8192 \
  -blockdev driver=qcow2,node-name=root,file.driver=file,file.filename=/var/lib/vm/root.qcow2,cache.direct=off,aio=threads \
  -device virtio-blk-pci,drive=root,bootindex=1,id=rootdev \
  -netdev tap,id=net0,ifname=tap0,script=no,downscript=no,vhost=on \
  -device virtio-net-pci,netdev=net0,mac=52:54:00:12:34:56,id=nic0 \
  -chardev socket,id=qmp0,path=/run/vm/prod.qmp,server=on,wait=off \
  -mon chardev=qmp0,mode=control \
  -chardev socket,id=serial0,path=/run/vm/prod.console,server=on,wait=off \
  -serial chardev:serial0 \
  -display none \
  -no-reboot -no-shutdown \
  -msg timestamp=on,guest-name=on \
  -pidfile /run/vm/prod.pid
```

## Current Project Context

Existing `internal/virt/qemu` files currently mix configuration defaults, argv building, process runner, `Driver`, and integration-test launch behavior. That implementation should be removed rather than adapted because it preserves responsibilities now explicitly out of scope.

Known call-chain impact:

```text
internal/node.Agent -> internal/virt/qemu.Driver
```

The new design removes `qemu.Driver` from the QEMU package. Any compile break in `internal/node` should be repaired with the smallest local placeholder or revised dependency boundary, not by keeping the old QEMU driver as a compatibility shim.

## Non-Goals

This phase does not:

- Create, attach, or configure bridge/TAP devices.
- Create, inspect, resize, or convert disk images; that remains `internal/virt/qemuimg`.
- Start, stop, signal, or wait for QEMU processes from `internal/virt/qemu`.
- Connect to QMP or model QMP events.
- Infer architecture from `runtime.GOARCH`.
- Perform cross-architecture scheduling or guest/host compatibility checks.
- Validate that external paths exist.
- Provide a complete QEMU option model beyond the required flags above.

## Official Documentation Basis

The design is based on the official QEMU System Emulator Invocation documentation:

- `https://www.qemu.org/docs/master/system/invocation.html`
- Relevant documented options: `-machine`, `-cpu`, `-smp`, `-m`, `-blockdev`, `-device`, `-netdev tap`, `-chardev socket`, `-mon`, `-serial`, `-display none`, `-no-reboot`, `-no-shutdown`, `-msg`, and `-pidfile`.
- The documentation recommends `-blockdev` plus `-device` as the explicit stable interface for management tools and scripting.

## Package Layout

Use small domain packages so callers can discover allowed values from types and constants:

```text
internal/virt/qemu
internal/virt/qemu/machine
internal/virt/qemu/cpu
internal/virt/qemu/blockdev
internal/virt/qemu/device
internal/virt/qemu/netdev
internal/virt/qemu/chardev
internal/virt/qemu/monitor
internal/virt/qemu/serial
internal/virt/qemu/display
cmd/qemucli
```

The root `qemu` package owns cross-cutting primitives and the fluent VM builder. Domain subpackages own structs and constants for a single QEMU flag family.

## Public Usage Shape

The target API should support this style:

```go
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
        File: blockdev.FileProtocol{Filename: "/var/lib/vm/root.qcow2"},
        Cache: blockdev.Cache{Direct: qemu.Off},
        AIO: blockdev.AIOThreads,
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

argv, err := vm.Argv()
```

`argv[0]` is the QEMU binary. `argv[1:]` are arguments suitable for `exec.Command` by a caller outside this package.

## Root QEMU Types

Root package primitives:

```go
type Arch string

const (
    ArchX86_64 Arch = "x86_64"
    ArchAArch64 Arch = "aarch64"
)

type OnOff string

const (
    On  OnOff = "on"
    Off OnOff = "off"
)

type OptionalInt struct { ... }
func Int(v int) OptionalInt

type Memory struct { MiB int }
func MiB(v int) Memory

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
```

`Arch` maps only to default binary names:

| Arch | Binary |
| --- | --- |
| `ArchX86_64` | `qemu-system-x86_64` |
| `ArchAArch64` | `qemu-system-aarch64` |

If later acceptance needs `/usr/libexec/qemu-kvm`, add an explicit `Binary(path string)` builder method. Do not infer that binary from the local runtime.

## Domain Package Requirements

### `machine`

Required output:

```text
-machine type=q35,accel=kvm,kernel-irqchip=split
```

Required types and constants:

```go
type Type string
const TypeQ35 Type = "q35"

type Accel string
const AccelKVM Accel = "kvm"

type IRQChip string
const IRQChipSplit IRQChip = "split"
```

The machine package should use option functions such as `WithAccel` and `WithKernelIRQChip` because `-machine` has a primary type plus optional properties.

### `cpu`

Required output:

```text
-cpu host
```

Required types and constants:

```go
type Model string
const ModelHost Model = "host"
```

### `blockdev`

Required output:

```text
-blockdev driver=qcow2,node-name=root,file.driver=file,file.filename=/var/lib/vm/root.qcow2,cache.direct=off,aio=threads
```

Required shape:

```go
type Ref string

type Qcow2 struct {
    NodeName string
    File     FileProtocol
    Cache    Cache
    AIO      AIO
}

type FileProtocol struct {
    Filename string
}

type Cache struct {
    Direct qemu.OnOff
}

type AIO string
const AIOThreads AIO = "threads"
```

Only qcow2 over file protocol is required now. Raw, backing files, discard, native AIO, and io_uring are deferred.

### `device`

Required outputs:

```text
-device virtio-blk-pci,drive=root,bootindex=1,id=rootdev
-device virtio-net-pci,netdev=net0,mac=52:54:00:12:34:56,id=nic0
```

Required structs:

```go
type VirtioBlkPCI struct {
    ID        string
    Drive     blockdev.Ref
    BootIndex qemu.OptionalInt
}

type MAC string

type VirtioNetPCI struct {
    ID     string
    Netdev netdev.Ref
    Mac    MAC
}
```

Device support is intentionally limited to these two structs.

### `netdev`

Required output:

```text
-netdev tap,id=net0,ifname=tap0,script=no,downscript=no,vhost=on
```

Required shape:

```go
type Ref string

type Script string
const ScriptNo Script = "no"

type Tap struct {
    ID         string
    IfName     string
    Script     Script
    DownScript Script
    Vhost      qemu.OnOff
}
```

The package only references existing TAP names. It does not create TAP devices or bridges.

### `chardev`

Required output:

```text
-chardev socket,id=qmp0,path=/run/vm/prod.qmp,server=on,wait=off
```

Required shape:

```go
type Ref string

type Socket struct {
    ID     string
    Path   string
    Server qemu.OnOff
    Wait   qemu.OnOff
}
```

Only Unix socket chardevs are required now.

### `monitor`

Required output:

```text
-mon chardev=qmp0,mode=control
```

Required shape:

```go
type Mode string
const ModeControl Mode = "control"

type Monitor struct {
    Chardev chardev.Ref
    Mode    Mode
}
```

### `serial`

Required output:

```text
-serial chardev:serial0
```

Required shape:

```go
type Serial struct { ... }
func Chardev(id string) Serial
```

### `display`

Required output:

```text
-display none
```

Required constants:

```go
type Display string
const None Display = "none"
```

## Argument Ordering

`VM.Argv()` must emit arguments in deterministic insertion order:

1. binary
2. `-name`
3. `-machine`
4. `-cpu`
5. `-smp`
6. `-m`
7. blockdevs in call order
8. devices and netdevs in call order relative to their own append sites, if stored in a single ordered option list
9. chardevs in call order
10. monitor
11. serial
12. display
13. `-no-reboot`
14. `-no-shutdown`
15. `-msg`
16. `-pidfile`

The implementation may use an internal ordered list of renderable options. The public API should not expose untyped raw flag insertion in this phase.

## Validation and Errors

Validation should be minimal and structural:

- Required ID/reference fields must not be empty.
- Required file paths provided by the caller must not be empty.
- Integer fields that appear in argv must be positive.
- Unsupported enum zero values should be omitted only when the field is optional; required enum zero values should return an error.

Validation must not check whether host files, sockets, TAP devices, or QEMU binaries exist. Those resources are externally provided.

Errors should be returned from `Build()` or `Argv()` with context. Use `%w` when wrapping sentinel errors if sentinel errors are introduced.

## CLI Scope

Add `cmd/qemucli` as a convenience printer, not an executor.

Expected behavior:

- Build one simple VM configuration from command-line flags or hard-coded defaults.
- Print the generated command line or argv.
- Do not call `exec.Command`.
- Keep implementation small; CLI completeness is not the focus of this task.

The CLI exists to prove package ergonomics and provide a quick manual inspection path.

## Testing Strategy

Use test-driven development:

1. Remove or replace old QEMU tests that encode the deleted `Config/Builder/Driver/Runner` contract.
2. Add a golden argv test for the exact required command above.
3. Add focused unit tests for domain renderers where behavior is non-trivial.
4. Add validation tests for missing required fields and invalid positive integers.
5. Add a CLI test only if the CLI grows beyond trivial `main` printing.

Baseline verification after implementation:

```text
go test ./...
```

Remote CirrOS boot acceptance is not required for this refactor because `internal/virt/qemu` no longer starts QEMU. Acceptance for this task is deterministic argv equivalence and compile/test health.

## Migration Notes

- Remove old QEMU process lifecycle abstractions from `internal/virt/qemu`.
- Update any direct consumer, currently `internal/node.Agent`, so it no longer imports the deleted QEMU driver.
- Keep `internal/virt/qemuimg` unchanged unless compile errors require import/path cleanup.
- Do not update roadmap checkboxes until implementation and verification are complete.

## Affected Call Relationships

Before:

```text
internal/node.Agent -> internal/virt/qemu.Driver -> internal/virt/qemu.Builder -> internal/virt/qemu.ProcessRunner
```

After this task:

```text
cmd/qemucli/main.go -> internal/virt/qemu.NewVM -> VM.Argv
Go callers -> internal/virt/qemu.NewVM -> domain option structs -> VM.Argv
```

No QEMU process execution path should remain inside `internal/virt/qemu` after this refactor.
