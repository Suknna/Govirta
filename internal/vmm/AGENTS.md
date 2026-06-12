# internal/vmm Knowledge Base

<!--
Verified-against:
  base_commit: dfad16b
  files:
    - internal/vmm/service.go
    - internal/vmm/lifecycle.go
    - internal/vmm/discover.go
    - internal/vmm/redefine.go
    - internal/vmm/argv.go
    - internal/vmm/state.go
    - internal/vmm/store.go
    - internal/vmm/paths.go
    - internal/vmm/facility.go
    - internal/vmm/vm.go
    - internal/vmm/errors.go
    - internal/vmm/qmp_factory.go
    - internal/vmm/proc/controller.go
    - internal/vmm/proc/controller_linux.go
    - internal/vmm/proc/controller_other.go
    - internal/vmm/proc/errors.go
    - pkg/virt/qemu/vm.go
    - test/acceptance/vmm_lifecycle_test.go
    - internal/vmm/redefine_test.go
    - internal/vmm/argv_test.go
  flows:
    - anchor: flow-vmm-create
      sources:
        - internal/vmm/service.go
        - internal/vmm/facility.go
        - internal/vmm/store.go
        - pkg/virt/qemu/vm.go
    - anchor: flow-vmm-start
      sources:
        - internal/vmm/lifecycle.go
        - internal/vmm/proc/controller_linux.go
        - internal/vmm/discover.go
    - anchor: flow-vmm-discover-reattach
      sources:
        - internal/vmm/discover.go
        - internal/vmm/state.go
        - internal/vmm/proc/controller_linux.go
    - anchor: flow-vmm-redefine
      sources:
        - internal/vmm/redefine.go
        - internal/vmm/argv.go
        - internal/vmm/facility.go
        - internal/vmm/store.go
-->

## OVERVIEW

VM-facing node-local QEMU process lifecycle layer, peer to `internal/storage` and `internal/network`, following the same shape: VM-facing service (`VMMService`) → swappable process-control primitive (`proc.ProcessController`), composing `pkg/virt/qemu` (typed argv builder) + `pkg/virt/qmp` (status/control). It owns process lifecycle only (Q1-A): it receives an already-resolved input (a configured-but-unbuilt `*qemu.Builder` + published disk paths + ensured TAP names + a caller-supplied UUID) and never calls storage/network. Upper composition (`internal/node`) wires storage + network + vmm.

QEMU is spawned with native `-daemonize` so guests survive orchestrator death (硬约束 1: no `Pdeathsig`, no shared process group, no dependence on orchestrator-held stdio/QMP). Runtime state is never cached: the authoritative run-state is the live QEMU process + QMP (上下一致). A per-VM `vm.json` persists only identity/spec/runtime-paths/intent (desired axis) plus the facility-injected argv snapshot; it is never the run-state authority.

Recovery is kubelet-shaped: `Discover` enumerates live actual VMs (CRI-list role), `Reattach` only adopts a live process. There is structurally no "auto-start on recovery" path — a dead `intent=Running` VM derives `Failed` and is reported, never relaunched (anti-split-brain guard for the migrated-away case).

## RUNTIME LAYOUT

```
/var/lib/govirtlet/<vm-uuid>/        # runtimeRoot/<uuid>; layout is vmm-private
  vm.json       # identity + spec + paths + IntendedPhase + facility-injected argv (atomic write: temp + rename)
  qemu.pid      # QEMU -pidfile self-write; ProcessAlive reads it
  qemu.log      # QEMU -D runtime log (also carries VNC service messages)
  qmp.sock      # QMP control unix socket (server=on,wait=off → reattach after restart)
  vnc.sock      # VNC unix socket (passively held + reported; byte-stream proxy is an upper-layer concern, Q54-A: never TCP)
  console.log   # -serial file: serial console output (this is Q52's "vnc日志")
```

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Service struct + Create + Delete | `service.go` | `VMMService`; `NewVMMService(runtimeRoot, proc, qmpFactory)`; `QMPFactory` type |
| Start / Stop / Kill | `lifecycle.go` | `Start` (spawn persisted argv), `Stop` (best-effort QMP `system_powerdown`), `Kill` (QMP `quit` → SIGKILL fallback, guaranteed stop) |
| Cold config redefine | `redefine.go` | replace persisted `Spec` + derived argv; does not touch `Intended` or QEMU process |
| Spec → argv derivation | `argv.go` | derive typed QEMU builder from `SpecSummary` + explicit `NodeEnv` |
| Status / List / Discover / Reattach | `discover.go` | live probe (`probe`/`statusFrom`), `Discover` scan, `Reattach` adopt-live-only |
| Phase derivation (pure) | `state.go` | `derivePhase`/`observedPhase`; live wins over intent |
| vm.json encode/decode + persistence | `store.go` | `encodeState`/`decodeState`/`writeState`/`loadState` |
| Runtime path layout (vmm-private) | `paths.go` | `runtimePathsFor(runtimeRoot, uuid)` |
| Facility flag injection | `facility.go` | `injectFacilityFlags`: pidfile/QMP chardev+mon/serial-file/vnc/daemonize → Build → argv snapshot |
| Domain types | `vm.go` | `Phase`, `IntendedPhase`, `SpecSummary`, `RuntimePaths`, `persistedState`, `VM`, `CreateRequest` |
| Error sentinels | `errors.go` | `ErrInvalidRequest`/`ErrNotFound`/`ErrAlreadyExists`/`ErrConflict`/`ErrNotReady` |
| Production QMP factory | `qmp_factory.go` | `ProductionQMPFactory` → real `qmp.NewSocketClient` |
| Process-control primitive (contract) | `proc/controller.go` | `ProcessController` interface; QMP is NOT in this boundary |
| Linux primitive impl | `proc/controller_linux.go` | `//go:build linux`; os/exec daemonize, signal 0, SIGKILL, atomic file IO, dir scan |
| Non-linux stub | `proc/controller_other.go` | `//go:build !linux`; returns `ErrUnsupportedPlatform` so darwin compiles |
| End-to-end lifecycle acceptance | `test/acceptance/vmm_lifecycle_test.go` | `//go:build acceptance && linux`; real QEMU daemonize on Lima |

## CODE MAP

| Symbol | Type | Location | Role |
| --- | --- | --- | --- |
| `VMMService` | struct | `service.go:21` | node-local QEMU lifecycle service; holds no qmp client, caches no run-state |
| `NewVMMService` | func | `service.go:32` | all three deps required (explicit 铁律) |
| `QMPFactory` | type | `service.go:15` | `func(socketPath) (qmp.Client, error)`; transient per-op client |
| `VMMService.Create` | method | `service.go:47` | inject facility flags → Build → persist argv + `IntendedDefined`; duplicate UUID → `ErrAlreadyExists` |
| `VMMService.Delete` | method | `service.go:87` | requires no live process else `ErrConflict`; removes whole runtime dir |
| `VMMService.Start` | method | `lifecycle.go:13` | spawn persisted daemonized argv, wait QMP ready, `IntendedRunning`; idempotent when already alive |
| `VMMService.Stop` | method | `lifecycle.go:50` | QMP `system_powerdown` (best-effort ACPI), `IntendedStopped`; phase stays `Stopping` until guest actually exits |
| `VMMService.Kill` | method | `lifecycle.go:77` | QMP `quit`; unreachable → `ForceKill` SIGKILL; both fail → `errors.Join` |
| `VMMService.Status` | method | `discover.go:55` | single-VM live probe → derived `Phase` |
| `VMMService.Discover` | method | `discover.go:64` | scan runtimeRoot, load each vm.json, live-verify; skips undiscoverable dirs; sorted by UUID |
| `VMMService.Reattach` | method | `discover.go:96` | adopt live process only; dead → `ErrNotReady`, never spawns |
| `VMMService.Redefine` | method | `redefine.go:25` | cold config convergence: rewrite vm.json Spec + argv without process mutation |
| `derivePhase` / `observedPhase` | func | `state.go:10` / `state.go:37` | intent + live probe → `Phase`; live wins; `observedPhase` adds the Defined special-case |
| `injectFacilityFlags` | func | `facility.go:17` | renders runtime facility flags onto the builder, returns argv snapshot |
| `runtimePathsFor` | func | `paths.go:15` | runtimeRoot + uuid → full `RuntimePaths` (private layout) |
| `ProcessController` | interface | `proc/controller.go:9` | spawn/alive/forcekill + atomic state IO + dir scan; swappable |
| `LinuxController` | struct | `proc/controller_linux.go:20` | real Linux impl; `NewLinuxController` |
| `ProductionQMPFactory` | func | `qmp_factory.go:5` | real socket-backed `qmp.Client` factory |

## STATE MODEL

Two axes: persisted `IntendedPhase` (desired) + live probe (process alive via pidfile+signal0; QMP reachable; QMP `query-status=running`). Observed `Phase` is derived; **live always wins, intent only disambiguates between states with identical live signals.**

| IntendedPhase | process | QMP | → observed Phase |
| --- | --- | --- | --- |
| Defined | dead | — | `PhaseDefined` (created, never started) |
| Running | alive | running | `PhaseRunning` |
| Running | alive | not ready/unreachable | `PhaseStarting` |
| Running | dead | — | `PhaseFailed` (abnormal exit / spawn failure) |
| Stopped | alive | any | `PhaseStopping` (powerdown sent, not yet exited) |
| Stopped | dead | — | `PhaseStopped` |

Conflict arbitration: `intent=Running` + process gone → `Failed` (never reports `Running`); `intent=Stopped` + process gone → `Stopped`. `PhaseDefined` is the `observedPhase` special-case (intent=defined + no process); `derivePhase` itself never produces it.

## CONVENTIONS

- Process lifecycle only (Q1-A): never call `internal/storage` or `internal/network`. Inputs (published disk paths, ensured TAP names, configured builder, UUID) are pre-resolved by the upper composition layer.
- VM UUID is caller-supplied in `CreateRequest.UUID`; vmm never generates it (一等公民判据 + explicit 铁律).
- The caller passes a configured-but-unbuilt `*qemu.Builder`; vmm injects runtime facility flags from its private path layout then calls `Build()` (Q6-A + Q70). The path layout never leaks to the upper layer (与 storage `local.Driver` 同构).
- Persisted argv model: `Create` renders the facility-injected argv once and stores it in vm.json. `Start` execs the stored argv; `Discover`/`Reattach` reconstruct everything from vm.json after restart — the builder never survives across operations.
- `Redefine` is the cold-only config convergence path: it replaces `persistedState.Spec`, re-derives facility-injected argv, and preserves identity/paths/intent/createdAt. The caller owns the cold gate.
- QMP clients are transient: built per-op via `QMPFactory` from the uuid-derived `qmp.sock` path, used, then `Disconnect`. A live QMP connection is never a guest-survival dependency (硬约束 1).
- Run-state is always read live (process + QMP); vm.json is authoritative for identity/spec/intent only, never for run-state (上下一致). On conflict, live wins.
- All OS side effects go through `proc.ProcessController` (spawn/alive/forcekill + atomic file IO + dir scan); unit tests inject a fake and never touch real QEMU. QMP stays a separate swappable boundary injected via `QMPFactory`.
- Errors propagate with `%w`; cleanup/rollback multi-errors compose with `errors.Join` (e.g. `Kill` joining QMP-quit + SIGKILL failures). Sentinels live in `errors.go`.
- `vmm` service code is platform-neutral pure logic; only `proc/controller_linux.go` is `//go:build linux`. `proc/controller_other.go` is a `//go:build !linux` stub returning `ErrUnsupportedPlatform` so darwin compiles (memory #700).

## ANTI-PATTERNS

- Never auto-start a VM during recovery. `Discover`/`Reattach` must not call `SpawnDaemonized`; a dead `intent=Running` VM derives `Failed` and is only reported, leaving control-plane to adjudicate (prevents relaunching a migrated-away guest → split-brain). The only `SpawnDaemonized` call site is `Start` (`lifecycle.go`).
- Do not couple QEMU to the orchestrator process: no `SysProcAttr.Pdeathsig`, no shared process group, no dependence on orchestrator-held stdio/pipes/QMP. `SpawnDaemonized` deliberately avoids `exec.CommandContext` so ctx cancellation cannot kill an already-daemonized guest.
- Do not cache observed run-state in `VMMService`; re-read live every time through `proc` + QMP.
- Do not generate/infer/default UUIDs, paths, or any VM config field; UUID is caller-supplied and the runtime path layout is computed only inside vmm from runtimeRoot + uuid.
- Do not open VNC over TCP; the facility renders `-vnc unix:<path>` only (Q54-A) and vmm just holds + reports the socket path — byte-stream proxying is an upper-layer (web frontend) concern.
- Do not treat this layer's vm.json as a control-plane metadata store; it is a node-local runtime checkpoint (libvirt/docker-style capability), orthogonal to the etcd-only control-plane rule (#652) and self-implemented (no libvirt, #651).
- Do not bypass the typed builder by string-assembling QEMU args in vmm; facility flags go through `pkg/virt/qemu` typed setters (积木式 + Q56).

## CALL GRAPHS & DATA FLOW (LOCAL)

### Flow: VM create {#flow-vmm-create}

- Entry: `internal/vmm/service.go:47 (VMMService.Create)` (caller has a configured builder + UUID + spec, wants the VM defined and persisted but not spawned)
- Local chain:
  1. `internal/vmm/service.go:57 (Create → runtimePathsFor)` — compute private runtime path layout from runtimeRoot + UUID
  2. `internal/vmm/service.go:60 (Create → proc.ReadState)` — duplicate detection: existing vm.json → `ErrAlreadyExists`
  3. `internal/vmm/facility.go:17 (Create → injectFacilityFlags)` — inject `-pidfile`/QMP chardev+`-mon`/`-serial file:`/`-vnc unix:`/`-daemonize` then `Build()` → argv snapshot
  4. `internal/vmm/store.go:37 (Create → writeState)` — atomic persist of `persistedState` with `IntendedDefined`
- Data: `CreateRequest{UUID, *qemu.Builder, SpecSummary}` → facility-injected `[]string` argv → `persistedState` (vm.json)
- Side effects: one `vm.json` written atomically; no process spawned
- Exit / next hop: `pkg/virt/qemu/vm.go:324 (Builder.Build)` → `vm.go:389 (VM.Argv)` [详见 `../../pkg/virt/qemu/AGENTS.md#flow-argv-build`]

### Flow: VM start {#flow-vmm-start}

- Entry: `internal/vmm/lifecycle.go:13 (VMMService.Start)` (caller wants a defined VM running)
- Local chain:
  1. `internal/vmm/lifecycle.go:15 (Start → loadState)` — read vm.json (missing → `ErrNotFound`)
  2. `internal/vmm/lifecycle.go:19 (Start → proc.ProcessAlive)` — already alive → idempotent `statusFrom` return, no re-spawn
  3. `internal/vmm/lifecycle.go:28 (Start → proc.SpawnDaemonized)` — exec the persisted daemonized argv (QEMU forks to background and returns); spawn failure still persists `IntendedRunning` (live probe later derives `Failed`)
  4. `internal/vmm/lifecycle.go:42 (Start → waitQMPReady)` — transient QMP client waits ready; non-fatal (Phase derives from live probe)
  5. `internal/vmm/discover.go:40 (Start → statusFrom)` — live-derive the returned `Phase`
- Data: `uuid` → persisted argv → daemonized QEMU process → live `VM` view
- Side effects: a daemonized QEMU process owning its own pidfile/QMP/VNC/console sockets; `IntendedRunning` persisted
- Exit / next hop: `internal/vmm/proc/controller_linux.go:26 (LinuxController.SpawnDaemonized)`; QMP via `pkg/virt/qmp` [详见 `../../pkg/virt/qmp/AGENTS.md`]

### Flow: discover + reattach (kubelet recovery) {#flow-vmm-discover-reattach}

- Entry: `internal/vmm/discover.go:64 (VMMService.Discover)` / `:96 (VMMService.Reattach)` (orchestrator restart wants the live actual VM set; reconcile loop lives in upper `internal/node`, not here)
- Local chain:
  1. `internal/vmm/discover.go:68 (Discover → proc.ListStateDirs)` — enumerate `<uuid>/` dirs under runtimeRoot
  2. `internal/vmm/discover.go:74 (Discover → loadState)` — read each vm.json; undiscoverable dirs logged + skipped, never fail the whole scan
  3. `internal/vmm/discover.go:11 (Discover → probe)` then `internal/vmm/state.go:37 (observedPhase)` — live process + QMP probe → derived `Phase`; dead `intent=Running` → `Failed`
  4. `internal/vmm/discover.go:104 (Reattach → proc.ProcessAlive)` — adopt live process only; dead → `ErrNotReady`, structurally no `SpawnDaemonized` call
- Data: runtimeRoot scan → `[]persistedState` → live-probed `[]VM` (sorted by UUID)
- Side effects: none (read-only live observation); never spawns
- Exit / next hop: `internal/vmm/proc/controller_linux.go:54 (LinuxController.ProcessAlive)` (pidfile + signal 0); transient QMP `query-status`

### Flow: VM redefine {#flow-vmm-redefine}

- Entry from root flow: `internal/vmm/redefine.go:25 (VMMService.Redefine)` (called by node VM controller after observed process is off)
- Local chain:
  1. `internal/vmm/redefine.go:28 (Redefine → loadState)` — read existing vm.json; missing → `ErrNotFound`
  2. `internal/vmm/redefine.go:33 (Redefine → deriveBuilder)` — rebuild typed QEMU builder from new `SpecSummary` and explicit `NodeEnv`
  3. `internal/vmm/redefine.go:37 (Redefine → injectFacilityFlags)` — re-apply pidfile/QMP/serial/VNC/daemonize facility flags from preserved runtime paths
  4. `internal/vmm/redefine.go:43 (Redefine)` — replace `st.Spec` and `st.Argv`; preserve `Intended`, `UUID`, `Paths`, and `CreatedAt`
  5. `internal/vmm/redefine.go:45 (Redefine → writeState)` — atomic vm.json rewrite; `UpdatedAt` changes
  6. `internal/vmm/redefine.go:49 (Redefine → statusFrom)` — return live-probed phase
- Data: `uuid` + desired `SpecSummary` → typed QEMU builder → facility-injected argv → updated `persistedState`
- Side effects: vm.json rewrite only; no QEMU process start/stop/kill/QMP command
- Exit / next hop: `internal/node/controllers/vm_config.go:74 (VMController.reconcileConfigDrift)` [详见 `../node/controllers/AGENTS.md#flow-vm-cold-config-change`]

## NOTES

- Differs deliberately from storage's "no scan, in-memory only, upper-layer replay" (#251/#367): storage qcow2 is an inert file with no live process, while a VM has a daemonized process surviving restart that must be re-discovered + adopted to Stop/Kill/Status it. User-authorized deviation, documented in the spec.
- Builder extension for this layer (`pkg/virt/qemu`): added `Daemonize()`, `VNC(display.VNCUnix)`, `serial.File(path)`, and typed direct-kernel boot (`Kernel`/`Initrd`/`Append`). Direct-kernel boot was required because cirros aarch64 has no EFI bootloader and cannot boot standalone from disk; every acceptance test boots via `-kernel`/`-initrd`/`-append`.
- Out of scope (deferred): fencing token / STONITH strong-consistency double-write prevention, VNC byte-stream proxy/websocket, hot-plug/live migration, control-plane etcd integration inside this package. Cold config edit is now implemented through `Redefine`; cold snapshot/resize live above vmm in node/storage.
- Verification: `go test -race -count=1 ./internal/vmm/...` (fake `ProcessController` + fake `qmp.Client`, full phase-derivation table, lifecycle, anti-split-brain guard, ctx cancellation); Lima-only `TestVMMLifecycleEndToEnd` + `TestVMMDiscoverNeverRestartsDeadVM` (`test/acceptance/vmm_lifecycle_test.go`) prove real daemonize/QMP/powerdown/kill/discover/reattach and the never-auto-restart guard on a real kernel.
- Graceful Stop is best-effort, matching libvirt/Proxmox/kubevirt: QMP `system_powerdown` only injects an ACPI power-button event and returns immediately; whether the guest exits depends on guest ACPI cooperation. direct-kernel cirros aarch64 has no UEFI/ACPI, so the acceptance test asserts only that Stop is accepted, intent lands `Stopped`, and phase ∈ {`Stopping`, `Stopped`} — never that the guest truly exits. Guaranteed stop is `Kill` (QMP `quit`, no guest cooperation); upper layers own the Stop→Kill grace-period escalation.
- `SpawnDaemonized` captures QEMU's stderr during the synchronous fork-before-daemonize phase. Pre-daemonize initialization errors (argv parse, disk open, QMP bind failure) are written to stderr with a non-zero exit code; the captured text is joined into the returned error so callers see the actual QEMU diagnostic instead of a bare "exit status 1". This does not violate the decoupling constraint: only the pre-fork parent's synchronous output is captured; after daemonize the parent exits and the guest is setsid-detached with its own `-D`/`-serial` log.
- Evidence: direct source reads + AFT outline for symbol/line confirmation at `base_commit dfad16b`. `[已验证]` 源码与单测断言；Lima acceptance 为真实内核网关。`[降级: LSP call hierarchy]`.
