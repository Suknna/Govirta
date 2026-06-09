# 刀 4：冷快照（Snapshot 第 7 类资源）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Snapshot 成为第 7 个独立一等公民资源，实现整机冷快照的「建 + 删」声明式闭环，并把 qemu-img snapshot 执行面补齐到 `-c`/`-d`/`-a`/`-l` 完整健全。

**Architecture:** 新增 `pkg/apis/snapshot/v1alpha1` 契约；apiserver 加 Snapshot kind dispatch + admission 接线（nodeName 从 vmRef 注入、引用完整性、反向引用扫描新增 `VM ← Snapshot.vmRef`、全 spec immutable、finalizer 两阶段）；新增第 7 个 node 控制器 `SnapshotController`，对 VM 所有 volumeRefs 扇出 qcow2 内部快照（全有或全无 + stopped 门禁）；执行面补齐 snapshot delete/revert/list；storage 层接线 `local.Driver.Snapshot`/新增 delete-snapshot。

**Tech Stack:** Go 1.26、qemu-img、现有 admission 框架（刀 3）、controller-manager 框架（ReconcileResult/RequeueAfter，刀 2/3）、zerolog。

**上游 spec:** `docs/superpowers/specs/2026-06-09-knife4-cold-snapshot-design.md`

---

## 文件结构（决策锁定）

新建：
- `pkg/apis/snapshot/v1alpha1/types.go` — Snapshot 契约（Spec/Status/枚举/Validate），~130 行
- `pkg/apis/snapshot/v1alpha1/types_test.go` — 契约层测试
- `pkg/virt/qemuimg/snapshot/snapshot.go` 内新增 delete/revert/list builder（与现有 create `Builder` 同文件或拆子文件，按行数决定）
- `internal/node/controllers/snapshot.go` — SnapshotController，~260 行
- `internal/node/controllers/snapshot_test.go`
- `test/e2e/manifests/07-snapshot.json` — e2e 快照 manifest

修改：
- `pkg/apis/meta/v1alpha1/types.go` — 加 `KindSnapshot`
- `pkg/virt/qemuimg/client.go` — QCOW2Client 加 delete/revert/list 入口方法
- `internal/storage/block/driver.go` — 加 delete-snapshot 契约（`Snapshot` 已有）
- `internal/storage/local/driver.go` — 接线 `Snapshot`（当前 ErrUnsupported）+ 新增 delete-snapshot
- `internal/storage/service.go` — `VolumeService` 加 SnapshotVolume / DeleteVolumeSnapshot
- `internal/storage/pool/service.go` — `pool.Service` 加对应 snapshot 转发
- `internal/controlplane/apiserver/handler_apply.go` — 加 KindSnapshot case + applySnapshot（nodeName 注入）
- `internal/controlplane/apiserver/apply_admission.go` — decodeObjectByKind 加 Snapshot
- `internal/controlplane/apiserver/admission/object.go` — Metadata/TypeMeta/Spec/Status switch 加 Snapshot
- `internal/controlplane/apiserver/admission/fields.go` — FieldPolicyValidator 加 Snapshot（全 immutable）
- `internal/controlplane/apiserver/admission/references.go` — ReferenceValidator 加 Snapshot vmRef（by name）；ReverseReferenceValidator 加 `VM ← Snapshot.vmRef`
- `internal/controlplane/apiserver/admission/status.go` — StatusTypeValidator 加 Snapshot
- `internal/node/agent.go` — 装配第 7 个控制器
- `test/e2e/closure_test.go` — 扩展快照场景

---

### Task 1: Snapshot API 契约

**Files:**
- Create: `pkg/apis/snapshot/v1alpha1/types.go`
- Create: `pkg/apis/snapshot/v1alpha1/types_test.go`
- Modify: `pkg/apis/meta/v1alpha1/types.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `SnapshotSpec.Validate()` / `SnapshotStatus.Validate()` / `SnapshotPhase.Valid()` / `DiskSnapshotState.Valid()` 与其他 6 个资源同构；`metav1.KindSnapshot` 常量存在。
Acceptance evidence:
- `go test ./pkg/apis/snapshot/... ./pkg/apis/meta/...` passes
- Snapshot round-trips through JSON with `apiVersion`/`kind`/`metadata`/`spec`/`status`.

- [ ] **Step 2: Add KindSnapshot to meta**

In `pkg/apis/meta/v1alpha1/types.go`, after `KindVM`:

```go
	// KindSnapshot identifies a Snapshot object (whole-VM cold snapshot).
	KindSnapshot Kind = "Snapshot"
```

- [ ] **Step 3: Write the Snapshot types**

Create `pkg/apis/snapshot/v1alpha1/types.go`:

```go
// Package v1alpha1 defines the Snapshot API object. A Snapshot is a whole-VM
// cold snapshot (ESXi-style): spec.vmRef points at a VM by name, and the node
// controller fans out one qcow2 internal snapshot per the VM's volumeRefs, all
// named by the Snapshot's UID. Snapshot is an immutable entity (like Image): its
// spec never changes after creation. It depends only on the standard library
// and the shared meta envelope; it never imports internal/.
package v1alpha1

import (
	"errors"
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// ErrInvalidSpec is returned when a SnapshotSpec is not internally valid.
var ErrInvalidSpec = errors.New("snapshot: invalid spec")

// ErrInvalidStatus is returned when a SnapshotStatus is not internally valid.
var ErrInvalidStatus = errors.New("snapshot: invalid status")

// SnapshotSpec is the desired state of a whole-VM cold snapshot.
type SnapshotSpec struct {
	// VMRef is the target VM's metadata.name (whole-VM snapshot target). It is
	// a NAME (like VM.volumeRefs/nicRefs), not a UID.
	VMRef string `json:"vmRef"`
}

// Validate reports whether the spec carries explicit, internally consistent fields.
func (s SnapshotSpec) Validate() error {
	if s.VMRef == "" {
		return fmt.Errorf("%w: vmRef is required", ErrInvalidSpec)
	}
	return nil
}

// SnapshotPhase is the observed lifecycle phase written by the node controller.
// State-machine enum (项目铁律: 专用类型 + 命名常量).
type SnapshotPhase string

const (
	// SnapshotPhasePending means the snapshot object exists but the fan-out has
	// not completed (waiting for VM stopped, or fan-out in progress).
	SnapshotPhasePending SnapshotPhase = "Pending"
	// SnapshotPhaseReady means every disk's internal snapshot has been created.
	SnapshotPhaseReady SnapshotPhase = "Ready"
	// SnapshotPhaseDeleting means teardown is in progress (waiting for VM
	// stopped, or per-disk delete in progress).
	SnapshotPhaseDeleting SnapshotPhase = "Deleting"
	// SnapshotPhaseFailed means the fan-out failed and already-created disk
	// snapshots were rolled back; the snapshot can be retried.
	SnapshotPhaseFailed SnapshotPhase = "Failed"
)

// Valid reports whether p is a known snapshot phase.
func (p SnapshotPhase) Valid() bool {
	switch p {
	case SnapshotPhasePending, SnapshotPhaseReady, SnapshotPhaseDeleting, SnapshotPhaseFailed:
		return true
	default:
		return false
	}
}

// DiskSnapshotState is a single disk's snapshot result (state-machine enum).
type DiskSnapshotState string

const (
	// DiskSnapshotStateCreated means the disk's internal snapshot was created.
	DiskSnapshotStateCreated DiskSnapshotState = "Created"
	// DiskSnapshotStateFailed means the disk's internal snapshot creation failed.
	DiskSnapshotStateFailed DiskSnapshotState = "Failed"
)

// Valid reports whether s is a known disk snapshot state.
func (s DiskSnapshotState) Valid() bool {
	return s == DiskSnapshotStateCreated || s == DiskSnapshotStateFailed
}

// DiskSnapshotResult is the per-disk fan-out result projection.
type DiskSnapshotResult struct {
	VolumeRef string            `json:"volumeRef"`
	Result    DiskSnapshotState `json:"result"`
}

// SnapshotStatus is the observed state written by the node controller.
type SnapshotStatus struct {
	Phase         SnapshotPhase        `json:"phase"`
	DiskSnapshots []DiskSnapshotResult `json:"diskSnapshots,omitempty"`
	Message       string               `json:"message,omitempty"`
}

// Validate reports whether the status carries a known observed phase and, when
// present, known per-disk states.
func (s SnapshotStatus) Validate() error {
	if !s.Phase.Valid() {
		return fmt.Errorf("%w: phase %q", ErrInvalidStatus, s.Phase)
	}
	for _, d := range s.DiskSnapshots {
		if !d.Result.Valid() {
			return fmt.Errorf("%w: diskSnapshot %q result %q", ErrInvalidStatus, d.VolumeRef, d.Result)
		}
	}
	return nil
}

// Snapshot is a first-class whole-VM cold snapshot API object.
type Snapshot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              SnapshotSpec   `json:"spec"`
	Status            SnapshotStatus `json:"status"`
}
```

- [ ] **Step 4: Write contract tests**

Create `pkg/apis/snapshot/v1alpha1/types_test.go` covering: valid spec passes; empty vmRef rejected; phase Valid() for all four + unknown; DiskSnapshotState Valid(); status Validate rejects unknown phase and unknown disk result; JSON round-trip preserves apiVersion/kind/metadata/spec/status. Mirror `pkg/apis/volume/v1alpha1/types_test.go` structure.

```go
package v1alpha1

import (
	"encoding/json"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

func TestSnapshotSpecValidate(t *testing.T) {
	if err := (SnapshotSpec{VMRef: "vm-a"}).Validate(); err != nil {
		t.Fatalf("valid spec: %v", err)
	}
	if err := (SnapshotSpec{}).Validate(); err == nil {
		t.Fatal("empty vmRef must be rejected")
	}
}

func TestSnapshotPhaseValid(t *testing.T) {
	for _, p := range []SnapshotPhase{SnapshotPhasePending, SnapshotPhaseReady, SnapshotPhaseDeleting, SnapshotPhaseFailed} {
		if !p.Valid() {
			t.Fatalf("%q must be valid", p)
		}
	}
	if SnapshotPhase("bogus").Valid() {
		t.Fatal("bogus phase must be invalid")
	}
}

func TestDiskSnapshotStateValid(t *testing.T) {
	if !DiskSnapshotStateCreated.Valid() || !DiskSnapshotStateFailed.Valid() {
		t.Fatal("known states must be valid")
	}
	if DiskSnapshotState("bogus").Valid() {
		t.Fatal("bogus state must be invalid")
	}
}

func TestSnapshotStatusValidate(t *testing.T) {
	ok := SnapshotStatus{Phase: SnapshotPhaseReady, DiskSnapshots: []DiskSnapshotResult{{VolumeRef: "v0", Result: DiskSnapshotStateCreated}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid status: %v", err)
	}
	if err := (SnapshotStatus{Phase: "bogus"}).Validate(); err == nil {
		t.Fatal("bogus phase must be rejected")
	}
	bad := SnapshotStatus{Phase: SnapshotPhaseReady, DiskSnapshots: []DiskSnapshotResult{{VolumeRef: "v0", Result: "bogus"}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("bogus disk result must be rejected")
	}
}

func TestSnapshotJSONRoundTrip(t *testing.T) {
	in := Snapshot{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindSnapshot},
		ObjectMeta: metav1.ObjectMeta{Name: "snap-a", UID: "snap-a-001"},
		Spec:       SnapshotSpec{VMRef: "vm-a"},
		Status:     SnapshotStatus{Phase: SnapshotPhaseReady, DiskSnapshots: []DiskSnapshotResult{{VolumeRef: "v0", Result: DiskSnapshotStateCreated}}},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Snapshot
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Kind != metav1.KindSnapshot || out.Spec.VMRef != "vm-a" || out.Status.Phase != SnapshotPhaseReady {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}
```

- [ ] **Step 5: Run verification**

Run: `go test ./pkg/apis/snapshot/... ./pkg/apis/meta/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w pkg/apis/snapshot pkg/apis/meta
git add pkg/apis/snapshot pkg/apis/meta/v1alpha1/types.go
git commit -m "feat(apis): add Snapshot resource contract"
```

---

### Task 2: qemu-img snapshot 执行面补齐（-d / -a / -l）

**Files:**
- Modify: `pkg/virt/qemuimg/snapshot/snapshot.go`
- Modify: `pkg/virt/qemuimg/client.go`
- Test: `pkg/virt/qemuimg/snapshot/snapshot_test.go`
- Test: `pkg/virt/qemuimg/client_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: snapshot 执行面提供 create（已有）/ delete / revert / list 四个 builder，argv 正确、ctx 取消传播、name 必填校验，与现有 create `Builder` 同构。
Acceptance evidence:
- `go test ./pkg/virt/qemuimg/...` passes
- delete builds `["snapshot","-d",<name>,<path>]`; revert builds `["snapshot","-a",<name>,<path>]`; list builds `["snapshot","-l",<path>]`.

- [ ] **Step 2: Add delete / revert / list builders**

In `pkg/virt/qemuimg/snapshot/snapshot.go`, mirror the existing `Builder` (Path/Name/Do). The existing create builder runs `["snapshot","-c",name,path]`. Add three sibling builders (each its own struct so argv stays a complete typed node):

```go
// DeleteBuilder builds `qemu-img snapshot -d <name> <path>` (delete an internal snapshot).
type DeleteBuilder struct {
	binary string
	runner imgexec.Runner
	path   string
	name   string
}

func NewDelete(binary string, runner imgexec.Runner) *DeleteBuilder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &DeleteBuilder{binary: binary, runner: runner}
}

func (b *DeleteBuilder) Path(path string) *DeleteBuilder { b.path = path; return b }
func (b *DeleteBuilder) Name(name string) *DeleteBuilder { b.name = name; return b }

func (b *DeleteBuilder) Do(ctx context.Context) error {
	path, err := imgargv.PathOperand("path", b.path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(b.name) == "" {
		return imgexec.InvalidRequest("name is required")
	}
	result, err := b.runner.Run(ctx, b.binary, []string{"snapshot", "-d", b.name, path})
	return imgexec.WrapError(result, err)
}

// RevertBuilder builds `qemu-img snapshot -a <name> <path>` (apply/revert to an
// internal snapshot). Execution-plane only; not wired to a declarative API in
// this knife (revert is a Job-backlog concern, memory 1042 / note #33).
type RevertBuilder struct {
	binary string
	runner imgexec.Runner
	path   string
	name   string
}

func NewRevert(binary string, runner imgexec.Runner) *RevertBuilder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &RevertBuilder{binary: binary, runner: runner}
}

func (b *RevertBuilder) Path(path string) *RevertBuilder { b.path = path; return b }
func (b *RevertBuilder) Name(name string) *RevertBuilder { b.name = name; return b }

func (b *RevertBuilder) Do(ctx context.Context) error {
	path, err := imgargv.PathOperand("path", b.path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(b.name) == "" {
		return imgexec.InvalidRequest("name is required")
	}
	result, err := b.runner.Run(ctx, b.binary, []string{"snapshot", "-a", b.name, path})
	return imgexec.WrapError(result, err)
}

// ListBuilder builds `qemu-img snapshot -l <path>` (list internal snapshots).
type ListBuilder struct {
	binary string
	runner imgexec.Runner
	path   string
}

func NewList(binary string, runner imgexec.Runner) *ListBuilder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &ListBuilder{binary: binary, runner: runner}
}

func (b *ListBuilder) Path(path string) *ListBuilder { b.path = path; return b }

// Do runs the list and returns the raw qemu-img output for the caller to parse.
func (b *ListBuilder) Do(ctx context.Context) (string, error) {
	path, err := imgargv.PathOperand("path", b.path)
	if err != nil {
		return "", err
	}
	result, err := b.runner.Run(ctx, b.binary, []string{"snapshot", "-l", path})
	if werr := imgexec.WrapError(result, err); werr != nil {
		return "", werr
	}
	return result.Stdout, nil
}
```

NOTE: confirm `imgexec.Result` has a `Stdout` field by reading `pkg/virt/qemuimg/internal/exec/exec.go`. If the field name differs, use the real one. If `WrapError` already consumes `result`, adapt to return stdout from the real result shape. The list parse contract is "return raw stdout"; structured parsing is not required by this knife (list is a Job-backlog aid), so returning the raw string keeps it minimal and honest.

- [ ] **Step 3: Add QCOW2Client entry methods**

In `pkg/virt/qemuimg/client.go`, beside the existing `Snapshot()` method:

```go
// SnapshotDelete returns a builder for deleting a qcow2 internal snapshot.
func (c QCOW2Client) SnapshotDelete() *snapshot.DeleteBuilder {
	return snapshot.NewDelete(c.binary, c.runner)
}

// SnapshotRevert returns a builder for reverting to a qcow2 internal snapshot.
// Execution-plane only; no declarative API wires this in the current scope.
func (c QCOW2Client) SnapshotRevert() *snapshot.RevertBuilder {
	return snapshot.NewRevert(c.binary, c.runner)
}

// SnapshotList returns a builder for listing a qcow2 file's internal snapshots.
func (c QCOW2Client) SnapshotList() *snapshot.ListBuilder {
	return snapshot.NewList(c.binary, c.runner)
}
```

- [ ] **Step 4: Add tests**

In `pkg/virt/qemuimg/snapshot/snapshot_test.go` add (mirroring existing `TestDoBuildsSnapshotArgv` + `TestDoRequiresName` + `TestDoReturnsContextCanceled`): delete argv `["snapshot","-d","snap-uid","/disk.qcow2"]`; revert argv `["snapshot","-a","snap-uid","/disk.qcow2"]`; list argv `["snapshot","-l","/disk.qcow2"]`; delete/revert require name; all three propagate a canceled ctx. Use the existing fake runner pattern in that test file.

In `pkg/virt/qemuimg/client_test.go` add a test mirroring `TestQCOW2SnapshotUsesConfiguredRunner` proving SnapshotDelete/SnapshotRevert/SnapshotList route through the configured runner.

- [ ] **Step 5: Run verification**

Run: `go test ./pkg/virt/qemuimg/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w pkg/virt/qemuimg
git add pkg/virt/qemuimg
git commit -m "feat(qemuimg): add snapshot delete/revert/list builders"
```

---

### Task 3: storage 层快照接线（block 契约 + local driver + service）

**Files:**
- Modify: `internal/storage/block/driver.go`
- Modify: `internal/storage/local/driver.go`
- Modify: `internal/storage/pool/service.go`
- Modify: `internal/storage/service.go`
- Test: `internal/storage/local/driver_test.go`
- Test: `internal/storage/service_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `VolumeService.SnapshotVolume(ctx, req)` creates a qcow2 internal snapshot on a volume; `VolumeService.DeleteVolumeSnapshot(ctx, req)` deletes it (idempotent on missing). They route VolumeService → pool.Service → local.Driver → qemuimg, never letting the controller touch qemuimg directly (积木式分层).
Acceptance evidence:
- `go test ./internal/storage/...` passes
- local.Driver.Snapshot creates a snapshot via the fake runner with argv `snapshot -c <name> <path>`; the new delete path uses `snapshot -d <name> <path>`.

- [ ] **Step 2: Extend block contract**

In `internal/storage/block/driver.go`, the `Driver.Snapshot` method already exists (`Snapshot(ctx, vol, SnapshotRequest) (volume.Snapshot, error)`). Add a delete-snapshot method to the interface and a request type:

```go
// DeleteSnapshot deletes a named internal snapshot from the volume's qcow2 file.
// Deleting a missing snapshot is an idempotent success.
DeleteSnapshot(ctx context.Context, vol volume.Volume, req DeleteSnapshotRequest) error
```

```go
// DeleteSnapshotRequest names the internal snapshot to delete from a volume.
type DeleteSnapshotRequest struct {
	Name string
}
```

Set `Capabilities.Snapshot = true` for the local driver's DriverInfo (it now supports snapshots).

- [ ] **Step 3: Implement local.Driver.Snapshot + DeleteSnapshot**

In `internal/storage/local/driver.go`, replace the `ErrUnsupported` Snapshot stub with a real implementation, and add DeleteSnapshot. Both resolve the qcow2 path from `vol.Context[PathKey]` (the create path records it there), validate the path lies under the pool root (reuse the existing `ensureOwnedDir`/path-safety helper used by Publish), and run the qemuimg builder:

```go
func (d *Driver) Snapshot(ctx context.Context, vol volume.Volume, req block.SnapshotRequest) (volume.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return volume.Snapshot{}, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return volume.Snapshot{}, fmt.Errorf("%w: snapshot name is required", volume.ErrInvalidRequest)
	}
	path := vol.Context[PathKey]
	if path == "" {
		return volume.Snapshot{}, fmt.Errorf("%w: volume has no host path", volume.ErrInvalidRequest)
	}
	if err := d.ensureOwnedFile(path); err != nil { // reuse the path-safety helper Publish uses
		return volume.Snapshot{}, err
	}
	if err := d.qemuimg.QCOW2().Snapshot().Path(path).Name(req.Name).Do(ctx); err != nil {
		return volume.Snapshot{}, fmt.Errorf("local: create snapshot %q on %q: %w", req.Name, path, err)
	}
	return volume.Snapshot{Name: req.Name, VolumeID: vol.ID}, nil
}

func (d *Driver) DeleteSnapshot(ctx context.Context, vol volume.Volume, req block.DeleteSnapshotRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("%w: snapshot name is required", volume.ErrInvalidRequest)
	}
	path := vol.Context[PathKey]
	if path == "" {
		return fmt.Errorf("%w: volume has no host path", volume.ErrInvalidRequest)
	}
	if err := d.ensureOwnedFile(path); err != nil {
		return err
	}
	if err := d.qemuimg.QCOW2().SnapshotDelete().Path(path).Name(req.Name).Do(ctx); err != nil {
		return fmt.Errorf("local: delete snapshot %q on %q: %w", req.Name, path, err)
	}
	return nil
}
```

NOTE: confirm the exact path-safety helper name local.Driver uses before qemu-img operations (read `Publish`/`ensurePublishableImage` and the `ensureOwnedDir` method already in the file). Use whichever existing helper validates a leaf qcow2 file path is owned/non-symlink. If only `ensurePublishableImage` exists for leaf files, reuse it.

DELETE idempotency on a missing internal snapshot: qemu-img `snapshot -d` returns non-zero if the named snapshot does not exist. The IDEMPOTENT-on-missing contract lives in the controller/service layer, not here — see Task 5 Step where the controller treats "snapshot not found" as already-deleted. Do NOT silently swallow the qemu-img error in the driver; return it and let the upper layer classify. (If a sentinel is needed, add `volume.ErrSnapshotNotFound` and map it, but only if the qemu-img error text is reliably classifiable; otherwise the controller's list-before-delete in Task 5 avoids the missing-delete entirely.)

- [ ] **Step 4: Add pool.Service forwarding**

In `internal/storage/pool/service.go`, add methods that look up the volume by id in the named pool (mirror `DeleteVolume`'s lookup) and forward to the driver:

```go
func (s *Service) SnapshotVolume(ctx context.Context, poolName string, volumeID volume.ID, snapshotName string) error
func (s *Service) DeleteVolumeSnapshot(ctx context.Context, poolName string, volumeID volume.ID, snapshotName string) error
```

Each: resolve pool (reuse existing pool-lookup + ErrPoolNotFound), resolve the volume by id (reuse the same index lookup `DeleteVolume` uses, returning `volume.ErrVolumeNotFound` if absent), then call `driver.Snapshot` / `driver.DeleteSnapshot`. Read `DeleteVolume` (`internal/storage/pool/service.go:459`) for the exact lookup pattern and reuse it.

- [ ] **Step 5: Add VolumeService methods**

In `internal/storage/service.go`, mirror `DeleteVolume` (`:213`):

```go
// SnapshotVolumeRequest identifies a volume and the internal snapshot name to create.
type SnapshotVolumeRequest struct {
	PoolName     string
	VolumeID     volume.ID
	SnapshotName string
}

// SnapshotVolume creates a qcow2 internal snapshot on an unpublished volume.
func (s *VolumeService) SnapshotVolume(ctx context.Context, req SnapshotVolumeRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.PoolName == "" {
		return pool.ErrPoolRequired
	}
	if req.VolumeID == "" || req.SnapshotName == "" {
		return volume.ErrInvalidRequest
	}
	return s.pools.SnapshotVolume(ctx, req.PoolName, req.VolumeID, req.SnapshotName)
}

// DeleteVolumeSnapshotRequest identifies a volume and the internal snapshot name to delete.
type DeleteVolumeSnapshotRequest struct {
	PoolName     string
	VolumeID     volume.ID
	SnapshotName string
}

// DeleteVolumeSnapshot deletes a qcow2 internal snapshot from an unpublished volume.
func (s *VolumeService) DeleteVolumeSnapshot(ctx context.Context, req DeleteVolumeSnapshotRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.PoolName == "" {
		return pool.ErrPoolRequired
	}
	if req.VolumeID == "" || req.SnapshotName == "" {
		return volume.ErrInvalidRequest
	}
	return s.pools.DeleteVolumeSnapshot(ctx, req.PoolName, req.VolumeID, req.SnapshotName)
}
```

- [ ] **Step 6: Add/update tests**

`internal/storage/local/driver_test.go`: replace `TestSnapshotAndResizeUnsupportedAfterContextCheck` (snapshot half — it now must succeed, resize stays unsupported) with a test that snapshots a volume whose `Context[PathKey]` points at a created qcow2, asserting the fake runner saw `snapshot -c <name> <path>`; add a delete-snapshot test asserting `snapshot -d <name> <path>`; keep ctx-cancel coverage for both. Update the resize half of the old test to remain (resize still ErrUnsupported in this knife).

`internal/storage/service_test.go`: add SnapshotVolume/DeleteVolumeSnapshot tests asserting param validation (empty pool → ErrPoolRequired, empty id/name → ErrInvalidRequest) and forwarding to a fake pool service.

- [ ] **Step 7: Run verification**

Run: `go test ./internal/storage/...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
gofmt -w internal/storage
git add internal/storage
git commit -m "feat(storage): wire qcow2 snapshot create/delete through volume service"
```

---

### Task 4: apiserver admission 接线（object/spec/status/fields/references）

**Files:**
- Modify: `internal/controlplane/apiserver/admission/object.go`
- Modify: `internal/controlplane/apiserver/admission/fields.go`
- Modify: `internal/controlplane/apiserver/admission/references.go`
- Modify: `internal/controlplane/apiserver/admission/status.go`
- Test: `internal/controlplane/apiserver/admission/*_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: admission recognizes Snapshot — Metadata/TypeMeta/Spec/Status extract it; FieldPolicyValidator rejects any spec change (full immutable); ReferenceValidator requires `vmRef` VM exists by name and rejects a deleting VM; ReverseReferenceValidator rejects deleting a VM still referenced by a Snapshot; StatusTypeValidator validates SnapshotStatus.
Acceptance evidence:
- `go test ./internal/controlplane/apiserver/admission/...` passes
- new unit tests for each behavior pass.

- [ ] **Step 2: Add Snapshot to object.go switches**

In `internal/controlplane/apiserver/admission/object.go`, add `case snapshotv1.Snapshot:` returning `o.ObjectMeta` / `o.TypeMeta` / `o.Spec` / `o.Status` in Metadata/TypeMeta/Spec/Status respectively, and to `normalizeObject` if it has a pointer-deref switch (read the file to confirm the pointer-normalization shape and mirror it). Import `snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"`.

- [ ] **Step 3: Add Snapshot immutable policy to fields.go**

In `FieldPolicyValidator.Validate`'s kind switch, add:

```go
	case metav1.KindSnapshot:
		oldSnap, ok := oldObj.(snapshotv1.Snapshot)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("old object type %T is not Snapshot", req.OldObject))
		}
		newSnap, ok := newObj.(snapshotv1.Snapshot)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("new object type %T is not Snapshot", req.NewObject))
		}
		if oldSnap.Spec != newSnap.Spec {
			return Reject(v.Name(), ReasonConflict, fmt.Errorf("%w: snapshot spec is immutable", snapshotv1.ErrInvalidSpec))
		}
		return nil
```

(SnapshotSpec is a single comparable string field, so `==` is sound full-immutable.)

- [ ] **Step 4: Add Snapshot vmRef to ReferenceValidator**

In `references.go` `ReferenceValidator.Validate`'s type switch, add:

```go
	case snapshotv1.Snapshot:
		return v.requireByName(ctx, metav1.KindVM, o.Spec.VMRef)
```

`requireByName` already returns 400 on missing and 409 on a deleting target — exactly the Snapshot contract (VM must exist by name, not deleting). vmRef is a NAME here (unlike Volume/NIC vmRef which are UIDs), so by-name lookup is correct.

- [ ] **Step 5: Add VM ← Snapshot.vmRef to ReverseReferenceValidator**

In `references.go` `ReverseReferenceValidator.Validate`'s `case metav1.KindVM:` (currently returns nil — VM was the apex), add a Snapshot scan. The VM is referenced by name, so the scan compares `Snapshot.spec.vmRef` to the VM's name (`req.Name`), matching the existing Volume/NIC-by-name pattern (NOT the removed UID pattern):

```go
	case metav1.KindVM:
		// A VM is referenced by Snapshot.spec.vmRef (by VM NAME). Knife 3 made VM
		// the apex with no reverse edge; Snapshot is the first legitimate VM
		// downstream reference (see knife4 spec §4.3). Volume.vmRef / NIC.vmRef are
		// ownership backpointers (UIDs) and deliberately do NOT block VM deletion.
		snaps, err := v.list(ctx, metav1.KindSnapshot, req.Kind)
		if err != nil {
			return err
		}
		for _, raw := range snaps {
			var proj snapshotDeleteRefProjection
			if derr := json.Unmarshal(raw.Value, &proj); derr != nil {
				return v.decodeReject(metav1.KindSnapshot, raw.Key, derr)
			}
			if proj.Spec.VMRef == req.Name {
				return v.reject(req.Kind, req.Name, metav1.KindSnapshot, proj.Metadata.Name)
			}
		}
		return nil
```

Add the projection type beside the others in `delete.go` (where `volumeDeleteRefProjection` etc. live):

```go
// snapshotDeleteRefProjection decodes a Snapshot's vmRef (names the target VM).
type snapshotDeleteRefProjection struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		VMRef string `json:"vmRef"`
	} `json:"spec"`
}
```

NOTE: the VM reverse-edge code lives in `delete.go` (the `ReverseReferenceValidator`), not `references.go`. Read `internal/controlplane/apiserver/admission/delete.go` `case metav1.KindVM:` and replace its `return nil` body with the scan above. Keep the doc comment block at the top of `ReverseReferenceValidator` in sync (the table that says "VM <- (nothing)" becomes "VM <- Snapshot.spec.vmRef (by name)").

- [ ] **Step 6: Add Snapshot to StatusTypeValidator**

In `status.go`, add `case metav1.KindSnapshot:` decoding `snapshotv1.SnapshotStatus` (mirror the VM/Volume cases) so a status PATCH on a Snapshot validates phase + per-disk states via `SnapshotStatus.Validate()`.

- [ ] **Step 7: Add tests**

`object_test.go`: Snapshot Metadata/TypeMeta/Spec/Status extraction (value + pointer).
`fields_test.go`: Snapshot spec change rejected 409; identical spec allowed.
`references_test.go`: apply Snapshot with absent VM → 400; with deleting VM → 409; with ready VM → allowed.
`delete_test.go`: delete VM referenced by a Snapshot → 409 "still referenced by Snapshot/x"; delete VM with no Snapshot → allowed.
`status_test.go`: Snapshot status with bogus phase → rejected; valid → accepted.

- [ ] **Step 8: Run verification**

Run: `go test ./internal/controlplane/apiserver/admission/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
gofmt -w internal/controlplane/apiserver/admission
git add internal/controlplane/apiserver/admission
git commit -m "feat(admission): recognize and protect Snapshot resource"
```

---

### Task 5: apiserver apply handler（Snapshot kind dispatch + nodeName 注入）

**Files:**
- Modify: `internal/controlplane/apiserver/apply_admission.go`
- Modify: `internal/controlplane/apiserver/handler_apply.go`
- Test: `internal/controlplane/apiserver/handler_apply_test.go`
- Test: `internal/controlplane/apiserver/handlers_test.go` (fixtures)

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `POST /apis/Snapshot/{name}` decodes a Snapshot, runs PreApplyChain, injects finalizer, resolves `spec.vmRef` → VM's nodeName → Snapshot.metadata.nodeName, preserves update metadata, runs PostApplyChain, stores.
Acceptance evidence:
- `go test ./internal/controlplane/apiserver/...` passes
- applying a Snapshot stores it with nodeName equal to its target VM's nodeName.

- [ ] **Step 2: Add Snapshot to decodeObjectByKind**

In `apply_admission.go` `decodeObjectByKind`, add:

```go
	case metav1.KindSnapshot:
		var obj snapshotv1.Snapshot
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("decode Snapshot: %w", err)
		}
		return obj, nil
```

Import `snapshotv1`.

- [ ] **Step 3: Add Snapshot case to apply switch**

In `handler_apply.go` `apply`'s kind switch, add a case mirroring the others but calling a Snapshot-specific apply that injects nodeName from vmRef:

```go
	case metav1.KindSnapshot:
		obj, req, aerr := s.decodeAndAdmitApply(ctx, kind, name, body)
		if aerr != nil {
			return nil, aerr
		}
		snap := obj.(snapshotv1.Snapshot)
		injectFinalizer(&snap.ObjectMeta)
		if aerr := preserveUpdateObjectMeta(req, &snap.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		raw, aerr := s.applySnapshot(ctx, storeKey(kind, snap.Name), &snap, req)
		if aerr != nil {
			return nil, aerr
		}
		snap.ResourceVersion = raw.ResourceVersion
		return marshalResponse(snap)
```

- [ ] **Step 4: Implement applySnapshot (nodeName injection)**

In `handler_apply.go` (beside `applyNIC`/`applyVM`):

```go
// applySnapshot resolves the Snapshot's nodeName from its target VM (the snapshot
// must run on the node that holds the VM's qcow2 files) and persists it. This is
// the third admission mutation precedent after NIC MAC allocation and VM
// scheduling: the user never supplies a Snapshot nodeName — it is a deterministic
// derivation of the target VM's placement (single source of truth). On create the
// VM must already exist (ReferenceValidator enforced it). On update the existing
// nodeName is preserved (a snapshot cannot migrate), so injection only runs for
// create.
func (s *Server) applySnapshot(ctx context.Context, key string, snap *snapshotv1.Snapshot, req admission.Request) (store.RawObject, *apiError) {
	if req.Operation == admission.OperationCreate {
		node, aerr := s.resolveVMNodeName(ctx, snap.Spec.VMRef)
		if aerr != nil {
			return store.RawObject{}, aerr
		}
		snap.NodeName = node
	}
	return s.putWithPostAdmission(ctx, key, *snap, req)
}

// resolveVMNodeName reads the VM named ref and returns its metadata.nodeName.
// ReferenceValidator already proved the VM exists and is not deleting, so a
// missing VM here is an internal inconsistency (500). An empty VM nodeName is
// also internal: a stored VM is always scheduled (bindVM) before it lands.
func (s *Server) resolveVMNodeName(ctx context.Context, vmName string) (string, *apiError) {
	raw, err := s.store.Get(ctx, storeKey(metav1.KindVM, vmName))
	if err != nil {
		return "", internalErr(fmt.Errorf("apiserver: resolve snapshot target VM %q nodeName: %w", vmName, err))
	}
	var vm vmv1.VM
	if err := json.Unmarshal(raw.Value, &vm); err != nil {
		return "", internalErr(fmt.Errorf("apiserver: decode snapshot target VM %q: %w", vmName, err))
	}
	if vm.NodeName == "" {
		return "", internalErr(fmt.Errorf("apiserver: snapshot target VM %q has no nodeName", vmName))
	}
	return vm.NodeName, nil
}
```

- [ ] **Step 5: Add fixtures + tests**

In `handlers_test.go`, add a `validSnapshot()` fixture helper (TypeMeta + ObjectMeta name/uid + Spec.VMRef pointing at the test VM). In `handler_apply_test.go`, add:
- apply Snapshot stores nodeName resolved from the target VM (seed a VM with nodeName=node0, apply Snapshot(vmRef=that VM), assert stored Snapshot.nodeName==node0).
- apply Snapshot whose vmRef VM does not exist → 400 (ReferenceValidator).
- the kind-coverage table test (if one exists enumerating all kinds) includes Snapshot.

- [ ] **Step 6: Run verification**

Run: `go test ./internal/controlplane/apiserver/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/controlplane/apiserver
git add internal/controlplane/apiserver
git commit -m "feat(apiserver): dispatch Snapshot apply with vmRef nodeName injection"
```

---

### Task 6: SnapshotController（node 控制器）

**Files:**
- Create: `internal/node/controllers/snapshot.go`
- Test: `internal/node/controllers/snapshot_test.go`
- Modify: `internal/node/agent.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: SnapshotController reconciles Snapshot objects — gates on target VM live phase == Stopped, fans out `qemu-img snapshot -c <snapshot-uid>` over the VM's volumeRefs (all-or-nothing with rollback), patches structured status, and on teardown deletes the snapshots then removes its finalizer.
Acceptance evidence:
- `go test ./internal/node/controllers/...` passes
- create path: VM not stopped → Pending + requeue; VM stopped + all disks succeed → Ready with all DiskSnapshots Created; mid-disk failure → rollback already-created + Failed.
- delete path: VM running → keep finalizer + Deleting + requeue; VM stopped → delete all + remove finalizer.

- [ ] **Step 2: Define narrow interfaces + struct**

Create `internal/node/controllers/snapshot.go`. The controller needs: read VM (live phase via vmm) + read VM object (for volumeRefs) + read each Volume object (for pool + derived id) via master client; snapshot/delete via VolumeService. Reuse the existing `DependencyReader` (master client: Get/PatchStatus/FinalizerRemover) and `VMRunner` (vmm Status for live phase) narrow interfaces already in this package. Add a `VolumeSnapshotter` narrow interface:

```go
// VolumeSnapshotter is the narrow slice of the volume service the snapshot
// controller needs: create and delete a qcow2 internal snapshot on a volume.
// *storage.VolumeService satisfies it (积木式 + 可测).
type VolumeSnapshotter interface {
	SnapshotVolume(ctx context.Context, req storage.SnapshotVolumeRequest) error
	DeleteVolumeSnapshot(ctx context.Context, req storage.DeleteVolumeSnapshotRequest) error
}

var (
	_ VolumeSnapshotter     = (*storage.VolumeService)(nil)
	_ controller.Controller = (*SnapshotController)(nil)
)

// SnapshotController reconciles Snapshot objects: a whole-VM cold snapshot. It
// reads the target VM's live phase (must be Stopped — qemu-img snapshot is unsafe
// on a running image), resolves the VM's volumeRefs to (pool, derived volume id),
// and fans out one qcow2 internal snapshot per disk, all named by the Snapshot's
// UID. The fan-out is all-or-nothing: a mid-disk failure rolls back the
// already-created snapshots so etcd never holds a half-complete whole-VM snapshot.
type SnapshotController struct {
	volumes VolumeSnapshotter
	vmm     VMRunner
	client  DependencyReader
}

func NewSnapshotController(volumes VolumeSnapshotter, runner VMRunner, client DependencyReader) *SnapshotController {
	return &SnapshotController{volumes: volumes, vmm: runner, client: client}
}

func (c *SnapshotController) Kind() string { return string(metav1.KindSnapshot) }
```

- [ ] **Step 3: Implement Reconcile (create + delete paths)**

```go
const snapshotRequeueDelay = 5 * time.Second

func (c *SnapshotController) Reconcile(ctx context.Context, ev controller.Event) (controller.ReconcileResult, error) {
	if err := ctx.Err(); err != nil {
		return controller.Done(), fmt.Errorf("snapshot controller: context done before reconcile: %w", err)
	}
	logger := zerolog.Ctx(ctx)

	if ev.Type == controller.EventDeleted {
		logger.Info().Str("kind", c.Kind()).Str("key", ev.Key).Msg("snapshot deleted; teardown driven by deletionTimestamp")
		return controller.Done(), nil
	}

	var snap snapshotv1.Snapshot
	if err := json.Unmarshal(ev.Object, &snap); err != nil {
		return controller.Done(), fmt.Errorf("snapshot controller: decode object %q: %w", ev.Key, err)
	}

	// Resolve the target VM object (for volumeRefs) and its live phase (stopped gate).
	vm, err := c.targetVM(ctx, snap.Spec.VMRef)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			// VM gone: nothing to snapshot/delete against. On teardown, drop the
			// finalizer (the qcow2 files are gone with the VM). On create, requeue.
			if isDeleting(snap.ObjectMeta) {
				if rerr := removeTeardownFinalizer(ctx, c.client, c.Kind(), snap.Name); rerr != nil {
					return controller.Requeue(), fmt.Errorf("snapshot controller: remove finalizer %q: %w", snap.Name, rerr)
				}
				return controller.Done(), nil
			}
			return controller.RequeueAfter(snapshotRequeueDelay), nil
		}
		return controller.RequeueAfter(snapshotRequeueDelay), err
	}

	livePhase, err := c.vmLivePhase(ctx, vm)
	if err != nil {
		return controller.RequeueAfter(snapshotRequeueDelay), err
	}

	if isDeleting(snap.ObjectMeta) {
		return c.reconcileDelete(ctx, snap, vm, livePhase)
	}
	return c.reconcileCreate(ctx, snap, vm, livePhase)
}
```

Create path:

```go
func (c *SnapshotController) reconcileCreate(ctx context.Context, snap snapshotv1.Snapshot, vm vmv1.VM, livePhase vmm.Phase) (controller.ReconcileResult, error) {
	// Level-triggered idempotence: a ready snapshot is already at desired state.
	if snap.Status.Phase == snapshotv1.SnapshotPhaseReady {
		return controller.Done(), nil
	}
	// stopped gate: qemu-img snapshot is unsafe on a running image (QEMU hard constraint).
	if livePhase != vmm.PhaseStopped {
		pending := snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhasePending, Message: "waiting for VM stopped"}
		if err := c.patchStatus(ctx, snap.Name, snap.Status, pending); err != nil {
			return controller.RequeueAfter(snapshotRequeueDelay), err
		}
		return controller.RequeueAfter(snapshotRequeueDelay), nil
	}

	created := make([]volumeTarget, 0, len(vm.Spec.VolumeRefs))
	results := make([]snapshotv1.DiskSnapshotResult, 0, len(vm.Spec.VolumeRefs))
	for _, volRef := range vm.Spec.VolumeRefs {
		target, err := c.resolveVolumeTarget(ctx, volRef)
		if err != nil {
			return c.failFanOut(ctx, snap, created, results, volRef, err)
		}
		if serr := c.volumes.SnapshotVolume(ctx, storage.SnapshotVolumeRequest{
			PoolName:     target.poolName,
			VolumeID:     target.volumeID,
			SnapshotName: snap.UID,
		}); serr != nil {
			return c.failFanOut(ctx, snap, created, results, volRef, serr)
		}
		created = append(created, target)
		results = append(results, snapshotv1.DiskSnapshotResult{VolumeRef: volRef, Result: snapshotv1.DiskSnapshotStateCreated})
	}

	ready := snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhaseReady, DiskSnapshots: results}
	if err := c.patchStatus(ctx, snap.Name, snap.Status, ready); err != nil {
		return controller.Requeue(), err
	}
	return controller.Done(), nil
}

// failFanOut rolls back already-created disk snapshots (all-or-nothing), then
// patches Failed with the per-disk results, and requeues for retry. Rollback
// errors are joined with the original cause (项目铁律: errors.Join).
func (c *SnapshotController) failFanOut(ctx context.Context, snap snapshotv1.Snapshot, created []volumeTarget, results []snapshotv1.DiskSnapshotResult, failedRef string, cause error) (controller.ReconcileResult, error) {
	rollbackErr := c.rollback(ctx, snap.UID, created)
	results = append(results, snapshotv1.DiskSnapshotResult{VolumeRef: failedRef, Result: snapshotv1.DiskSnapshotStateFailed})
	failed := snapshotv1.SnapshotStatus{
		Phase:         snapshotv1.SnapshotPhaseFailed,
		DiskSnapshots: results,
		Message:       cause.Error(),
	}
	patchErr := c.patchStatus(ctx, snap.Name, snap.Status, failed)
	return controller.RequeueAfter(snapshotRequeueDelay), errors.Join(cause, rollbackErr, patchErr)
}

func (c *SnapshotController) rollback(ctx context.Context, snapUID string, created []volumeTarget) error {
	var errs []error
	for _, t := range created {
		if err := c.volumes.DeleteVolumeSnapshot(ctx, storage.DeleteVolumeSnapshotRequest{
			PoolName:     t.poolName,
			VolumeID:     t.volumeID,
			SnapshotName: snapUID,
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
```

Delete path:

```go
func (c *SnapshotController) reconcileDelete(ctx context.Context, snap snapshotv1.Snapshot, vm vmv1.VM, livePhase vmm.Phase) (controller.ReconcileResult, error) {
	// stopped gate also applies to delete (qemu-img snapshot -d unsafe on running image).
	if livePhase != vmm.PhaseStopped {
		deleting := snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhaseDeleting, Message: "waiting for VM stopped"}
		if err := c.patchStatus(ctx, snap.Name, snap.Status, deleting); err != nil {
			return controller.RequeueAfter(snapshotRequeueDelay), err
		}
		return controller.RequeueAfter(snapshotRequeueDelay), nil
	}

	var errs []error
	for _, volRef := range vm.Spec.VolumeRefs {
		target, err := c.resolveVolumeTarget(ctx, volRef)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if derr := c.volumes.DeleteVolumeSnapshot(ctx, storage.DeleteVolumeSnapshotRequest{
			PoolName:     target.poolName,
			VolumeID:     target.volumeID,
			SnapshotName: snap.UID,
		}); derr != nil {
			errs = append(errs, derr)
		}
	}
	if err := errors.Join(errs...); err != nil {
		deleting := snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhaseDeleting, Message: err.Error()}
		_ = c.patchStatus(ctx, snap.Name, snap.Status, deleting)
		return controller.RequeueAfter(snapshotRequeueDelay), fmt.Errorf("snapshot controller: delete %q: %w", snap.Name, err)
	}
	if err := removeTeardownFinalizer(ctx, c.client, c.Kind(), snap.Name); err != nil {
		return controller.Requeue(), fmt.Errorf("snapshot controller: remove finalizer %q: %w", snap.Name, err)
	}
	return controller.Done(), nil
}
```

Helpers (`volumeTarget`, `resolveVolumeTarget`, `targetVM`, `vmLivePhase`, `patchStatus`):

```go
// volumeTarget is a resolved (pool, derived volume id) pair for one of the VM's
// volumeRefs. The volume id MUST be derived the same way the storage layer keys
// it (VMRef-role-diskIndex) — the same derivation the volume controller teardown
// uses (mapVolumeRole lives in volume.go, same package).
type volumeTarget struct {
	poolName string
	volumeID volume.ID
}

// resolveVolumeTarget reads the named Volume object from the master and derives
// its storage key (pool + VMRef-role-diskIndex id). The qcow2 file the snapshot
// runs on is owned by that volume in that pool.
func (c *SnapshotController) resolveVolumeTarget(ctx context.Context, volName string) (volumeTarget, error) {
	raw, err := c.client.Get(ctx, string(metav1.KindVolume), volName)
	if err != nil {
		return volumeTarget{}, fmt.Errorf("snapshot controller: get Volume %q: %w", volName, err)
	}
	var vol volumev1.Volume
	if err := json.Unmarshal(raw, &vol); err != nil {
		return volumeTarget{}, fmt.Errorf("snapshot controller: decode Volume %q: %w", volName, err)
	}
	id := volume.ID(fmt.Sprintf("%s-%s-%d", vol.Spec.VMRef, mapVolumeRole(vol.Spec.Role), vol.Spec.DiskIndex))
	return volumeTarget{poolName: vol.Spec.PoolRef, volumeID: id}, nil
}

func (c *SnapshotController) targetVM(ctx context.Context, vmName string) (vmv1.VM, error) {
	raw, err := c.client.Get(ctx, string(metav1.KindVM), vmName)
	if err != nil {
		return vmv1.VM{}, err // ErrNotFound handled by caller
	}
	var vm vmv1.VM
	if err := json.Unmarshal(raw, &vm); err != nil {
		return vmv1.VM{}, fmt.Errorf("snapshot controller: decode VM %q: %w", vmName, err)
	}
	return vm, nil
}

// vmLivePhase reads the VM's live process phase via vmm (上下一致: live is the
// single source of truth, not the VM object's status projection). The vmm key is
// the VM's UID (the runtime is keyed by uid, as the VM controller does).
func (c *SnapshotController) vmLivePhase(ctx context.Context, vm vmv1.VM) (vmm.Phase, error) {
	live, err := c.vmm.Status(ctx, vm.UID)
	if err != nil {
		return "", fmt.Errorf("snapshot controller: read VM %q live phase: %w", vm.Name, err)
	}
	return live.Phase, nil
}

func (c *SnapshotController) patchStatus(ctx context.Context, name string, observed, desired snapshotv1.SnapshotStatus) error {
	if snapshotStatusEqual(observed, desired) {
		return nil
	}
	body, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("snapshot controller: marshal status for %q: %w", name, err)
	}
	if _, err := c.client.PatchStatus(ctx, c.Kind(), name, body); err != nil {
		return fmt.Errorf("snapshot controller: patch status for %q: %w", name, err)
	}
	return nil
}

// snapshotStatusEqual compares two statuses including the DiskSnapshots slice
// (SnapshotStatus is not == comparable because it holds a slice). Used by the
// no-op patch guard to break the status->MODIFIED->reconcile->PATCH loop.
func snapshotStatusEqual(a, b snapshotv1.SnapshotStatus) bool {
	if a.Phase != b.Phase || a.Message != b.Message || len(a.DiskSnapshots) != len(b.DiskSnapshots) {
		return false
	}
	for i := range a.DiskSnapshots {
		if a.DiskSnapshots[i] != b.DiskSnapshots[i] {
			return false
		}
	}
	return true
}
```

NOTE: confirm `vmm.VM` exposes `.Phase` and `.UID`/the runtime key field by reading `internal/vmm/vm.go` (the `vmm.VM` struct). The VM controller calls `c.vmm.Status(ctx, uuid)` — confirm whether the uuid arg is `vm.UID` (apis VM metadata uid) by reading how the VM controller derives it (`internal/node/controllers/vm.go` create/status path). Use the SAME identity the VM controller uses so the snapshot reads the same runtime.

- [ ] **Step 4: Register controller in agent**

In `internal/node/agent.go` `NewAgent`, add to the `list`:

```go
		controllers.NewSnapshotController(volumeSvc, vmmSvc, master),
```

- [ ] **Step 5: Write tests**

`internal/node/controllers/snapshot_test.go` with fake VolumeSnapshotter (records SnapshotVolume/DeleteVolumeSnapshot calls, can be told to fail on the Nth call), fake VMRunner (Status returns a configurable phase), fake DependencyReader (Get returns seeded VM/Volume objects, PatchStatus + RemoveFinalizer recorded). Cases:
- VM not stopped → status Pending + RequeueAfter, no SnapshotVolume calls.
- VM stopped, 2 volumeRefs, both succeed → status Ready with 2 Created, 2 SnapshotVolume calls with SnapshotName==snap.UID.
- VM stopped, 2nd disk fails → 1st rolled back (DeleteVolumeSnapshot called for disk 0), status Failed with disk0 Created + disk1 Failed, RequeueAfter.
- ready snapshot re-reconcile → no-op (no SnapshotVolume calls).
- delete path, VM running → keep finalizer (no RemoveFinalizer), status Deleting, RequeueAfter.
- delete path, VM stopped → DeleteVolumeSnapshot for each disk + RemoveFinalizer called.
- delete path, VM gone (ErrNotFound) → RemoveFinalizer called (qcow2 gone with VM).
- status no-op guard: observed==desired → no PatchStatus call.

- [ ] **Step 6: Run verification**

Run: `go test ./internal/node/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/node
git add internal/node
git commit -m "feat(node): add SnapshotController whole-VM cold snapshot reconciler"
```

---

### Task 7: e2e 快照场景

**Files:**
- Create: `test/e2e/manifests/07-snapshot.json`
- Modify: `test/e2e/closure_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the distributed spine e2e proves cold snapshot create + delete + reverse-reference protection on a real three-node topology.
Acceptance evidence:
- `scripts/e2e.sh full` passes with the new snapshot scenario.

- [ ] **Step 2: Add snapshot manifest**

Create `test/e2e/manifests/07-snapshot.json`:

```json
{
  "apiVersion": "govirta.io/v1alpha1",
  "kind": "Snapshot",
  "metadata": { "name": "snap-e2e", "uid": "snap-e2e-001" },
  "spec": { "vmRef": "vm-e2e" }
}
```

- [ ] **Step 3: Extend closure_test.go**

After the existing flow leaves VM at `powerState=Off` / phase `stopped` (and before reverse teardown), insert:

1. apply `07-snapshot.json` → poll Snapshot until `status.phase=Ready`; assert `diskSnapshots` all `Created`.
2. host-side live check: run `qemu-img snapshot -l <root-volume-qcow2>` inside the Lima guest, assert the internal snapshot `snap-e2e-001` is listed (live truth, not status projection).
3. attempt `delete vm vm-e2e` while the Snapshot exists → expect 409 "still referenced by Snapshot/snap-e2e".
4. `delete snapshot snap-e2e` → poll until 404 (finalizer two-phase).
5. host-side: `qemu-img snapshot -l` → assert `snap-e2e-001` no longer listed.

Then the existing reverse teardown (delete VM, NIC, Volume, Network, Image, pools) proceeds — now VM delete is unblocked because the Snapshot is gone.

Follow the existing closure_test.go helpers for apply/get/delete/poll and the Lima guest exec pattern (read the current host-orphan-check section for how it runs commands in the guest).

- [ ] **Step 4: Run verification**

Run: `scripts/e2e.sh full`
Expected: `TestDistributedSpineClosure` PASS, snapshot scenario included, host-side snapshot list confirms create+delete.

- [ ] **Step 5: Commit**

```bash
git add test/e2e
git commit -m "test(e2e): cover cold snapshot create/delete + reverse-reference"
```

---

### Task 8: 全量验证 + 最终 review

**Files:** Review all changed files.

- [ ] **Step 1: Inspect final diff**

```bash
git status --short
git diff --stat main...HEAD
git diff --check main...HEAD
```

Expected: clean status, no whitespace errors.

- [ ] **Step 2: Local CI equivalent**

```bash
scripts/verify.sh
```

Expected: PASS (gofmt clean, all tests, main builds).

- [ ] **Step 3: Race tests**

```bash
go test -race ./internal/controlplane/apiserver/... ./internal/node/... ./internal/storage/... ./pkg/virt/qemuimg/...
go test -race ./...
```

Expected: PASS.

- [ ] **Step 4: Real distributed e2e**

```bash
scripts/e2e.sh full
```

Expected: `TestDistributedSpineClosure` PASS including the snapshot scenario.

- [ ] **Step 5: Spec coverage checklist**

Confirm each spec section has implementation evidence:
- §3 API contract → Task 1 (types + KindSnapshot).
- §3 全 spec immutable → Task 4 Step 3.
- §3 内部快照命名 = Snapshot UID → Task 6 (SnapshotName: snap.UID).
- §4.1 nodeName 注入 → Task 5 Step 4.
- §4.2 引用完整性 → Task 4 Step 4.
- §4.3 反向引用 VM ← Snapshot → Task 4 Step 5.
- §4.4 finalizer 两阶段 → Task 5 (injectFinalizer) + Task 6 (removeTeardownFinalizer).
- §5 控制器收敛（建/删/门禁/全有或全无）→ Task 6.
- §6 执行面 -c/-d/-a/-l → Task 2.
- §7 storage 接线 → Task 3.
- §8 govirtctl → no change needed (kind-agnostic; verified by e2e using apply/get/delete).
- §9 e2e → Task 7.

- [ ] **Step 6: Final code review**

Dispatch parallel reviewers (correctness/regression + maintainability/contract-consistency) with base SHA and HEAD SHA. Fix Critical/Important before merge.

- [ ] **Step 7: Commit any review fixes**

```bash
git commit -m "fix(...): <review finding>"
```

---

## Execution notes

- Execute in an isolated worktree (using-git-worktrees).
- Recommended mode: subagent-driven development, one fresh subagent per task, main-agent review after every commit (verify每个 commit, memory 822).
- Do NOT wire snapshot revert (`-a`) to any declarative API — it is execution-plane-ready only, Job-backlog (memory 1042 / note #33).
- Do NOT add single-volume/single-disk snapshot API (OpenStack model excluded).
- stopped gate uses LIVE vmm phase, never the VM object's status projection (上下一致).
- If a task finds a real spec mismatch, stop and bring it back for design correction rather than silently changing the contract.
