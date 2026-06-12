# pkg/virt Knowledge Base

<!--
Verified-against:
  base_commit: dfad16b
  files:
    - pkg/virt/qemu/vm.go
    - pkg/virt/qemuimg/client.go
    - pkg/virt/qemuimg/resize/resize.go
    - pkg/virt/qemuimg/snapshot/snapshot.go
    - pkg/virt/qmp/client.go
    - internal/node/agent.go
  flows: []
-->

## OVERVIEW

Local virtualization boundary aggregator. Owns three sibling packages: typed QEMU argv builder (`qemu/`), qemu-img command wrappers (`qemuimg/`), and the QMP client boundary (`qmp/`). This file is a navigation hub; module-internal call graphs live in the per-package AGENTS.md.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Build QEMU argv | `qemu/AGENTS.md` | Full Builder API, profile whitelist, golden test contract |
| qemu-img subcommands | `qemuimg/AGENTS.md` | Create/Info/Convert/Resize/Snapshot/SnapshotDelete/SnapshotList/SnapshotRevert/Check/Remove builders + Runner boundary |
| QMP boundary | `qmp/AGENTS.md` | Project-owned QMP client; root facade + internal go-qemu direct socket subset |
| Composition by node agent | `internal/node/agent.go:89` | `NewAgent` injects `qmp.NewNoopClient()` + shared `netpool` core + 7 controllers (composition lives in `internal/node`) |

## CONVENTIONS

- This package tree renders deterministic argv and exposes typed command boundaries; process spawn / QMP lifecycle / TAP creation belongs above (`internal/node`) or beside (`pkg/hostnet/link` primitives orchestrated by `internal/network`) this layer.
- New virtualization boundaries (e.g., a future `nbd/` or `migration/` package) sit at this level only when they own a distinct external command/protocol surface; otherwise extend an existing subpackage.

## ANTI-PATTERNS

- Do not introduce libvirt or libvirt-derived abstractions anywhere under this tree. (Repo-wide rule restated here for proximity.)
- Do not let `qemu/` import `qemuimg/` or vice versa; they are independent boundaries that share a caller (future node-agent code).
- Do not let unit tests under this tree depend on a real `qemu-system-*` binary, real `qemu-img` binary, or live QMP socket. Reserve those for gated integration/acceptance.

## CALL GRAPHS & DATA FLOW (LOCAL)

This directory is a pure aggregator with no symbols of its own. Per-flow chains live in:

- `qemu/AGENTS.md#flow-argv-build` — typed Builder → `VM.Argv()` argv flatten (consumed by `cmd/qemucli` today; future runtime caller).
- `qemuimg/AGENTS.md#flow-qcow2-do` — `Client.QCOW2().<sub>().Do(ctx)` → argv/local deletion → `Runner.Run` or filesystem → external `qemu-img` process / trusted qcow2 delete.
- `qmp/AGENTS.md#flow-qmp-ready` — `SocketClient.Connect` -> vendored direct socket monitor -> QMP capabilities handshake.
- `qmp/AGENTS.md#flow-qmp-commands` — typed status/power methods -> internal command packages -> QMP `Run`.
- `qmp/AGENTS.md#flow-qmp-events` — single-use event stream -> internal event conversion/filtering.

`[已验证]` 通过直接读取 `qemu/`、`qemuimg/`、`qmp/` 三个子包的 `client.go` / `vm.go`。

## NOTES

- `qemu.NewVM(arch)` auto-selects binary only: `ArchX86_64` → `qemu-system-x86_64`, `ArchAArch64` → `qemu-system-aarch64`. Machine profile, CPU model, and runtime binary overrides stay caller-set.
- Rocky Linux aarch64 acceptance requires firmware `-bios /usr/share/edk2/aarch64/QEMU_EFI.fd` for ARM cirros boot.
- For QEMU argv changes, update both `qemu/vm_test.go` and `cmd/qemucli/main_test.go` when the example CLI output should change.
- `qmp/` still keeps `NoopClient` for node skeleton composition tests, but the real `SocketClient` is available for future runtime integration.
