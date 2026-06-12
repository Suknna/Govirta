# internal/controlplane/apiserver Knowledge Base

<!--
Verified-against:
  base_commit: dfad16b
  files:
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
    - internal/controlplane/apiserver/admission/apply.go
    - internal/controlplane/apiserver/admission/delete.go
    - internal/controlplane/apiserver/admission/fields.go
    - internal/controlplane/apiserver/admission/finalizers.go
    - internal/controlplane/apiserver/admission/references.go
    - internal/controlplane/apiserver/admission/status.go
    - internal/controlplane/store/store.go
    - internal/controlplane/store/etcd/etcd.go
    - pkg/apis/meta/v1alpha1/types.go
    - pkg/apis/snapshot/v1alpha1/types.go
  flows:
    - anchor: flow-apiserver-apply
      sources:
        - internal/controlplane/apiserver/server.go
        - internal/controlplane/apiserver/handler_apply.go
        - internal/controlplane/apiserver/apply_admission.go
        - internal/controlplane/apiserver/admission/registry.go
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
        - internal/controlplane/store/etcd/etcd.go
    - anchor: flow-apiserver-apply-snapshot
      sources:
        - internal/controlplane/apiserver/handler_apply.go
        - internal/controlplane/apiserver/handler_replace.go
        - internal/controlplane/apiserver/admission/references.go
        - pkg/apis/meta/v1alpha1/types.go
        - pkg/apis/snapshot/v1alpha1/types.go
    - anchor: flow-apiserver-watch
      sources:
        - internal/controlplane/apiserver/handler_watch.go
        - internal/controlplane/store/etcd/etcd.go
    - anchor: flow-apiserver-status
      sources:
        - internal/controlplane/apiserver/handler_status.go
        - internal/controlplane/apiserver/admission/status.go
        - internal/controlplane/store/store.go
-->

## OVERVIEW

Control-plane HTTP API surface. It owns kind dispatch, admission chains, optimistic concurrency, deletion/finalizer state transitions, status patches, and watch/list transport for the 7 API resources.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Route registration | `server.go:67` | `Handler()` registers POST apply, PUT replace, GET/list/watch, PATCH status/finalizers, DELETE |
| Apply write path | `handler_apply.go:47` | unconditional create/update with admission, finalizer injection, NIC MAC allocation, VM/Snapshot node binding |
| Replace write path | `handler_replace.go:26` | update-only full-object replacement requiring submitted `metadata.resourceVersion` |
| Delete entry | `handler_delete.go:48` | two-phase delete entry: reverse-reference guard, stamp `deletionTimestamp`, CAS write, 202 |
| Finalizer exit | `handler_finalizers.go:54` | node teardown finalizer removal; last finalizer + deletion timestamp performs real `DeleteIfVersion` |
| Admission registry | `admission/registry.go` | named validator chains for apply/replace/delete/status/finalizers |
| Field policy | `admission/fields.go` | immutable fields plus cold-mutable VM/Volume gates |
| Reverse references | `admission/delete.go` | prevents deleting objects still referenced by dependents; VM is blocked by Snapshot refs |
| Status patch | `handler_status.go:38` | bare-status patch, CAS retry, lifecycle recheck |
| Watch/list | `handler_watch.go:35` / `handler_get.go:86` | list-then-watch, nodeName routing, NDJSON stream |

## CONVENTIONS

- POST apply is unconditional create/update; PUT replace is update-only CAS and requires `metadata.resourceVersion` in the submitted object.
- Server-owned metadata is preserved on update/replace; `metadata.uid`, `nodeName`, `deletionTimestamp`, and `finalizers` are not caller-controlled except via the finalizers subresource.
- Delete is two-phase: DELETE only stamps `deletionTimestamp`; actual removal happens only when `PATCH .../finalizers` drains the finalizer list.
- Snapshot placement is derived from its target VM's `metadata.nodeName` on create and replace; callers do not choose a Snapshot node.
- Admission errors are mapped to uniform API errors through `admissionToAPIError`; plain validator failures become internal errors, not silent success.

## ANTI-PATTERNS

- Do not create objects through PUT replace; missing target returns 404 by design.
- Do not mutate spec/status in delete/finalizer metadata patch helpers; they intentionally decode only metadata and pass object bytes through.
- Do not let finalizer removal bypass CAS; concurrent apply/status/delete writes must surface as 409.
- Do not route node teardown from `DELETED` watch events; node controllers act on ordinary objects carrying `deletionTimestamp`.

## CALL GRAPHS & DATA FLOW (LOCAL)

### Flow: apply pipeline {#flow-apiserver-apply}

- Entry from root flow: `internal/controlplane/apiserver/server.go:69 (Handler)` → `internal/controlplane/apiserver/handler_apply.go:47 (Server.Apply)`
- Local chain:
  1. `internal/controlplane/apiserver/handler_apply.go:68 (Server.apply)` — read body, switch on `metav1.Kind`
  2. `internal/controlplane/apiserver/handler_apply.go:199 (decodeAndAdmitApply)` — decode typed object, load old object if any, run `admission.PreApplyChain`
  3. `internal/controlplane/apiserver/handler_apply.go:314 (applyNIC)` — empty NIC MAC enters `MACAllocator.WithAllocation`; provided MAC is preserved
  4. `internal/controlplane/apiserver/handler_apply.go:411 (bindVM)` — empty VM nodeName goes through scheduler
  5. `internal/controlplane/apiserver/handler_apply.go:433 (put)` / `:448 (putWithPostAdmission)` — marshal, post-admission, `store.Put(..., "")`
- Data: HTTP JSON → typed API object → admitted object JSON → `store.RawObject` with resourceVersion
- Side effects: etcd unconditional put; MAC allocation; VM node binding; node-teardown finalizer injection
- Exit / next hop: store watch event flows to node [详见 `../AGENTS.md#flow-apiserver-watch`]

### Flow: replace pipeline {#flow-apiserver-replace}

- Entry from root flow: `internal/controlplane/apiserver/server.go:70 (Handler)` → `internal/controlplane/apiserver/handler_replace.go:26 (Server.Replace)`
- Local chain:
  1. `internal/controlplane/apiserver/handler_replace.go:45 (Server.replace)` — read body and delegate decode/admission
  2. `internal/controlplane/apiserver/handler_replace.go:118 (decodeAndAdmitReplace)` — decode submitted object, `store.Get` existing object, run `admission.PreReplaceChain`
  3. `internal/controlplane/apiserver/handler_replace.go:159 (preserveUpdateStatus)` — keep existing status and server-owned lifecycle metadata
  4. `internal/controlplane/apiserver/handler_replace.go:167 (putReplaceResponse)` — `store.Put(ctx, key, data, expectedVersion)` CAS
- Data: GET/edit object JSON → typed object → admitted replacement JSON → conditional store write
- Side effects: etcd compare-and-swap; stale resourceVersion maps to HTTP 409
- Exit / next hop: node watch sees MODIFIED event through `#flow-apiserver-watch`

### Flow: delete + finalizers {#flow-apiserver-delete-finalizers}

- Entry from root flow: `internal/controlplane/apiserver/server.go:79 (deleteHandler)` / `:85 (finalizersHandler)`
- Local chain:
  1. `internal/controlplane/apiserver/handler_delete.go:73 (Server.delete)` — read stored object, decode metadata only
  2. `internal/controlplane/apiserver/admission/registry.go:44 (DeleteChain)` → `admission/delete.go:112 (ReverseReferenceValidator.Validate)` — reject referenced objects before stamping
  3. `internal/controlplane/apiserver/handler_delete.go:117 (Server.delete)` — stamp UTC RFC3339 `deletionTimestamp`, ensure teardown finalizer, `store.Put(... raw.ResourceVersion)`
  4. `internal/controlplane/apiserver/handler_finalizers.go:94 (Server.patchFinalizers)` — validate body, get target, validate deletion precondition, remove whitelisted finalizer
  5. `internal/controlplane/apiserver/handler_finalizers.go:151 (Server.patchFinalizers)` — finalizer list empty + deletionTimestamp → `store.DeleteIfVersion`
- Data: stored object bytes → metadata-only patch object → deleting object JSON → finalizer-trimmed object or tombstone delete
- Side effects: etcd CAS write on delete intent; etcd conditional delete on finalizer drain
- Exit / next hop: node controllers observe `deletionTimestamp` [详见 `../../node/controllers/AGENTS.md#flow-node-finalizer-teardown`]

### Flow: Snapshot apply/replace {#flow-apiserver-apply-snapshot}

- Entry from root flow: `internal/controlplane/apiserver/handler_apply.go:177 (KindSnapshot)` / `internal/controlplane/apiserver/handler_replace.go:102 (Snapshot)`
- Local chain:
  1. `internal/controlplane/apiserver/admission/registry.go:13 (PreApplyChain)` / `:32 (PreReplaceChain)` — `ReferenceValidator` proves `Snapshot.spec.vmRef` exists and is not deleting
  2. `internal/controlplane/apiserver/handler_apply.go:383 (applySnapshot)` — resolve target VM placement
  3. `internal/controlplane/apiserver/handler_apply.go:392 (resolveVMNodeName)` — read VM object and return `metadata.nodeName`
  4. `internal/controlplane/apiserver/handler_replace.go:107 (Server.replace)` — replace path re-resolves nodeName instead of trusting caller bytes
- Data: `snapshotv1.Snapshot{Spec.VMRef}` → target VM metadata → Snapshot object with derived `NodeName`
- Side effects: etcd write of node-routed Snapshot object; no image bytes or qcow2 operations in control plane
- Exit / next hop: node watch routes Snapshot to target VM node [详见 `../../node/controllers/AGENTS.md#flow-node-snapshot-cold-lifecycle`]

### Flow: watch pipeline {#flow-apiserver-watch}

- Entry from root flow: `internal/controlplane/apiserver/handler_watch.go:46 (Server.Watch)`
- Local chain:
  1. `internal/controlplane/apiserver/handler_watch.go:46 (Server.Watch)` — require `nodeName`, choose start revision
  2. `internal/controlplane/store/etcd/etcd.go:82 (Store.Watch)` — list-then-watch for empty startRevision; resume via `WithRev` for non-empty cursor
  3. `internal/controlplane/apiserver/handler_watch.go:143 (nodeNameMatches)` — filter routed objects by `metadata.nodeName`
  4. `internal/controlplane/apiserver/handler_watch.go:158 (writeWatchEvent)` — stream NDJSON + flush
- Data: store events → nodeName-filtered `watchEventWire` → chunked NDJSON
- Side effects: long-lived HTTP stream; etcd watch channel
- Exit / next hop: `internal/node/client/watch.go:71 (WatchSource.Watch)` [详见 `../../node/AGENTS.md#flow-node-reconcile`]

### Flow: status patch {#flow-apiserver-status}

- Entry from root flow: `internal/controlplane/apiserver/handler_status.go:38 (Server.PatchStatus)`
- Local chain:
  1. `internal/controlplane/apiserver/admission/registry.go:48 (StatusPatchChain)` — validate bare status body and target lifecycle
  2. `internal/controlplane/apiserver/handler_status.go:66 (Server.patchStatus)` — read object, merge `status`, CAS write, retry on revision conflict
  3. `internal/controlplane/apiserver/handler_status.go:143 (mergeStatus)` — replace only the `status` key
- Data: bare status JSON → stored object JSON with replaced status → `store.RawObject`
- Side effects: etcd CAS write; 409 after retry exhaustion
- Exit / next hop: watch stream emits status MODIFIED events to nodes

## NOTES

- `internal/controlplane/AGENTS.md` owns composition (`NewService` + store/mac/scheduler wiring); this file owns HTTP/admission/write-path internals.
- Focused tests: `go test -count=1 ./internal/controlplane/apiserver/... ./internal/controlplane/store/...`.
- Evidence: AFT outlines/zooms + read-only exploration subagents. `[已验证]` 源码与测试符号；`[降级: LSP call hierarchy]`.
