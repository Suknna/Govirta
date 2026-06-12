# internal/node/controllers Knowledge Base

<!--
Verified-against:
  base_commit: dfad16b
  files:
    - internal/node/agent.go
    - internal/node/controllers/storagepool.go
    - internal/node/controllers/image.go
    - internal/node/controllers/volume.go
    - internal/node/controllers/volume_resize.go
    - internal/node/controllers/network.go
    - internal/node/controllers/nic.go
    - internal/node/controllers/vm.go
    - internal/node/controllers/vm_power.go
    - internal/node/controllers/vm_config.go
    - internal/node/controllers/snapshot.go
    - internal/node/controllers/coldgate.go
    - internal/node/controllers/teardown.go
    - internal/node/client/client.go
    - internal/storage/service.go
    - internal/storage/local/driver.go
    - internal/vmm/redefine.go
    - pkg/virt/qemuimg/client.go
  flows:
    - anchor: flow-node-finalizer-teardown
      sources:
        - internal/node/controllers/teardown.go
        - internal/node/client/client.go
        - internal/node/controllers/storagepool.go
        - internal/node/controllers/image.go
        - internal/node/controllers/volume.go
        - internal/node/controllers/network.go
        - internal/node/controllers/nic.go
        - internal/node/controllers/vm.go
        - internal/node/controllers/snapshot.go
    - anchor: flow-node-snapshot-cold-lifecycle
      sources:
        - internal/node/agent.go
        - internal/node/controllers/snapshot.go
        - internal/node/controllers/coldgate.go
        - internal/storage/service.go
        - internal/storage/local/driver.go
        - pkg/virt/qemuimg/client.go
    - anchor: flow-node-volume-cold-resize
      sources:
        - internal/node/controllers/volume.go
        - internal/node/controllers/volume_resize.go
        - internal/node/controllers/coldgate.go
        - internal/storage/service.go
        - internal/storage/local/driver.go
        - pkg/virt/qemuimg/client.go
    - anchor: flow-vm-cold-config-change
      sources:
        - internal/node/controllers/vm.go
        - internal/node/controllers/vm_config.go
        - internal/node/controllers/coldgate.go
        - internal/vmm/redefine.go
-->

## OVERVIEW

Seven resource reconcilers run behind the node controller-manager: StoragePool, Image, Volume, Network, NIC, VM, and Snapshot. They translate routed API objects into node-local storage/network/vmm actions and report status back to the control plane.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Shared deletion helpers | `teardown.go` | `isDeleting` and `removeTeardownFinalizer` drive finalizer-based teardown |
| Cold gate | `coldgate.go` | `vmIsCold` allows only `Defined`/`Stopped` or missing runtime; `Failed` is not cold |
| StoragePool reconciler | `storagepool.go` | register/unregister pool; patch usage/phase |
| Image reconciler | `image.go` | fetch local/HTTP source into file pool; delete image on teardown |
| Volume reconciler | `volume.go`, `volume_resize.go` | create root/data volumes; cold offline resize convergence |
| Network/NIC reconcilers | `network.go`, `nic.go` | register/ensure/delete host network primitives |
| VM reconciler | `vm.go`, `vm_power.go`, `vm_config.go` | create/start/stop/kill/delete and cold config redefine |
| Snapshot reconciler | `snapshot.go` | whole-VM cold qcow2 internal snapshot fan-out and delete drain |

## CONVENTIONS

- Controllers react to level-triggered object state; `EventDeleted` is normally a no-op because deletion intent is represented by `metadata.deletionTimestamp`.
- Every controller keeps a narrow service interface for tests; do not pass concrete storage/network/vmm implementations into tests.
- `patchStatus` no-op guards prevent status feedback loops; desired status is compared before PATCH.
- Cold operations gate on live vmm state, not the VM object's projected status. Runtime absence is cold; `PhaseFailed` is not cold because intent may still be running.
- Teardown removes the node-teardown finalizer only after node-local resources are drained or proven already gone.

## ANTI-PATTERNS

- Do not perform snapshot, resize, or config redefine while a VM process is live.
- Do not treat `DELETED` watch events as the teardown signal; teardown comes from ordinary ADDED/MODIFIED objects with `deletionTimestamp`.
- Do not drop finalizers after a partial teardown failure; requeue and preserve the failure through `%w` / `errors.Join`.
- Do not auto-start a `Failed` VM just to complete cold operations; failed means intent may still be running and the controller must re-evaluate power flow.

## CALL GRAPHS & DATA FLOW (LOCAL)

### Flow: finalizer-driven teardown {#flow-node-finalizer-teardown}

- Entry from root flow: `internal/node/controller/loop.go:115 (reconcileLoop)` dispatches an object carrying `metadata.deletionTimestamp`
- Local chain:
  1. `internal/node/controllers/teardown.go:77 (isDeleting)` — common deletion predicate used by all resource reconcilers
  2. `internal/node/controllers/storagepool.go:109 (StoragePoolController.Reconcile)` / `image.go:118` / `volume.go:130` / `network.go:118` / `nic.go:129` / `vm.go:130` / `snapshot.go:110` — branch to resource-specific teardown
  3. `internal/node/controllers/teardown.go:91 (removeTeardownFinalizer)` — call client after local drain succeeds
  4. `internal/node/client/client.go:146 (Client.RemoveFinalizer)` — PATCH `/apis/{kind}/{name}/finalizers`
- Data: routed API object → resource-specific delete request(s) → finalizer patch body
- Side effects: local storage/network/vmm cleanup; apiserver finalizer removal
- Exit / next hop: `internal/controlplane/apiserver/handler_finalizers.go:94 (Server.patchFinalizers)` [详见 `../../controlplane/apiserver/AGENTS.md#flow-apiserver-delete-finalizers`]

### Flow: Snapshot cold lifecycle {#flow-node-snapshot-cold-lifecycle}

- Entry from root flow: `internal/node/agent.go:129 (NewSnapshotController)` and `internal/node/controllers/snapshot.go:72 (SnapshotController.Reconcile)`
- Local chain:
  1. `internal/node/controllers/snapshot.go:89 (SnapshotController.Reconcile → targetVM)` — read target VM object by `Snapshot.spec.vmRef`
  2. `internal/node/controllers/snapshot.go:303 (SnapshotController.vmIsCold)` → `coldgate.go:18 (vmIsCold)` — live vmm cold gate
  3. `internal/node/controllers/snapshot.go:119 (reconcileCreate)` — for each VM volumeRef resolve volume target and call `SnapshotVolume`
  4. `internal/node/controllers/snapshot.go:160 (failFanOut)` — rollback already-created disk snapshots on mid-fan-out failure
  5. `internal/node/controllers/snapshot.go:195 (reconcileDelete)` — cold-gated delete path, missing volume is already drained, finalizer removed after all disks converge
- Data: `snapshotv1.Snapshot` + target `vmv1.VM` → per-volume `storage.SnapshotVolumeRequest` / `DeleteVolumeSnapshotRequest` → `SnapshotStatus.DiskSnapshots`
- Side effects: qcow2 internal snapshots via storage/qemu-img; status PATCH; finalizer PATCH on delete completion
- Exit / next hop: `internal/storage/service.go:268 (VolumeService.SnapshotVolume)` / `:291 (DeleteVolumeSnapshot)` [详见 `../../storage/AGENTS.md#flow-storage-snapshot`]

### Flow: Volume cold resize {#flow-node-volume-cold-resize}

- Entry from root flow: `internal/node/controllers/volume.go:94 (VolumeController.Reconcile)` for a ready Volume whose `spec.capacityBytes` is the absolute desired size
- Local chain:
  1. `internal/node/controllers/volume_resize.go:28 (VolumeController.reconcileResize)` — read owning VM object
  2. `internal/node/controllers/volume_resize.go:47 (reconcileResize → vmIsCold)` — live cold gate; non-cold requeues without status failure
  3. `internal/node/controllers/volume_resize.go:59 (reconcileResize → ResizeVolume)` — pass absolute target capacity to storage
  4. `internal/node/controllers/volume_resize.go:64 (reconcileResize)` — failure keeps phase Ready and requeues; resize failure does not invalidate existing usable volume
- Data: `volumev1.Volume.Spec.CapacityBytes` → `storage.ResizeVolumeRequest` → live qcow2 size convergence
- Side effects: qemu-img info/resize through storage; no status phase flip on failure
- Exit / next hop: `internal/storage/service.go:242 (VolumeService.ResizeVolume)` [详见 `../../storage/AGENTS.md#flow-storage-resize`]

### Flow: VM cold config change {#flow-vm-cold-config-change}

- Entry from root flow: `internal/node/controllers/vm.go:281 (VMController.reconcileExistingVMOff)` after observed power is off
- Local chain:
  1. `internal/node/controllers/vm_config.go:53 (VMController.reconcileConfigDrift)` — gather current Volume/NIC dependencies
  2. `internal/node/controllers/vm_config.go:68 (buildSpecSummary)` — derive desired vmm spec from API object + dependencies
  3. `internal/node/controllers/vm_config.go:36 (specDrifted)` — compare desired with live persisted `vmm.SpecSummary`
  4. `internal/node/controllers/vm_config.go:74 (reconcileConfigDrift → vmm.Redefine)` — rewrite vm.json spec + argv only when drift exists
  5. `internal/node/controllers/vm_config.go:95 (reconcileConfigDrift)` — patch live power status after pure disk redefine
- Data: cold `vmv1.VM` → `vmm.SpecSummary` → persisted `vm.json` + argv snapshot
- Side effects: vmm state file rewrite only; no QEMU process mutation
- Exit / next hop: `internal/vmm/redefine.go:25 (VMMService.Redefine)` [详见 `../../vmm/AGENTS.md#flow-vmm-redefine`]

## NOTES

- Focused tests: `go test -count=1 ./internal/node/controllers/...` plus `go test -count=1 ./internal/node/...` when controller-manager behavior is touched.
- Cold operations are also covered in `test/e2e/closure_test.go` via snapshot, cold resize, and cold config change cycles.
- Evidence: AFT outlines/zooms + read-only flow subagent. `[已验证]` 源码与测试符号；`[降级: LSP call hierarchy]`.
