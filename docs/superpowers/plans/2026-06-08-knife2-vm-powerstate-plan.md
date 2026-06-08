# 刀 2 实现计划：VM powerState 停止 / 启动生命周期

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 VM API 增加显式 `spec.powerState`，让用户用声明式 `On` / `Shutdown` / `Off` 表达 ESXi 风格电源意图，并让节点控制器通过真实 live QEMU/QMP 状态收敛启动、ACPI 关机、强制断电。

**Architecture:** 刀 2 保持 k8s 式 level-triggered reconcile，但用户体验对齐 ESXi：`On` / `Off` 是物理电源状态，`Shutdown` 是软关机动作。apiserver apply 路径增加通用 create/update admission 分类，create 拒绝 `Shutdown`；VM controller 将当前“创建即启动”改为 powerState 矩阵，并通过新 `RequeueAfter` 结果避免长期 `ShutdownRequested` busy-loop。

**Tech Stack:** Go 1.26 + 自建 apiserver/store + 自建 controller-manager + `internal/vmm` QMP/daemonized QEMU 生命周期 + 现有 `govirtctl apply/get` + `scripts/e2e.sh full` 三节点闭环。

---

## 文件结构（第十八章体量预判）

**当前行数（已验证）**：
- `pkg/apis/vm/v1alpha1/types.go` 75 行 → 约 120 行，安全。
- `internal/controlplane/apiserver/handler_apply.go` 426 行 → 约 470 行；只保留 apply 主流程调用，不继续堆复杂 admission 细节。
- `internal/node/controller/controller.go` 48 行 → 约 90 行，安全。
- `internal/node/controller/loop.go` 142 行 → 约 190 行，安全。
- `internal/node/controllers/vm.go` 481 行 → 已到软上限边缘；刀 2 禁止把全部电源矩阵继续塞进该文件。
- `test/e2e/closure_test.go` 248 行 → 约 340 行，安全。

**新建文件**：
- `internal/node/controllers/vm_power.go` — VM 电源状态映射、desired/observed/transition helper、`RequeueAfter` 常量（~140 行）。避免 `vm.go` 超过 600 行并保持单一职责。
- `internal/node/controllers/vm_power_test.go` — 纯 helper 矩阵测试（~180 行）。
- `internal/controlplane/apiserver/apply_admission.go` — `ApplyOperation` / `classifyApply` / VM create/update admission helper（~120 行），避免 `handler_apply.go` 继续接近软上限。

**修改文件**：
- `pkg/apis/vm/v1alpha1/types.go` — `PowerState`、`ObservedPowerState`、`PowerTransition` 强类型常量；`VMSpec.PowerState`；`VMStatus` 两个结构化字段。
- `pkg/apis/vm/v1alpha1/types_test.go` — spec/status JSON round-trip + validate。
- `internal/controlplane/apiserver/handler_apply.go` + tests — 通用 create/update 分类；create `Shutdown` admission 拒绝；VM update 保留既有 node binding，避免 power update 变成隐式迁移。
- `internal/node/controller/controller.go` / `loop.go` / `queue.go` + tests — `ReconcileResult` / `RequeueAfter` / 延迟 requeue。
- `internal/node/controllers/*.go` + `*_test.go` — 将 6 控制器从 `(bool, error)` 迁移到 `controller.ReconcileResult`，保持非 VM 行为不变。
- `internal/node/controllers/vm.go` + `vm_test.go` — `VMRunner.Stop`、powerState 收敛矩阵、status no-op guard 继续有效。
- `test/e2e/manifests/07-vm.json` — 初始 `powerState=Off`（先定义不启动）。
- `test/e2e/closure_test.go` — Off create → On start → Shutdown request → Off force poweroff → Shutdown create rejection。

---

## Task 1: API 契约 — VM powerState + 结构化电源 status

**Files:**
- Modify: `pkg/apis/vm/v1alpha1/types.go`
- Test: `pkg/apis/vm/v1alpha1/types_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: VM spec 必须显式声明 `powerState`；status 始终携带机器可读的 `observedPowerState` 与 `powerTransition`。

Acceptance evidence:
- `go test ./pkg/apis/vm/...` PASS。
- 缺少 `powerState` 的 `VMSpec.Validate()` 返回 `ErrInvalidSpec`。
- `PowerTransitionNone` 的 JSON 值是显式字符串 `"None"`，不是空字符串。

- [ ] **Step 2: 添加强类型常量与字段**

在 `pkg/apis/vm/v1alpha1/types.go` 中新增：

```go
// PowerState is the user's desired VM power intent.
type PowerState string

const (
	PowerStateOn       PowerState = "On"
	PowerStateShutdown PowerState = "Shutdown"
	PowerStateOff      PowerState = "Off"
)

// ObservedPowerState is the physical power state derived from live QEMU/QMP.
type ObservedPowerState string

const (
	ObservedPowerStateOn  ObservedPowerState = "On"
	ObservedPowerStateOff ObservedPowerState = "Off"
)

// PowerTransition describes the current convergence action.
type PowerTransition string

const (
	PowerTransitionNone              PowerTransition = "None"
	PowerTransitionStarting          PowerTransition = "Starting"
	PowerTransitionShutdownRequested PowerTransition = "ShutdownRequested"
	PowerTransitionPoweringOff       PowerTransition = "PoweringOff"
)
```

修改 `VMSpec` 与 `VMStatus`：

```go
type VMSpec struct {
	Arch       string     `json:"arch"`
	VCPUs      int        `json:"vcpus"`
	MemoryMiB  int        `json:"memoryMiB"`
	VolumeRefs []string   `json:"volumeRefs"`
	NICRefs    []string   `json:"nicRefs"`
	PowerState PowerState `json:"powerState"`
}

type VMStatus struct {
	Phase              VMPhase            `json:"phase"`
	ObservedPowerState ObservedPowerState `json:"observedPowerState"`
	PowerTransition    PowerTransition    `json:"powerTransition"`
	Message            string             `json:"message,omitempty"`
}
```

- [ ] **Step 3: 更新 Validate**

`VMSpec.Validate()` 追加：

```go
switch s.PowerState {
case PowerStateOn, PowerStateShutdown, PowerStateOff:
default:
	return fmt.Errorf("%w: powerState must be On, Shutdown, or Off", ErrInvalidSpec)
}
```

注意：这里仅验证枚举合法性；`create + Shutdown` 是 apiserver admission 语义，不能放到纯 spec validate 里，因为 validate 不知道 create/update。

- [ ] **Step 4: 添加 API 测试**

新增或扩展测试，测试体必须覆盖以下断言：

```go
func TestVMSpecValidateRequiresExplicitPowerState(t *testing.T) {
	spec := VMSpec{Arch: "aarch64", VCPUs: 1, MemoryMiB: 512, VolumeRefs: []string{"root"}, NICRefs: []string{"nic"}}
	if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("Validate() error = %v, want ErrInvalidSpec", err)
	}
}

func TestVMSpecValidateAcceptsKnownPowerStates(t *testing.T) {
	for _, state := range []PowerState{PowerStateOn, PowerStateShutdown, PowerStateOff} {
		spec := VMSpec{Arch: "aarch64", VCPUs: 1, MemoryMiB: 512, VolumeRefs: []string{"root"}, NICRefs: []string{"nic"}, PowerState: state}
		if err := spec.Validate(); err != nil {
			t.Fatalf("Validate(%s) error = %v", state, err)
		}
	}
}

func TestVMStatusPowerFieldsRoundTrip(t *testing.T) {
	status := VMStatus{Phase: VMPhaseDefined, ObservedPowerState: ObservedPowerStateOff, PowerTransition: PowerTransitionNone}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"observedPowerState":"Off"`)) || !bytes.Contains(data, []byte(`"powerTransition":"None"`)) {
		t.Fatalf("encoded status %s does not include explicit power fields", data)
	}
}
```

Round-trip 测试必须断言 JSON 含：

```json
"observedPowerState":"Off"
"powerTransition":"None"
```

- [ ] **Step 5: 更新现有 VM 测试 fixture**

所有 `validVMObject()` / manifests / API fixture 都必须显式设置 `PowerState`。测试中不要依赖默认零值。

- [ ] **Step 6: 验证并提交**

Run: `go test ./pkg/apis/vm/...`
Expected: PASS

```bash
git add pkg/apis/vm/v1alpha1
git commit -m "feat(apis): add explicit VM powerState contract"
```

---

## Task 2: apiserver apply create/update 分类 + VM create admission

**Files:**
- Modify: `internal/controlplane/apiserver/handler_apply.go`
- Create: `internal/controlplane/apiserver/apply_admission.go`
- Test: `internal/controlplane/apiserver/handler_apply_test.go` or new `apply_admission_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: apply 路径通用识别 create/update，但仍保持当前 unconditional replace Put 语义；VM create 拒绝 `spec.powerState=Shutdown`，update 允许，且 power update 不能重新调度或清空既有 node binding。

Acceptance evidence:
- create VM + `Shutdown` → HTTP 400。
- create VM + `On` / `Off` → accepted。
- update existing VM + `Shutdown` → accepted。
- update existing VM 时 body 省略 `metadata.nodeName` 会保留已有 node binding。
- update existing VM 时 body 显式给出不同 `metadata.nodeName` 会 400，避免把 power update 伪装成迁移。
- `store.Put(ctx, key, data, "")` 仍使用空 expectedVersion，不引入 CAS。

- [ ] **Step 2: 添加操作分类类型**

```go
type ApplyOperation string

const (
	ApplyOperationCreate ApplyOperation = "Create"
	ApplyOperationUpdate ApplyOperation = "Update"
)
```

实现：

```go
func (s *Server) classifyApply(ctx context.Context, key string) (ApplyOperation, []byte, error) {
	if existing, err := s.store.Get(ctx, key); err == nil {
		return ApplyOperationUpdate, existing, nil
	} else if errors.Is(err, store.ErrNotFound) {
		return ApplyOperationCreate, nil, nil
	} else {
		return "", nil, err
	}
}
```

如 `store.ErrNotFound` 的实际名字不同，以 `internal/controlplane/store` 真实 sentinel 为准；不得 string-match error。

- [ ] **Step 3: 在 VM apply admission 使用分类**

在 `applyVM` 或 VM-specific admission 路径中，在 `put` 前调用：

```go
op, existingRaw, err := s.classifyApply(ctx, key)
if err != nil {
	return nil, fmt.Errorf("classify apply %s: %w", key, err)
}
if op == ApplyOperationCreate && obj.Spec.PowerState == vmv1.PowerStateShutdown {
	return nil, fmt.Errorf("%w: powerState Shutdown is only valid for updates", vmv1.ErrInvalidSpec)
}
```

保留现有调度绑定、finalizer 注入、MAC 分配行为；分类不能改变这些 admission mutation 的顺序语义。

- [ ] **Step 4: VM update 保留或校验 node binding**

`metadata.nodeName` 是调度结果，不应在用户只更新 `spec.powerState` 时被重新调度或清空。`op == ApplyOperationUpdate` 时，先解码 `existingRaw`：

```go
if op == ApplyOperationUpdate {
	var existing vmv1.VM
	if err := json.Unmarshal(existingRaw, &existing); err != nil {
		return nil, fmt.Errorf("decode existing VM %s: %w", obj.Name, err)
	}
	if obj.NodeName == "" {
		obj.NodeName = existing.NodeName
	} else if existing.NodeName != "" && obj.NodeName != existing.NodeName {
		return nil, fmt.Errorf("%w: nodeName is immutable for VM update", vmv1.ErrInvalidSpec)
	}
}
```

这不是默认调度；它是保留已经存在的控制面绑定，防止 power update 变成隐式计算迁移。真正迁移属于未来 Job spec，不在本刀。

- [ ] **Step 5: 添加 admission 测试**

使用 fake store：
- 空 store apply `VM{PowerState: Shutdown}` → response 400。
- 空 store apply `VM{PowerState: Off}` → 201/accepted。
- 先 apply `Off`，再 apply 同名 `Shutdown` → accepted。
- 先 apply `On` 得到 `nodeName=node0`，再用省略 `nodeName` 的同名 manifest 更新 `Shutdown` → stored object 仍为 `node0`。
- 先 apply `On` 得到 `nodeName=node0`，再用 `nodeName=node1` 的同名 manifest 更新 → 400。

同时断言 update 后对象的 `spec.powerState` 是 `Shutdown`。

- [ ] **Step 6: 验证并提交**

Run: `go test ./internal/controlplane/apiserver/...`
Expected: PASS

```bash
git add internal/controlplane/apiserver
git commit -m "feat(apiserver): classify apply create vs update"
```

---

## Task 3: controller framework — ReconcileResult + RequeueAfter

**Files:**
- Modify: `internal/node/controller/controller.go`
- Modify: `internal/node/controller/loop.go`
- Modify: `internal/node/controller/queue.go` if an `AddAfter` helper is preferred
- Test: `internal/node/controller/loop_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: controller framework 支持显式延迟重试，避免 `ShutdownRequested` 等长期未收敛状态 busy-loop。

Acceptance evidence:
- 现有立即 requeue 行为仍可表达。
- `RequeueAfter(10ms)` 不会立即再次调用 reconcile。
- context cancel 时延迟 requeue goroutine/timer 不泄漏、不阻塞退出。

- [ ] **Step 2: 定义 ReconcileResult**

在 `controller.go`：

```go
type ReconcileResult struct {
	Requeue      bool
	RequeueAfter time.Duration
}

func Done() ReconcileResult { return ReconcileResult{} }

func Requeue() ReconcileResult { return ReconcileResult{Requeue: true} }

func RequeueAfter(delay time.Duration) ReconcileResult {
	if delay <= 0 {
		return Requeue()
	}
	return ReconcileResult{RequeueAfter: delay}
}

func (r ReconcileResult) shouldRequeue() bool {
	return r.Requeue || r.RequeueAfter > 0
}
```

`Controller` 接口改为：

```go
Reconcile(ctx context.Context, ev Event) (ReconcileResult, error)
```

- [ ] **Step 3: loop 支持延迟 requeue**

在 `loop.go` 中把 `(requeue bool, err error)` 改为 result：

```go
result, err := c.Reconcile(ctx, ev)
if err != nil && !result.shouldRequeue() {
	result = Requeue()
}
switch {
case result.RequeueAfter > 0:
	scheduleRequeue(ctx, q, ev, result.RequeueAfter)
case result.Requeue:
	q.Add(ev)
}
```

新增 owner-bound helper，禁止无 owner fire-and-forget：

```go
func scheduleRequeue(ctx context.Context, q *Queue, ev Event, delay time.Duration) {
	timer := time.NewTimer(delay)
	go func() {
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			q.Add(ev)
		}
	}()
}
```

该 goroutine 由 `runController` 的 `ctx` 拥有，ctx cancel 或 queue shutdown 后不会 panic；`q.Add` 已经保证 closed queue 上 drop。

- [ ] **Step 4: 更新错误日志字段**

把原 `Bool("requeue", requeue)` 改成：

```go
.Bool("requeue", result.shouldRequeue()).
.Dur("requeue_after", result.RequeueAfter)
```

- [ ] **Step 5: 添加 loop 测试**

覆盖：
- `Done()` 不 requeue。
- `Requeue()` 立即重试。
- `RequeueAfter(20*time.Millisecond)` 延迟重试：第一次 reconcile 后 5ms 内不应第二次，50ms 内应第二次。
- `err` 且 result empty → 自动立即 requeue + error log。

- [ ] **Step 6: 验证并提交**

Run: `go test ./internal/node/controller/...`
Expected: PASS

```bash
git add internal/node/controller
git commit -m "feat(controller): support delayed reconcile requeue"
```

---

## Task 4: 迁移 6 个节点控制器到 ReconcileResult（行为不变）

**Files:**
- Modify: `internal/node/controllers/storagepool.go` + `_test.go`
- Modify: `internal/node/controllers/image.go` + `_test.go`
- Modify: `internal/node/controllers/volume.go` + `_test.go`
- Modify: `internal/node/controllers/network.go` + `_test.go`
- Modify: `internal/node/controllers/nic.go` + `_test.go`
- Modify: `internal/node/controllers/vm.go` + `_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: 全部 controller 编译通过，除 VM 后续 power task 外，原有 requeue 语义不变：原 `false` → `controller.Done()`，原 `true` → `controller.Requeue()`。

Acceptance evidence:
- `go test ./internal/node/controllers/...` PASS。
- 迁移提交不改变业务行为，只改返回类型。

- [ ] **Step 2: 批量改返回语句**

规则：

```go
return false, nil  -> return controller.Done(), nil
return true, nil   -> return controller.Requeue(), nil
return true, err   -> return controller.Requeue(), err
return false, err  -> return controller.Done(), err
```

不要在本任务引入 `RequeueAfter`；延迟重试只在 Task 6 的 VM power convergence 使用。

- [ ] **Step 3: 批量改测试断言**

测试从：

```go
requeue, err := c.Reconcile(ctx, ev)
if err != nil {
	t.Fatalf("Reconcile() error = %v", err)
}
if !requeue {
	t.Fatalf("Reconcile() requeue = false, want true")
}
```

改为：

```go
result, err := c.Reconcile(ctx, ev)
if err != nil {
	t.Fatalf("Reconcile() error = %v", err)
}
if !result.Requeue {
	t.Fatalf("Reconcile() result.Requeue = false, want true")
}
if result.RequeueAfter != 0 {
	t.Fatalf("Reconcile() result.RequeueAfter = %s, want 0", result.RequeueAfter)
}
```

- [ ] **Step 4: 验证并提交**

Run: `go test ./internal/node/controllers/...`
Expected: PASS

```bash
git add internal/node/controllers
git commit -m "refactor(node): migrate controllers to ReconcileResult"
```

---

## Task 5: VMRunner 接口补 Stop + VM 电源 helper

**Files:**
- Modify: `internal/node/controllers/vm.go`
- Create: `internal/node/controllers/vm_power.go`
- Create: `internal/node/controllers/vm_power_test.go`
- Modify: `internal/node/controllers/vm_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: VM controller 的窄 `VMRunner` 接口包含 `Stop(ctx, uuid)`，并有独立 helper 把 live `vmm.Phase` 映射到 `observedPowerState` / `powerTransition`。

Acceptance evidence:
- fakeVMRunner 可记录 Stop 调用。
- unknown vmm phase 仍 warn + Failed，但 status 电源字段不为空。

- [ ] **Step 2: 扩展 VMRunner**

在 `vm.go`：

```go
type VMRunner interface {
	Create(ctx context.Context, req vmm.CreateRequest) (vmm.VM, error)
	Start(ctx context.Context, uuid string) (vmm.VM, error)
	Stop(ctx context.Context, uuid string) error
	Kill(ctx context.Context, uuid string) error
	Status(ctx context.Context, uuid string) (vmm.VM, error)
	Delete(ctx context.Context, uuid string) error
}
```

`*vmm.VMMService` 已有 `Stop(ctx, uuid string) error`，无需改 `internal/vmm/lifecycle.go`。

- [ ] **Step 3: 新建 power helper 文件**

`internal/node/controllers/vm_power.go`：

```go
const vmPowerRequeueDelay = 3 * time.Second

type vmPowerObservation struct {
	Phase      vmv1.VMPhase
	Observed   vmv1.ObservedPowerState
	Transition vmv1.PowerTransition
	KnownPhase bool
}

func observePower(phase vmm.Phase, desired vmv1.PowerState) vmPowerObservation {
	apiPhase, known := mapVMPhase(phase)
	observed := vmv1.ObservedPowerStateOff
	if phase == vmm.PhaseRunning || phase == vmm.PhaseStarting || phase == vmm.PhaseStopping {
		observed = vmv1.ObservedPowerStateOn
	}
	transition := vmv1.PowerTransitionNone
	switch desired {
	case vmv1.PowerStateOn:
		if observed == vmv1.ObservedPowerStateOff || phase == vmm.PhaseStarting {
			transition = vmv1.PowerTransitionStarting
		}
	case vmv1.PowerStateShutdown:
		if observed == vmv1.ObservedPowerStateOn {
			transition = vmv1.PowerTransitionShutdownRequested
		}
	case vmv1.PowerStateOff:
		if observed == vmv1.ObservedPowerStateOn {
			transition = vmv1.PowerTransitionPoweringOff
		}
	}
	return vmPowerObservation{Phase: apiPhase, Observed: observed, Transition: transition, KnownPhase: known}
}

func vmStatus(obs vmPowerObservation, message string) vmv1.VMStatus {
	return vmv1.VMStatus{Phase: obs.Phase, ObservedPowerState: obs.Observed, PowerTransition: obs.Transition, Message: message}
}

func powerNeedsDelayedRequeue(obs vmPowerObservation) bool {
	return obs.Transition == vmv1.PowerTransitionStarting ||
		obs.Transition == vmv1.PowerTransitionShutdownRequested ||
		obs.Transition == vmv1.PowerTransitionPoweringOff
}
```

- [ ] **Step 4: helper 测试**

`vm_power_test.go` 覆盖矩阵：
- `On + PhaseRunning` → `Observed=On, Transition=None`。
- `On + PhaseStopped` → `Observed=Off, Transition=Starting`。
- `Shutdown + PhaseRunning` → `Observed=On, Transition=ShutdownRequested`。
- `Shutdown + PhaseStopped` → `Observed=Off, Transition=None`。
- `Off + PhaseRunning` → `Observed=On, Transition=PoweringOff`。
- `Off + PhaseDefined/Stopped/Failed` → `Observed=Off, Transition=None`。

- [ ] **Step 5: fakeVMRunner 补 Stop**

`vm_test.go` fake 加：

```go
stopErr error
stopCalls int

func (f *fakeVMRunner) Stop(ctx context.Context, uuid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	return f.stopErr
}
```

- [ ] **Step 6: 验证并提交**

Run: `go test ./internal/node/controllers/...`
Expected: PASS

```bash
git add internal/node/controllers/vm.go internal/node/controllers/vm_power.go internal/node/controllers/vm_power_test.go internal/node/controllers/vm_test.go
git commit -m "feat(node): add VM power observation helpers"
```

---

## Task 6: VM controller powerState 收敛矩阵 + fresh status guard

**Files:**
- Modify: `internal/node/controllers/vm.go`
- Modify: `internal/node/controllers/vm_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: VM controller 不再“对象存在就只 re-report phase 并早返回”，而是按 `spec.powerState` 收敛：`Off` create 只定义，`On` start，`Shutdown` ACPI，`Off` running force kill。延迟自重试时 status no-op guard 必须读取最新 VM 对象，不能拿旧 watch event 的旧 status 比较。

Acceptance evidence:
- `Off` create: `Create` called, `Start` not called, status `Off/None`。
- `On` create: `Create` + `Start`, status eventually `On/None`。
- `Shutdown` running: `Stop` called, result `RequeueAfter(vmPowerRequeueDelay)`。
- `Off` running: `Kill` called, result `RequeueAfter(vmPowerRequeueDelay)`。
- existing VM `Defined/Stopped/Failed + PowerStateOn` 会调用 `Start`，不能被当前 `Status` 成功早返回吞掉。
- delayed self-requeue 后如果 status 已经等于 desired，不会因为旧 `Event.Object` 里 status 陈旧而重复 PATCH。

- [ ] **Step 2: 重构 normal reconcile 为 probe → create/converge 两段**

当前 `vm.go` 的风险点是：`runner.Status` 成功后直接 patch phase 并 return，因此所有 update power intent 都会被吞掉。改为：

```go
live, err := c.runner.Status(ctx, obj.UID)
switch {
case errors.Is(err, vmm.ErrNotFound):
	return c.reconcileMissingVM(ctx, obj)
case err != nil:
	return controller.RequeueAfter(vmPowerRequeueDelay), err
default:
	return c.reconcileExistingVM(ctx, obj, live)
}
```

`reconcileExistingVM` 必须读取 `obj.Spec.PowerState` 并执行矩阵，不能只 re-report `live.Phase`。

- [ ] **Step 3: create path 按 powerState 分支**

在 `Status == vmm.ErrNotFound` 且依赖 Ready 后：
- `PowerStateOff`: `Create` 后 patch `Defined + Off + None`，不调用 `Start`。
- `PowerStateOn`: `Create` 后调用 `Start`。
- `PowerStateShutdown`: 理论上 apiserver 已拒绝；controller 防御性 patch Failed 或返回 invalid error，但不要 Start/Stop/Kill。

核心伪码：

```go
created, err := c.runner.Create(ctx, req)
if err != nil {
	desired := vmStatus(observePower(vmm.PhaseFailed, obj.Spec.PowerState), err.Error())
	if patchErr := c.patchVMStatusIfChanged(ctx, obj.Name, desired); patchErr != nil {
		return controller.RequeueAfter(vmPowerRequeueDelay), errors.Join(err, patchErr)
	}
	return controller.RequeueAfter(vmPowerRequeueDelay), err
}
if obj.Spec.PowerState == vmv1.PowerStateOff {
	desired := vmStatus(observePower(created.Phase, obj.Spec.PowerState), "")
	if err := c.patchVMStatusIfChanged(ctx, obj.Name, desired); err != nil {
		return controller.RequeueAfter(vmPowerRequeueDelay), err
	}
	return controller.Done(), nil
}
started, err := c.runner.Start(ctx, obj.UID)
if err != nil {
	desired := vmStatus(observePower(created.Phase, obj.Spec.PowerState), err.Error())
	if patchErr := c.patchVMStatusIfChanged(ctx, obj.Name, desired); patchErr != nil {
		return controller.RequeueAfter(vmPowerRequeueDelay), errors.Join(err, patchErr)
	}
	return controller.RequeueAfter(vmPowerRequeueDelay), err
}
desired := vmStatus(observePower(started.Phase, obj.Spec.PowerState), "")
if err := c.patchVMStatusIfChanged(ctx, obj.Name, desired); err != nil {
	return controller.RequeueAfter(vmPowerRequeueDelay), err
}
if powerNeedsDelayedRequeue(observePower(started.Phase, obj.Spec.PowerState)) {
	return controller.RequeueAfter(vmPowerRequeueDelay), nil
}
return controller.Done(), nil
```

- [ ] **Step 4: existing path 按矩阵分支**

`runner.Status` 返回 existing VM 后：

```go
switch obj.Spec.PowerState {
case vmv1.PowerStateOn:
	if live.Phase == vmm.PhaseDefined || live.Phase == vmm.PhaseStopped || live.Phase == vmm.PhaseFailed {
		live, err = c.runner.Start(ctx, obj.UID)
	}
case vmv1.PowerStateShutdown:
	if observePower(live.Phase, obj.Spec.PowerState).Observed == vmv1.ObservedPowerStateOn {
		err = c.runner.Stop(ctx, obj.UID)
	}
case vmv1.PowerStateOff:
	if observePower(live.Phase, obj.Spec.PowerState).Observed == vmv1.ObservedPowerStateOn {
		err = c.runner.Kill(ctx, obj.UID)
	}
}
```

`Stop/Kill/Start` 失败时 patch 当前 observed + 对应 transition + message，然后返回 delayed requeue + error；不要自动升级 Shutdown→Off。

- [ ] **Step 5: status no-op guard 改为 fresh GET**

延迟 requeue 重新加入的是同一个旧 `Event`。如果继续用 `obj.Status == desired` 判断，旧 event 里的 status 永远是第一次 patch 前的旧值，会绕过 Knife 1 的 no-op guard 并反复 PATCH。所有 VM status patch 必须统一走 fresh GET：

```go
func (c *VMController) patchVMStatusIfChanged(ctx context.Context, name string, desired vmv1.VMStatus) error {
	raw, err := c.client.Get(ctx, string(metav1.KindVM), name)
	if err != nil {
		return err
	}
	var current vmv1.VM
	if err := json.Unmarshal(raw, &current); err != nil {
		return err
	}
	if current.Status == desired {
		return nil
	}
	return c.patchStatus(ctx, name, desired)
}
```

`VMController` 现在的依赖 reader 已经有 `Get(ctx, kind, name)`，真实 `client.Client` 可直接 GET VM；测试 fake 必须支持 `KindVM` 返回当前 VM 对象，并在 `PatchStatus` 后更新 fake 内部 VM status，使 no-op guard 与真实 apiserver 一致。

- [ ] **Step 6: delayed requeue 规则**

当 `powerNeedsDelayedRequeue(obs)` 为 true，返回：

```go
return controller.RequeueAfter(vmPowerRequeueDelay), nil
```

错误场景返回：

```go
return controller.RequeueAfter(vmPowerRequeueDelay), err
```

VM controller 内所有等待 live 状态变化的路径都用延迟重试，禁止 busy-loop。适用路径：
- `Starting` / `Stopping` transient phase；
- `ShutdownRequested`；
- `PoweringOff`；
- `PowerStateOn` 但 observed still Off after `Start`；
- VM 依赖 Volume/NIC 暂未 Ready（使用同一个 `vmPowerRequeueDelay` 或另一个显式 `vmDependencyRequeueDelay`）。

非 VM controller 在本刀可以保持立即 `controller.Requeue()`；本刀只强制 VM power convergence 不 busy-loop。

- [ ] **Step 7: 更新 VM controller 测试**

新增/更新测试：
- `TestVMReconcilePowerOffCreateDefinesWithoutStart`
- `TestVMReconcilePowerOnCreateStarts`
- `TestVMReconcilePowerOnExistingStoppedStarts`
- `TestVMReconcilePowerOnExistingDefinedStarts`
- `TestVMReconcilePowerOnExistingFailedStarts`
- `TestVMReconcileShutdownRunningRequestsStopWithDelayedRequeue`
- `TestVMReconcileShutdownStoppedIsConvergedNoOp`
- `TestVMReconcilePowerOffRunningKillsWithDelayedRequeue`
- `TestVMReconcileStatusNoOpIncludesPowerFields`
- `TestVMReconcileStatusNoOpUsesFreshVMStatus`
- `TestVMReconcilePowerErrorStatusIncludesPowerFields`
- `TestVMReconcileUnknownPhaseStillWritesStructuredPowerStatus`

- [ ] **Step 8: 验证并提交**

Run: `go test ./internal/node/controllers/...`
Expected: PASS

```bash
git add internal/node/controllers/vm.go internal/node/controllers/vm_test.go internal/node/controllers/vm_power.go internal/node/controllers/vm_power_test.go
git commit -m "feat(node): reconcile VM powerState intent"
```

---

## Task 7: e2e manifests + distributed power lifecycle closure

**Files:**
- Modify: `test/e2e/manifests/07-vm.json`
- Modify: `test/e2e/closure_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: 现有三节点 e2e 证明 Off define → On start → Shutdown request → Off force poweroff → delete teardown，全程不另起拓扑。

Acceptance evidence:
- `scripts/e2e.sh full` PASS。
- e2e 日志显示 VM 先 `observedPowerState: Off`，再 Running/On，ShutdownRequested 或 Off/None，最后 Off/None。

- [ ] **Step 2: 初始 manifest 改为 powerState Off**

`test/e2e/manifests/07-vm.json` 的 `spec` 加：

```json
"powerState": "Off"
```

注意：其它资源 manifest 不变。

- [ ] **Step 3: closure_test 增加 power apply helper**

新增 helper：复制 `07-vm.json` 到测试工作目录，替换 `"powerState": "Off"` 为目标值，然后 `govirtctl apply -f`。不要在仓库根或 `/tmp` 写临时文件；路径用 e2e 脚本提供的工作目录或 `t.TempDir()`。

- [ ] **Step 4: forward segment 改为四段**

在 `TestDistributedSpineClosure`：
1. `applySpine` 后等待 VM `observedPowerState: Off` + `powerTransition: None`。
2. apply VM `On`，等待 `phase: running` + `observedPowerState: On` + `powerTransition: None`。
3. apply VM `Shutdown`，等待：
   - either `observedPowerState: On` + `powerTransition: ShutdownRequested`
   - or `observedPowerState: Off` + `powerTransition: None`
4. apply VM `Off`，等待 `observedPowerState: Off` + `powerTransition: None`。

- [ ] **Step 5: create Shutdown rejection 测试**

新增独立 VM manifest（不同 name/uid，例如 `vm-shutdown-create-rejected`）并设置 `powerState=Shutdown`，apply 预期失败且输出包含 `Shutdown is only valid for updates` 或 admission 错误文本。

- [ ] **Step 6: teardown 仍复用 Knife 1 逆序删除**

Off 后再 delete VM，delete teardown 不再需要先让 VM running；但必须继续证明 finalizer two-phase 和 host orphan check。

- [ ] **Step 7: 验证并提交**

Run: `go test -tags e2e ./test/e2e/...`
Expected: PASS or skip unless env set（本地无 env 时 skip 是正常）。

```bash
git add test/e2e/closure_test.go test/e2e/manifests/07-vm.json
git commit -m "test(e2e): cover VM powerState lifecycle"
```

---

## Task 8: 全量验证与收口

**Files:**
- No source changes unless verification reveals a valid bug.

- [ ] **Step 1: gofmt**

Run: `gofmt -l pkg/apis/vm internal/controlplane/apiserver internal/node/controller internal/node/controllers test/e2e`
Expected: no output.

- [ ] **Step 2: fast local verification**

Run: `scripts/verify.sh`
Expected: PASS.

- [ ] **Step 3: focused race verification**

Run: `go test -race -count=1 ./internal/node/controller/... ./internal/node/controllers/... ./internal/controlplane/apiserver/...`
Expected: PASS.

- [ ] **Step 4: cross-platform build**

Run: `GOOS=linux GOARCH=arm64 go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl`
Expected: PASS.

- [ ] **Step 5: full distributed e2e**

Run: `scripts/e2e.sh full`
Expected: `E2E_EXIT=0` and `TestDistributedSpineClosure` PASS.

- [ ] **Step 6: inspect diff and status**

Run: `git status --short && git diff --stat HEAD`
Expected: only intended Knife 2 files changed since the task commits.

- [ ] **Step 7: final commit if verification fixes were needed**

If Task 8 required source fixes, commit them with a narrow message such as:

```bash
git add pkg/apis/vm internal/controlplane/apiserver internal/node/controller internal/node/controllers test/e2e
git commit -m "fix(vm): stabilize powerState lifecycle verification"
```

If no fixes were needed, do not create an empty commit.

---

## Self-review checklist

- Spec coverage: `powerState` API, create/update admission, structured status, delayed requeue, VM convergence matrix, govirtctl apply/get path, e2e, and Knife 3 boundary are all mapped to tasks above.
- Placeholder scan: no unresolved placeholder markers.
- Type consistency: field names match spec exactly: `powerState`, `observedPowerState`, `powerTransition`; enum values match locked decisions exactly: `On`, `Shutdown`, `Off`, `None`, `Starting`, `ShutdownRequested`, `PoweringOff`.
- File-size discipline: VM power helper is split into `vm_power.go` because `vm.go` is already 481 lines.
