# 刀 5：冷扩容（Volume.capacityBytes 冷扩容）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用户改大已存在 Volume 的 `spec.capacityBytes`，node 在卷所属 VM 处于冷态（stopped/defined/runtime-absent）时把 live qcow2 `qemu-img resize` 扩到目标容量（只增不减）。

**Architecture:** 声明式 level-triggered 冷扩容。对外（控制器/service）声明式强制收敛（直接声明目标容量，不比对）；对内（local.Driver）读 live qcow2 virtual size 判断幂等（局部封装读 live 在 driver 一层）。pool 记账只算预分配（与 CreateVolume 一致），按增量 delta 过 overcommit；记账模型是 volumes map 动态求和，无累加器，改 map 即账本自动正确。VolumeController 注入 VM live phase 读取能力做 cold 门禁，复用 snapshot 的 vmIsCold 逻辑。

**Tech Stack:** Go 1.26、qemu-img（resize/info）、现有 storage 三层（VolumeService→pool.Service→block.Driver→qemuimg）、node 控制器框架（ReconcileResult/RequeueAfter）。

**设计依据：** `docs/superpowers/specs/2026-06-10-knife5-cold-volume-resize-design.md`（六决策 + 记账模型已用真实源码核实）。

**行数预判（AGENTS 第十八章）：**
- `internal/storage/pool/service.go` 已 864 行（超 800 硬上限）→ `ResizeVolume` 放**新文件** `internal/storage/pool/resize.go`，不再撑大 service.go。
- `internal/storage/local/driver.go` 727 行 + Resize 实现（替换 7 行 stub）≈ 757，under 800。
- `internal/node/controllers/volume.go` 384 行 → cold gate + resize 收敛 helper 放**新文件** `internal/node/controllers/volume_resize.go`，volume.go 只改 Reconcile ready 分支。
- 其余文件改动小，无逼近硬上限。

---

## Task 1: 抽取共享 vmIsCold 助手（纯重构，行为保持）

**Files:**
- Create: `internal/node/controllers/coldgate.go`
- Modify: `internal/node/controllers/snapshot.go:304-318`（`vmIsCold` 方法改为委托共享函数）
- Test: `internal/node/controllers/coldgate_test.go`

**背景：** `vmIsCold` 现为 `SnapshotController` 的方法（snapshot.go:304-318），只用 `Status`。VolumeController 需同款逻辑。抽成包级共享函数 + 窄接口，DRY，两控制器复用。这是**纯行为保持重构**（snapshot 行为不变），独立 commit。

- [ ] **Step 1: 确认目标与验收证据**

Goal: 包级 `vmIsCold(ctx, reader, vm)` 对 `PhaseStopped`/`PhaseDefined`/`vmm.ErrNotFound` 返回 `(true, nil)`，其他 phase 返回 `(false, nil)`，读 phase 失败返回 `(false, wrapped err)`。snapshot 控制器现有行为不变。
验收证据：
- `go test ./internal/node/controllers/ -run TestVMIsCold -v` PASS
- `go test ./internal/node/controllers/ -run TestSnapshot -v` 仍全 PASS（行为保持）

- [ ] **Step 2: 写共享函数 + 窄接口**

`internal/node/controllers/coldgate.go`：
```go
package controllers

import (
	"context"
	"errors"
	"fmt"

	"github.com/suknna/govirta/internal/vmm"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

// VMStatusReader 是冷门禁所需的最窄 VM 运行时切片：只读 live phase。
// *vmm.VMMService（经 controllers.VMRunner）满足它（积木式 + 可测）。
type VMStatusReader interface {
	Status(ctx context.Context, uuid string) (vmm.VM, error)
}

// vmIsCold 报告目标 VM 对 qemu-img 冷操作（snapshot/resize）是否安全，即没有
// QEMU 进程持有 qcow2（上下一致：live 是唯一事实源，不信 VM 对象的 status 投影）。
// vmm 运行时按 VM 的 UID 索引（与 VM 控制器同一身份）。
//
// "Cold" = 进程已死 AND 非运行意图：PhaseStopped（运行后停）或 PhaseDefined
// （powerState=Off，从未启动）——两者进程已死且意图非运行，控制器不会在冷操作
// 期间 Start 它（无重启竞态）。PhaseFailed 意图=运行、控制器可能重启它，故非 cold。
// vmm.ErrNotFound（运行时 vm.json 缺失）= 根本无进程，等价 cold。
func vmIsCold(ctx context.Context, reader VMStatusReader, vm vmv1.VM) (bool, error) {
	live, err := reader.Status(ctx, vm.UID)
	if err != nil {
		if errors.Is(err, vmm.ErrNotFound) {
			return true, nil
		}
		return false, fmt.Errorf("cold gate: read VM %q live phase: %w", vm.Name, err)
	}
	switch live.Phase {
	case vmm.PhaseStopped, vmm.PhaseDefined:
		return true, nil
	default:
		return false, nil
	}
}
```

- [ ] **Step 3: snapshot.go 的 vmIsCold 改为委托共享函数**

`internal/node/controllers/snapshot.go` 把 `func (c *SnapshotController) vmIsCold(...)` 整段（:304-318）替换为：
```go
func (c *SnapshotController) vmIsCold(ctx context.Context, vm vmv1.VM) (bool, error) {
	return vmIsCold(ctx, c.vmm, vm)
}
```
（`c.vmm` 是 `VMRunner`，满足 `VMStatusReader`。保留方法包装是为最小化 snapshot.go 改动 + 保持其调用点不变。）

- [ ] **Step 4: 写共享函数测试**

`internal/node/controllers/coldgate_test.go`：用 fake reader 覆盖五态。
```go
package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/vmm"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

type fakeColdReader struct {
	phase vmm.Phase
	err   error
}

func (f fakeColdReader) Status(ctx context.Context, uuid string) (vmm.VM, error) {
	if f.err != nil {
		return vmm.VM{}, f.err
	}
	return vmm.VM{Phase: f.phase}, nil
}

func TestVMIsCold(t *testing.T) {
	vm := vmv1.VM{}
	vm.Name = "vm-x"
	vm.UID = "uid-x"

	cases := []struct {
		name     string
		reader   fakeColdReader
		wantCold bool
		wantErr  bool
	}{
		{"stopped is cold", fakeColdReader{phase: vmm.PhaseStopped}, true, false},
		{"defined is cold", fakeColdReader{phase: vmm.PhaseDefined}, true, false},
		{"runtime absent is cold", fakeColdReader{err: vmm.ErrNotFound}, true, false},
		{"running not cold", fakeColdReader{phase: vmm.PhaseRunning}, false, false},
		{"failed not cold", fakeColdReader{phase: vmm.PhaseFailed}, false, false},
		{"read error propagates", fakeColdReader{err: errors.New("boom")}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cold, err := vmIsCold(context.Background(), tc.reader, vm)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if cold != tc.wantCold {
				t.Fatalf("cold = %v, want %v", cold, tc.wantCold)
			}
		})
	}
}
```
注：核实 `vmm.PhaseRunning`/`vmm.PhaseFailed` 常量真名（读 `internal/vmm`），若不同则按真名改。

- [ ] **Step 5: 运行验证**

Run: `go test ./internal/node/controllers/ -run 'TestVMIsCold|TestSnapshot' -v`
Expected: PASS（新函数测试 + snapshot 行为保持）

- [ ] **Step 6: 若失败修实现或陈旧测试**

snapshot 行为变了 → 修委托；常量名错 → 按 `internal/vmm` 真名改。

- [ ] **Step 7: commit**

```bash
git add internal/node/controllers/coldgate.go internal/node/controllers/coldgate_test.go internal/node/controllers/snapshot.go
git commit -m "refactor: extract shared vmIsCold cold gate helper"
```

---

## Task 2: local.Driver.Resize 实现（C′ 读 live 幂等）

**Files:**
- Modify: `internal/storage/local/driver.go:430-436`（替换 Resize stub）+ DriverInfo Capabilities 开 ResizeOffline 位
- Test: `internal/storage/local/driver_test.go`

- [ ] **Step 1: 确认目标与验收证据**

Goal: `local.Driver.Resize(ctx, vol, req)` 读 live virtual size，`live >= req.CapacityBytes` 则跳过 resize 返回容量更新后的 volume（幂等成功），否则 `qemu-img resize` 后返回。`DriverInfo.Capabilities.ResizeOffline == true`。
验收证据：
- `go test ./internal/storage/local/ -run TestDriverResize -v` PASS（三态：真 resize、幂等跳过、ctx 取消）
- `go test ./internal/storage/local/ -run TestDriverInfo -v` PASS（ResizeOffline 位开启）

- [ ] **Step 2: 实现 Resize**

先读 `internal/storage/local/driver.go` 确认：`pathFromVolume`/`ensurePublishableImage` 的真实签名、`d.qemuimg`（QCOW2Client）字段名、`CreateFromReader` 里 `Resize()` 调用样式（:213 附近）、`DriverInfo` 方法里 Capabilities 构造处。然后替换 `:430-436`：
```go
// Resize grows the volume's qcow2 to req.CapacityBytes when the live virtual
// size is below the target. Reading the live virtual size here (上下一致：the
// qcow2 file is the single source of truth) makes resize idempotent: a live
// size already >= target is an accepted no-op, so repeated level-triggered
// reconciles and crash-retries converge without error. Shrink never happens
// (admission rejects capacity decrease and this never passes --shrink).
func (d *Driver) Resize(ctx context.Context, vol volume.Volume, req block.ResizeRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	if req.CapacityBytes <= 0 {
		return volume.Volume{}, volume.ErrInvalidRequest
	}

	path, err := d.pathFromVolume(vol)
	if err != nil {
		return volume.Volume{}, err
	}
	if err := ensurePublishableImage(path); err != nil {
		return volume.Volume{}, err
	}

	info, err := d.qemuimg.QCOW2().Info().Path(path).Do(ctx)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("resize volume %q: read live size: %w", vol.Name, err)
	}

	// Idempotent: live already at or beyond target (manual grow, or a prior
	// successful resize whose ledger update did not land). Accept as no-op.
	if info.VirtualSizeBytes >= req.CapacityBytes {
		resized := vol
		resized.CapacityBytes = req.CapacityBytes
		return resized, nil
	}

	if err := d.qemuimg.QCOW2().Resize().Path(path).SizeBytes(req.CapacityBytes).Do(ctx); err != nil {
		return volume.Volume{}, fmt.Errorf("resize volume %q to %d: %w", vol.Name, req.CapacityBytes, err)
	}

	resized := vol
	resized.CapacityBytes = req.CapacityBytes
	return resized, nil
}
```
注：`d.qemuimg.QCOW2().Info()`/`Resize()` 的确切链式调用以 `local/driver.go` 既有 `CreateFromReader` 里的真实写法为准（核对 client 字段名与 `QCOW2()` 是否存在；spec 写的是 `QCOW2Client.Info()`/`.Resize()`，实现时对齐真实 client 入口）。

- [ ] **Step 3: DriverInfo 开 ResizeOffline 位**

在 `local/driver.go` 的 `DriverInfo` 方法里，`Capabilities` 构造处把 `ResizeOffline` 设为 `true`（核对现有 Capabilities 字面量位置，加 `ResizeOffline: true`）。

- [ ] **Step 4: 写测试**

`internal/storage/local/driver_test.go` 加（复用既有 fake runner 模式——核对该文件现有 fake runner 如何注入 qemu-img info/resize 的 stdout/exit）：
- `live < target` → 断言执行了 `qemu-img resize`（fake runner 记录 argv 含 "resize"）、返回 vol.CapacityBytes == target
- `live >= target` → 断言**未**执行 resize（argv 无 "resize"）、返回 vol.CapacityBytes == target（幂等）
- `ctx 取消` → 返回 ctx.Err()
- DriverInfo Capabilities.ResizeOffline == true

测试需让 fake runner 对 `info` 子命令返回带 `virtual-size` 的 JSON（参照该文件既有 info/create 测试的 fake stdout 构造）。

- [ ] **Step 5: 运行验证**

Run: `go test ./internal/storage/local/ -run 'TestDriverResize|TestDriverInfo' -v`
Expected: PASS

- [ ] **Step 6: 若失败修实现或陈旧测试**

fake runner 注入方式不符 → 按该文件既有模式改；链式调用名错 → 按真实 client 改。

- [ ] **Step 7: commit**

```bash
git add internal/storage/local/driver.go internal/storage/local/driver_test.go
git commit -m "feat: implement local driver offline qcow2 resize with live-size idempotency"
```

---

## Task 3: pool.Service.ResizeVolume（记账编排，新文件）

**Files:**
- Create: `internal/storage/pool/resize.go`（service.go 已 864 行超限，新方法另起文件）
- Test: `internal/storage/pool/resize_test.go`

- [ ] **Step 1: 确认目标与验收证据**

Goal: `(*Service).ResizeVolume(ctx, poolName, volumeID, capacityBytes)` 在锁内按增量 `delta=new-old` 过 `reserveCapacityLocked` 预分配准入，调 `driver.Resize` 成功后改 `p.volumes[id].CapacityBytes=new`。delta==0 跳过准入仍下沉 driver；delta<0 拒绝；卷不存在返回 `volume.ErrVolumeNotFound`；超 overcommit 返回 `ErrPoolCapacityExceeded`。
验收证据：
- `go test ./internal/storage/pool/ -run TestServiceResizeVolume -v` PASS

- [ ] **Step 2: 确认锁临界区边界（核对 CreateVolume）**

读 `internal/storage/pool/service.go:196-249`（CreateVolume）确认其临界区模式：`p.mu.Lock()` + `defer p.mu.Unlock()`，**全程锁内**完成 `reserveCapacityLocked` → `driver.Create` → `recordCreatedVolumeLocked`（driver 调用也在锁内）。ResizeVolume 必须严格对齐：reserve + driver.Resize + 改 map 全程同一把锁内，防并发超卖。

- [ ] **Step 3: 写 ResizeVolume**

`internal/storage/pool/resize.go`：
```go
package pool

import (
	"context"
	"fmt"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/volume"
)

// ResizeVolume grows an existing block volume's declared capacity to
// capacityBytes (cold offline resize). Accounting is pre-allocation only and
// matches CreateVolume: the delta (new - old) passes the same overcommit
// admission via reserveCapacityLocked. The allocated total is a live sum over
// p.volumes (no counter), so updating the map entry's CapacityBytes is the only
// ledger mutation needed — there is no addAllocated/releaseAllocated to keep in
// sync. Ordering is critical: reserve → driver.Resize success → mutate the map.
// If driver.Resize fails the map is untouched, so the next reconcile recomputes
// the same delta and retries (level-triggered, spec §4 ordering invariant).
//
// The whole reserve→resize→record sequence runs under p.mu (same critical
// section discipline as CreateVolume) to prevent concurrent over-commit.
func (s *Service) ResizeVolume(ctx context.Context, poolName string, volumeID volume.ID, capacityBytes int64) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	if capacityBytes <= 0 {
		return volume.Volume{}, volume.ErrInvalidRequest
	}

	p, err := s.getPool(poolName)
	if err != nil {
		return volume.Volume{}, err
	}
	if p.Config.Type != PoolTypeBlock {
		return volume.Volume{}, volume.ErrUnsupported
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	existing, ok := p.volumes[volumeID]
	if !ok {
		return volume.Volume{}, volume.ErrVolumeNotFound
	}
	oldCap := existing.CapacityBytes
	delta := capacityBytes - oldCap
	if delta < 0 {
		// Defensive: admission already rejects capacity decrease; never shrink.
		return volume.Volume{}, volume.ErrInvalidRequest
	}
	if delta > 0 {
		if err := reserveCapacityLocked(p, delta); err != nil {
			return volume.Volume{}, err
		}
	}

	driver := p.Driver
	resized, err := driver.Resize(ctx, existing, block.ResizeRequest{CapacityBytes: capacityBytes})
	if err != nil {
		return volume.Volume{}, fmt.Errorf("resize volume %q in pool %q: %w", volumeID, poolName, err)
	}

	// Ledger advance only after the live resize succeeded.
	existing.CapacityBytes = capacityBytes
	p.volumes[volumeID] = cloneVolume(existing)
	_ = resized // driver returns its observed volume; the pool owns the indexed record.
	return cloneVolume(existing), nil
}
```
注：核对 `cloneVolume`/`getPool`/`reserveCapacityLocked`/`p.mu` 真实可见性（同包私有，可用）。`reserveCapacityLocked` 形参是 `(p *Pool, bytes int64)`（service.go:306），在锁内调用——但它内部用 `p.allocatedLocked()` 不再加锁（确认 reserveCapacityLocked 不自持锁，CreateVolume 也是在锁内调它的，对齐即可）。

- [ ] **Step 4: 写测试**

`internal/storage/pool/resize_test.go`（复用该包既有 fake driver + testConfig 模式，核对 service_test.go 现有 helper）：
- delta>0 准入通过：注册 block pool（容量足）+ 建一个卷 → ResizeVolume 增大 → 断言返回卷 CapacityBytes==new、fake driver 收到 Resize 调用、`GetPoolUsage().AllocatedBytes` 反映新值
- 超 overcommit：小容量 pool，resize 增量超 `capacity*1.5-allocated` → 断言 `ErrPoolCapacityExceeded`、map 未变（CapacityBytes 仍 old）
- delta==0：resize 到当前容量 → 断言**仍调** driver.Resize（C′ 幂等兜底）、无准入失败
- 卷不存在 → `volume.ErrVolumeNotFound`
- driver.Resize 失败 → 错误上抛、map CapacityBytes 仍 old（账本未推进）
- 非 block pool → `volume.ErrUnsupported`

fake driver 的 Resize 应可配置成功/失败 + 记录是否被调用。

- [ ] **Step 5: 运行验证**

Run: `go test ./internal/storage/pool/ -run TestServiceResizeVolume -v`
Expected: PASS

- [ ] **Step 6: 若失败修实现或陈旧测试**

fake driver 缺 Resize 记录 → 扩展 fake；记账断言不符 → 核对 allocatedLocked 求和逻辑。

- [ ] **Step 7: commit**

```bash
git add internal/storage/pool/resize.go internal/storage/pool/resize_test.go
git commit -m "feat: add pool ResizeVolume with delta-based pre-allocation accounting"
```

---

## Task 4: storage.VolumeService.ResizeVolume（VM-facing 入口）

**Files:**
- Modify: `internal/storage/service.go`（加 `ResizeVolumeRequest` + `ResizeVolume` 方法）
- Test: `internal/storage/service_test.go`

- [ ] **Step 1: 确认目标与验收证据**

Goal: `(*VolumeService).ResizeVolume(ctx, req)` 校验显式字段（PoolName/VolumeID/CapacityBytes 全必填，缺一 `ErrInvalidRequest`）→ 转 `pool.Service.ResizeVolume`。
验收证据：
- `go test ./internal/storage/ -run TestVolumeServiceResize -v` PASS

- [ ] **Step 2: 确认 pool 字段名**

读 `internal/storage/service.go:16` 附近确认 VolumeService 持有的 pool service 字段名（CreateVolume 用 `s.pools.CreateVolume`，故字段名 `pools`）。

- [ ] **Step 3: 写 ResizeVolumeRequest + ResizeVolume**

`internal/storage/service.go` 加（放在 `DeleteVolumeRequest` 之后或 SnapshotVolume 附近，保持窄入口聚集）：
```go
// ResizeVolumeRequest carries the explicit pool, volume id, and new declared
// capacity for an offline volume resize. Every field is required (显式优于隐式);
// the storage layer never infers pool, volume, or target capacity.
type ResizeVolumeRequest struct {
	PoolName      string
	VolumeID      volume.ID
	CapacityBytes int64
}

// ResizeVolume grows an existing volume's declared capacity within its pool
// (cold offline resize, increase-only). Convergence is declarative: callers
// pass the absolute target capacity and the pool/driver layers make the live
// qcow2 match it idempotently (driver reads live size; reaching the target is a
// no-op). Pre-allocation accounting and the actual qemu-img resize live in
// pool.Service.ResizeVolume.
func (s *VolumeService) ResizeVolume(ctx context.Context, req ResizeVolumeRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.PoolName == "" || req.VolumeID == "" || req.CapacityBytes <= 0 {
		return volume.ErrInvalidRequest
	}
	if _, err := s.pools.ResizeVolume(ctx, req.PoolName, req.VolumeID, req.CapacityBytes); err != nil {
		return err
	}
	return nil
}
```
注：核对 `volume.ID` 是否为 string 底层类型（`req.VolumeID == ""` 比较合法性）；若 ID 非 string-comparable 则改用其零值判定。

- [ ] **Step 4: 写测试**

`internal/storage/service_test.go` 加 `TestVolumeServiceResize`（复用既有 fake pool service 模式——核对该文件如何 fake `s.pools`）：
- 缺 PoolName / 缺 VolumeID / CapacityBytes<=0 → 各自 `ErrInvalidRequest`
- 合法 → 透传到 fake pool 的 ResizeVolume，参数原样
- fake pool 返回错误 → 上抛

- [ ] **Step 5: 运行验证**

Run: `go test ./internal/storage/ -run TestVolumeServiceResize -v`
Expected: PASS

- [ ] **Step 6: 若失败修实现或陈旧测试**

fake pool 接口缺 ResizeVolume → 扩展 fake（同时这会暴露：若 VolumeService 通过接口而非具体类型持有 pools，需在该接口加 ResizeVolume）。

- [ ] **Step 7: commit**

```bash
git add internal/storage/service.go internal/storage/service_test.go
git commit -m "feat: add VolumeService.ResizeVolume VM-facing entry"
```

---

## Task 5: VolumeController 注入 vmm + resize 收敛路径 + cold 门禁

**Files:**
- Modify: `internal/node/controllers/volume.go`（struct 加 vmm 字段、构造函数签名、Reconcile ready 分支、RootVolumeCreator 接口加 ResizeVolume）
- Create: `internal/node/controllers/volume_resize.go`（resize 收敛 helper，保持 volume.go 聚焦）
- Modify: `internal/node/agent.go:114`（NewVolumeController 传 vmmSvc）
- Test: `internal/node/controllers/volume_test.go`

- [ ] **Step 1: 确认目标与验收证据**

Goal: ready Volume reconcile 不再直接 `Done()`，而是：VM 对象 404→RequeueAfter；VM 非 cold→RequeueAfter + Message"等待 VM 停机"；cold→`ResizeVolume(spec.CapacityBytes)`，成功保持 Ready（no-op guard）、失败保持 Ready+Message+RequeueAfter；已收敛（driver 幂等跳过 + status 无变化）→Done。
验收证据：
- `go test ./internal/node/controllers/ -run TestVolumeReconcile -v` PASS（全分支）

- [ ] **Step 2: struct + 构造函数 + 接口扩展**

`internal/node/controllers/volume.go`：
- `RootVolumeCreator` 接口（:27-30）加：
```go
	ResizeVolume(ctx context.Context, req storage.ResizeVolumeRequest) error
```
- `VolumeController` struct（:66-70）加字段：
```go
	vmm     VMStatusReader
```
- `NewVolumeController` 签名加 `runner VMStatusReader` 形参并赋值（核对现有构造函数体）：
```go
func NewVolumeController(volumes RootVolumeCreator, images ImageGetter, runner VMStatusReader, client DependencyReader) *VolumeController {
	return &VolumeController{volumes: volumes, images: images, vmm: runner, client: client}
}
```
- 编译期断言处（:51-55 附近）确认 `*storage.VolumeService` 仍满足扩展后的 `RootVolumeCreator`（它已有 ResizeVolume 自 Task 4）。

- [ ] **Step 3: Reconcile ready 分支改为 resize 收敛**

`internal/node/controllers/volume.go` 把 `:136-138`：
```go
	if vol.Status.Phase == volumev1.VolumePhaseReady {
		return controller.Done(), nil
	}
```
替换为：
```go
	// Ready volume: drive cold-resize convergence instead of an unconditional
	// no-op. A grown spec.capacityBytes is applied once the owning VM is cold
	// (上下一致: gate on live phase, not the VM's status projection). See刀5 spec §6.
	if vol.Status.Phase == volumev1.VolumePhaseReady {
		return c.reconcileResize(ctx, vol)
	}
```

- [ ] **Step 4: 写 resize 收敛 helper**

`internal/node/controllers/volume_resize.go`：
```go
package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/storage"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// volumeResizeRequeueDelay paces the cold gate and transient-failure re-drive
// (the framework queue has no backoff). Same cadence as snapshotRequeueDelay.
const volumeResizeRequeueDelay = snapshotRequeueDelay

// reconcileResize drives cold-resize convergence for a ready volume. It is
// declarative强制收敛: it does not compare capacities — it hands the absolute
// target (spec.CapacityBytes) to VolumeService.ResizeVolume, and the driver
// decides idempotently whether a real qemu-img resize is needed (C′). The phase
// stays Ready on success and on failure (A2: a resize failure does not negate
// the volume's already-achieved usability); status no-op guard prevents PATCH
// churn.
func (c *VolumeController) reconcileResize(ctx context.Context, vol volumev1.Volume) (controller.ReconcileResult, error) {
	logger := zerolog.Ctx(ctx)

	// Resolve the owning VM object to read its live phase.
	vmRaw, err := c.client.Get(ctx, string(metav1.KindVM), vol.Spec.VMName)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			// Orphan volume (owning VM object deleted): wait, do not resize a
			// volume with no VM (决策6). No status change beyond a wait message.
			logger.Info().Str("volume", vol.Name).Str("vm", vol.Spec.VMName).
				Msg("owning VM object not found; waiting before resize")
			return controller.RequeueAfter(volumeResizeRequeueDelay), nil
		}
		return controller.Requeue(), fmt.Errorf("volume controller: read owning VM %q for resize: %w", vol.Spec.VMName, err)
	}

	var vm vmv1.VM
	if err := json.Unmarshal(vmRaw, &vm); err != nil {
		return controller.Requeue(), fmt.Errorf("volume controller: decode owning VM %q: %w", vol.Spec.VMName, err)
	}

	cold, err := vmIsCold(ctx, c.vmm, vm)
	if err != nil {
		return controller.Requeue(), fmt.Errorf("volume controller: cold gate for volume %q: %w", vol.Name, err)
	}
	if !cold {
		// cold-mutable暂缓: accept the spec change but defer apply until stopped.
		logger.Info().Str("volume", vol.Name).Str("vm", vol.Spec.VMName).
			Msg("owning VM not cold; deferring volume resize until stopped")
		return controller.RequeueAfter(volumeResizeRequeueDelay), nil
	}

	// Declarative强制收敛: hand the absolute target; driver reads live size and
	// no-ops if already >= target.
	if err := c.volumes.ResizeVolume(ctx, storage.ResizeVolumeRequest{
		PoolName:      vol.Spec.PoolRef,
		VolumeID:      deriveVolumeID(vol.Spec),
		CapacityBytes: vol.Spec.CapacityBytes,
	}); err != nil {
		// A2: keep Ready, record message, requeue. Do not flip to Failed.
		logger.Error().Err(err).Str("volume", vol.Name).
			Msg("volume resize failed; volume remains usable, will retry")
		return controller.RequeueAfter(volumeResizeRequeueDelay), fmt.Errorf("volume controller: resize %q: %w", vol.Name, err)
	}

	logger.Info().Str("volume", vol.Name).Int64("capacityBytes", vol.Spec.CapacityBytes).
		Msg("volume resize converged")
	return controller.Done(), nil
}
```

注 — 本 helper 的三个真实细节均已核实（写计划前已确认）：
1. **`client.ErrNotFound`**：node client 的 not-found sentinel 真名是 `client.ErrNotFound`（`internal/node/client/client.go:25`），volume.go gate 路径已用过（`storagePoolReady`/`imageReady` 对 `errors.Is(err, client.ErrNotFound)` 的处理，:236/:255）——复用同一 sentinel。
2. **VM 对象解码**：直接 `var vm vmv1.VM; json.Unmarshal(vmRaw, &vm)`（与 snapshot.go 解析 VM 对象的真实写法一致，snapshot.go:280-285 `Get(KindVM)`+`Unmarshal`）。`vmv1.VM` 含 `.Name`/`.UID`，原样传 `vmIsCold`。
3. **`deriveVolumeID(vol)`**：已确认 `func deriveVolumeID(spec volumev1.VolumeSpec) volume.ID` 存在于 `volume.go:353`（Knife1 teardown 建），形参是 `volumev1.VolumeSpec`——故调用为 `deriveVolumeID(vol.Spec)`，**不是** `deriveVolumeID(vol)`。`mapVolumeRole` 同在 `volume.go:331`。直接复用，不重复定义。

- [ ] **Step 5: agent.go 装配传 vmm**

`internal/node/agent.go:114` 把：
```go
		controllers.NewVolumeController(volumeSvc, imageSvc, master),
```
改为：
```go
		controllers.NewVolumeController(volumeSvc, imageSvc, vmmSvc, master),
```
（`vmmSvc` 已在 :106 构造，满足 `VMStatusReader`。）

- [ ] **Step 6: 写测试**

`internal/node/controllers/volume_test.go` 加 `TestVolumeReconcileResize*`（核对现有 fake `RootVolumeCreator`/`DependencyReader` + 加 fake `VMStatusReader`）：
- ready + VM 对象 404 → RequeueAfter，无 ResizeVolume 调用
- ready + VM 运行中（fake reader 返回 PhaseRunning）→ RequeueAfter，无 ResizeVolume 调用
- ready + VM cold（PhaseStopped）→ ResizeVolume 被调、参数 = (PoolRef, derivedID, spec.CapacityBytes)，返回 Done
- ready + VM cold + ResizeVolume 返回 err → RequeueAfter + 错误上抛，phase 未翻 Failed
- 现有非 ready 创建路径测试仍 PASS（行为保持——只改 ready 分支）

fake `RootVolumeCreator` 需加 ResizeVolume 记录；fake `VMStatusReader` 可配置 phase/err。**所有现有 NewVolumeController 测试调用点需补 fake VMStatusReader 实参**（签名变了）。

- [ ] **Step 7: 运行验证**

Run: `go test ./internal/node/controllers/ -run TestVolume -v`
Expected: PASS（resize 分支 + 现有创建/teardown 行为保持）

- [ ] **Step 8: 若失败修实现或陈旧测试**

签名变更导致旧测试编译失败 → 补 fake VMStatusReader 实参；派生 ID 不符 → 核对 teardown 真实派生函数。

- [ ] **Step 9: 编译全包 + commit**

Run: `go build ./... && go vet ./internal/node/...`
Expected: 退出 0
```bash
git add internal/node/controllers/volume.go internal/node/controllers/volume_resize.go internal/node/controllers/volume_test.go internal/node/agent.go
git commit -m "feat: drive cold volume resize convergence in VolumeController"
```

---

## Task 6: e2e 冷扩容场景（guest-side live-truth 验证）

**Files:**
- Modify: `test/e2e/closure_test.go`（VM 冷停机窗口内插入扩容场景）
- Modify: `test/e2e/guest.go`（加语义化方法 `QcowVirtualSize` / `AssertQcowVirtualSize`）
- Modify: 相关 Volume manifest（核对 `test/e2e` 下 Volume manifest 文件，capacityBytes 可被改大）

- [ ] **Step 1: 确认目标与验收证据**

Goal: `scripts/e2e.sh full` 在 VM 已 powerState=Off 的冷窗口内，apply 改大的 Volume capacityBytes，断言 Volume 仍 Ready，且 guest 内 `qemu-img info` 读到的 virtual size == 新目标值；负向：apply 缩小 capacityBytes → apiserver 409 拒绝。
验收证据：
- `scripts/e2e.sh full` 退出 0，日志含冷扩容 guest-side live-truth 断言命中

- [ ] **Step 2: guest.go 加 QcowVirtualSize 语义方法**

读 `test/e2e/guest.go` 确认 `Exec` 原语签名 + 既有 `AssertQcowHasSnapshot` 写法（刀4落地的 qcow2 路径 + guest-exec 模式）。加：
```go
// QcowVirtualSize runs `qemu-img info --output=json <path>` inside the guest and
// returns the qcow2 virtual size in bytes — live-truth容量, read straight from
// the file the node operates on (上下一致).
func (g *Guest) QcowVirtualSize(ctx context.Context, qcowPath string) (int64, error) {
	out, err := g.Exec(ctx, fmt.Sprintf("sudo qemu-img info --output=json %s", qcowPath))
	if err != nil {
		return 0, fmt.Errorf("guest qemu-img info %s: %w", qcowPath, err)
	}
	var info struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return 0, fmt.Errorf("guest parse qemu-img info %s: %w", qcowPath, err)
	}
	return info.VirtualSize, nil
}

// AssertQcowVirtualSize fails the test unless the guest-side qcow2 virtual size
// equals want.
func (g *Guest) AssertQcowVirtualSize(ctx context.Context, t *testing.T, qcowPath string, want int64) {
	t.Helper()
	got, err := g.QcowVirtualSize(ctx, qcowPath)
	if err != nil {
		t.Fatalf("read guest qcow virtual size: %v", err)
	}
	if got != want {
		t.Fatalf("guest qcow virtual size = %d, want %d", got, want)
	}
}
```
注：核对 guest.go 既有 import（fmt/json/testing/context）、`Exec` 真实签名与是否已 sudo（参照既有方法是否前缀 sudo）。qcow 路径派生复用刀4/重构后的 `guest_paths.go` 既有 qcow2 路径常量/函数。

- [ ] **Step 3: closure_test.go 插入冷扩容场景**

读 `test/e2e/closure_test.go` 确认刀2加的 VM powerState=Off 冷停机点位置 + Volume manifest 名/路径常量 + `runCtl`/`waitObjectPhase` helper。在 VM 已 Off（冷）之后、teardown 之前插入：
```go
// 冷扩容: grow the root volume's capacity while the VM is cold, then prove the
// live qcow2 actually grew (guest-side live-truth, 不只信 status).
coldResizeVolume(ctx, t, g)
```
并实现 `coldResizeVolume`（放 closure_test.go 或同包 _test.go）：
1. apply 改大 capacityBytes（旧值 2×）的 Volume manifest（用既有 apply helper + 改过的 manifest，或在测试里渲染）
2. `waitObjectPhase` Volume == Ready（A2，phase 不变）
3. `g.AssertQcowVirtualSize(ctx, t, rootVolQcowPath, newCapacityBytes)` — guest 内读真实 virtual size
4. 负向：apply 缩小 capacityBytes 的 manifest → 断言 govirtctl 收到 409（核对 runCtl 如何暴露 HTTP 状态/退出码）

注：核对 e2e 里 Volume manifest 的 capacityBytes 当前值，新目标取 2× 且不超 pool 容量（核对 StoragePool manifest 容量，避免 ErrPoolCapacityExceeded 误失败）。

- [ ] **Step 4: 运行快速编译验证**

Run: `go vet -tags e2e ./test/e2e/...`
Expected: 退出 0（e2e build tag 下编译通过）

- [ ] **Step 5: 运行 e2e 全闭环**

Run: `scripts/e2e.sh full`
Expected: 退出 0；日志含冷扩容场景：Volume apply→Ready、guest qemu-img info virtual size == 新值、缩小被 409 拒绝

- [ ] **Step 6: 若失败按 systematic-debugging 定位**

guest qemu-img info 路径错 → 核对 guest_paths.go 真实 qcow 路径；virtual size 不符 → 检查 driver resize 是否真执行（node 日志）；409 未触发 → 核对 admission 缩小拒绝 + runCtl 状态码暴露。

- [ ] **Step 7: commit**

```bash
git add test/e2e/closure_test.go test/e2e/guest.go
# + 改动的 Volume manifest（若有独立文件）
git commit -m "test(e2e): verify cold volume resize with guest-side live-truth"
```

---

## Task 7: 全量验证

**Files:** 无（验证 only）

- [ ] **Step 1: 确认目标**

Goal: 全部门禁绿——gofmt、单测、race、跨平台 linux 构建、e2e 全闭环。

- [ ] **Step 2: 本地 CI 等价**

Run: `scripts/verify.sh`
Expected: 退出 0（gofmt -l 空 + go test ./... + 三 main 构建）

- [ ] **Step 3: race**

Run: `go test -race ./internal/storage/... ./internal/node/...`
Expected: 退出 0（ResizeVolume 锁正确性、控制器并发）

- [ ] **Step 4: 跨平台 linux 构建 + vet**

Run: `GOOS=linux GOARCH=arm64 go build ./... && go vet ./...`
Expected: 退出 0

- [ ] **Step 5: e2e 全闭环**

Run: `scripts/e2e.sh full`
Expected: 退出 0（含冷扩容 guest-side live-truth 场景）

- [ ] **Step 6: 若任一失败**

按 systematic-debugging 定位根因后修，不跳过证据。

- [ ] **Step 7: 无新增 commit（验证 only）**

验证全绿即 Task 完成；若修了东西，按所属 Task 的文件归属补 commit。

---

## 自审清单（写完计划后）

- **Spec 覆盖**：决策1（cold gate 注入 vmm）→T1+T5；决策2（C′ driver 幂等）→T2；决策3（status 不加字段）→全程不加；决策4（预分配按 delta 记账）→T3；决策5（A2 失败保持 Ready）→T5;决策6（VM 404 RequeueAfter）→T5；顺序约束（reserve→resize→改 map）→T3；e2e live-truth→T6。✓ 全覆盖。
- **行数**：pool ResizeVolume 入新文件（service.go 已超限不再撑大）；volume resize 入新文件 volume_resize.go；local driver.go +~30 under 800。✓
- **类型一致**：`ResizeVolumeRequest`（T4）字段 PoolName/VolumeID/CapacityBytes 与 T5 调用一致；`VMStatusReader`（T1）与 T5 字段类型一致；`pool.Service.ResizeVolume` 签名（T3）与 VolumeService 调用（T4）一致。✓
- **占位**：无。T5 helper 直接用真实 `json.Unmarshal` 到 `vmv1.VM` + 已确认存在的 `deriveVolumeID(vol.Spec)`（volume.go:353）/`mapVolumeRole`（volume.go:331）；`client.ErrNotFound`（client.go:25）真名已核实。所有引用解析到真实符号。✓
