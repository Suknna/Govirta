# 电源模型修复 — 设计

**日期**：2026-06-11
**状态**：设计已确认（brainstorming 协作产出）
**定位**：刀 6 冷配置改的前置独立修复（与 vmm 配置权威整顿同性质）

## 1. 背景与问题

刀 2（VM stop/start）引入了 `VM.spec.powerState` 电源意图字段，但把
**「电源状态」和「关机动作」拍平进了同一个三值枚举**：

```
PowerState = On | Shutdown | Off
```

`Shutdown` 是一个**动作**（触发 ACPI，让 guest OS 主动关机），不是一个
**电源状态**——它的物理终态必然是 `Off`（发送 shutdown，最终电源形态是 off）。

### 证据（已验证，直接读源码）

同文件 `pkg/apis/vm/v1alpha1/types.go` 里的 `ObservedPowerState`（status 投影，
物理实况）本就只有 `On | Off` 两值：

```go
// ObservedPowerState is the physical power state derived from live QEMU/QMP.
ObservedPowerStateOn  ObservedPowerState = "On"
ObservedPowerStateOff ObservedPowerState = "Off"
```

这反证：**真实物理电源态只有两种**，`Shutdown` 不该和它们并列在 `powerState`。
当前实现把动作冒充状态，是刀 2 的建模缺陷。

### 为何现在修

这是刀 6（冷配置改）的前置地基：冷配置 gate 依赖「VM 真正 Off」的判定，
电源模型不修对，gate 就建在歪地基上。和 vmm 配置权威整顿一样，作为独立前置
修复（独立 spec + plan + 实现 + e2e + 合并），修完再开始刀 6 设计。

## 2. 修复后的二维模型

把「电源状态」与「到达 Off 的方式」拆成两个独立维度：

```
spec.powerState:   On | Off        电源状态（物理终态，唯一两值）
spec.powerOffMode: Acpi | Force     到达 Off 的方式（仅 Off 语境消费）
```

- `Acpi` = **关机**：触发 ACPI → `vmm.Stop`（QMP `system_powerdown`），
  guest OS 主动关机。
- `Force` = **断电**：直接 `vmm.Kill`（QMP `quit` + SIGKILL 兜底）。
- 两种方式最终物理态都收敛到 `ObservedPowerState=Off`。

产品语义对齐用户日常运维 ESXi 的心智：**关机=acpi，断电=kill**。

## 3. API 契约改动（`pkg/apis/vm/v1alpha1/types.go`）

### PowerState：删除 Shutdown，只剩两值

```go
PowerStateOn  PowerState = "On"
PowerStateOff PowerState = "Off"
// PowerStateShutdown 删除（硬切，无兼容别名）
```

### PowerOffMode：新增枚举

```go
type PowerOffMode string

const (
    PowerOffModeAcpi  PowerOffMode = "Acpi"   // 触发 ACPI 优雅关机 = vmm.Stop
    PowerOffModeForce PowerOffMode = "Force"   // 强制断电 = vmm.Kill
)

func (m PowerOffMode) Valid() bool // Acpi | Force
```

### VMSpec：新增 powerOffMode 字段

```go
type VMSpec struct {
    Arch         string
    VCPUs        int
    MemoryMiB    int
    VolumeRefs   []string
    NICRefs      []string
    PowerState   PowerState   `json:"powerState"`
    PowerOffMode PowerOffMode `json:"powerOffMode,omitempty"` // On 时空，Off 时必填
}
```

### status 层：零改动

`ObservedPowerState`（On/Off）+ `PowerTransition`（None/Starting/
ShutdownRequested/PoweringOff）保持不变——它们本就是「转换中动作」的正确投影：

- `Acpi` 路径未收敛 → `PowerTransition=ShutdownRequested`
- `Force` 路径未收敛 → `PowerTransition=PoweringOff`
- 收敛后都 → `PowerTransition=None` + `ObservedPowerState=Off`

### 硬切（无兼容）

`Shutdown` 直接从枚举删除，不保留 deprecated 别名，admission 不做任何值翻译
（守刀 3「只 validating 不 mutating」+ AGENTS fast-iteration 不留向后兼容）。
所有 fixture/manifest/测试里的 `Shutdown` 全量迁移到 `Off`+`Acpi`。

## 4. 校验（两层分工）

### `VMSpec.Validate()`（apis 层，单对象内部一致性）

枚举合法性 + 条件必填都放这里（create + update 都经 admission 的 SpecValidator
调用）：

```
1. powerState ∈ {On, Off}              否则 ErrInvalidSpec（旧 Shutdown 值现在非法）
2. 条件必填：
   powerState=On  + powerOffMode 非空            → ErrInvalidSpec（On 时方式必须空）
   powerState=Off + powerOffMode 空              → ErrInvalidSpec（Off 时方式必填）
   powerState=Off + powerOffMode ∉ {Acpi,Force}  → ErrInvalidSpec
```

把「方式」严格绑定在「关机」语境：On 时方式无意义就强制不出现，Off 时方式必须
显式选 Acpi/Force。零隐式默认（显式铁律）。

### `FieldPolicyValidator.validateVM`（admission 层，新旧对比）

**保持不变**——只管 immutable arch 对比：

```
oldVM.Spec.Arch != newVM.Spec.Arch → ReasonConflict 拒绝
```

`powerState` 和 `powerOffMode` 都是 live-mutable（运行中可改，正是
stop/start/断电的触发），不进 immutable 检查。

### 校验分工总结

| 校验 | 位置 | 性质 |
| --- | --- | --- |
| 枚举合法（On/Off、Acpi/Force） | `VMSpec.Validate()` | 单对象 |
| 条件必填（On↔powerOffMode 依赖） | `VMSpec.Validate()` | 单对象 |
| arch immutable | `FieldPolicyValidator.validateVM` | 新旧对比 |

admission 框架（刀 3）已就位，本修复不新增 validator，只扩展
`VMSpec.Validate()` 的规则。

## 5. VM 控制器收敛矩阵 + 分支重构（`internal/node/controllers/vm.go`）

### 收敛矩阵

```
desired = (spec.powerState, spec.powerOffMode),  live = vm.Status().Phase

On            + 进程死(Defined/Stopped/Failed) → vmm.Start → Starting→Running
On            + 进程活(Running)                → no-op（已收敛；配置 drift 是刀6的事）
Off + Acpi    + 进程活(Running/Starting)        → vmm.Stop（ACPI）→ ShutdownRequested, requeue
Off + Acpi    + 进程死(Stopped/Defined/Failed)  → no-op（已到 Off, None）
Off + Force   + 进程活(Running/Starting)        → vmm.Kill        → PoweringOff, requeue
Off + Force   + 进程死(Stopped/Defined/Failed)  → no-op（已到 Off, None）
```

### 控制器分支重构

当前三分支（对应旧三值枚举）：

```
reconcileExistingVMOn       → 保留（On 收敛逻辑不变）
reconcileExistingVMShutdown → 删除
reconcileExistingVMOff      → 重写（内部按 powerOffMode 分流）
```

`reconcileExistingVM` 分发从三值变两值：

```go
switch obj.Spec.PowerState {
case PowerStateOn:
    return reconcileExistingVMOn(...)   // 不变
case PowerStateOff:
    return reconcileExistingVMOff(...)  // 内部按 powerOffMode 分 Acpi(Stop)/Force(Kill)
default:
    reportFailure(...)             // 非法值（含旧 Shutdown）
}
```

`reconcileExistingVMOff` 内部：

```
phase 进程活(Running/Starting)：
   Acpi  → vmm.Stop  → patch ShutdownRequested, RequeueAfter
   Force → vmm.Kill  → patch PoweringOff,       RequeueAfter
phase 进程死(Stopped/Defined/Failed)：
   → patch ObservedPowerState=Off, None（已收敛，no-op guard 防 PATCH 回环）
```

### create 路径（`reconcileMissingVM`）

- 删除旧的「create 时 powerState=Shutdown → 拒绝」分支（Shutdown 值已不存在）。
- create 接受 `On|Off`：
  - `On` → Create + Start（不变）
  - `Off` + 任意 mode → Create 但不 Start（无进程可关，mode 不消费，
    直接 defined/Off）

### 关键兑现

- **关机=acpi**：`Off`+`Acpi` → `vmm.Stop`。CirrOS 0.6.2 文档支持 ACPI
  （≥0.3.1 引入 `support acpi shutdown` LP:#944151），e2e 验真收敛。
  guest 不理就持续 `ShutdownRequested`，用户改 `Force` 强制。
- **断电=kill**：`Off`+`Force` → `vmm.Kill`，进程必死，必然收敛 Off。
- 复用刀 2 已建的 `RequeueAfter`（避免 ShutdownRequested tight-loop）。

## 6. Fixture / manifest 迁移（硬切连带）

`Shutdown` 删除后所有引用点必须迁移：

- e2e VM manifest（`test/e2e` 下）：加 `powerOffMode`，凡 `Off` 补 `Acpi`/`Force`
- 单元测试 fixture（vm 控制器测试、admission 测试、apis 测试）：
  旧 `Shutdown` → `Off`+`Acpi`；旧裸 `Off` → 补 `powerOffMode`
- 任何构造 `VMSpec` 的测试 helper

## 7. e2e 场景（替换刀 2 旧 powerState 序列）

```
1. create powerState=Off + powerOffMode=Acpi  → defined, ObservedPowerState=Off, None
2. update powerState=On（powerOffMode 空）     → running, On, None（QMP running + guest 活）
3. update powerState=Off + powerOffMode=Acpi   → 验真实收敛：轮询等 ObservedPowerState=Off
                                                  （CirrOS 0.6.2 支持 ACPI，给 60s 超时）
4. update powerState=On                        → 重新 running
5. update powerState=Off + powerOffMode=Force  → vmm.Kill，验无 QEMU 进程，Off, None
6. admission 拒绝（400）：
   - create powerState=Shutdown                 → 旧值已非法
   - create powerState=On + powerOffMode=Acpi    → On 时方式必须空
   - create powerState=Off（无 powerOffMode）     → Off 时方式必填
```

**验证强度**：场景 3 验 `Acpi` 真优雅收敛（依赖 CirrOS ACPI，e2e 实跑做权威
验证）；场景 5 验 `Force` 强制断电（不依赖 guest 合作，进程必消失）。两条路都
验到 `ObservedPowerState=Off` 终态。

`[待实测]`：aarch64 virt machine 的 guest poweroff 走 GPIO（PL061）+ ACPI/DT
信号，`system_powerdown` 能否在 CirrOS 0.6.2 arm64 真收敛由 e2e 给权威证据；
若场景 3 不收敛，e2e 会暴露，届时判断是 arch 细节还是 guest 配置（反经验主义：
以文档 + 实测为准，不固守刀 1「CirrOS 不理 ACPI」的未验证推断）。

## 8. Out of Scope（显式边界声明）

- **reboot / reset** → 未来命令式「VM 操作 Job」（note #36，与换池/迁移/快照
  回滚同线）。reboot 是非幂等一次性动作、无终态，塞进声明式 powerState 会重蹈
  Shutdown 覆辙（甚至更糟：level-triggered 会无限重启）。正确归属是带
  generation/nonce 防重放的 Job spec。本修复不碰。
- **冷配置 drift 检测 / Redefine** → 刀 6（本修复的后继）。On+Running 此刻
  仍 no-op。
- **热操作**（hot-plug / live migration）→ 按项目既定 deferred。
- **热加能力标识 + admission gate**（用户在刀 6 brainstorm 中提出的前瞻）→
  属于刀 6 范围，不在本电源模型修复内。
