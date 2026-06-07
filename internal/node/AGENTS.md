# internal/node Knowledge Base

<!--
Verified-against:
  base_commit: 8778cb4
  files:
    - internal/node/agent.go
    - internal/node/agent_test.go
    - internal/node/hostdeps_linux.go
    - internal/node/hostdeps_other.go
    - internal/node/identity/identity.go
    - internal/node/client/client.go
    - internal/node/client/watch.go
    - internal/node/controller/controller.go
    - internal/node/controller/manager.go
    - internal/node/controller/queue.go
    - internal/node/controller/loop.go
    - internal/node/controllers/storagepool.go
    - internal/node/controllers/image.go
    - internal/node/controllers/volume.go
    - internal/node/controllers/network.go
    - internal/node/controllers/nic.go
    - internal/node/controllers/vm.go
  flows:
    - anchor: flow-node-boot
      sources:
        - cmd/govirtlet/main.go
        - internal/node/agent.go
        - internal/node/hostdeps_linux.go
    - anchor: flow-node-reconcile
      sources:
        - internal/node/controller/manager.go
        - internal/node/controller/loop.go
        - internal/node/client/watch.go
        - internal/node/controllers/storagepool.go
        - internal/node/controllers/image.go
        - internal/node/controllers/volume.go
        - internal/node/controllers/network.go
        - internal/node/controllers/nic.go
        - internal/node/controllers/vm.go
-->

## OVERVIEW

Compute node agent: assembles a self-built controller-manager framework that watches the control plane apiserver for 6 resource kinds and reconciles each through domain services (storage, network, vmm). Each controller follows a uniform pattern: decode → level-trigger no-op guard → dependency gating → domain work → status patch.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Agent assembly | `agent.go` | `NewAgent(cfg)` wires host managers, client, watch source, services, 6 controllers, manager |
| Platform-specific deps | `hostdeps_linux.go` / `hostdeps_other.go` | Linux: real netlink/nftables/CoreDHCP/exec; non-Linux: no-op stubs |
| Kernel identity derivation | `identity/identity.go` | pure functions: network→nftables identity, NIC→TAP name + anti-spoof identity |
| HTTP client to master | `client/client.go` | `Get`/`List`/`PatchStatus`; `ErrNotFound` sentinel |
| Watch source | `client/watch.go` | `WatchSource.Watch` → HTTP NDJSON stream → `controller.Event` channel |
| Controller interface | `controller/controller.go` | `Kind()` + `Reconcile(ctx, Event)` |
| Controller manager | `controller/manager.go` | `Manager.Run` spawns one goroutine per controller |
| Work queue | `controller/queue.go` | deduplicating FIFO: `map[string]Event` + order slice |
| Reconcile loop | `controller/loop.go` | `feed` (reconnect + resume cursor) → `consume` → `reconcileLoop` |
| StoragePool controller | `controllers/storagepool.go` | register pool + read usage → patch phase |
| Image controller | `controllers/image.go` | fetch source (file/http) into file pool → patch phase |
| Volume controller | `controllers/volume.go` | gate on pool+image deps → stream image → qcow2 root volume |
| Network controller | `controllers/network.go` | parse spec + derive identity → register+ensure+status |
| NIC controller | `controllers/nic.go` | gate on Network ready → derive TAP identity → register+ensure+status |
| VM controller | `controllers/vm.go` | gate on Volume+NIC deps → build qemu argv → vmm create+start |

## CONVENTIONS

- Each controller defines its own narrow interface (`PoolRegistrar`, `ImagePutter`, `RootVolumeCreator`, `NetworkEnsurer`, `NICEnsurer`, `VMRunner`) — tests inject fakes implementing only the needed slice.
- Every controller follows the same 9-step pattern: ctx check → DELETED no-op → unmarshal → level-trigger guard → dependency gating → domain work → patchStatus → permanent→failed/requeue=false → transient→failed/requeue=true.
- The controller framework never parses `Event.Object` bytes; it passes raw JSON to each controller's `Reconcile`.
- `feed()` reconnects the watch on server hangup, passing the last non-empty `ResourceVersion` as resume cursor.
- `patchStatus()` compares observed vs desired and skips the PATCH when identical (breaks the status→MODIFIED→watch→reconcile feedback loop).
- MAC is threaded unchanged from apiserver allocation through TAP + DHCP binding + anti-spoofing.
- Network/NIC identity (nftables table/chain names, TAP names) is derived deterministically from stable resource names via the `identity` package, never carried in the API spec.

## ANTI-PATTERNS

- Do not parse resource schemas inside the controller framework; kind dispatch is the framework's only concern.
- Do not generate or infer MACs, TAP names, or firewall identities in controllers; derive from stable names via `identity` package or thread from apiserver.
- Do not let controllers swallow errors; all errors must surface through `requeue` + log at the framework chokepoint.
- Do not start fire-and-forget goroutines; all goroutines are joined via `sync.WaitGroup` in `Manager.Run`.
- Do not skip dependency gating; Volume/NIC/VM controllers must verify all referenced resources are Ready before doing work.

## CALL GRAPHS & DATA FLOW (LOCAL)

### Flow: node agent boot {#flow-node-boot}

- Entry from root flow: `cmd/govirtlet/main.go:20 (main)` → `internal/node/agent.go:89 (NewAgent)`
- Local chain:
  1. `agent.go:90 (buildHostManagers)` — platform-specific: Linux wires real netlink/nftables/CoreDHCP/exec managers
  2. `agent.go:95 (client.New)` — HTTP client to master apiserver
  3. `agent.go:96 (client.NewWatchSource)` — streaming watch source
  4. `agent.go:98-106` — construct pool.Service, VolumeService, ImageService, netpool.Service, NetworkService, NICService, VMMService
  5. `agent.go:111-119` — construct 6 controllers (StoragePool, Image, Volume, Network, NIC, VM)
  6. `agent.go:120 (newAgentWithDeps)` — wrap in `controller.Manager`
  7. `agent.go:143 (Agent.Run)` → `controller.Manager.Run` — spawn goroutine per controller
- Data: `Config` → `hostManagers` + `client.Client` + `client.WatchSource` + domain services + 6 controllers + `controller.Manager`
- Side effects: HTTP connections to master, goroutines per controller
- Exit / next hop: controller reconcile loops

### Flow: reconcile loop {#flow-node-reconcile}

- Entry: `internal/node/controller/loop.go:21 (runController)` — one per resource kind
- Local chain:
  1. `loop.go:27 (feeder goroutine)` → `loop.go:61 (feed)` — reconnect loop: `WatchSource.Watch(ctx, kind, lastRV)` → `consume(ctx, ch, q, lastRV)`
  2. `loop.go:90 (consume)` — drain events into `Queue.Add` (dedup by Key, latest wins)
  3. `loop.go:115 (reconcileLoop)` — `Queue.Get` → `Controller.Reconcile(ctx, ev)` → re-Add on error/requeue
  4. Per-controller `Reconcile` example (StoragePool):
     - `controllers/storagepool.go:82` → `:97` unmarshal → `:102 buildPool` → `:112 RegisterPool` → `:119 GetPoolUsage` → `:131 patchStatus`
  5. Per-controller `Reconcile` example (VM):
     - `controllers/vm.go:90` → `:105` unmarshal → `:112 Status` (check existing) → `:144 gatherDependencies` → `:154 buildVM` → `:175 vmm.Create` → `:182 vmm.Start` → `:198 patchStatus`
- Data: HTTP NDJSON → `controller.Event{Type, Key, ResourceVersion, Object}` → typed API object → domain service call → status JSON → HTTP PATCH
- Side effects: per controller domain work (storage/network/vmm); status patches to master
- Exit / next hop: `client.PatchStatus` → master apiserver [详见 `../../AGENTS.md#flow-apiserver-status`]

## NOTES

- `hostdeps_linux.go` and `hostdeps_other.go` use `//go:build linux` / `//go:build !linux` to wire real vs no-op managers. The agent compiles on macOS but cannot serve guests there.
- The controller framework is deliberately thin and k8s-client-go-free. It uses the project's own HTTP watch contract, not the Kubernetes informer/watch API.
- `identity.DeriveNICIdentity` produces TAP names that fit Linux's `IFNAMSIZ-1` (15 char) limit via `"gv" + sha256(vmUID)[:8] + "." + nicIndex`.
- The e2e test (`test/e2e/closure_test.go`) exercises the full node reconciliation path through the distributed spine.
