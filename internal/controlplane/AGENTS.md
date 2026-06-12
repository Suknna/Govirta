# internal/controlplane Knowledge Base

<!--
Verified-against:
  base_commit: dfad16b
  files:
    - internal/controlplane/service.go
    - internal/controlplane/service_test.go
    - internal/controlplane/apiserver/server.go
    - internal/controlplane/apiserver/handler_apply.go
    - internal/controlplane/apiserver/handler_replace.go
    - internal/controlplane/apiserver/handler_delete.go
    - internal/controlplane/apiserver/handler_finalizers.go
    - internal/controlplane/apiserver/handler_get.go
    - internal/controlplane/apiserver/handler_watch.go
    - internal/controlplane/apiserver/handler_status.go
    - internal/controlplane/apiserver/apply_admission.go
    - internal/controlplane/apiserver/admission/registry.go
    - internal/controlplane/apiserver/admission/delete.go
    - internal/controlplane/apiserver/admission/fields.go
    - internal/controlplane/apiserver/admission/finalizers.go
    - internal/controlplane/apiserver/admission/references.go
    - internal/controlplane/store/store.go
    - internal/controlplane/store/contract.go
    - internal/controlplane/store/etcd/etcd.go
    - internal/controlplane/store/fake/fake.go
    - internal/controlplane/mac/allocator.go
    - internal/controlplane/mac/pool.go
    - internal/controlplane/scheduler/scheduler.go
    - cmd/govirtad/main.go
    - pkg/apis/meta/v1alpha1/types.go
    - pkg/apis/vm/v1alpha1/types.go
    - pkg/apis/nic/v1alpha1/types.go
    - pkg/apis/network/v1alpha1/types.go
    - pkg/apis/storagepool/v1alpha1/types.go
    - pkg/apis/image/v1alpha1/types.go
    - pkg/apis/volume/v1alpha1/types.go
    - pkg/apis/snapshot/v1alpha1/types.go
  flows:
    - anchor: flow-controlplane-boot
      sources:
        - cmd/govirtad/main.go
        - internal/controlplane/service.go
        - internal/controlplane/apiserver/server.go
        - internal/controlplane/store/etcd/etcd.go
    - anchor: flow-apiserver-apply
      sources:
        - internal/controlplane/apiserver/server.go
        - internal/controlplane/apiserver/handler_apply.go
        - internal/controlplane/apiserver/apply_admission.go
        - internal/controlplane/apiserver/admission/registry.go
        - internal/controlplane/store/store.go
        - internal/controlplane/mac/allocator.go
        - internal/controlplane/scheduler/scheduler.go
    - anchor: flow-apiserver-replace
      sources:
        - internal/controlplane/apiserver/server.go
        - internal/controlplane/apiserver/handler_replace.go
        - internal/controlplane/apiserver/admission/registry.go
        - internal/controlplane/store/store.go
        - internal/controlplane/store/etcd/etcd.go
    - anchor: flow-apiserver-delete-finalizers
      sources:
        - internal/controlplane/apiserver/server.go
        - internal/controlplane/apiserver/handler_delete.go
        - internal/controlplane/apiserver/handler_finalizers.go
        - internal/controlplane/apiserver/admission/delete.go
        - internal/controlplane/apiserver/admission/finalizers.go
        - internal/controlplane/store/store.go
    - anchor: flow-apiserver-apply-snapshot
      sources:
        - internal/controlplane/apiserver/handler_apply.go
        - internal/controlplane/apiserver/handler_replace.go
        - internal/controlplane/apiserver/admission/references.go
        - pkg/apis/snapshot/v1alpha1/types.go
    - anchor: flow-apiserver-watch
      sources:
        - internal/controlplane/apiserver/server.go
        - internal/controlplane/apiserver/handler_watch.go
        - internal/controlplane/store/etcd/etcd.go
    - anchor: flow-apiserver-status
      sources:
        - internal/controlplane/apiserver/handler_status.go
        - internal/controlplane/store/store.go
-->

## OVERVIEW

Control plane composition root: assembles etcd-backed store, MAC allocator, scheduler, and HTTP apiserver into a single `Service`. The apiserver exposes a Kubernetes-style REST surface (`/apis/{kind}/{name}`) for 7 resource kinds with apply/replace/get/list/watch/status/delete/finalizer operations.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Composition root | `service.go` | `NewService(ctx, Config)` dials etcd, assembles all deps; `newServiceWithStore` is test seam |
| HTTP server + routing | `apiserver/server.go` | `Server.Handler()` registers all routes; `Server.Run` binds TCP; internals详见 `apiserver/AGENTS.md` |
| Apply handler (unconditional create/update) | `apiserver/handler_apply.go` | kind-dispatched decode→validate→admit→store pipeline for POST apply |
| Replace handler (guarded update) | `apiserver/handler_replace.go` | PUT replace requires body `metadata.resourceVersion` and CASes through store.Put |
| Delete/finalizers | `apiserver/handler_delete.go`, `apiserver/handler_finalizers.go` | DELETE stamps deletionTimestamp; finalizers subresource performs real delete |
| Admission chains | `apiserver/admission/` | apply/replace/status/delete/finalizers validators and reference policy |
| Get/List handler | `apiserver/handler_get.go` | `Get` single object + `List` by prefix |
| Watch handler (streaming) | `apiserver/handler_watch.go` | chunked NDJSON, nodeName filter, list-then-watch + resume |
| Status patch handler | `apiserver/handler_status.go` | read-modify-write CAS with retry (max 3) |
| Store interface | `store/store.go` | `Put`/`Get`/`List`/`Delete`/`DeleteIfVersion`/`Watch`/`Close` + sentinel errors |
| Store behavior contract | `store/contract.go` | `RunStoreContract` reusable test suite |
| etcd implementation | `store/etcd/etcd.go` | `clientv3`-backed, linearizable get, list-then-watch |
| Fake implementation | `store/fake/fake.go` | in-memory, monotonic revision, watcher fan-out |
| MAC allocator | `mac/allocator.go` | `MACAllocator` interface; `etcdAllocator` list-occupancy→pick-free→commit |
| MAC pool | `mac/pool.go` | 3-byte OUI prefix + suffix interval; `Candidates()` iterator |
| Scheduler | `scheduler/scheduler.go` | `Scheduler` interface; `NoopScheduler` picks first node |
| Flag parsing | `cmd/govirtad/main.go` | `parseConfig` → `controlplane.Config`; all fields explicit flags |

## CONVENTIONS

- All API objects use `pkg/apis/*` typed structs. The store is kind-agnostic: it stores raw JSON bytes keyed by `/govirta/{Kind}/{Name}`.
- `Config` has no hidden defaults; every field is an explicit flag in `cmd/govirtad/main.go`.
- MAC allocation is atomic: `WithAllocation` holds a mutex across the entire list→pick→commit flow.
- `store.Store` is the swappable boundary: etcd and fake implement the same `RunStoreContract` behavior suite.
- Watch uses list-then-watch (startRevision="") for initial connect, resume (startRevision=N) for reconnection.
- GET single-object responses inject `metadata.resourceVersion` into the response body; the store remains raw JSON and is not mutated by GET.
- PUT replace is update-only optimistic concurrency: the request body must carry `metadata.resourceVersion`; stale versions return 409.
- Status PATCH uses read-modify-write CAS with max 3 retries; 409 on exhaustion.
- `newServiceWithStore(st, cfg)` is the test seam that bypasses etcd dial.

## ANTI-PATTERNS

- Do not introduce non-etcd persistent stores for control-plane data (etcd-only, permanently).
- Do not add hidden defaults to `Config`; all behavior-affecting fields must be explicit.
- Do not parse resource types inside the store layer; kind dispatch belongs in the apiserver.
- Do not let the store generate or infer resource names, MACs, or node assignments.

## CALL GRAPHS & DATA FLOW (LOCAL)

### Flow: control plane boot {#flow-controlplane-boot}

- Entry from root flow: `cmd/govirtad/main.go:34 (main)` → `internal/controlplane/service.go:71 (NewService)`
- Local chain:
  1. `cmd/govirtad/main.go:38 (parseConfig)` — flag parsing → `controlplane.Config`
  2. `internal/controlplane/service.go:72 (NewService → etcd.New)` — dial etcd cluster
  3. `internal/controlplane/service.go:80 (newServiceWithStore)` — assemble mac pool/allocator, scheduler, apiserver
  4. `internal/controlplane/apiserver/server.go:86 (Server.Run)` — `net.Listen` → `serve(ctx, ln)`
  5. `internal/controlplane/apiserver/server.go:104 (serve)` — `http.Server.Serve` + graceful shutdown on ctx cancel
- Data: `Config` → `*etcd.Store` + `*mac.Pool` + `*etcdAllocator` + `*NoopScheduler` + `*apiserver.Server`
- Side effects: etcd gRPC connection, TCP listener
- Exit / next hop: apiserver HTTP accept loop; `store.Close()` on shutdown

### Flow: apply pipeline {#flow-apiserver-apply}

- Entry: `internal/controlplane/apiserver/handler_apply.go:47 (Server.Apply)` — POST `/apis/{kind}/{name}` [详见 `apiserver/AGENTS.md#flow-apiserver-apply`]
- Local chain:
  1. `internal/controlplane/apiserver/handler_apply.go:68 (Server.apply)` — kind dispatch for StoragePool/Image/Volume/Network/NIC/VM/Snapshot
  2. `internal/controlplane/apiserver/handler_apply.go:199 (decodeAndAdmitApply)` — decode typed object and run admission
  3. `internal/controlplane/apiserver/handler_apply.go:318 (applyNIC)` — empty MAC → `alloc.WithAllocation`
  4. `internal/controlplane/apiserver/handler_apply.go:418 (bindVM)` — no explicit node → `sched.Schedule(ctx, vm, nodeNames)`
  5. `internal/controlplane/apiserver/handler_apply.go:383 (applySnapshot)` — Snapshot nodeName derived from target VM placement
  6. `internal/controlplane/apiserver/handler_apply.go:433 (put)` — `json.Marshal` → `store.Put(ctx, key, data, "")`
- Data: HTTP body `[]byte` → typed API object → `json.Marshal` → etcd key-value
- Side effects: etcd write; MAC allocation (NIC); node binding (VM/Snapshot)
- Exit / next hop: HTTP 201 + JSON body with `ResourceVersion`

### Flow: replace pipeline {#flow-apiserver-replace}

- Entry: `internal/controlplane/apiserver/handler_replace.go:26 (Server.Replace)` — PUT `/apis/{kind}/{name}` [详见 `apiserver/AGENTS.md#flow-apiserver-replace`]
- Local chain:
  1. `internal/controlplane/apiserver/handler_replace.go:45 (Server.replace)` — decode incoming object and require `metadata.resourceVersion`.
  2. `internal/controlplane/apiserver/handler_replace.go:128 (decodeAndAdmitReplace)` — `store.Get` old object; missing object returns HTTP 404 because replace is update-only.
  3. `internal/controlplane/apiserver/handler_replace.go:152 (decodeAndAdmitReplace)` — run replace admission with old/new objects, preserving server-owned metadata and status.
  4. `internal/controlplane/apiserver/handler_replace.go:175 (putReplaceResponse)` — `store.Put(ctx, key, data, expectedVersion)` CAS using submitted resourceVersion.
- Data: editable GET response JSON → typed API object → admission → CAS JSON write.
- Side effects: etcd conditional write only; no create-on-missing behavior.
- Exit / next hop: HTTP 200 + stored object body with new `metadata.resourceVersion`; stale RV returns 409.

### Flow: delete + finalizers {#flow-apiserver-delete-finalizers}

- Entry: `internal/controlplane/apiserver/handler_delete.go:48 (Server.Delete)` / `internal/controlplane/apiserver/handler_finalizers.go:54 (Server.PatchFinalizers)` [详见 `apiserver/AGENTS.md#flow-apiserver-delete-finalizers`]
- Local chain:
  1. `internal/controlplane/apiserver/handler_delete.go:73 (Server.delete)` — read object, reverse-reference guard, stamp `deletionTimestamp`, CAS write.
  2. `internal/controlplane/apiserver/handler_finalizers.go:94 (Server.patchFinalizers)` — validate finalizer patch and target deleting state.
  3. `internal/controlplane/apiserver/handler_finalizers.go:151 (Server.patchFinalizers)` — last finalizer + deletionTimestamp → `store.DeleteIfVersion`.
- Data: stored object bytes → metadata-only patch object → deleting object or tombstone delete.
- Side effects: etcd CAS write and conditional delete.
- Exit / next hop: node controllers remove finalizers after local teardown [详见 `../node/controllers/AGENTS.md#flow-node-finalizer-teardown`]

### Flow: Snapshot admission placement {#flow-apiserver-apply-snapshot}

- Entry: `internal/controlplane/apiserver/handler_apply.go:177 (KindSnapshot)` / `internal/controlplane/apiserver/handler_replace.go:102 (Snapshot)` [详见 `apiserver/AGENTS.md#flow-apiserver-apply-snapshot`]
- Local chain:
  1. `internal/controlplane/apiserver/admission/registry.go:13 (PreApplyChain)` / `:32 (PreReplaceChain)` — `ReferenceValidator` proves target VM exists.
  2. `internal/controlplane/apiserver/handler_apply.go:383 (applySnapshot)` — resolve target VM nodeName.
  3. `internal/controlplane/apiserver/handler_replace.go:107 (Server.replace)` — replace path re-resolves nodeName instead of trusting caller bytes.
- Data: `snapshotv1.Snapshot{Spec.VMRef}` → target VM metadata → Snapshot routed to target VM node.
- Side effects: etcd write of derived `metadata.nodeName`; no qcow2 operation in control plane.
- Exit / next hop: Snapshot routed to node [详见 `../node/controllers/AGENTS.md#flow-node-snapshot-cold-lifecycle`]

### Flow: watch pipeline {#flow-apiserver-watch}

- Entry: `internal/controlplane/apiserver/handler_watch.go:60 (Server.Watch)` — GET `/apis/{kind}?watch=true&nodeName=X`
- Local chain:
  1. `:68` — validate `nodeName` query param (missing → 400)
  2. `:89` — `store.Watch(ctx, prefix, startRevision)` → `<-chan WatchEvent`
  3. `:101` — commit HTTP 200 + flush headers
  4. `:106` — event loop: `nodeNameMatches` filter → `writeWatchEvent` (NDJSON) → flush
- Data: etcd watch events → `store.WatchEvent` → filtered NDJSON stream
- Side effects: etcd watch goroutine (in store impl)
- Exit / next hop: HTTP chunked response; ctx cancel closes stream

### Flow: status patch {#flow-apiserver-status}

- Entry: `internal/controlplane/apiserver/handler_status.go:42 (Server.PatchStatus)` — PATCH `/apis/{kind}/{name}/status`
- Local chain:
  1. `:77` — read + validate JSON body
  2. `:89-105` — retry loop (max 3): `store.Get` → `mergeStatus` (replace "status" key) → `store.Put` with CAS
  3. `ErrRevisionConflict` → retry; exhaustion → HTTP 409
- Data: status JSON body → merged object JSON → etcd CAS write
- Side effects: etcd read + write
- Exit / next hop: HTTP 200 + merged JSON

## NOTES

- The old `internal/apiserver/server.go` (noop skeleton) still exists but is no longer wired into `controlplane.NewService`. The real apiserver lives in `internal/controlplane/apiserver/`.
- `store/etcd` integration tests require `GOVIRTA_ETCD_ENDPOINTS` env var.
- `mac.Pool.Candidates()` returns `iter.Seq[net.HardwareAddr]`; it is a pure iterator, not a slice allocation.
- The apiserver keeps stored values as raw bytes. Single-object GET injects `metadata.resourceVersion` into the response body plus `X-Resource-Version` header for get→edit→replace; List/Watch still stream stored raw object bytes.
