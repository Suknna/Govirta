# 刀 2 设计：VM 电源意图（PowerState）与 stop/start 收敛

**日期**：2026-06-08  
**状态**：设计已锁，待写实现计划  
**上游**：`docs/superpowers/specs/2026-06-07-lifecycle-coldops-overview-design.md`（生命周期后半 + 冷操作总览）  
**前置**：刀 1 Delete 生命周期已完成（finalizer 两阶段删除 + 逆向拆除 e2e）  
**本刀定位**：补齐 VM 生命周期中的声明式电源意图，让用户能按 ESXi 体验显式 Power On、Shut Down Guest、Power Off，并为后续冷操作的 stopped 门禁提供真实基础。

---

## 1. 背景与目标

当前 VM controller 已能在 create-only walking skeleton 中把 VM 对象收敛到真实 QEMU 进程 `running`，但它的行为仍是单向的：对象存在且依赖就绪后，总是 `Create` + `Start`，用户无法通过 API 表达关机、断电或创建但不开机。

刀 2 引入 `VM.spec.powerState`，把 VM 电源从“控制器固定动作”提升为“用户显式声明的期望态”：

- 创建 VM 时，用户必须显式选择物理电源态 `On` 或 `Off`。
- 更新 VM 时，用户可声明 `On`、`Shutdown`、`Off`。
- `Shutdown` 对应 ESXi 的 **Shut Down Guest**：向 guest 注入 ACPI 软关机请求。
- `Off` 对应 ESXi 的 **Power Off**：直接强制断电，使用 `vmm.Kill`。
- status 必须结构化呈现 desired 与 observed 的差异，不能只靠非结构化 message。

本刀只处理 VM 电源意图，不展开 CPU/内存/硬件增删等刀 3 update 分级。

---

## 2. 产品语义：ESXi 体验优先

本项目架构可参考 k8s（声明式 API、watch/reconcile、status 回写），但用户体验对齐 ESXi，而不是公有云/OpenStack。

因此本刀采用 ESXi 的双操作模型：

| 用户意图 | ESXi 语义 | Govirta API | 执行动作 |
| --- | --- | --- | --- |
| 开机 | Power On | `spec.powerState=On` | `vmm.Start` |
| 软关机 | Shut Down Guest | `spec.powerState=Shutdown` | `vmm.Stop` / QMP `system_powerdown` |
| 断电 | Power Off | `spec.powerState=Off` | `vmm.Kill` / QMP quit + SIGKILL fallback |

关键语义：

- `On` / `Off` 是**物理电源态**，可用于创建态。
- `Shutdown` 是**软关机动作**，只对已存在 VM 的更新有意义；创建态没有 guest 可软关，所以必须拒绝。
- `Shutdown` 不自动升级为 `Off`。guest 不响应 ACPI 时，系统透明呈现“仍在等待 guest 关机”，由用户显式改成 `Off` 才强制断电。

---

## 3. API 契约新增（`pkg/apis/vm/v1alpha1`）

### 3.1 `VMSpec.powerState`：必填显式电源意图

`VMSpec` 新增字段：

```go
type VMSpec struct {
    // existing fields ...
    PowerState PowerState `json:"powerState"`
}
```

强类型枚举：

```go
type PowerState string

const (
    PowerStateOn       PowerState = "On"       // 物理开机
    PowerStateShutdown PowerState = "Shutdown" // ACPI 软关机动作
    PowerStateOff      PowerState = "Off"      // 物理断电
)
```

校验规则：

- `powerState` 必填；空值非法。
- 只允许 `On` / `Shutdown` / `Off`。
- `Shutdown` 在值域层是合法枚举值，但在 **create admission** 中非法；create/update 合法性由 apiserver admission 根据 `ApplyOperation` 判断。
- 不存在隐式默认值。“不开机”必须显式写 `Off`，不是省略字段后的默认行为。

### 3.2 `VMStatus`：结构化 observed 与 transition

`VMStatus` 新增字段：

```go
type VMStatus struct {
    Phase              VMPhase            `json:"phase"`
    ObservedPowerState ObservedPowerState `json:"observedPowerState"`
    PowerTransition    PowerTransition    `json:"powerTransition"`
    Message            string             `json:"message,omitempty"`
}
```

观测到的物理电源态：

```go
type ObservedPowerState string

const (
    ObservedPowerStateOn  ObservedPowerState = "On"
    ObservedPowerStateOff ObservedPowerState = "Off"
)
```

当前电源收敛动作：

```go
type PowerTransition string

const (
    PowerTransitionNone              PowerTransition = "None"
    PowerTransitionStarting          PowerTransition = "Starting"
    PowerTransitionShutdownRequested PowerTransition = "ShutdownRequested"
    PowerTransitionPoweringOff       PowerTransition = "PoweringOff"
)
```

约束：

- `observedPowerState` 必须始终写 `On` 或 `Off`，不允许空。
- `powerTransition` 必须始终写 `None` / `Starting` / `ShutdownRequested` / `PoweringOff`，不允许空；`None` 是显式值，不用空字符串表达。
- `message` 只做人读说明，不承载机器契约。CLI / Web 判断应依赖 `spec.powerState`、`status.observedPowerState`、`status.powerTransition`。

---

## 4. apiserver apply admission：通用 create/update 分类

当前 apply 路径是无条件 replace：`store.Put(ctx, key, data, expectedVersion="")`。刀 2 不改变这个写入语义，不引入 resourceVersion/CAS update；只在 admission 阶段增加“本次 apply 是 create 还是 update”的通用分类能力。

### 4.1 内部操作分类

新增 apiserver 内部枚举：

```go
type ApplyOperation string

const (
    ApplyOperationCreate ApplyOperation = "Create"
    ApplyOperationUpdate ApplyOperation = "Update"
)
```

分类函数：

```go
classifyApply(ctx, key) (ApplyOperation, error)
```

语义：

- `store.Get(ctx, key)` 返回 not found → `ApplyOperationCreate`。
- `store.Get(ctx, key)` 成功 → `ApplyOperationUpdate`。
- 其他错误向上传播。
- 分类只供 admission 判断，不改变最终 `Put` 的无条件 replace 行为。

### 4.2 VM powerState 写时规则

VM admission 使用 `ApplyOperation`：

| 操作 | 合法 `spec.powerState` | 非法 |
| --- | --- | --- |
| Create | `On` / `Off` | `Shutdown` |
| Update | `On` / `Shutdown` / `Off` | 空值或未知值 |

拒绝规则：

- create + `Shutdown` 返回 400 Bad Request，原因是创建态没有 guest 可软关。
- 空值或未知值同样返回 400。
- update + `Shutdown` 合法，触发后续 node controller `vmm.Stop`。

该分类能力为刀 3 的 immutable/cold-mutable 字段分级复用，但刀 2 不实现刀 3 的字段 diff。

---

## 5. VM controller 收敛设计

刀 2 将 VM controller 从“对象存在就 Create+Start”改成“按 `spec.powerState` 收敛”。

原则：

1. `spec.powerState` 是用户声明意图，controller 不自动改写。
2. live QEMU/QMP 是运行态事实源；status 只是投影。
3. `status.observedPowerState` 从 live phase 派生。
4. `status.powerTransition` 只描述当前收敛动作，不是私有账本。
5. `Shutdown` 无超时升级；reconcile 可重复发 ACPI powerdown。
6. `Off` 是强制断电，运行中使用 `vmm.Kill`。
7. 需要继续观察 live 状态的电源动作必须使用显式延迟重试（`RequeueAfter`），禁止 busy-loop。

### 5.1 controller 对 `vmm` 的接口要求

真实 `VMMService` 已有：

- `Create(ctx, request)`
- `Start(ctx, uuid)`
- `Stop(ctx, uuid)`
- `Kill(ctx, uuid)`
- `Status(ctx, uuid)`
- `Delete(ctx, uuid)`

当前 VM controller 的 `VMRunner` fake/interface 若缺少 `Stop(ctx, uuid)`，刀 2 必须补齐。控制器只依赖 runner 抽象，不直接依赖具体 `VMMService`。

### 5.2 收敛矩阵

| `spec.powerState` | VM 定义是否存在 | live phase | controller 动作 | status |
| --- | --- | --- | --- | --- |
| `On` | 不存在 | — | gate deps → `Create` → `Start` → 延迟观察 | `observedPowerState=Off` + `transition=Starting`，最终 `On/None` |
| `On` | 存在 | Defined/Stopped/Failed | `Start` → 延迟观察 | `transition=Starting` |
| `On` | 存在 | Starting | 不重复 spawn，延迟观察 | `observedPowerState=On` + `transition=Starting` |
| `On` | 存在 | Running | no-op | `observedPowerState=On` + `transition=None` |
| `Shutdown` | 不存在 | — | create-time 已被 apiserver 拒绝 | — |
| `Shutdown` | 存在 | Running/Starting/Stopping | `vmm.Stop`（ACPI）并延迟观察 | `observedPowerState=On` + `transition=ShutdownRequested` |
| `Shutdown` | 存在 | Defined/Stopped/Failed | no-op | `observedPowerState=Off` + `transition=None` |
| `Off` | 不存在 | — | gate deps → `Create`，不 `Start` | `observedPowerState=Off` + `transition=None` |
| `Off` | 存在 | Running/Starting/Stopping | `vmm.Kill` 并延迟观察 | `observedPowerState=On` + `transition=PoweringOff` |
| `Off` | 存在 | Defined/Stopped/Failed | no-op | `observedPowerState=Off` + `transition=None` |

说明：

- **Create + Off**：仍会创建 vmm runtime definition（例如 `vm.json`、argv snapshot、runtime 目录），但不启动 QEMU 进程。它表示“VM 对象已存在、节点已绑定、物理关机”。
- **Shutdown-on-stopped**：update 到 `Shutdown` 时若 VM 已经无 live 进程，视为已收敛；不报错，不自动把 spec 改成 `Off`。

### 5.3 phase 到 observedPowerState 的派生

推荐派生：

| live `VMPhase` | `ObservedPowerState` |
| --- | --- |
| Running | On |
| Starting | On |
| Stopping | On |
| Defined | Off |
| Stopped | Off |
| Failed | Off |

未知 phase 沿用既有 M-3 修复：warn + map to `VMPhaseFailed`，但 status 必须补齐 `observedPowerState=Off` 与合适的 `powerTransition`，不能只写 `phase`。

---

## 6. Shutdown 与 Off 的重试语义

### 6.1 Shutdown：重复 ACPI，不自动升级

当 `spec.powerState=Shutdown` 且 observed 仍为 `On`：

- 每次 reconcile 允许调用 `vmm.Stop(ctx, uuid)`。
- `vmm.Stop` 对应 QMP `system_powerdown`，它是“请求 guest 自行关机”，不是 kill。
- 后续观察必须通过显式延迟重试触发，不能立即重新入队形成 tight loop。
- guest 不响应时，系统长期呈现：

```json
{
  "phase": "running",
  "observedPowerState": "On",
  "powerTransition": "ShutdownRequested",
  "message": "shutdown requested via ACPI; waiting for guest to power off"
}
```

- 系统不设置隐藏超时，不自动 `Kill`。
- 用户需要强制断电时，必须显式把 `spec.powerState` 改为 `Off`。

### 6.2 Off：强制物理断电

当 `spec.powerState=Off` 且 observed 仍为 `On`：

- controller 调用 `vmm.Kill(ctx, uuid)`。
- `vmm.Kill` 已实现 QMP quit + SIGKILL fallback，符合 Knife 1 删除时锁定的强制收敛语义。
- 后续确认同样通过显式延迟重试观察 live phase，直到 observed 变为 `Off`。
- 收敛中 status：

```json
{
  "phase": "running",
  "observedPowerState": "On",
  "powerTransition": "PoweringOff",
  "message": "force power off requested"
}
```

- Kill 成功且 live phase 变成 stopped/defined 后，status 变为 `observedPowerState=Off` + `powerTransition=None`。

---

## 7. status patch、RequeueAfter 与错误处理

### 7.1 status no-op guard 保持

Knife 1 已修复 status feedback loop。刀 2 新增字段后继续沿用：

```go
if obj.Status == desiredStatus {
    return false, nil // 不 PatchStatus
}
```

`VMStatus` 仍由可比较字段组成，因此直接比较可用。

### 7.2 controller 框架新增延迟重试能力

当前 controller 框架的 retry 是立即 `q.Add(ev)`，没有 backoff。刀 2 必须补一个显式延迟重试机制，否则 `ShutdownRequested` 对不响应 ACPI 的 guest 会无限紧密循环。

推荐把 controller reconcile 结果从布尔 `requeue` 扩展成可表达延迟的结果结构，例如：

```go
type ReconcileResult struct {
    RequeueAfter time.Duration
}
```

语义：

- `RequeueAfter == 0`：不主动重试，等待下一次 watch/event。
- `RequeueAfter > 0`：在指定延迟后重新入队同一事件，用于轮询 live 状态。
- `err != nil`：仍必须记录错误并重试；错误重试是否复用默认延迟由 implementation plan 决定，但不得 silent busy-loop。
- 延迟重试必须受 controller `ctx` 约束；关闭时不得泄漏 goroutine/timer。

本刀只需要一个保守固定间隔即可，不引入指数退避、超时升级、隐藏 Kill 计时器。

### 7.3 RequeueAfter 规则

需要 `RequeueAfter > 0` 的场景：

- 依赖资源未 Ready。
- live phase 是 transient：`Starting` / `Stopping`。
- desired 与 observed 未收敛：
  - `On` + observed `Off`
  - `Shutdown` + observed `On`
  - `Off` + observed `On`

不主动重试（`RequeueAfter == 0`）的场景：

- `On` + observed `On` + transition `None`。
- `Shutdown` + observed `Off` + transition `None`。
- `Off` + observed `Off` + transition `None`。
- 永久 spec 错误已 patch Failed，不重试。

### 7.4 错误状态

- `vmm.Stop` 失败：patch 当前 observed + `ShutdownRequested`，message 说明 ACPI request failed；返回 error 触发延迟重试；不自动 Kill。
- `vmm.Kill` 失败：patch 当前 observed + `PoweringOff`，message 说明 force poweroff failed；返回 error 触发延迟重试。
- `vmm.Start` 失败：沿用现有启动失败处理，但 status 必须结构化，例如 `phase=Failed`、`observedPowerState=Off`、`powerTransition=Starting`、message 带失败原因。
- 未知 phase：warn + map failed，status 结构字段仍写全。

---

## 8. govirtctl 行为

刀 2 不新增子命令。用户继续通过现有命令修改 manifest：

```bash
govirtctl apply -f vm.yaml
govirtctl get vm vm-e2e
```

示例 manifest 片段：

```yaml
spec:
  powerState: On
```

或：

```yaml
spec:
  powerState: Shutdown
```

或：

```yaml
spec:
  powerState: Off
```

`govirtctl get` 返回 apiserver 中的完整 JSON/YAML 投影，用户可读取 `status.observedPowerState` 与 `status.powerTransition` 判断实际状态。

---

## 9. 验收与测试策略

### 9.1 单元测试

必须覆盖：

1. `VMSpec.Validate`：
   - 缺少 `powerState` → invalid。
   - `On` / `Shutdown` / `Off` 均为已知枚举值。
   - `Shutdown` 虽是合法枚举值，但 create admission 必须拒绝，update admission 才允许。
   - 未知值 invalid。

2. apiserver apply admission：
   - create + `Shutdown` → 400。
   - create + `On` / `Off` → accepted。
   - update + `Shutdown` → accepted。
   - 分类能力不改变 `store.Put(... expectedVersion="")` replace 语义。

3. VM controller：
   - Off create：`Create` called，`Start` not called。
   - On create：`Create` + `Start`。
   - On existing stopped：`Start`。
   - Shutdown running：`Stop` called，可重复调用，status=`ShutdownRequested`。
   - Shutdown stopped：no-op，status=`Off/None`。
   - Off running：`Kill` called，status=`PoweringOff`，后续 `Off/None`。
   - `Starting` / `ShutdownRequested` / `PoweringOff` 等未收敛状态使用 `RequeueAfter`，不能立即 tight-loop。
   - status no-op guard 仍跳过 PATCH。
   - unknown vmm phase 仍 warn + structured status。

### 9.2 e2e 验收路径

在现有 distributed spine closure 上扩展，不另起拓扑：

1. **Create with `powerState=Off`**
   - apply VM manifest。
   - 依赖资源 ready，VM runtime definition 创建完成。
   - status 最终 `observedPowerState=Off`、`powerTransition=None`。
   - 验证没有 QEMU live process。

2. **Update to `powerState=On`**
   - apply 同一个 VM，改 `spec.powerState=On`。
   - controller 调用 `vmm.Start`。
   - 最终 `phase=running`、`observedPowerState=On`、`powerTransition=None`。
   - 验证 QMP running / guest alive。

3. **Update to `powerState=Shutdown`**
   - apply VM，改 `spec.powerState=Shutdown`。
   - controller 调用 `vmm.Stop`。
   - 对 CirrOS 这类不保证响应 ACPI 的 guest，验收不强制要求最终停机。
   - 接受两种结果：
     - guest 未退出：`observedPowerState=On`、`powerTransition=ShutdownRequested`。
     - guest 已退出：`observedPowerState=Off`、`powerTransition=None`。

4. **Update to `powerState=Off`**
   - apply VM，改 `spec.powerState=Off`。
   - controller 调用 `vmm.Kill`。
   - 最终 `observedPowerState=Off`、`powerTransition=None`。
   - 验证 QEMU live process 不存在。

5. **Create with `powerState=Shutdown` is rejected**
   - 创建新 VM manifest，初始 `spec.powerState=Shutdown`。
   - apiserver admission 返回 400。
   - 证明 create/update 分类生效。

---

## 10. Out of Scope

刀 2 明确不做：

- VM CPU / memory 修改。
- VM `volumeRefs` / `nicRefs` 增删硬件。
- Volume `sizeBytes` 扩容。
- immutable / cold-mutable / live-mutable 全字段校验。
- running VM 下的 cold-mutable gate。
- resourceVersion / CAS apply。
- 引用 `deletionTimestamp` 对象的 apply 准入守卫（Knife 1 backlog #31；属于引用完整性补洞，可独立插入后续刀）。
- 新增命令式 power endpoint（例如 `POST /poweroff`）；电源仍走声明式 `apply`。

刀 2 只建立两块后续可复用基础：

1. `spec.powerState` 作为 VM 生命周期意图入口。
2. apiserver apply create/update 分类能力，为刀 3 field-grading 做准备。

---

## 11. 交付证据要求

实现刀 2 时，交付物必须包含：

- 修改内容：API 字段、apiserver admission、VM controller、电源状态 e2e。
- 修改原因：对齐 ESXi 电源操作体验，补齐生命周期 stop/start 基础。
- 验证方法：至少 `scripts/verify.sh`、相关 Go 单测、`scripts/e2e.sh full` 中的电源状态闭环。
- 官方文档引用：本刀不引入第三方 SDK / 库；不涉及外部官方文档。QMP `system_powerdown` / `quit` 已由现有 `pkg/virt/qmp` facade 封装，刀 2 只接线现有项目内接口。
