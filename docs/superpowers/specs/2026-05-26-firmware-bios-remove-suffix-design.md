# Firmware BIOS Typed Boundary and Case-Insensitive qcow2 Removal Design

## Status

Approved design for implementation. Remote CirrOS acceptance on `192.168.139.206` is explicitly out of scope for this task.

## Context

Govirta is in the single-node cold-operation closure phase. The QEMU layer must keep argv construction typed and deterministic, while host-specific filesystem and process concerns remain above or beside that layer. Recent hardening made `AddArgument` allowlist-only and arity-checked. Keeping `-bios` as a generic exception would preserve a technical debt path for a value that is semantically firmware configuration.

`qemuimg.Remove` currently accepts only lowercase `.qcow2`. That is stricter than necessary for local trusted storage deletion and leaves a known behavior gap for uppercase or mixed-case qcow2 filenames.

## Goals

- Add a first-class typed QEMU firmware boundary for `-bios`.
- Remove the generic `-bios` escape hatch so callers must use the typed firmware API.
- Preserve deterministic argv ordering and existing internal typed renderer validation behavior.
- Make `qemuimg.Remove` accept `.qcow2` extensions case-insensitively.
- Keep validation local and unit-testable; do not require real QEMU, qemu-img, or remote host access.

## Non-Goals

- No remote CirrOS boot acceptance in this task.
- No file existence check for BIOS paths in the QEMU argv builder.
- No pflash, UEFI vars, or broader firmware model in this task.
- No change to `qemuimg.Remove` trusted-storage contract or TOCTOU limitations.
- No qemu-img native delete behavior.

## Design

### QEMU firmware package

Add a new package:

```text
internal/virt/qemu/firmware/
  firmware.go
```

The package exposes:

```go
package firmware

type BIOS struct {
    Path string
}

func (b BIOS) Arg() (string, error)
```

`BIOS.Arg` returns the firmware path when valid. It rejects an empty path and a path whose first byte is `'-'`, preventing QEMU from interpreting a caller-provided path as another option. It does not check filesystem existence because the argv builder may run on a different machine than the eventual QEMU process.

### QEMU builder API

Add to `internal/virt/qemu/vm.go`:

```go
func (b *Builder) BIOS(v firmware.BIOS) *Builder
```

The method appends a package-internal typed argument:

```go
b.ordered = append(b.ordered, typedArg("-bios", v.Arg))
```

This keeps `-bios` in the same ordered segment as block devices, devices, netdevs, chardevs, monitor, serial, and generic arguments. Callers retain deterministic control over relative ordering without introducing another fixed field into `VM.Argv()`.

### Generic argument policy

Remove `-bios` from the generic allowlist and add it to the typed-builder-covered reject list. After this change:

- `AddArgument(Arg("-bios", path))` returns an error wrapping `qemu.ErrInvalidVM` during `Build()`.
- `AddArgument(TypedArg("-bios", renderer))` returns an error wrapping `qemu.ErrInvalidVM` during `Build()`.
- `Builder.BIOS(firmware.BIOS{Path: path})` is the only supported `-bios` path.

The remaining generic allowlist stays narrow:

- `-enable-kvm` accepts only `Flag("-enable-kvm")`.
- `-rtc` accepts only value forms.

### qemu-img remove suffix behavior

Change the `.qcow2` suffix check in `internal/virt/qemuimg/remove/remove.go` from a case-sensitive comparison to a case-insensitive extension comparison:

```go
strings.EqualFold(filepath.Ext(path), ".qcow2")
```

This accepts `.qcow2`, `.QCOW2`, and mixed-case variants. It still rejects files such as `disk.qcow2.bak`, paths with no extension, and leading-dash path operands. Existing `Lstat` guardrails for directories, symlinks, and non-regular files remain unchanged.

## Tests

Add or update unit tests for:

- `firmware.BIOS{Path: valid}` renders the exact path.
- empty BIOS path returns an error.
- BIOS path beginning with `-` returns an error.
- `Builder.BIOS(firmware.BIOS{Path: path})` renders `-bios path` in argv.
- generic `Arg("-bios", path)` is rejected with `errors.Is(err, qemu.ErrInvalidVM)`.
- generic `TypedArg("-bios", renderer)` is rejected with `errors.Is(err, qemu.ErrInvalidVM)`.
- `qemuimg.Remove` deletes uppercase and mixed-case `.qcow2` files.
- non-qcow2 extensions remain rejected.

## Documentation Updates

- Update `internal/virt/qemu/AGENTS.md` to point remote acceptance firmware usage at `Builder.BIOS(firmware.BIOS{Path: ...})` and to state that `-bios` is no longer generic allowlist material.
- Update `internal/virt/qemuimg/AGENTS.md` to state that `Remove` accepts `.qcow2` suffixes case-insensitively while preserving the trusted-path contract.

## Verification

Run local verification only:

```bash
go test ./internal/virt/qemu/...
go test ./internal/virt/qemuimg/remove
scripts/verify.sh
```

Remote `192.168.139.206` CirrOS boot acceptance is out of scope for this task.
