# QEMU Entrypoints Design

## Status

- Date: 2026-05-13
- Scope: design specification only
- Target implementation: structured Go bindings for `qemu-system-*` / `qemu-kvm` and `qemu-img`

## Goal

Govirta needs a small, typed, testable QEMU boundary that can start a CirrOS VM during development without making QEMU itself part of `go build`.

This design introduces two tool boundaries:

- `internal/virt/qemu`: structured `qemu-system-*` or distribution-provided `qemu-kvm` VM launch configuration, argument building, process execution, and local capability probing.
- `internal/virt/qemuimg`: structured `qemu-img` image operations for offline disk image management.

The first acceptance target is: after building Govirta locally, copy or run the resulting test binary on `192.168.139.206`; with the host's installed QEMU system binary, `qemu-img`, a CirrOS image matching the current Go runtime architecture, and a pre-created TAP device, Govirta can build the required arguments, start a basic CirrOS VM, and inject a TAP-backed virtio NIC into that VM.

Manual `qemu-system-*` or `/usr/libexec/qemu-kvm` shell commands are reference evidence only. Final acceptance must start the VM through Govirta's Go package boundary: `internal/virt/qemu.Config` -> `internal/virt/qemu.Builder` -> `internal/virt/qemu.Runner`. Image inspection must go through `internal/virt/qemuimg`, not a hand-written shell-only workflow.

## Current Project Context

Current relevant files:

- `internal/virt/qemu/driver.go` defines a minimal `Driver` interface and `NoopDriver`.
- `internal/virt/qmp/client.go` defines the QMP abstraction boundary.
- `internal/network/bridge` is the intended Linux bridge boundary.
- `configs/govirtlet.example.yaml` currently configures `qemu.binary: qemu-system-x86_64` and a QMP socket directory.
- `docs/architecture.md` defines the current process model as `govirtctl -> govirtad -> scheduler/store/control loops -> govirtlet -> QEMU/QMP/Linux bridge`.

Govirta is in fast iteration. The existing no-op QEMU abstraction may be replaced rather than preserved if it blocks the real boundary.

## Non-Goals

This phase does not:

- Download, compile, vendor, or embed QEMU.
- Make `go build` compile QEMU.
- Create or manage Linux bridge devices.
- Create or manage TAP devices.
- Configure firewall, routes, IP addresses, DHCP, metadata service, or cloud-init.
- Implement full VM scheduling or persistence.
- Implement hotplug, migration, snapshots, advanced storage graph modeling, NUMA, vhost-user, GPU, SPICE/VNC, USB redirection, or production hardening.

## Required Host Tools

Development and integration validation require local host binaries:

- a QEMU system binary matching the host/runtime architecture, such as `qemu-system-x86_64`, `qemu-system-aarch64`, or a distribution-provided `/usr/libexec/qemu-kvm`
- `qemu-img`

Deferred tools, not required for this phase:

- `qemu-nbd`
- `qemu-storage-daemon`
- `qemu-io`

## Runtime Architecture Defaults

The first phase follows the current Go runtime architecture rather than introducing a scheduler-level guest architecture model.

Default selection should derive from:

```go
runtime.GOARCH
```

Supported first-phase defaults:

| `runtime.GOARCH` | Default QEMU binary candidates | Default machine | Default CPU | Firmware |
| --- | --- | --- | --- | --- |
| `amd64` | `qemu-system-x86_64` | `q35` | empty or `host` | optional in the first phase |
| `arm64` | `qemu-system-aarch64`, `/usr/libexec/qemu-kvm` | `virt` | `cortex-a57` or `max` | required for CirrOS-style UEFI boot |

The implementation should allow explicit overrides for `Binary`, `Machine.Type`, `Machine.Accelerator`, `Compute.CPUModel`, and firmware path. Cross-architecture guest emulation is not a first-phase automatic behavior; if needed during development, callers may explicitly override fields and use `ExtraArgs`.

The aarch64 Linux validation host `192.168.139.206` demonstrated this model using:

```text
Binary: /usr/libexec/qemu-kvm
Machine.Type: virt
Machine.Accelerator: tcg
Compute.CPUModel: cortex-a57
Firmware.BIOSPath: /usr/share/edk2/aarch64/QEMU_EFI.fd
Image: cirros-0.6.3-aarch64-disk.img
Network: pre-created gv-tap0 attached to govirta0 bridge
```

## Package Boundaries

### `internal/virt/qemu`

Owns VM process launch through `qemu-system-*`.

Responsibilities:

- Define typed VM launch configuration.
- Validate configuration needed to produce deterministic arguments.
- Build `qemu-system-*` argv without invoking a shell.
- Start and stop QEMU through a runner using caller-provided `context.Context`.
- Probe local `qemu-system-*` version/capabilities at development or startup time.

It may express QEMU network parameters, but must not create or configure host network devices.

### `internal/virt/qemuimg`

Owns offline disk image operations through `qemu-img`.

Responsibilities:

- Define typed image operation requests.
- Build `qemu-img` argv without invoking a shell.
- Run short-lived image commands using caller-provided `context.Context`.
- Parse machine-readable `qemu-img info --output=json` output.

It must not operate on images known to be attached to running VMs. The caller or future storage layer owns that safety check.

### `internal/network/bridge`

Owns host network lifecycle.

Responsibilities in later phases:

- Create bridge.
- Create TAP.
- Attach TAP to bridge.
- Set interface state and MTU.
- Clean up host networking resources.

In this phase, development usage assumes the TAP device already exists.

## QEMU System Typed Model

The first implementation should structure only Govirta's core VM lifecycle parameters.

### Top-Level Config

Conceptual shape:

```go
type Config struct {
    Binary  string
    Name    string
    Machine MachineConfig
    Compute ComputeConfig
    Firmware FirmwareConfig
    QMP     QMPConfig
    Disks   []DiskConfig
    NICs    []NICConfig
    Console ConsoleConfig
    Logging LoggingConfig
    Process ProcessConfig
    Boot    BootConfig
    ExtraArgs []string
}
```

`ExtraArgs` is an escape hatch for development-only QEMU flags not yet modeled. It should be appended after structured arguments and should be visibly marked as unstructured in logs or debug output.

### Machine and Compute

Required structured fields:

```go
type MachineConfig struct {
    Type        string // example: "q35"
    Accelerator string // "tcg" or "kvm"
}

type ComputeConfig struct {
    MemoryMiB int
    VCPUs     int
    CPUModel  string // example: "host"; empty means omit -cpu
}
```

Generated flags:

```text
-name <name>
-machine <type>,accel=<accelerator>
-m <memoryMiB>
-smp <vcpus>
-cpu <cpuModel>       # only when CPUModel is set
```

`Accelerator` should initially support only `tcg` and `kvm`.

When fields are omitted, defaults may be derived from `runtime.GOARCH` as described in Runtime Architecture Defaults.

### Firmware

Required structured field for `arm64`; optional for `amd64` in the first phase:

```go
type FirmwareConfig struct {
    BIOSPath string
}
```

Generated flag when set:

```text
-bios <biosPath>
```

For `arm64` CirrOS validation, the implementation should support firmware paths such as:

```text
/usr/share/edk2/aarch64/QEMU_EFI.fd
/usr/share/edk2/aarch64/QEMU_EFI.silent.fd
/usr/share/AAVMF/AAVMF_CODE.fd
```

Pflash, writable variable stores, OVMF/AAVMF variable persistence, and secure boot are deferred.

### QMP

Required structured field:

```go
type QMPConfig struct {
    SocketPath string
}
```

Generated flag:

```text
-qmp unix:<socketPath>,server=on,wait=off
```

Complex `-chardev` and `-mon chardev=...,mode=control` modeling is deferred.

### Disk

Required structured fields:

```go
type DiskConfig struct {
    ID        string
    Path      string
    Format    string // qcow2 or raw
    Interface string // first phase: virtio
}
```

Generated flag for the first phase:

```text
-drive if=virtio,file=<path>,format=<format>
```

The modern `-blockdev` plus explicit `virtio-blk-pci` graph is deferred until Govirta needs backing chains, snapshots, hotplug, or advanced storage jobs.

### Network

Required structured fields:

```go
type NICConfig struct {
    ID        string
    Model     string // first phase: virtio-net-pci
    MAC       string
    Tap       TapBackendConfig
}

type TapBackendConfig struct {
    IfName string
}
```

Generated flags:

```text
-netdev tap,id=<id>,ifname=<tapIfName>,script=no,downscript=no
-device <model>,netdev=<id>,mac=<mac>
```

Only TAP networking is in scope for the first implementation. The TAP device must already exist before QEMU starts. Host TAP and bridge lifecycle is explicitly out of scope for `internal/virt/qemu` and remains the responsibility of development setup or future `internal/network/bridge` integration.

### Console, Logging, and Process

Required structured fields:

```go
type ConsoleConfig struct {
    SerialLogPath string
}

type LoggingConfig struct {
    QEMULogPath string
}

type ProcessConfig struct {
    PIDFilePath string
}
```

Generated flags:

```text
-display none
-nographic
-D <qemuLogPath>
-pidfile <pidFilePath>
```

For the first acceptance path, `SerialLogPath` means Govirta's process runner captures QEMU stdout/stderr to that file while QEMU runs with `-nographic`. The manual aarch64 validation showed this is the reliable way to capture CirrOS boot output on the `virt` machine. `-serial file:<path>` is deferred until a later console model supports machine-specific serial routing explicitly.

`-daemonize` should not be used in the first phase. Govirta should keep process ownership through the runner.

### Boot

Optional first-phase field:

```go
type BootConfig struct {
    Order string // example: "c"
}
```

Generated flag when set:

```text
-boot order=<order>
```

BIOS, OVMF, pflash, and secure boot are deferred.

## QEMU Image Typed Model

The first implementation should support offline image operations needed for development and CirrOS validation.

### Client Interface

Conceptual shape:

```go
type Client interface {
    Create(ctx context.Context, req CreateRequest) error
    Info(ctx context.Context, path string) (ImageInfo, error)
    Resize(ctx context.Context, req ResizeRequest) error
}
```

`Convert` is useful but can be deferred unless the implementation needs to import an image format during CirrOS validation.

### Create

```go
type CreateRequest struct {
    Path          string
    Format        string // qcow2 or raw
    SizeBytes     int64
    BackingFile   string
    BackingFormat string
}
```

Generated argv examples:

```text
qemu-img create -f qcow2 <path> <size>
qemu-img create -f qcow2 -b <backingFile> -F <backingFormat> <path> <size>
```

### Info

`Info` should prefer JSON output:

```text
qemu-img info --output=json <path>
```

The implementation should parse at least:

- format
- virtual size
- actual size when present
- backing filename when present

### Resize

```go
type ResizeRequest struct {
    Path      string
    SizeBytes int64
}
```

Generated argv:

```text
qemu-img resize <path> <size>
```

## Runner Boundary

Both `qemu` and `qemuimg` should use an injectable runner abstraction.

Requirements:

- Unit tests must not require real QEMU binaries.
- Builders should be testable without spawning commands.
- Runner implementations must use caller-provided `context.Context`.
- Commands must use binary plus `[]string` args, not shell strings.
- stdout/stderr should be capturable for diagnostics and JSON parsing.

Conceptual shape:

```go
type CommandRunner interface {
    Run(ctx context.Context, binary string, args []string) (CommandResult, error)
}

type CommandResult struct {
    Stdout string
    Stderr string
}
```

Long-running `qemu-system-*` process management may require a separate `ProcessRunner` with `Start` and `Stop`, while `qemu-img` can use short command execution.

## Development and Integration Testing

### Unit Tests

`go test ./...` must not require:

- real QEMU system binary
- real `qemu-img`
- real TAP device
- real CirrOS image

Unit tests should cover:

- config validation errors
- deterministic argv construction
- fake runner command capture
- `qemu-img info --output=json` parsing

### Integration Tests

Integration tests are opt-in.

Suggested environment flags:

```text
GOVIRTA_QEMU_INTEGRATION=1
GOVIRTA_QEMUIMG_INTEGRATION=1
GOVIRTA_CIRROS_IMAGE=<path-to-cirros-qcow2>
GOVIRTA_QEMU_BINARY=<optional-qemu-system-or-qemu-kvm-path>
GOVIRTA_QEMU_FIRMWARE=<optional-firmware-path>
GOVIRTA_QEMU_TAP=<existing-tap-name>
GOVIRTA_REMOTE_LINUX=root@192.168.139.206
```

All temporary files must be placed under repository `.tmp/`, for example:

```text
.tmp/qemu/cirros/qmp.sock
.tmp/qemu/cirros/qemu.pid
.tmp/qemu/cirros/qemu.log
.tmp/qemu/cirros/serial.log
.tmp/qemuimg/
```

### CirrOS Acceptance Path

The acceptance scenario should be:

1. Developer builds Govirta locally.
2. Developer copies or runs the relevant verification binary on `192.168.139.206`.
3. The remote Linux host has a QEMU system binary and `qemu-img` installed.
4. The remote Linux host has a CirrOS qcow2 image matching its `runtime.GOARCH`.
5. The remote Linux host has a pre-created TAP device; bridge/TAP setup is outside this QEMU package phase.
6. Govirta uses `qemuimg.Info` to inspect the CirrOS image.
7. Govirta builds a `qemu.Config` with:
   - `Name`: `cirros-dev`
   - `Binary`: explicit value when configured, otherwise selected from runtime defaults
   - `Machine.Type`: `q35` on `amd64`, `virt` on `arm64`
   - `Machine.Accelerator`: `tcg` by default, `kvm` when available
   - `Compute.MemoryMiB`: small CirrOS-compatible value such as `256`
   - `Compute.VCPUs`: `1`
   - `Compute.CPUModel`: runtime default or explicit override
   - `Firmware.BIOSPath`: required for `arm64` CirrOS-style UEFI boot, optional for `amd64`
   - one qcow2 virtio disk pointing at the CirrOS image
   - one tap-backed virtio NIC using `GOVIRTA_QEMU_TAP`
   - QMP, pidfile, QEMU log, and serial log paths under `.tmp/`
8. Govirta starts the selected QEMU system binary through the process runner.
9. Validation proves the process starts and exposes QMP or produces expected pid/log/socket artifacts.
10. Validation proves the TAP-backed NIC was injected into the guest by checking serial output for guest network initialization such as `eth0: carrier acquired`, and by checking host-side TAP RX/TX counters increased during guest boot.
11. Test teardown stops the QEMU process and removes only files created under `.tmp/`.

For `arm64` Linux validation, `/usr/libexec/qemu-kvm` is an acceptable QEMU system binary. Networking must use an existing TAP device. On `192.168.139.206`, a normal acceptance pass is defined as: Govirta starts the VM, injects the `gv-tap0` TAP-backed virtio NIC, QMP reports the VM as running, CirrOS reaches the Linux boot path, and the guest serial log shows the NIC link being acquired. A DHCP lease is not required for first-phase acceptance because the validation bridge may be isolated and intentionally not provide DHCP.

### Verified Remote TAP Baseline

The following remote baseline has already been verified manually and should be treated as the reference for implementation acceptance:

```text
Host: root@192.168.139.206
OS: Rocky Linux 8.10
Host arch: aarch64
QEMU system binary: /usr/libexec/qemu-kvm
QEMU version: 6.2.0
QEMU image tool: /usr/bin/qemu-img
Firmware: /usr/share/edk2/aarch64/QEMU_EFI.fd
Bridge: govirta0
TAP: gv-tap0
CirrOS image: /root/govirta-qemu-test/images/cirros-aarch64.qcow2
```

Reference QEMU arguments:

```text
/usr/libexec/qemu-kvm
  -name cirros-dev-tap
  -machine virt,accel=tcg
  -cpu cortex-a57
  -m 256
  -smp 1
  -nographic
  -bios /usr/share/edk2/aarch64/QEMU_EFI.fd
  -qmp unix:/root/govirta-qemu-test/run/qmp.sock,server=on,wait=off
  -pidfile /root/govirta-qemu-test/run/qemu.pid
  -D /root/govirta-qemu-test/run/qemu.log
  -drive if=virtio,file=/root/govirta-qemu-test/images/cirros-aarch64.qcow2,format=qcow2
  -netdev tap,id=net0,ifname=gv-tap0,script=no,downscript=no
  -device virtio-net-pci,netdev=net0,mac=52:54:00:12:34:56
```

These arguments define the behavior that Govirta's qemu package must generate and execute. Running this shell command manually is not sufficient for final acceptance.

Reference acceptance evidence:

```text
QMP: {"return": {"status": "running", "singlestep": false, "running": true}}
Serial: EFI stub: Booting Linux Kernel...
Serial: Linux version 5.15.0-117-generic ...
Serial: Starting network: dhcpcd-9.4.1 starting
Serial: eth0: carrier acquired
Host TAP: gv-tap0 has non-zero RX/TX packet counters during guest boot
```

## Error Handling

Validation should reject:

- empty binary path
- empty VM name
- non-positive memory or vCPU counts
- unsupported accelerator values
- missing firmware path when runtime defaults or explicit config require firmware
- disk with empty path, unsupported format, or unsupported interface
- NIC with empty ID, empty TAP name, empty MAC, or unsupported model
- missing QMP socket path when VM start requires QMP
- missing qemu-img image path or unsupported image format

Errors should be returned to callers with context and wrapping where appropriate.

## Logging and Observability

Runtime code should use structured logging through the project logging approach. It should not print directly with `fmt.Println`.

Useful stable fields include:

- `vm_name`
- `qemu_binary`
- `qemu_args`
- `go_arch`
- `machine_type`
- `firmware_path`
- `qmp_socket`
- `pid_file`
- `image_path`
- `image_format`
- `tool`

Do not log secrets or sensitive host paths beyond development paths needed for diagnostics.

## Documentation References

- QEMU tool documentation describes `qemu-img` as the disk image utility for creating, converting, and modifying disk images.
- QEMU tool documentation describes `qemu-storage-daemon` and `qemu-nbd` as storage-related tools, but they are deferred from this phase.
- QEMU system documentation demonstrates TAP network usage through `-netdev tap,...` and device attachment through `-device virtio-net-pci,...`.
- Go `os/exec.CommandContext(ctx, name, arg...)` supports command execution with context cancellation and should be used by runner implementations.

## Affected Call Relationships

Initial implementation target:

```text
cmd/govirtlet/main.go
  -> internal/node.Agent.Run
  -> internal/virt/qemu.Driver.Start
  -> internal/virt/qemu.Builder.Build
  -> internal/virt/qemu.ProcessRunner.Start
  -> os/exec.CommandContext
```

Image operation target:

```text
cmd/govirtctl or future storage workflow
  -> internal/virt/qemuimg.Client.Create/Info/Resize
  -> internal/virt/qemuimg.CommandRunner.Run
  -> os/exec.CommandContext
```

Future network-integrated path:

```text
cmd/govirtlet/main.go
  -> internal/node.Agent.Run
  -> internal/network/bridge.Manager.EnsureVMNetwork
  -> internal/virt/qemu.Driver.Start
```

## Open Decisions for Implementation Planning

Before implementation, decide whether `qemu-img convert` is required in the first implementation plan. It is not required by this spec unless CirrOS validation needs format conversion.
