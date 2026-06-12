# API Write Concurrency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split Govirta's write API into `POST apply` for static declarative manifests and `PUT replace` for resourceVersion-based optimistic concurrency, while closing the finalizer real-delete blind-delete window.

**Architecture:** Keep the store raw-byte and kind-agnostic. Add explicit conditional delete to the store boundary, inject `metadata.resourceVersion` into single-object GET responses, and add a `Replace` apiserver path that reuses existing decode/admission/server-owned-field preservation but writes with `store.Put(..., expectedVersion=<body RV>)`. `govirtctl apply` stays unchanged; `govirtctl replace` is the new get/edit/replace CAS workflow.

**Tech Stack:** Go 1.26, standard `net/http` ServeMux, etcd clientv3, existing fake store, existing apiserver validating admission, existing `govirtctl` JSON CLI, existing e2e harness.

---

## Context

- Spec: `docs/superpowers/specs/2026-06-12-api-write-concurrency-design.md`.
- Official reference: Kubernetes API Concepts, resource versions and update conflicts: `https://kubernetes.io/docs/reference/using-api/api-concepts/`.
- `POST /apis/{kind}/{name}` currently goes to `Server.Apply` and performs unconditional `store.Put(ctx, key, body, "")` after admission/mutation.
- `PUT /apis/{kind}/{name}` currently also routes to `Server.Apply`; this plan changes PUT to `Server.Replace`.
- Existing update preservation helpers:
  - `preserveUpdateStatus(req, obj)` in `internal/controlplane/apiserver/handler_apply.go`.
  - `preserveUpdateObjectMeta(req, meta)` and `preserveVMUpdateMetadata(oldObject, vm)` in `internal/controlplane/apiserver/apply_admission.go`.
- Existing admission issue to account for: `EnvelopeValidator` rejects `metadata.resourceVersion` for `OperationUpdate`; replace must introduce `OperationReplace` and allow body RV while still rejecting caller-owned `finalizers`/`deletionTimestamp`.
- `test/e2e/closure_test.go` is already 912 lines. Do **not** add the replace e2e scenario to that file. Create `test/e2e/replace_test.go` and reuse same-package helpers (`runCtl`, `requireEnv`, etc.).

## File Structure Forecast

- Modify `internal/controlplane/store/store.go`: extend `Store` with `DeleteIfVersion`.
- Modify `internal/controlplane/store/contract.go`: add conditional-delete contract cases.
- Modify `internal/controlplane/store/fake/fake.go`: implement `DeleteIfVersion` with the same watch-event behavior as `Delete` on successful deletion.
- Modify `internal/controlplane/store/etcd/etcd.go`: implement `DeleteIfVersion` with etcd Txn comparing `ModRevision`.
- Modify `internal/controlplane/apiserver/admission/request.go`, `apply.go`, `registry.go`: add `OperationReplace` and replace validators.
- Create `internal/controlplane/apiserver/handler_replace.go`: keep replace logic out of `handler_apply.go` (already 540 lines).
- Create or modify `internal/controlplane/apiserver/resource_version.go`: helper for injecting body `metadata.resourceVersion` into raw JSON; do not bloat `handler_get.go`.
- Modify `internal/controlplane/apiserver/handler_get.go`: call body injection for single GET.
- Modify `internal/controlplane/apiserver/handler_finalizers.go`: use `DeleteIfVersion` on real-delete branch.
- Modify `internal/controlplane/apiserver/server.go`: route POST to Apply and PUT to Replace.
- Modify `internal/govirtctl/client.go`, `command.go`: add `Replace` client method and `replace` command.
- Create `test/e2e/replace_test.go`: focused e2e replace scenario outside oversized `closure_test.go`.

No new source file is expected to approach 500 lines. Existing `closure_test.go` is already over the 800-line hard limit; this plan avoids growing it.

## Tasks

### Task 1: Store Conditional Delete Contract

**Files:**
- Modify: `internal/controlplane/store/store.go`
- Modify: `internal/controlplane/store/contract.go`
- Modify: `internal/controlplane/store/fake/fake.go`
- Modify: `internal/controlplane/store/etcd/etcd.go`
- Test: `internal/controlplane/store/fake/fake_test.go`
- Test: `internal/controlplane/store/etcd/etcd_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: every `store.Store` implementation supports explicit conditional delete.

Acceptance evidence:
- `go test ./internal/controlplane/store/fake/...` passes.
- With `GOVIRTA_ETCD_ENDPOINTS` set, `go test ./internal/controlplane/store/etcd/...` passes.
- Contract cases prove: matching version deletes; stale version returns `store.ErrRevisionConflict` and keeps object; missing key returns nil; empty expectedVersion returns `store.ErrRevisionConflict` and keeps object.

- [ ] **Step 2: Extend Store interface**

In `internal/controlplane/store/store.go`, change the interface to:

```go
type Store interface {
    Put(ctx context.Context, key string, value []byte, expectedVersion string) (RawObject, error)
    Get(ctx context.Context, key string) (RawObject, error)
    List(ctx context.Context, prefix string) ([]RawObject, error)
    Delete(ctx context.Context, key string) error
    // DeleteIfVersion removes key only when the currently stored ResourceVersion
    // equals expectedVersion. Missing keys are idempotent success. Empty or stale
    // expectedVersion returns ErrRevisionConflict and does not delete a present key.
    DeleteIfVersion(ctx context.Context, key string, expectedVersion string) error
    Watch(ctx context.Context, prefix string, startRevision string) (<-chan WatchEvent, error)
    Close() error
}
```

- [ ] **Step 3: Add shared contract tests**

In `internal/controlplane/store/contract.go`, add a `DeleteIfVersionCompareAndSwap` subtest:

```go
t.Run("DeleteIfVersionCompareAndSwap", func(t *testing.T) {
    t.Helper()
    s := newStore()
    defer s.Close()
    ctx := context.Background()

    first, err := s.Put(ctx, "/govirta/pod/a", []byte(`{"v":1}`), "")
    if err != nil {
        t.Fatalf("Put initial: unexpected error: %v", err)
    }

    if err := s.DeleteIfVersion(ctx, "/govirta/pod/a", ""); !errors.Is(err, ErrRevisionConflict) {
        t.Fatalf("DeleteIfVersion empty version: error = %v, want ErrRevisionConflict", err)
    }
    if _, err := s.Get(ctx, "/govirta/pod/a"); err != nil {
        t.Fatalf("Get after empty-version delete: unexpected error: %v", err)
    }

    if err := s.DeleteIfVersion(ctx, "/govirta/pod/a", "stale"); !errors.Is(err, ErrRevisionConflict) {
        t.Fatalf("DeleteIfVersion stale: error = %v, want ErrRevisionConflict", err)
    }
    if _, err := s.Get(ctx, "/govirta/pod/a"); err != nil {
        t.Fatalf("Get after stale delete: unexpected error: %v", err)
    }

    if err := s.DeleteIfVersion(ctx, "/govirta/pod/a", first.ResourceVersion); err != nil {
        t.Fatalf("DeleteIfVersion matching: unexpected error: %v", err)
    }
    if _, err := s.Get(ctx, "/govirta/pod/a"); !errors.Is(err, ErrNotFound) {
        t.Fatalf("Get after matching delete: error = %v, want ErrNotFound", err)
    }

    if err := s.DeleteIfVersion(ctx, "/govirta/pod/a", first.ResourceVersion); err != nil {
        t.Fatalf("DeleteIfVersion missing key must be idempotent, got %v", err)
    }
})
```

Add a watch assertion in the existing watch subtest or a new subtest proving successful `DeleteIfVersion` emits `EventDeleted` exactly like `Delete`.

- [ ] **Step 4: Implement fake store**

In `internal/controlplane/store/fake/fake.go`, add:

```go
func (s *Store) DeleteIfVersion(ctx context.Context, key string, expectedVersion string) error {
    if err := ctx.Err(); err != nil {
        return err
    }

    s.mu.Lock()
    defer s.mu.Unlock()

    if s.closed {
        return store.ErrClosed
    }

    existing, found := s.data[key]
    if !found {
        return nil
    }
    if expectedVersion == "" || existing.ResourceVersion != expectedVersion {
        return store.ErrRevisionConflict
    }

    delete(s.data, key)
    s.rev++
    s.broadcastLocked(store.WatchEvent{
        Type: store.EventDeleted,
        Object: store.RawObject{Key: key, ResourceVersion: strconv.FormatInt(s.rev, 10)},
    })
    return nil
}
```

- [ ] **Step 5: Implement etcd store**

In `internal/controlplane/store/etcd/etcd.go`, add a transaction-based implementation:

```go
func (s *Store) DeleteIfVersion(ctx context.Context, key string, expectedVersion string) error {
    rev, err := strconv.ParseInt(expectedVersion, 10, 64)
    if err != nil || rev == 0 {
        return store.ErrRevisionConflict
    }

    resp, err := s.cli.Txn(ctx).
        If(clientv3.Compare(clientv3.ModRevision(key), "=", rev)).
        Then(clientv3.OpDelete(key)).
        Else(clientv3.OpGet(key)).
        Commit()
    if err != nil {
        return fmt.Errorf("etcd: conditional delete %q: %w", key, err)
    }
    if resp.Succeeded {
        return nil
    }

    // Missing key is idempotent success. A present key with a different version
    // is a CAS conflict.
    getResp := resp.Responses[0].GetResponseRange()
    if getResp == nil || len(getResp.Kvs) == 0 {
        return nil
    }
    return store.ErrRevisionConflict
}
```

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test ./internal/controlplane/store/fake/...
```

Expected: PASS.

Run the etcd contract against the same local etcd endpoint used by previous store work:

```bash
GOVIRTA_ETCD_ENDPOINTS=http://127.0.0.1:2379 go test ./internal/controlplane/store/etcd/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/controlplane/store
git commit -m "feat(store): add conditional delete by resource version"
```

### Task 2: GET Body ResourceVersion Injection

**Files:**
- Create: `internal/controlplane/apiserver/resource_version.go`
- Modify: `internal/controlplane/apiserver/handler_get.go`
- Test: `internal/controlplane/apiserver/handler_get_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: single-object GET response body carries the same `metadata.resourceVersion` as `X-Resource-Version`.

Acceptance evidence:
- `go test ./internal/controlplane/apiserver -run 'TestGetHitReturnsStoredObject|TestGetBodyResourceVersionMatchesHeader'` passes.
- The stored raw object remains unchanged; RV injection is response-only.

- [ ] **Step 2: Add response helper**

Create `internal/controlplane/apiserver/resource_version.go`:

```go
package apiserver

import (
    "encoding/json"
    "fmt"
)

func withBodyResourceVersion(raw []byte, resourceVersion string) ([]byte, error) {
    var obj map[string]any
    if err := json.Unmarshal(raw, &obj); err != nil {
        return nil, fmt.Errorf("decode object for resourceVersion injection: %w", err)
    }
    meta, ok := obj["metadata"].(map[string]any)
    if !ok {
        return nil, fmt.Errorf("object metadata is missing or not an object")
    }
    meta["resourceVersion"] = resourceVersion
    out, err := json.Marshal(obj)
    if err != nil {
        return nil, fmt.Errorf("encode object with resourceVersion: %w", err)
    }
    return out, nil
}
```

- [ ] **Step 3: Use helper in GET**

In `handler_get.go`, after `raw, apiErr := s.get(...)` succeeds and before writing the body:

```go
body, err := withBodyResourceVersion(raw.Value, raw.ResourceVersion)
if err != nil {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusInternalServerError)
    if _, err := w.Write(errorBody(internalErr(fmt.Errorf("apiserver: get inject resourceVersion: %w", err)))); err != nil {
        zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write get resourceVersion error response")
    }
    return
}
```

Then write `body` instead of `raw.Value`.

- [ ] **Step 4: Update tests**

Update `TestGetHitReturnsStoredObject` comment and assertion. Add:

```go
if got.ResourceVersion == "" {
    t.Fatal("response body metadata.resourceVersion is empty")
}
if hv := rec.Header().Get(resourceVersionHeader); got.ResourceVersion != hv {
    t.Fatalf("body RV %q != header RV %q", got.ResourceVersion, hv)
}
```

Add a test that reads `storedRaw` after GET and asserts the stored JSON still has empty `metadata.resourceVersion`, proving injection is response-only.

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test ./internal/controlplane/apiserver -run TestGet -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/controlplane/apiserver/resource_version.go internal/controlplane/apiserver/handler_get.go internal/controlplane/apiserver/handler_get_test.go
git commit -m "feat(apiserver): include resourceVersion in get response bodies"
```

### Task 3: Replace Admission and Handler

**Files:**
- Modify: `internal/controlplane/apiserver/admission/request.go`
- Modify: `internal/controlplane/apiserver/admission/apply.go`
- Modify: `internal/controlplane/apiserver/admission/registry.go`
- Create: `internal/controlplane/apiserver/handler_replace.go`
- Modify: `internal/controlplane/apiserver/server.go`
- Test: `internal/controlplane/apiserver/handler_replace_test.go`
- Test: `internal/controlplane/apiserver/admission/apply_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `PUT /apis/{kind}/{name}` updates only existing objects with body `metadata.resourceVersion` matching current store version.

Acceptance evidence:
- Missing object → 404.
- Missing RV → 400.
- Stale RV → 409.
- Matching RV → 200 + body with new RV.
- status/finalizers/deletionTimestamp/nodeName are preserved.
- immutable/reference/cold-mutable validators still run.

- [ ] **Step 2: Add OperationReplace and validator behavior**

In `admission/request.go`, add:

```go
OperationReplace Operation = "Replace"
```

In `EnvelopeValidator.Validate`, add a `case OperationReplace` branch:

```go
case OperationReplace:
    oldMeta, err := Metadata(req.OldObject)
    if err != nil {
        return Reject(v.Name(), ReasonInternal, err)
    }
    if oldMeta.UID != meta.UID {
        return Reject(v.Name(), ReasonConflict, fmt.Errorf("uid is immutable: existing %q vs requested %q", oldMeta.UID, meta.UID))
    }
    if meta.ResourceVersion == "" {
        return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("resourceVersion is required for replace"))
    }
    if meta.DeletionTimestamp != "" {
        return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("deletionTimestamp is server-owned on replace"))
    }
    if len(meta.Finalizers) != 0 {
        return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("finalizers are server-owned on replace"))
    }
```

Update `ApplyOperationValidator` to allow `OperationReplace` with old object present, or create a new `ReplaceOperationValidator` with the same old-object requirement. Prefer a new validator to keep error messages specific.

- [ ] **Step 3: Add Replace chain**

In `admission/registry.go`, add:

```go
func PreReplaceChain(st StoreReader) Chain {
    return NewChain(
        EnvelopeValidator{},
        SpecValidator{},
        ReplaceOperationValidator{},
        VMPowerStateValidator{},
        FieldPolicyValidator{},
        ReferenceValidator{Store: st},
    )
}

func PostReplaceChain() Chain {
    return NewChain(NICFinalMACValidator{})
}
```

Update `VMPowerStateValidator` so its update-only nodeName check also applies to `OperationReplace`. Use:

```go
if (req.Operation != OperationUpdate && req.Operation != OperationReplace) || vm.NodeName == "" {
    return nil
}
```

Update any validator that currently treats only `OperationUpdate` as an old/new-object comparison to also treat `OperationReplace` that way. In particular:

- `VMPowerStateValidator` must use `(OperationUpdate || OperationReplace)` for VM nodeName immutability.
- `FieldPolicyValidator` must run immutable/cold-mutable comparisons for both `OperationUpdate` and `OperationReplace`.
- `ReferenceValidator` remains active for replace through `PreReplaceChain` and must reject references to missing or deleting targets exactly like apply update.

- [ ] **Step 4: Create handler_replace.go**

Implement `Replace` as a separate handler. The shape should mirror `Apply` but route-specific:

```go
func (s *Server) Replace(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    body, apiErr := s.replace(ctx, r)
    if apiErr != nil {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(apiErr.code)
        if _, err := w.Write(errorBody(apiErr)); err != nil {
            zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write replace error response")
        }
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    if _, err := w.Write(body); err != nil {
        zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write replace response")
    }
}
```

Core `replace` flow:

1. Decode body with `decodeObjectByKind(kind, body)`.
2. `store.Get(ctx, key)`; `ErrNotFound` → 404.
3. Decode `oldObj` with `decodeObjectByKind(kind, raw.Value)`.
4. Build `admission.Request{Operation: OperationReplace, Kind, Name, NewRaw: body, NewObject: obj, OldRaw: raw.Value, OldObject: oldObj}`.
5. Run `admission.PreReplaceChain(s.store).Validate(ctx, req)`.
6. Extract submitted RV from `obj` metadata before preservation:
   ```go
   submittedMeta, err := admission.Metadata(obj)
   expectedVersion := submittedMeta.ResourceVersion
   ```
7. Reuse existing preservation logic: `preserveUpdateStatus(req, obj)` then per-kind server-owned metadata preservation and mutation (same as update path), but ensure the final object's `ResourceVersion` field is not the submitted stale RV before marshal.
8. Run post replace validation.
9. `store.Put(ctx, key, encoded, expectedVersion)`; `store.ErrRevisionConflict` → 409.
10. Set response object's `ResourceVersion` to `raw.ResourceVersion` returned by store and marshal response.

Keep `handler_replace.go` self-contained enough to review independently. Extract a tiny helper only when both apply and replace need identical code and the helper preserves current `POST apply` behavior byte-for-byte; do not refactor unrelated apply logic.

- [ ] **Step 5: Update routing**

In `server.go`:

```go
mux.HandleFunc("POST /apis/{kind}/{name}", s.Apply)
mux.HandleFunc("PUT /apis/{kind}/{name}", s.Replace)
```

Update the comment that currently says POST and PUT share apply.

- [ ] **Step 6: Add handler tests**

Create `handler_replace_test.go` with focused tests:

```go
func doReplace(t *testing.T, srv *Server, kind metav1.Kind, name string, obj any) *httptest.ResponseRecorder {
    t.Helper()
    data, err := json.Marshal(obj)
    if err != nil { t.Fatalf("marshal replace object: %v", err) }
    req := httptest.NewRequest(http.MethodPut, "/apis/"+string(kind)+"/"+name, bytes.NewReader(data))
    rec := httptest.NewRecorder()
    srv.Handler().ServeHTTP(rec, req)
    return rec
}
```

Test cases:
- `TestReplaceMissingObjectReturns404`.
- `TestReplaceMissingResourceVersionReturns400`.
- `TestReplaceStaleResourceVersionReturns409`.
- `TestReplaceMatchingResourceVersionUpdatesSpecAndBumpsRV`.
- `TestReplacePreservesStatusAndFinalizersAndDeletionTimestamp`.
- `TestReplacePreservesVMNodeName`.
- `TestReplaceImmutableFieldStillRejected` (e.g. Volume `poolRef` or VM `arch`).

- [ ] **Step 7: Run targeted verification**

Run:

```bash
go test ./internal/controlplane/apiserver -run 'TestReplace|TestApply|TestGet' -count=1
go test ./internal/controlplane/apiserver/admission -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/controlplane/apiserver
git commit -m "feat(apiserver): add resourceVersion guarded replace"
```

### Task 4: Finalizer Real Delete Uses Conditional Delete

**Files:**
- Modify: `internal/controlplane/apiserver/handler_finalizers.go`
- Test: `internal/controlplane/apiserver/handler_finalizers_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the last-finalizer real-delete branch deletes only the store version it read.

Acceptance evidence:
- Existing finalizer tests still pass.
- A forced stale delete returns HTTP 409 and leaves the object present.

- [ ] **Step 2: Update handler**

In `handler_finalizers.go`, replace:

```go
if err := s.store.Delete(ctx, key); err != nil {
```

with:

```go
if err := s.store.DeleteIfVersion(ctx, key, raw.ResourceVersion); err != nil {
    if errors.Is(err, store.ErrRevisionConflict) {
        return store.RawObject{}, false, conflictErr(fmt.Errorf("apiserver: finalizers delete %s/%s: %w", kind, name, err))
    }
    return store.RawObject{}, false, internalErr(fmt.Errorf("apiserver: finalizers delete %s/%s: %w", kind, name, err))
}
```

Remove the old comment that says the unconditional delete window is accepted. Replace it with a comment saying real delete is now conditional on the version read by the same request.

- [ ] **Step 3: Add conflict test**

Add a fake store wrapper for DeleteIfVersion conflict. Do not reuse `stalePatchStore` if it only intercepts Put; create a small wrapper:

```go
type staleDeleteStore struct {
    store.Store
    failsRemaining int
}

func (s *staleDeleteStore) DeleteIfVersion(ctx context.Context, key string, expectedVersion string) error {
    if s.failsRemaining > 0 {
        s.failsRemaining--
        return store.ErrRevisionConflict
    }
    return s.Store.DeleteIfVersion(ctx, key, expectedVersion)
}
```

Test: seed a deleting Volume with only `FinalizerNodeTeardown`; patch finalizer; expect 409; assert object still exists.

- [ ] **Step 4: Run targeted verification**

Run:

```bash
go test ./internal/controlplane/apiserver -run TestPatchFinalizers -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/controlplane/apiserver/handler_finalizers.go internal/controlplane/apiserver/handler_finalizers_test.go
git commit -m "fix(apiserver): conditionally delete finalized objects"
```

### Task 5: govirtctl Replace Command

**Files:**
- Modify: `internal/govirtctl/client.go`
- Modify: `internal/govirtctl/command.go`
- Test: `internal/govirtctl/client_test.go`
- Test: `internal/govirtctl/command_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: users can run `govirtctl replace --server <url> -f <object.json>` and the client issues `PUT /apis/{kind}/{name}`.

Acceptance evidence:
- Client test sees method PUT.
- Command test prints `<Kind>/<name> replaced` on 200.
- Missing `metadata.resourceVersion` returns exit code 2 before HTTP.
- 409 response text surfaces to stderr.

- [ ] **Step 2: Add Client.Replace**

In `client.go`, add:

```go
func (c *Client) Replace(ctx context.Context, kind, name string, body []byte) (_ []byte, err error) {
    url := fmt.Sprintf("%s/apis/%s/%s", c.baseURL, kind, name)
    req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("govirtctl: build replace request for %s/%s: %w", kind, name, err)
    }
    req.Header.Set("Content-Type", "application/json")

    resp, err := c.hc.Do(req)
    if err != nil {
        return nil, fmt.Errorf("govirtctl: replace %s/%s: %w", kind, name, err)
    }
    defer func() {
        if cerr := resp.Body.Close(); cerr != nil && err == nil {
            err = fmt.Errorf("govirtctl: close replace response body: %w", cerr)
        }
    }()

    respBody, readErr := io.ReadAll(resp.Body)
    if readErr != nil {
        return nil, fmt.Errorf("govirtctl: read replace response for %s/%s: %w", kind, name, readErr)
    }
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        return nil, fmt.Errorf("govirtctl: replace %s/%s: master returned %d: %s", kind, name, resp.StatusCode, errorMessage(respBody))
    }
    return respBody, nil
}
```

- [ ] **Step 3: Add command parsing**

Extend `objectEnvelope` to decode RV:

```go
type objectEnvelope struct {
    Kind string `json:"kind"`
    Metadata struct {
        Name            string `json:"name"`
        ResourceVersion string `json:"resourceVersion"`
    } `json:"metadata"`
}
```

Add `replace` to `Run` and usage text. Implement `runReplace` mirroring `runApply`, except:

```go
if env.Metadata.ResourceVersion == "" {
    fmt.Fprintf(stderr, "govirtctl replace: manifest %q has no metadata.resourceVersion; run govirtctl get first\n", *file)
    return 2
}
if _, err := c.Replace(ctx, env.Kind, env.Metadata.Name, body); err != nil {
    fmt.Fprintf(stderr, "govirtctl replace: %v\n", err)
    return 1
}
fmt.Fprintf(stdout, "%s/%s replaced\n", env.Kind, env.Metadata.Name)
```

- [ ] **Step 4: Add tests**

Client tests:
- `TestClientReplaceUsesPutAndReturnsStoredObject`.
- `TestClientReplaceSurfacesConflictEnvelope`.

Command tests:
- `TestRunReplacePutsManifest` (httptest validates PUT path).
- `TestRunReplaceRequiresResourceVersion` (no HTTP server needed; exit code 2).
- `TestRunReplaceConflictExitsOneAndSurfacesMessage`.

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test ./internal/govirtctl -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/govirtctl
git commit -m "feat(govirtctl): add resourceVersion guarded replace"
```

### Task 6: E2E Replace Scenario

**Files:**
- Create: `test/e2e/replace_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the real three-node e2e proves `get → replace → stale replace 409` without growing `closure_test.go`.

Acceptance evidence:
- `scripts/e2e.sh full` passes.
- Logs show `replace` success and stale-RV 409.

- [ ] **Step 2: Create focused test file**

Create `test/e2e/replace_test.go` with `//go:build e2e` and package `e2e`.

Do **not** create a second full cluster orchestration. Add the replace helper in the new file and add exactly one call from `closure_test.go`:

```go
replaceCycle(ctx, t, ctl, server, tmpDir)
```

Place the call immediately after the existing Scenario 1 in `TestDistributedSpineClosure`, after:

```go
applyVMVariant(ctx, t, ctl, server, manifests, tmpDir, vmName, vmUID, "Off", "Acpi")
waitVMOffConverged(ctx, t, ctl, server, 3*time.Minute)
```

At that point VM `vm-e2e` exists, is cold, and still has `spec.powerState=Off` / `spec.powerOffMode=Acpi`, so a no-op power-intent replace is valid and does not interfere with later power, snapshot, resize, or cold-config assertions. Keep all implementation in `replace_test.go`.

`replaceCycle` should:
1. Run `govirtctl get VM vm-e2e` and capture JSON output.
2. Decode the first JSON object from stdout. Existing `govirtctl get` also prints `phase: ...`; use `json.Decoder` on a `strings.Reader` so trailing non-JSON text is ignored after the first object.
3. Assert `metadata.resourceVersion` is non-empty.
4. Modify a safe spec field without changing lifecycle state: set the VM object's `spec.powerState` to `Off` and `spec.powerOffMode` to `Acpi` (the base e2e VM starts in this desired state before power lifecycle). This avoids colliding with Knife 6's later memory/hardware assertions.
5. Write modified object to a temp file and run `govirtctl replace`.
6. Reuse the original pre-replace object with stale RV, write it to another temp file, run `govirtctl replace`, and assert non-zero error output contains `409`.

The e2e purpose is RV/CAS, not another config-change assertion; do not change memory, volumeRefs, or nicRefs in this replace scenario.

- [ ] **Step 3: Run e2e-focused verification**

Run:

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -tags e2e ./test/e2e -o .tmp/e2e.test
```

Expected: compile PASS.

Full run:

```bash
scripts/e2e.sh full
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add test/e2e/replace_test.go test/e2e/closure_test.go
git commit -m "test(e2e): verify resourceVersion guarded replace"
```

### Task 7: Full Verification and Documentation Cleanup

**Files:**
- Review: `docs/superpowers/specs/2026-06-12-api-write-concurrency-design.md`

- [ ] **Step 1: Run full local verification**

Run:

```bash
scripts/verify.sh
go test -race ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go vet ./...
```

Expected: all exit 0.

- [ ] **Step 2: Run full e2e**

Run:

```bash
scripts/e2e.sh full
```

Expected: PASS with replace success + stale replace 409 evidence.

- [ ] **Step 3: Check docs drift**

Compare implemented semantics against `docs/superpowers/specs/2026-06-12-api-write-concurrency-design.md`. If a user-approved implementation adjustment changes the design, update the spec in a separate commit before final handoff. Do not silently leave known drift.

- [ ] **Step 4: Final status check**

Run:

```bash
git status --short
git log --oneline -8
```

Expected: working tree clean, commits correspond to Tasks 1-7.

## Verification Summary

The feature is complete only when:

- Store fake and etcd share the conditional delete contract.
- GET body includes `metadata.resourceVersion` matching `X-Resource-Version`.
- POST apply remains unguarded declarative apply.
- PUT replace is update-only, requires body RV, conflicts on stale RV, and preserves server-owned metadata/status.
- Finalizer real delete uses conditional delete and returns 409 on version conflict.
- `govirtctl replace` drives PUT and enforces manifest RV locally.
- Real e2e proves get/replace/stale-replace behavior.
- `scripts/verify.sh`, `go test -race ./...`, linux cross-build/vet, and `scripts/e2e.sh full` pass.

## Out of Scope

- Server-Side Apply / managedFields / field ownership.
- Making `govirtctl apply -f` require RV.
- Interactive `govirtctl edit`.
- ListMeta / collection resourceVersion.
- Multi-object transactions.
