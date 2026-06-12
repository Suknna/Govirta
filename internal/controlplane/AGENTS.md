# internal/controlplane Knowledge Base

<!--
Verified-against:
  base_commit: 8778cb4
  files:
    - internal/controlplane/service.go
    - internal/controlplane/service_test.go
    - internal/controlplane/apiserver/server.go
    - internal/controlplane/apiserver/handler_apply.go
    - internal/controlplane/apiserver/handler_get.go
    - internal/controlplane/apiserver/handler_watch.go
    - internal/controlplane/apiserver/handler_status.go
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
        - internal/controlplane/store/store.go
        - internal/controlplane/mac/allocator.go
        - internal/controlplane/scheduler/scheduler.go
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

Control plane composition root: assembles etcd-backed store, MAC allocator, scheduler, and HTTP apiserver into a single `Service`. The apiserver exposes a Kubernetes-style REST surface (`/apis/{kind}/{name}`) for 7 resource kinds with apply/replace/get/list/watch/status/finalizer operations.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Composition root | `service.go` | `NewService(ctx, Config)` dials etcd, assembles all deps; `newServiceWithStore` is test seam |
| HTTP server + routing | `apiserver/server.go` | `Server.Handler()` registers all routes; `Server.Run` binds TCP |
| Apply handler (unconditional create/update) | `apiserver/handler_apply.go` | kind-dispatched decode→validate→admit→store pipeline for POST apply |
| Replace handler (guarded update) | `apiserver/handler_replace.go` | PUT replace requires body `metadata.resourceVersion` and CASes through store.Put |
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

- Entry: `internal/controlplane/apiserver/handler_apply.go:60 (Server.Apply)` — POST `/apis/{kind}/{name}`
- Local chain:
  1. `:81 (Server.apply)` — kind dispatch: StoragePool/Image/Volume/Network/NIC/VM
  2. `:91-185` — `json.Unmarshal` → typed object → `validateObject(meta, spec)` → `requireName`
  3. `:167 (KindNIC → applyNIC)` — empty MAC → `alloc.WithAllocation` (list NICs → pick free → commit)
  4. `:185 (KindVM → bindVM)` — no explicit node → `sched.Schedule(ctx, vm, nodeNames)`
  5. `:273 (Server.put)` — `json.Marshal` → `store.Put(ctx, key, data, "")`
- Data: HTTP body `[]byte` → typed API object → `json.Marshal` → etcd key-value
- Side effects: etcd write; MAC allocation (NIC); node binding (VM)
- Exit / next hop: HTTP 201 + JSON body with `ResourceVersion`

### Flow: replace pipeline {#flow-apiserver-replace}

- Entry: `internal/controlplane/apiserver/handler_replace.go:24 (Server.Replace)` — PUT `/apis/{kind}/{name}`
- Local chain:
  1. Decode incoming object and require `metadata.resourceVersion`.
  2. `store.Get` old object; missing object returns HTTP 404 because replace is update-only.
  3. Run replace admission with old/new objects, preserving server-owned metadata and status.
  4. `store.Put(ctx, key, data, expectedVersion)` — CAS using submitted resourceVersion.
- Data: editable GET response JSON → typed API object → admission → CAS JSON write.
- Side effects: etcd conditional write only; no create-on-missing behavior.
- Exit / next hop: HTTP 200 + stored object body with new `metadata.resourceVersion`; stale RV returns 409.

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
