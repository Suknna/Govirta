# test/e2e Knowledge Base

<!--
Verified-against:
  base_commit: dfad16b
  files:
    - scripts/e2e.sh
    - test/e2e/doc.go
    - test/e2e/closure_test.go
    - test/e2e/lifecycle_test.go
    - test/e2e/replace_test.go
    - test/e2e/guest.go
    - test/e2e/guest_paths.go
    - test/e2e/manifests/01-storagepool-block.json
    - test/e2e/manifests/02-storagepool-file.json
    - test/e2e/manifests/03-image.json
    - test/e2e/manifests/04-volume.json
    - test/e2e/manifests/05-network.json
    - test/e2e/manifests/06-nic.json
    - test/e2e/manifests/07-vm.json
    - test/e2e/manifests/08-snapshot.json
    - test/e2e/manifests/09-volume-data.json
  flows:
    - anchor: flow-e2e-distributed-spine
      sources:
        - scripts/e2e.sh
        - test/e2e/closure_test.go
        - test/e2e/guest.go
        - test/e2e/guest_paths.go
    - anchor: flow-e2e-replace-cycle
      sources:
        - test/e2e/replace_test.go
        - test/e2e/closure_test.go
    - anchor: flow-e2e-resource-lifecycle
      sources:
        - test/e2e/lifecycle_test.go
        - test/e2e/closure_test.go
-->

## OVERVIEW

Host-driven distributed-spine E2E suite. `scripts/e2e.sh` starts Docker etcd, host `govirtad`, Lima guest `govirtlet`, then Go tests drive `govirtctl` against the real master/node/watch/control loop.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Orchestration script | `scripts/e2e.sh` | Docker etcd + host govirtad + Lima govirtlet + `go test -tags e2e` |
| Main closure test | `closure_test.go:102` | apply dependencies, boot VM, snapshot, cold resize, cold config change, teardown |
| Replace cycle | `replace_test.go:15` | get/edit/replace CAS workflow through `govirtctl replace` |
| Lifecycle helpers | `lifecycle_test.go:13` | generic apply/delete verification for resource lifecycle cases |
| Guest assertions | `guest.go` | live guest/qcow2/QEMU argv checks inside the Lima node |
| Path contract | `guest_paths.go` | guest state root paths mirrored by `scripts/e2e.sh` constants |
| Manifests | `manifests/*.json` | ordered resources: pools, image, root volume, network, NIC, VM, Snapshot, data volume |

## CONVENTIONS

- Run via `scripts/e2e.sh full`; the script owns topology setup, cache preparation, binaries, cleanup, and log paths.
- All intermediate files belong under project `.tmp/e2e/` or `.lima/cache/`; do not write global `/tmp`.
- Manifests use explicit `metadata.uid` and behavior-affecting fields; tests should mutate copies rather than relying on hidden defaults.
- The guest node is the live source of truth for qcow2 snapshots, virtual size, and running QEMU argv; the host test shells into the guest for these checks.

## ANTI-PATTERNS

- Do not make the guest connect directly to etcd; only `govirtad` talks to etcd.
- Do not require host-to-guest inbound connectivity; the node dials back to `host.lima.internal`.
- Do not add E2E assertions that inspect only control-plane status when a live guest/qcow2/QEMU fact is the accepted behavior.

## CALL GRAPHS & DATA FLOW (LOCAL)

### Flow: distributed spine closure {#flow-e2e-distributed-spine}

- Entry from root flow: `scripts/e2e.sh:301 (run_e2e)` starts `go test -v -tags e2e -count=1 ./test/e2e/...`
- Local chain:
  1. `test/e2e/closure_test.go:102 (TestDistributedSpineClosure)` — require tool paths/env, apply dependency manifests
  2. `test/e2e/closure_test.go:213 (applyVMVariant)` — create VM variants for power/admission coverage
  3. `test/e2e/closure_test.go:255 (snapshotColdCycle)` — create/delete Snapshot and assert qcow2 internal snapshot appears/disappears in guest
  4. `test/e2e/closure_test.go:281 (coldResizeVolume)` — apply larger data-volume capacity and wait for guest qcow2 virtual size
  5. `test/e2e/closure_test.go:330 (coldConfigChange)` — update powered-off VM config and assert next running QEMU argv reflects memory/disk/NIC changes
  6. `test/e2e/closure_test.go:772 (teardownSpine)` — delete resources and assert no guest orphans
- Data: manifest JSON → `govirtctl` HTTP calls → master watch → node reconcile → live guest/qcow2/QEMU checks
- Side effects: real etcd keys, host/guest processes, guest storage/network/vmm resources
- Exit / next hop: control-plane/node flows in root `AGENTS.md#flow-apiserver-apply`, `#flow-node-reconcile`, and cold operation flows

### Flow: replace cycle {#flow-e2e-replace-cycle}

- Entry from root flow: `test/e2e/replace_test.go:15 (replaceCycle)`
- Local chain:
  1. `test/e2e/replace_test.go:15 (replaceCycle)` — get existing object with `metadata.resourceVersion`
  2. `test/e2e/replace_test.go:85 (writeJSONManifest)` — write edited manifest copy
  3. `test/e2e/closure_test.go:912 (runCtl)` — call `govirtctl replace`
  4. `test/e2e/closure_test.go:659 (waitObjectPhase)` — wait for status convergence after replace
- Data: GET response JSON → edited JSON manifest → PUT replace request → converged resource status
- Side effects: apiserver CAS write through replace path
- Exit / next hop: `internal/controlplane/apiserver/handler_replace.go:26 (Server.Replace)` [详见 `../../internal/controlplane/apiserver/AGENTS.md#flow-apiserver-replace`]

### Flow: resource lifecycle helper {#flow-e2e-resource-lifecycle}

- Entry from root flow: `test/e2e/lifecycle_test.go:28 (applyAndVerify)` / `:43 (deleteAndVerify)`
- Local chain:
  1. `test/e2e/lifecycle_test.go:28 (applyAndVerify)` — apply manifest and wait for desired phase
  2. `test/e2e/lifecycle_test.go:43 (deleteAndVerify)` — delete resource and wait for disappearance
  3. `test/e2e/closure_test.go:872 (deleteAndWaitGone)` — uniform delete polling
- Data: `resourceLifecycle` spec → manifest path + kind/name/phase → `govirtctl` apply/delete/get loops
- Side effects: real API writes and finalizer-driven node teardown
- Exit / next hop: apiserver delete/finalizers path [详见 `../../internal/controlplane/apiserver/AGENTS.md#flow-apiserver-delete-finalizers`]

## NOTES

- `scripts/e2e.sh check-tools` verifies Docker, Lima, Go, curl, and checksum tooling without starting the topology.
- Current `test/e2e/doc.go` still says six resources; source has already expanded to Snapshot and data-volume flows. Treat this AGENTS.md as the current navigation evidence until that doc comment is separately refreshed.
- E2E is heavier than `scripts/verify.sh`; use it for distributed spine or watch/list/delete/finalizer/cold-operation changes.
