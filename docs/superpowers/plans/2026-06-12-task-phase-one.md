# Task Phase One Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the first Task control loop: Task becomes an internal API resource, govirtad can create/progress Node and Cluster no-op Tasks, and govirtlet can watch assigned Node Tasks and patch status.

**Architecture:** Reuse the existing `/apis/{kind}` store/watch/status surface for Task read/watch/status, while rejecting user `POST apply` and `PUT replace` for Task. Add a store-backed control-plane Task client/manager for internal creation and Cluster execution, and add a govirtlet Task controller that uses the existing node controller-manager and HTTP status patch client.

**Tech Stack:** Go 1.26, existing `store.Store`, existing `internal/controlplane/apiserver`, existing `internal/node/controller`, existing `internal/node/client`, stdlib JSON/time/context.

---

## File Structure

- Create `pkg/apis/task/v1alpha1/types.go`: Task API contract, typed scope/operation/phase/error class, no-op input/observed types, validation.
- Create `pkg/apis/task/v1alpha1/types_test.go`: Task validation and JSON round-trip tests.
- Modify `pkg/apis/meta/v1alpha1/types.go`: add `KindTask`.
- Modify `internal/controlplane/apiserver/apply_admission.go`: decode Task objects by kind.
- Modify `internal/controlplane/apiserver/handler_apply.go`: reject user `POST /apis/Task/{name}` with 403.
- Modify `internal/controlplane/apiserver/handler_replace.go`: reject user `PUT /apis/Task/{name}` with 403.
- Modify `internal/controlplane/apiserver/admission/object.go`: include Task in metadata/type/spec/status helpers.
- Modify `internal/controlplane/apiserver/admission/status.go`: decode and validate `TaskStatus`.
- Modify `internal/controlplane/apiserver/admission/fields.go`: treat Task as internal-only if field-policy validation ever sees update/replace.
- Create `internal/controlplane/apiserver/handler_task_test.go`: prove Task apply/replace rejection, Task watch routing, and Task status patch.
- Create `internal/controlplane/controller/task_client.go`: store-backed typed Task client with create-or-update and status patch.
- Create `internal/controlplane/controller/manager.go`: minimal no-op Task manager and lifecycle.
- Create `internal/controlplane/controller/manager_test.go`: NodeTask and ClusterTask closed-loop tests over fake store.
- Modify `internal/controlplane/service.go`: wire the controller manager next to apiserver and run both under one ctx.
- Create `internal/node/controllers/task.go`: govirtlet TaskExecutor controller.
- Create `internal/node/controllers/task_test.go`: TaskExecutor no-op, terminal no-op, invalid operation failure tests.
- Modify `internal/node/agent.go`: add Task controller to the existing controller list without removing business controllers.

---

### Task 1: Task API Contract

**Files:**
- Create: `pkg/apis/task/v1alpha1/types.go`
- Create: `pkg/apis/task/v1alpha1/types_test.go`
- Modify: `pkg/apis/meta/v1alpha1/types.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `Task.Validate()` accepts explicit Node and Cluster no-op tasks and rejects missing scope, nodeName mistakes, missing owner identity, unsupported operation, and invalid phase.
Acceptance evidence:
- `go test -count=1 ./pkg/apis/task/...` passes.
- `go test -count=1 ./pkg/apis/...` passes and existing round-trip tests remain valid.

- [ ] **Step 2: Add `KindTask`**

In `pkg/apis/meta/v1alpha1/types.go`, extend the existing kind constants:

```go
// KindTask identifies an internal Task object.
KindTask Kind = "Task"
```

- [ ] **Step 3: Implement Task API types**

Create `pkg/apis/task/v1alpha1/types.go` with this public shape:

```go
package v1alpha1

import (
    "encoding/json"
    "errors"
    "fmt"
    "time"

    metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

var ErrInvalidTask = errors.New("task api: invalid task")

type TaskScope string

const (
    TaskScopeNode    TaskScope = "Node"
    TaskScopeCluster TaskScope = "Cluster"
)

func (s TaskScope) Valid() bool { return s == TaskScopeNode || s == TaskScopeCluster }

type TaskOperation string

const (
    TaskOperationNoopNode    TaskOperation = "NoopNode"
    TaskOperationNoopCluster TaskOperation = "NoopCluster"
)

func (o TaskOperation) Valid() bool { return o == TaskOperationNoopNode || o == TaskOperationNoopCluster }

type TaskPhase string

const (
    TaskPhasePending   TaskPhase = "Pending"
    TaskPhaseRunning   TaskPhase = "Running"
    TaskPhaseSucceeded TaskPhase = "Succeeded"
    TaskPhaseFailed    TaskPhase = "Failed"
)

func (p TaskPhase) Valid() bool {
    return p == "" || p == TaskPhasePending || p == TaskPhaseRunning || p == TaskPhaseSucceeded || p == TaskPhaseFailed
}

func (p TaskPhase) Terminal() bool { return p == TaskPhaseSucceeded || p == TaskPhaseFailed }

type TaskErrorClass string

const (
    TaskErrorClassNone               TaskErrorClass = "None"
    TaskErrorClassInvalidInput       TaskErrorClass = "InvalidInput"
    TaskErrorClassUnsupportedOperation TaskErrorClass = "UnsupportedOperation"
    TaskErrorClassExecutionFailed    TaskErrorClass = "ExecutionFailed"
)

func (c TaskErrorClass) Valid() bool {
    return c == "" || c == TaskErrorClassNone || c == TaskErrorClassInvalidInput || c == TaskErrorClassUnsupportedOperation || c == TaskErrorClassExecutionFailed
}

type Task struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata"`
    Spec              TaskSpec   `json:"spec"`
    Status            TaskStatus `json:"status"`
}

type TaskSpec struct {
    Scope     TaskScope       `json:"scope"`
    OwnerKind metav1.Kind      `json:"ownerKind"`
    OwnerName string           `json:"ownerName"`
    OwnerUID  string           `json:"ownerUID"`
    Operation TaskOperation    `json:"operation"`
    Input     json.RawMessage  `json:"input"`
}

type NoopInput struct {
    Marker string `json:"marker"`
}

type NoopObserved struct {
    Executor string `json:"executor"`
    Marker   string `json:"marker"`
}

type TaskStatus struct {
    Phase       TaskPhase       `json:"phase"`
    Observed    json.RawMessage `json:"observed,omitempty"`
    ErrorClass  TaskErrorClass  `json:"errorClass,omitempty"`
    Message     string          `json:"message,omitempty"`
    StartedAt   string          `json:"startedAt,omitempty"`
    CompletedAt string          `json:"completedAt,omitempty"`
}
```

Implement validation rules:

```go
func (t Task) Validate() error {
    if t.APIVersion != metav1.APIGroupVersion {
        return fmt.Errorf("%w: apiVersion %q must be %q", ErrInvalidTask, t.APIVersion, metav1.APIGroupVersion)
    }
    if t.Kind != metav1.KindTask {
        return fmt.Errorf("%w: kind %q must be %q", ErrInvalidTask, t.Kind, metav1.KindTask)
    }
    if err := t.ObjectMeta.Validate(); err != nil {
        return fmt.Errorf("%w: %v", ErrInvalidTask, err)
    }
    if err := t.Spec.Validate(t.ObjectMeta); err != nil {
        return err
    }
    if err := t.Status.Validate(); err != nil {
        return err
    }
    return nil
}

func (s TaskSpec) Validate(meta metav1.ObjectMeta) error {
    if !s.Scope.Valid() {
        return fmt.Errorf("%w: spec.scope %q is invalid", ErrInvalidTask, s.Scope)
    }
    if s.Scope == TaskScopeNode && meta.NodeName == "" {
        return fmt.Errorf("%w: node-scoped task requires metadata.nodeName", ErrInvalidTask)
    }
    if s.Scope == TaskScopeCluster && meta.NodeName != "" {
        return fmt.Errorf("%w: cluster-scoped task must not carry metadata.nodeName", ErrInvalidTask)
    }
    if s.OwnerKind == "" || s.OwnerName == "" || s.OwnerUID == "" {
        return fmt.Errorf("%w: ownerKind, ownerName, and ownerUID are required", ErrInvalidTask)
    }
    if !s.Operation.Valid() {
        return fmt.Errorf("%w: spec.operation %q is invalid", ErrInvalidTask, s.Operation)
    }
    if len(s.Input) == 0 || string(s.Input) == "null" {
        return fmt.Errorf("%w: spec.input is required", ErrInvalidTask)
    }
    var input NoopInput
    if err := json.Unmarshal(s.Input, &input); err != nil {
        return fmt.Errorf("%w: decode noop input: %v", ErrInvalidTask, err)
    }
    if input.Marker == "" {
        return fmt.Errorf("%w: noop input marker is required", ErrInvalidTask)
    }
    if s.Scope == TaskScopeNode && s.Operation != TaskOperationNoopNode {
        return fmt.Errorf("%w: node task operation must be %q", ErrInvalidTask, TaskOperationNoopNode)
    }
    if s.Scope == TaskScopeCluster && s.Operation != TaskOperationNoopCluster {
        return fmt.Errorf("%w: cluster task operation must be %q", ErrInvalidTask, TaskOperationNoopCluster)
    }
    return nil
}

func (s TaskStatus) Validate() error {
    if !s.Phase.Valid() {
        return fmt.Errorf("%w: status.phase %q is invalid", ErrInvalidTask, s.Phase)
    }
    if !s.ErrorClass.Valid() {
        return fmt.Errorf("%w: status.errorClass %q is invalid", ErrInvalidTask, s.ErrorClass)
    }
    if s.StartedAt != "" {
        if _, err := time.Parse(time.RFC3339, s.StartedAt); err != nil {
            return fmt.Errorf("%w: startedAt must be RFC3339: %v", ErrInvalidTask, err)
        }
    }
    if s.CompletedAt != "" {
        if _, err := time.Parse(time.RFC3339, s.CompletedAt); err != nil {
            return fmt.Errorf("%w: completedAt must be RFC3339: %v", ErrInvalidTask, err)
        }
    }
    return nil
}
```

- [ ] **Step 4: Add focused Task API tests**

In `pkg/apis/task/v1alpha1/types_test.go`, add tests named:

```go
func TestTaskValidateAcceptsExplicitNodeNoopTask(t *testing.T)
func TestTaskValidateAcceptsExplicitClusterNoopTask(t *testing.T)
func TestTaskValidateRejectsNodeTaskWithoutNodeName(t *testing.T)
func TestTaskValidateRejectsClusterTaskWithNodeName(t *testing.T)
func TestTaskValidateRejectsMissingNoopInputMarker(t *testing.T)
func TestTaskJSONRoundTripPreservesEnvelope(t *testing.T)
```

Use helpers that build valid tasks with explicit `NoopInput{Marker: "phase-one"}` marshalled into `Spec.Input`.

- [ ] **Step 5: Run targeted verification**

Run: `go test -count=1 ./pkg/apis/task/... ./pkg/apis/...`
Expected: PASS.

---

### Task 2: Task Apiserver Semantics

**Files:**
- Modify: `internal/controlplane/apiserver/apply_admission.go`
- Modify: `internal/controlplane/apiserver/handler_apply.go`
- Modify: `internal/controlplane/apiserver/handler_replace.go`
- Modify: `internal/controlplane/apiserver/admission/object.go`
- Modify: `internal/controlplane/apiserver/admission/status.go`
- Modify: `internal/controlplane/apiserver/admission/fields.go`
- Create: `internal/controlplane/apiserver/handler_task_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: Task can be decoded/read/listed/watched/status-patched through `/apis/Task`, but user apply/replace is rejected.
Acceptance evidence:
- `go test -count=1 ./internal/controlplane/apiserver/...` passes.
- Tests prove `POST` and `PUT` Task return 403.
- Tests prove nodeName-filtered watch receives NodeTask and not ClusterTask.

- [ ] **Step 2: Add Task to decode and admission helpers**

Import `taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"` in helper files.

In `decodeObjectByKind`, add:

```go
case metav1.KindTask:
    var obj taskv1.Task
    if err := json.Unmarshal(raw, &obj); err != nil {
        return nil, fmt.Errorf("decode Task: %w", err)
    }
    return obj, nil
```

In `admission.Metadata`, `TypeMeta`, `Spec`, `Status`, and `normalizeObject`, add `taskv1.Task`, `*taskv1.Task`, `taskv1.TaskStatus`, and `*taskv1.TaskStatus` branches matching the existing resource patterns.

In `decodeStatus`, add:

```go
case metav1.KindTask:
    var s taskv1.TaskStatus
    if err := decodeStrictStatus(raw, &s); err != nil {
        return nil, fmt.Errorf("decode %s status: %w", kind, err)
    }
    return s, nil
```

In `FieldPolicyValidator.Validate`, add a `metav1.KindTask` branch returning a conflict rejection, because Task spec updates are internal-only in phase one.

- [ ] **Step 3: Reject public Task apply and replace**

In `Server.apply`, before generic decode/admit, add:

```go
case metav1.KindTask:
    return nil, forbidden(fmt.Errorf("apiserver: Task is internal and cannot be applied through the public API"))
```

In `Server.replace`, before `decodeAndAdmitReplace`, add:

```go
if kind == metav1.KindTask {
    return nil, forbidden(fmt.Errorf("apiserver: Task is internal and cannot be replaced through the public API"))
}
```

If `forbidden` does not exist, add an `apiError` constructor in the existing error helper file:

```go
func forbidden(err error) *apiError { return &apiError{code: http.StatusForbidden, err: err} }
```

- [ ] **Step 4: Add tests for Task HTTP semantics**

In `handler_task_test.go`, write tests:

```go
func TestTaskApplyAndReplaceAreRejected(t *testing.T)
func TestTaskStatusPatchAcceptsBareStatus(t *testing.T)
func TestTaskWatchRoutesNodeScopeAndSkipsClusterScope(t *testing.T)
```

Seed Task objects directly into fake store with `st.Put(ctx, storeKey(metav1.KindTask, task.Name), data, "")`, not through public apply. For watch routing, start `srv := NewServer(...)`, open `GET /apis/Task?watch=true&nodeName=node0`, then put one NodeTask with `metadata.nodeName=node0` and one ClusterTask with no nodeName. Assert the stream contains only the NodeTask name.

- [ ] **Step 5: Run targeted verification**

Run: `go test -count=1 ./internal/controlplane/apiserver/...`
Expected: PASS.

---

### Task 3: Control-plane Task Manager

**Files:**
- Create: `internal/controlplane/controller/task_client.go`
- Create: `internal/controlplane/controller/manager.go`
- Create: `internal/controlplane/controller/manager_test.go`
- Modify: `internal/controlplane/service.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `controller.Manager.Run(ctx)` creates one NodeTask and one ClusterTask, and directly completes the ClusterTask through the store-backed Task client.
Acceptance evidence:
- `go test -count=1 ./internal/controlplane/controller/... ./internal/controlplane/...` passes.
- Test proves NodeTask remains visible as pending/running/succeeded only after node patch, while ClusterTask reaches `Succeeded` without govirtlet.

- [ ] **Step 2: Implement store-backed TaskClient**

Create `task_client.go` with:

```go
package controller

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"

    "github.com/suknna/govirta/internal/controlplane/store"
    metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
    taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

type TaskClient struct { store store.Store }

func NewTaskClient(st store.Store) *TaskClient { return &TaskClient{store: st} }

func taskKey(name string) string { return "/govirta/" + string(metav1.KindTask) + "/" + name }

func (c *TaskClient) Get(ctx context.Context, name string) (taskv1.Task, error)
func (c *TaskClient) CreateOrUpdate(ctx context.Context, task taskv1.Task) (taskv1.Task, error)
func (c *TaskClient) PatchStatus(ctx context.Context, name string, status taskv1.TaskStatus) (taskv1.Task, error)
```

Implementation rules:
- `CreateOrUpdate` validates `task.Validate()`, preserves existing `metadata.resourceVersion` by using `expectedVersion` when an existing Task is found, and skips the write when stored spec/status already match.
- `PatchStatus` reads current Task, no-ops if status already equals desired, sets `task.Status = status`, validates, and writes with the current resourceVersion.
- Any primary read/write/marshal error is wrapped with `%w`; revision conflicts are returned for the manager to retry later.

- [ ] **Step 3: Implement minimal manager**

Create `manager.go` with:

```go
type Manager struct {
    tasks *TaskClient
    nodeNames []string
}

func NewManager(st store.Store, nodeNames []string) *Manager
func (m *Manager) Run(ctx context.Context) error
```

`Run` should:
- Return `nil` immediately if `len(nodeNames) == 0`; this preserves explicit static node configuration without inventing a node.
- Build a deterministic NodeTask for `nodeNames[0]` named `phase-one-node-noop-<node>` with `Scope=Node`, `Operation=NoopNode`, `OwnerKind=Task`, `OwnerName="phase-one-node-noop"`, `OwnerUID="phase-one-node-noop"`, and input marker `phase-one-node`.
- Build a deterministic ClusterTask named `phase-one-cluster-noop` with `Scope=Cluster`, `Operation=NoopCluster`, `OwnerKind=Task`, `OwnerName="phase-one-cluster-noop"`, `OwnerUID="phase-one-cluster-noop"`, and input marker `phase-one-cluster`.
- Create/update both Tasks.
- Patch ClusterTask status to `Running`, then `Succeeded` with observed executor `control-plane` and marker `phase-one-cluster`.
- Block on `<-ctx.Done()` after initial reconciliation so the manager shares the service lifecycle.

- [ ] **Step 4: Wire controlplane Service**

Modify `internal/controlplane/service.go`:
- Add `controllerManager *controller.Manager` to `Service`.
- In `newServiceWithStore`, construct `controller.NewManager(st, cfg.NodeNames)`.
- In `Run`, run apiserver and controller manager concurrently under the same ctx using a `sync.WaitGroup` and `errCh`.
- On first component error, cancel the derived context, wait for both goroutines, close store, and return `errors.Join(runErrs..., closeErr)`.

- [ ] **Step 5: Add manager tests**

In `manager_test.go`, use fake store and explicit `[]string{"node0"}`:

```go
func TestManagerCreatesNodeAndClusterTasks(t *testing.T)
func TestManagerCompletesClusterTask(t *testing.T)
func TestManagerDoesNotInventNodeTaskWithoutNodes(t *testing.T)
```

Run `mgr.Run(ctx)` in a goroutine with a cancelable context, wait until Tasks appear, then cancel and assert:
- NodeTask has `Spec.Scope == TaskScopeNode` and `NodeName == "node0"`.
- ClusterTask has `Spec.Scope == TaskScopeCluster`, empty `NodeName`, and `Status.Phase == TaskPhaseSucceeded`.

- [ ] **Step 6: Run targeted verification**

Run: `go test -count=1 ./internal/controlplane/controller/... ./internal/controlplane/...`
Expected: PASS.

---

### Task 4: Govirtlet TaskExecutor

**Files:**
- Create: `internal/node/controllers/task.go`
- Create: `internal/node/controllers/task_test.go`
- Modify: `internal/node/agent.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: govirtlet watches `Task` and executes only assigned Node no-op Tasks by patching `Running` then `Succeeded`, while preserving all existing business controllers.
Acceptance evidence:
- `go test -count=1 ./internal/node/controllers/... ./internal/node/...` passes.
- Tests prove pending NodeTask is completed, terminal Task is ignored, and unsupported operation is marked Failed.

- [ ] **Step 2: Implement TaskController**

Create `task.go` in package `controllers` with:

```go
type TaskStatusPatcher interface {
    PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error)
}

type TaskController struct {
    nodeName string
    client TaskStatusPatcher
    now func() time.Time
}

func NewTaskController(nodeName string, client TaskStatusPatcher) *TaskController
func (c *TaskController) Kind() string { return string(metav1.KindTask) }
func (c *TaskController) Reconcile(ctx context.Context, ev controller.Event) (controller.ReconcileResult, error)
```

Reconcile rules:
- Ignore `controller.EventDeleted`.
- Decode `taskv1.Task` from `ev.Object` and call `task.Validate()`.
- Ignore if `task.Spec.Scope != TaskScopeNode` or `task.NodeName != c.nodeName`.
- Ignore if `task.Status.Phase.Terminal()`.
- If operation is not `TaskOperationNoopNode`, patch `Failed` with `TaskErrorClassUnsupportedOperation` and return `controller.Done()`.
- Patch `Running` with `StartedAt` set to `now().UTC().Format(time.RFC3339)` if current phase is not already `Running`.
- Decode `task.Spec.Input` as `taskv1.NoopInput` and patch `Succeeded` with `Observed` equal to `taskv1.NoopObserved{Executor: c.nodeName, Marker: input.Marker}` and `CompletedAt` set.
- Use `json.Marshal(status)` and `c.client.PatchStatus(ctx, string(metav1.KindTask), task.Name, body)` for every status update.

- [ ] **Step 3: Wire TaskController into agent**

In `internal/node/agent.go`, append to the existing `list := []controller.Controller{...}`:

```go
controllers.NewTaskController(cfg.NodeName, master),
```

Do not remove or reorder the existing seven business controllers unless tests require deterministic ordering; preserving existing behavior is the priority.

- [ ] **Step 4: Add TaskController tests**

In `task_test.go`, add a fake patcher that records raw status bodies. Add tests:

```go
func TestTaskControllerCompletesPendingNodeNoopTask(t *testing.T)
func TestTaskControllerIgnoresTerminalTask(t *testing.T)
func TestTaskControllerMarksUnsupportedOperationFailed(t *testing.T)
func TestTaskControllerIgnoresOtherNodeTask(t *testing.T)
```

Use a fixed `now` function returning `time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)` so status timestamps are deterministic.

- [ ] **Step 5: Run targeted verification**

Run: `go test -count=1 ./internal/node/controllers/... ./internal/node/...`
Expected: PASS.

---

### Task 5: End-to-End Task Watch Closure

**Files:**
- Create or modify: `internal/node/client/task_integration_test.go` if needed, otherwise add to existing `internal/node/client/manager_integration_test.go`
- Modify tests only unless implementation gaps are found.

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: A Task written by control plane fake store is delivered through HTTP watch to govirtlet's manager and patched back to Task status through the apiserver.
Acceptance evidence:
- `go test -count=1 ./internal/node/client/... ./internal/node/... ./internal/controlplane/...` passes.

- [ ] **Step 2: Add integration test**

Build test topology:
- fake store
- `apiserver.NewServer(st, alloc, scheduler.NewNoopScheduler(), []string{"node0"}, "")`
- `httptest.NewServer(srv.Handler())`
- `client.NewWatchSource(ts.URL, ts.Client(), "node0")`
- `client.New(ts.URL, ts.Client())`
- `controllers.NewTaskController("node0", master)` inside `controller.NewManager(source, []controller.Controller{taskController})`

Write a valid NodeTask directly to fake store. Run manager in a cancelable goroutine. Poll `st.Get(ctx, "/govirta/Task/<name>")` until decoded `Task.Status.Phase == taskv1.TaskPhaseSucceeded`. Assert `Observed.Executor == "node0"`.

- [ ] **Step 3: Run targeted verification**

Run: `go test -count=1 ./internal/node/client/... ./internal/node/... ./internal/controlplane/...`
Expected: PASS.

---

### Task 6: Full Verification and Documentation Check

**Files:**
- No new files unless verification reveals a real issue.

- [ ] **Step 1: Run diagnostics checkpoint**

Run AFT diagnostics over touched packages.
Expected: no new compile/type diagnostics in touched packages.

- [ ] **Step 2: Run focused package tests**

Run:

```bash
go test -count=1 ./pkg/apis/... ./internal/controlplane/apiserver/... ./internal/controlplane/controller/... ./internal/node/controllers/... ./internal/node/...
```

Expected: PASS.

- [ ] **Step 3: Run full verification**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Inspect git diff**

Run:

```bash
git diff --stat
git diff -- docs/superpowers/specs/2026-06-12-task-phase-one-design.md docs/superpowers/plans/2026-06-12-task-phase-one.md pkg/apis/meta/v1alpha1/types.go pkg/apis/task/v1alpha1/types.go internal/controlplane/apiserver internal/controlplane/controller internal/controlplane/service.go internal/node/controllers/task.go internal/node/agent.go
```

Expected: diff contains only Task phase-one changes and no unrelated formatting churn.

---

## Self-Review Notes

- Spec coverage: Task API, `/apis/Task` read/watch/status with public apply/replace rejection, control-plane Node/Cluster no-op manager, govirtlet TaskExecutor, existing business controllers retained, and verification are covered by Tasks 1-6.
- Placeholder scan: no `TBD`, `TODO`, `implement later`, or unspecified test commands remain.
- Type consistency: plan consistently uses `TaskScope`, `TaskOperation`, `TaskPhase`, `TaskErrorClass`, `TaskClient`, and `TaskController`.
