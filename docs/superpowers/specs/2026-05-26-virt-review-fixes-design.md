# Virt Review Fixes Design

## Status

- Date: 2026-05-26
- Scope: design specification only
- Approved approach: fix blocking + important findings from `/review-deep internal/virt`, with tests that lock the repaired boundaries

## Goal

Fix the high-risk issues found in `internal/virt` after the virt boundary hardening work. The fixes must preserve Govirta's current phase boundary: complete the single-node cold-operation loop with strongly typed QEMU argv, offline qcow2 management, and a project-owned QMP facade. The implementation must not introduce libvirt, shell command strings, QEMU-owned TAP creation, or compatibility shims for incorrect internal APIs.

The immediate success criteria are:

1. Generic QEMU argv extension points cannot bypass typed builders for typed flags.
2. QEMU CPU and display values are validated before `VM.Argv()` can be exposed.
3. qemu-img qcow2 input commands do not rely on image format probing.
4. qemu-img process failures and JSON decode failures are classifiable as different error types while preserving stdout/stderr.
5. qcow2 remove semantics are explicit about trusted storage ownership and do not claim pathname checks provide stronger safety than they do.
6. QMP event streams are released by `Disconnect` even when callers forget to cancel the events context.
7. QMP command writes and handshake writes are cancellation-aware enough not to ignore caller context indefinitely.

## Non-Goals

- Do not implement distributed scheduling, Kubernetes integration, migration, hot-plug, or multi-node behavior.
- Do not make `internal/virt/qemu` start QEMU processes or create host networking resources.
- Do not add support for non-qcow2 image management in `QCOW2Client`.
- Do not redesign storage ownership beyond the minimum needed to make remove semantics honest and testable.
- Do not address unrelated nit-only review items unless they directly protect a blocking or important fix.

## Design Overview

Use a boundary-tightening approach rather than a broad cleanup. Each package keeps its current responsibility, but risky extension points become explicit contracts with tests.

```text
internal/virt/qemu
  typed config -> Build validation -> immutable VM.Argv()

internal/virt/qemuimg
  QCOW2 builders -> validated argv / typed errors -> Runner.Run(ctx,binary,args)

internal/virt/qmp
  root SocketClient -> lifecycle-owned event cancellation -> internal monitor/socket
```

## QEMU argv Boundary

### Generic argument policy

`AddArgument` remains available as a narrow escape hatch for QEMU options that do not yet justify a dedicated typed builder. It must not be a way to bypass typed validation for options already modeled by Govirta.

`Build()` should reject any generic flag that overlaps with current typed fields or typed entries:

- `-machine`, `-M`
- `-name`
- `-cpu`
- `-smp`
- `-m`
- `-blockdev`
- `-device`
- `-netdev`
- `-chardev`
- `-mon`
- `-serial`
- `-display`
- `-msg`
- `-pidfile`
- `-no-reboot`
- `-no-shutdown`

The initial allowlist for generic arguments is intentionally small:

- `-bios`, required by Rocky Linux aarch64 acceptance with `/usr/share/edk2/aarch64/QEMU_EFI.fd`.
- `-rtc`, already used by existing tests as a generic flag/value example.
- `-enable-kvm`, already used by existing tests as a generic flag example.

Future flags can be added to the allowlist only when they have no typed builder and their risk is understood. If a flag becomes common or has structured comma-separated values, add a typed builder instead of widening generic acceptance.

### CPU and display validation

`cpu.Model` and `display.Display` are string-backed types and must mirror the validation pattern used by machine profiles and `qflag.OnOff`.

- `cpu.Model.Valid()` returns true for empty, `host`, and `cortex-a57`.
- `display.Display.Valid()` returns true for empty and `none`.
- `Builder.Build()` rejects unsupported non-empty values with `ErrInvalidVM`.

### Tests

Add table-driven tests that prove:

- Generic typed flags such as `-netdev`, `-device`, `-blockdev`, `-chardev`, `-mon`, `-serial`, `-name`, `-cpu`, `-display`, and `-msg` are rejected.
- Allowed generic flags still render: `-bios`, `-rtc`, and `-enable-kvm`.
- Invalid CPU and display values return errors matching `ErrInvalidVM`.

## qemu-img Boundary

### Explicit qcow2 input format

`QCOW2Client` is a qcow2-specific resource entry. Commands that read an image should pass the input format explicitly instead of relying on qemu-img probing.

- `Info().Do(ctx)` renders `qemu-img info -f qcow2 --output=json <path>`.
- `Check().Do(ctx)` renders `qemu-img check -f qcow2 --output=json <path>`.
- `Convert().Do(ctx)` renders `qemu-img convert -f qcow2 -O qcow2 <source> <target>`.

If future conversion needs arbitrary source formats, add an explicit source-format enum or a different client entry. Do not silently reintroduce probing under `QCOW2Client`.

### Error model

Keep `CommandError` for process-level failures from `Runner.Run`. Add a separate decode error type for successful qemu-img execution whose stdout cannot be parsed as the expected JSON schema.

Recommended public surface:

```go
type DecodeError = imgexec.DecodeError
```

The decode error should carry:

- the original decode error through `Unwrap()`;
- the captured `imgexec.Result` for stdout/stderr diagnostics.

This preserves observability without making callers classify schema/format drift as a qemu-img process failure.

### Remove semantics

`Remove().Do(ctx)` is local filesystem deletion, not a qemu-img command. It may keep the current API shape, but the implementation and comments must state its real contract:

- It is only safe for paths resolved by Govirta's trusted storage layer or otherwise located in a trusted directory not writable by untrusted actors.
- It rejects non-`.qcow2` paths, directories, symlinks, and non-regular files before deletion.
- Its pre-delete `Lstat` check is a guardrail, not a full defense against hostile pathname replacement in an untrusted parent directory.

If the implementation can use a directory-fd unlink path in a small, maintainable way on the target platforms, prefer that. If that would introduce platform-specific complexity outside this package's current scope, keep the simpler implementation but encode the trust contract in documentation and tests instead of claiming TOCTOU is fully solved.

### Tests

Add or update tests for:

- Expected argv includes `-f qcow2` for `info`, `check`, and `convert`.
- JSON parse failures return the decode error type and preserve stdout/stderr.
- Process failures still return `CommandError`.
- `info` and `check` propagate `context.Canceled` through their runner.
- Remove tests explicitly lock the trusted regular-file contract and existing directory/symlink/non-regular rejection behavior.

## QMP Lifecycle and Cancellation

### Event stream ownership

`SocketClient.Events(ctx, names...)` should create an internal child context with cancel function. The client stores that cancel function for the current connection/event stream.

Lifecycle rules:

- `Connect` resets event-stream state.
- `Events` remains single-use for a connected socket after successful stream start.
- If `mon.Events(ctx)` fails before returning a stream, the single-use marker is reset so callers can retry.
- `Disconnect` cancels the stored event context before or while closing the monitor, so conversion goroutines stop even when callers do not cancel the original events context.
- Event goroutines clear stored cancellation state when they exit, without re-enabling a second event stream on the same still-connected socket unless the existing single-use contract explicitly allows it. Current contract should remain single-use per connected socket.

This design makes `Disconnect` the lifecycle owner for resources created by a connection, while still allowing caller cancellation to stop event consumption earlier.

### Write cancellation

`internal/goqemu.SocketMonitor` already has read-side cancellation support for blocking handshake reads and response waits. Extend the write side as well.

Required behavior:

- Before writing, check `ctx.Err()`.
- During handshake `qmp_capabilities` writes and `Run` command writes, use a temporary write deadline or watchdog so context cancellation can unblock a stuck write.
- If the context is canceled while forcing a write to unblock, return the context error.
- After successful writes, restore the connection write deadline to the zero value.

The implementation should avoid permanently poisoning deadlines for the listener goroutine or later commands.

### Tests

Add tests for:

- `Disconnect` releases an event stream even when the caller never cancels the events context and stops reading.
- `Events` can be retried after the underlying monitor returns an error before stream creation.
- Root `SocketClient` methods preserve `qmp.ResponseError` so callers can use `errors.As` to inspect class and description.
- Write cancellation behavior for handshake or command write using a controllable connection/test server. If deterministic write blocking is too brittle in unit tests, use a focused fake `net.Conn` in the internal socket package rather than sleeping timeouts.

## Verification

Run these commands after implementation:

```bash
go test ./internal/virt/...
go test -race ./internal/virt/qmp/...
scripts/verify.sh
```

If `scripts/verify.sh` is broader than the modified scope but still passes, it becomes the final local CI evidence. Remote QEMU acceptance is not required for this fix because the changes are boundary validation, qemu-img argv/error modeling, and QMP lifecycle tests; no live VM behavior is changed directly.

## Risks and Trade-offs

- A generic QEMU flag allowlist may reject a future legitimate option. This is intentional: future options must be consciously admitted or modeled as typed builders.
- Adding `-f qcow2` changes qemu-img behavior for mislabeled or non-qcow2 inputs. This matches `QCOW2Client`'s contract and should fail early rather than probe.
- QMP write-cancellation tests can be flaky if based on real socket buffer saturation. Prefer fake connections or deterministic test doubles.
- Fully eliminating remove TOCTOU for untrusted directories may require platform-specific filesystem APIs. The immediate design avoids overstating guarantees and keeps the stronger storage-layer ownership decision explicit.

## Documentation Updates

Update relevant AGENTS knowledge only where current text would become misleading after implementation:

- `internal/virt/qemu/AGENTS.md`: generic escape hatch is allowlisted; typed flags cannot be reintroduced through `AddArgument`.
- `internal/virt/qemuimg/AGENTS.md`: `info/check/convert` input side is explicitly qcow2; decode errors are separate from command errors; remove requires trusted storage ownership.
- `internal/virt/qmp/AGENTS.md`: event streams are canceled by `Disconnect`; write-side context cancellation is covered.
