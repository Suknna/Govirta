# 刀 5：冷扩容（Volume.capacityBytes 冷扩容）— 设计说明

**日期**：2026-06-10
**状态**：设计确认（brainstorming 协作产出，六决策 + 记账模型已用真实源码核实）
**依赖**：刀 2（VM stopped 门禁前提）+ 刀 3（admission cold-mutable 契约门禁，已就位）

## 1. 背景与目标

按生命周期总览（`2026-06-07-lifecycle-coldops-overview-design.md` 第 8 节）路线图 8，
刀 5 实现 **`Volume.spec.capacityBytes` 冷扩容（只增不减）**：用户改大已存在卷的
声明容量 → node 在卷所属 VM 处于冷态（stopped/defined）时 `qemu-img resize` 把
live qcow2 扩到目标容量。

本刀是「cold-mutable 字段变更 → 控制器 cold gate → 落 live 资源」这一执行闭环的
**首次完整落地**，为刀 6（冷配置改）立模板。

### 1.1 执行面就绪度（已验证，直接读源码）

**已就绪（直接复用）**：

| 能力 | 状态 | 证据 |
| --- | --- | --- |
| admission 契约门禁 | ✅ | `capacityBytes` cold-mutable + 只增不减（`admission/fields.go:146-148`） |
| qemu-img resize 执行面 | ✅ | `pkg/virt/qemuimg/resize` Builder + 单测；`client.go:119` `QCOW2Client.Resize()` |
| qemu-img info 执行面 | ✅ | `pkg/virt/qemuimg/info` 暴露 `VirtualSizeBytes`；`client.go:110` `QCOW2Client.Info()` |
| block.Driver.Resize 接口 | ✅ 声明 | `block/driver.go:40` + `ResizeRequest{CapacityBytes}` + `Capabilities.ResizeOffline` 位 |
| vmIsCold 门禁模板 | ✅ | `controllers/snapshot.go:304-318`（唯一先例）；`VMRunner`/`vmm.Phase*`/`vmm.ErrNotFound` 同包可用 |
| pool 预分配记账 | ✅ | `reserveCapacityLocked`（`service.go:306-317`）+ `allocatedLocked`（map 求和，`pool.go:193-214`） |

**空缺（本刀新建）**：

- `local.Driver.Resize` 实现（当前返回 `volume.ErrUnsupported`，`local/driver.go:430-436`）+ 开 `ResizeOffline` 能力位
- `pool.Service.ResizeVolume`（复用预分配记账，按增量 delta）
- `storage.VolumeService.ResizeVolume` + `ResizeVolumeRequest`
- `VolumeController` 注入 `vmm.VMRunner` + resize 收敛路径 + VM cold 门禁

## 2. 产品哲学对齐

冷扩容对齐 ESXi：磁盘容量可在 VM 关机时调大（只增），运行中不可改。声明式
level-triggered——用户改 `spec.capacityBytes` 再 apply，node 收敛到目标，
不暴露命令式 resize 动作端点。

## 3. 六个已锁定决策

### 3.1 cold 门禁：注入 vmm.VMRunner，读 live phase（决策 1/A）

VolumeController 注入 `vmm.VMRunner`，照搬 snapshot 的门禁路径：
`client.Get(KindVM, vol.Spec.VMName)` 拿 VM uid → `vmm.Status(ctx, uid)` 读 **live**
phase → 复用 `vmIsCold` 判定（`PhaseStopped`/`PhaseDefined`/`vmm.ErrNotFound(runtime absent)`
均为 cold=true）。

**live 实况是唯一权威（上下一致铁律）**——不读 VM 对象的 `status.observedPowerState`
投影。理由：滞后的 status 说 Off、实际 QEMU 还在跑，resize 会损坏运行中磁盘。
这正是 snapshot 设计时明确否决「读 status 投影」的原因。

### 3.2 收敛模型 C′：声明式强制收敛 + driver 内部读 live 幂等（决策 2）

- **对外（控制器/service）声明式强制收敛**：控制器不读 live 容量、不比对，直接声明
  `ResizeVolume(target=spec.CapacityBytes)`。控制器侧零容量比对逻辑——纯 level-triggered
  「把 live 收敛到 spec，不关心上次到哪」。
- **对内（driver）读 live 幂等**：`local.Driver.Resize` 内部 `qemu-img info` 读 live
  virtual size，`live >= target` 则跳过 `qemu-img resize`（幂等成功），否则真 resize。
- **读 live 逻辑局部封装在 driver 一层**，不打穿 storage 抽象（pool/service/controller
  三层零读 live 容量逻辑）。

权衡记录：方案 A（控制器读 live 比对）需在 block.Driver/local/pool/VolumeService 四层
各加「读 live 容量」通道，纯负债；C′ 把读 live 局部化在 driver 一层，对上层是声明式
强制收敛的简洁，对内是读 live 比对的上下一致，最优组合且只新建一条 resize 通道。

### 3.3 status：不加容量字段，沿用 phase（决策 3）

`VolumeStatus` 不新增 observed capacity 字段。live 容量的唯一权威是 qcow2 本身
（上下一致：status 是投影、不当容量的第二事实源）。「展示卷当前多大」走未来
metrics / 节点资源汇报通道（可观测性规约本就规定指标从 live 真实资源读），
不往 status 塞会漂移的容量副本。

### 3.4 pool 记账：只算预分配，按增量准入（决策 4，已用真实源码修正）

**关键事实（已验证真实源码）**：pool 有两个量，准入只用其一：

| 量 | 来源 | 是否参与准入 |
| --- | --- | --- |
| AllocatedBytes（预分配） | `allocatedLocked()` 动态遍历 `p.volumes` 把 `CapacityBytes` 求和 | ✅ 准入唯一依据 |
| ActualUsedBytes（实际占用） | `driver.GetActualUsedBytes(ctx)` 读真实物理占用 | ❌ 仅 `GetPoolUsage` 报告，从不参与准入 |

`reserveCapacityLocked`（`service.go:306-317`）只算预分配：
`limit = capacity * overcommitRatio(block=1.5)`；`allocated = allocatedLocked()`；
`allocated > limit || bytes > limit-allocated → ErrPoolCapacityExceeded`。**ActualUsedBytes
一行都没进准入。**

**扩容是纯预分配扩容**：准入只核算预分配，与 CreateVolume 一个标准。overcommit 1.5
的全部意义就是「声明容量之和可超卖到物理容量 1.5 倍」——它管声明容量，物理占用由
系数间接容纳。两套准入逻辑分裂（create 看预分配、resize 看实际占用）是反模式，禁止。

**记账实现（真实模型，无累加器）**：`allocated` 不是累加计数器，而是每次从 volumes map
**动态求和**。所以 `ResizeVolume` 只需在锁内改 `p.volumes[id].CapacityBytes = newCap`，
求和下次自动反映新值——**没有「账本与 map 脱节」的泄漏风险**（账本即 map 求和）。
无需也不存在 `addAllocated`/`releaseAllocated` 累加器。

### 3.5 失败语义 A2：保持 Ready + 结构化日志 + 重试（决策 5/A2）

resize 失败时 **phase 保持 `Ready`**（不 patch status）+ 结构化日志记原因 +
`RequeueAfter` 重试。理由：扩容是 ready 卷上的增量收敛，失败不该否定「卷可用」
这个已达成的事实（卷扩容前已 Ready、能正常挂载用，没扩成不等于卷坏了）。phase
仍 Ready 表达「卷可用」底座，持续 requeue 表达「扩容未完成、在重试」。

**为什么不 patch status Message**：失败/暂缓是每轮 requeue 反复进入的瞬时态，
若每轮 patch 一条 Message 会制造 status 抖动（MODIFIED→reconcile→再 patch），
违反 no-op status guard（决策见 6.3）。失败原因走 `zerolog` 结构化日志（可观测性
规约的日志支柱），不进 status——status 只反映「卷可用」这个稳定事实。

与 snapshot 的「失败退 Failed」不同——snapshot 失败就是真失败（无部分可用态），
volume 扩容失败是「可用卷没扩成」，不污染 Ready 底座。

### 3.6 VM 对象 404：RequeueAfter 等待（决策 6/A）

卷的 `client.Get(KindVM, vmName)` 返回 404（owning VM 对象被删、留下悬空 vmRef 卷）时，
**RequeueAfter 等待**（结构化日志记"等待 owning VM"，不 patch status），不对孤儿卷擅自扩容。理由：owning VM
不存在的卷是异常状态，用户应删掉它；控制器持续等待（而非给一个没有 VM 的卷扩容）
是更保守正确的姿态。

**区分两个不同的「VM 不在」**：
- VM 对象存在、runtime absent（`vmm.ErrNotFound`）→ `vmIsCold` 定义为 cold=true，proceed
- VM 对象本身 404（本决策）→ RequeueAfter 等待，不 proceed

## 4. 顺序约束（正确性核心）

记账 / resize 顺序必须是：**reserveCapacity(delta) 准入 → driver.Resize 成功 → 才改
volumes map 的 CapacityBytes**。

反例后果：若先改 map（推进账本）再 resize 失败，下次 reconcile 算出 `delta=0` 跳过，
永不重试——破坏 A2 重试语义。先 resize 成功才推进账本，配合 C′ 的 driver 读 live 幂等，
crash 在任意中间点都能正确收敛（下次 reconcile 重新声明 target，driver 读 live 决定补做）。

## 5. storage 层改动（自底向上）

### 5.1 local.Driver.Resize（C′ 读 live 幂等封装层）

```
Resize(ctx, vol, req block.ResizeRequest) (volume.Volume, error):
  1. ctx 检查 + 路径定位（复用 pathFromVolume / ensurePublishableImage）
  2. 读 live：qemuimg.QCOW2().Info().Path(path).Do(ctx) → live.VirtualSizeBytes
  3. 幂等判断：live.VirtualSizeBytes >= req.CapacityBytes → 已达标，跳过 resize，
     返回 CapacityBytes 更新后的 volume.Volume（不报错，幂等成功）
  4. 否则 qemuimg.QCOW2().Resize().Path(path).SizeBytes(req.CapacityBytes).Do(ctx)
  5. 返回 CapacityBytes=req.CapacityBytes 的 volume.Volume
```

- 幂等判断用 `>=` 而非 `==`：live 已 ≥ 目标（手动扩过、或上次 resize 成功但账本未推进
  的重试）都算达标，安全跳过。
- 开 `DriverInfo.Capabilities` 的 `ResizeOffline` 位（当前未置位）。

### 5.2 pool.Service.ResizeVolume（记账编排）

```
ResizeVolume(ctx, poolName, volumeID, capacityBytes) (volume.Volume, error):
  getPool + 校验 PoolTypeBlock（与 CreateVolume 一致）
  [p.mu.Lock，临界区严格对齐 CreateVolume]
    old := p.volumes[volumeID]（不存在 → ErrVolumeNotFound）
    oldCap := old.CapacityBytes
    delta := capacityBytes - oldCap
    delta < 0 → 拒绝（防御；admission 已挡，driver double-check）
    delta > 0 → reserveCapacityLocked(p, delta)  // 按增量过 overcommit 预分配准入
    driver.Resize(ctx, old, block.ResizeRequest{CapacityBytes: capacityBytes})
      失败 → 返回 err，不改 map（账本不推进 → 下次重试，A2）
    成功 → p.volumes[volumeID].CapacityBytes = capacityBytes  // 求和自动反映
  返回更新后的 volume.Volume
```

- **delta == 0**：跳过准入，**仍下沉 driver.Resize**（C′ 幂等兜底——账本与 live 万一不一致时，
  driver 读 live 才是权威）。
- **锁临界区严格对齐 CreateVolume**（实现时核对确切边界，防并发超卖）。
- **顺序铁律**：reserve → driver.Resize 成功 → 改 map（第 4 节）。

### 5.3 storage.VolumeService.ResizeVolume（VM-facing 入口）

```go
type ResizeVolumeRequest struct {
    PoolName      string
    VolumeID      volume.ID
    CapacityBytes int64
}
func (s *VolumeService) ResizeVolume(ctx context.Context, req ResizeVolumeRequest) error
```

薄封装：校验显式字段（PoolName/VolumeID/CapacityBytes 全必填，显式铁律）→ 转
`pool.Service.ResizeVolume`。与现有 `SnapshotVolume`/`DeleteVolume` 同款窄入口。

### 5.4 积木式边界

逐层只依赖下层契约：controller → VolumeService → pool.Service → block.Driver → qemuimg。
读 live 幂等封装在 driver 内，上层只声明目标容量，不感知「读 live 比对」逻辑。

## 6. VolumeController resize 路径 + cold 门禁

### 6.1 结构体注入 vmm.VMRunner

```go
type VolumeController struct {
    volumes RootVolumeCreator   // 现有
    images  ImageGetter         // 现有
    client  DependencyReader    // 现有
    vmm     VMRunner            // 新增：读 live phase 做 cold 门禁
}
```

`VMRunner` 接口、`vmIsCold` 逻辑同包现成（snapshot.go），直接复用，不新建抽象。
agent.go 装配处补注入 vmm。

### 6.2 打破「ready 卷直接 Done()」——加 resize 收敛分支

当前 `volume.go:136-138` ready 卷直接 `Done()`，完全无视 spec 变更。改为：

```
ready 卷 reconcile：
1. client.Get(KindVM, vol.Spec.VMName)
     └─ 404 → RequeueAfter（决策 6；结构化日志记原因，不 patch status）
2. vmIsCold(ctx, vm)?
     ├─ 否（VM 运行中）→ RequeueAfter（cold-mutable 暂缓；结构化日志记原因，不 patch status）
     └─ ErrNotFound / Stopped / Defined → cold=true，proceed
3. cold → VolumeService.ResizeVolume(target=spec.CapacityBytes)（强制收敛 C′）
     ├─ 成功 → driver 幂等收敛；phase 保持 Ready，no-op guard 防 PATCH 抖动
     └─ 失败 → phase 保持 Ready + 结构化日志记原因 + RequeueAfter 重试（决策 5/A2）
```

### 6.3 关键正确性细节

- **强制收敛 C′，控制器不比对容量**：直接声明 `ResizeVolume(spec.CapacityBytes)`，
  「需不需要真 resize」由 driver 内部读 live 判断。控制器侧零容量比对逻辑。
- **no-op status guard 保留**：resize 成功后 phase 仍 Ready，沿用 `patchVolumeStatusIfChanged`
  （desired status 与 obj.Status 相等则跳过 PATCH，防 MODIFIED→reconcile 反馈环）。
- **稳态收敛后不 requeue**：VM cold + 卷已达目标（driver 幂等跳过）+ status 无变化 →
  `Done()`，不空转。只有未收敛（VM 运行中暂缓 / resize 失败 / VM 404）才 RequeueAfter。
- **fresh-GET 防陈旧事件**：照搬 Knife 2 `currentVM` 模式经验——延迟事件可能携带旧
  `spec.capacityBytes`，side-effect（resize）前需基于当前对象。实现时核对是否需对 volume
  也做 fresh-GET，对齐 Knife 2。

### 6.4 cold-mutable 门禁落点（呼应总览第 3 原则）

总览定的「cold-mutable 由 node 用 live phase 门禁」在此首次落地为可执行闭环——admission
只做契约门禁（capacityBytes 只增不减），**真正的 stopped 门禁在控制器用 live phase 把守**。
这是刀 5 给刀 6（冷配置改）立的模板：cold-mutable 字段变更 → 控制器 cold gate → 落 live 资源。

## 7. govirtctl + e2e 验证

### 7.1 govirtctl

无需新增子命令。声明式——用户改 Volume manifest 的 `spec.capacityBytes` 再
`govirtctl apply -f`，`govirtctl get volume` 观测。与刀 2 powerState 同款。

### 7.2 e2e 场景（复用 e2e 框架重构解锁的 guest-side live-truth 钩子）

在现有 `TestDistributedSpineClosure` 的 VM 冷停机窗口内插入扩容场景（VM 已 Off/cold，
正好是 resize 前提）：

```
1. VM 已 powerState=Off（复用刀 2 已有冷停机点）
2. apply 改大的 Volume manifest（capacityBytes: 旧值 → 2×）
3. waitObjectPhase: Volume 仍 Ready（A2，phase 不变）
4. afterReady 钩子（guest-side live-truth，复用 e2e 框架新能力）：
     Guest.Exec → qemu-img info <vol qcow2 path> → 断言 virtual size == 新目标值
     ← 资源存活期 live 实况验证，证明真 resize 落到 qcow2（不只信 status）
5. （负向）apply 缩小的 capacityBytes → 断言 apiserver admission 409 拒绝（只增不减契约）
6. （负向，可选）VM 运行中改 capacityBytes → 断言卷仍 Ready 但容量未变（cold gate 暂缓）
```

### 7.3 单元测试最小覆盖

- **driver**：`local.Driver.Resize` 三态（live<目标→真 resize、live>=目标→幂等跳过、
  ctx 取消）+ `ResizeOffline` 能力位开启
- **pool**：`ResizeVolume` 记账（delta 准入通过 / 超 overcommit 拒绝、volumes map
  CapacityBytes 更新、delta==0 仍下沉 driver、卷不存在 ErrVolumeNotFound）
- **service**：`VolumeService.ResizeVolume` 显式字段校验
- **controller**：resize 路径全分支（VM 404→RequeueAfter、VM 运行中→暂缓、cold→ResizeVolume、
  resize 失败→Ready+结构化日志+requeue（不 patch status）、已收敛→Done no-op、no-op status guard）

### 7.4 验证铁证

`scripts/e2e.sh full` 三节点真实闭环：guest 内 `qemu-img info` 读到扩容后真实 virtual size
（live 实况，非 status 投影），证明冷扩容端到端落真 qcow2。这是 e2e 框架重构解锁的
「资源存活期 live-truth 验证」，补上刀 4 快照 items 2/5 未做成的部分。

## 8. Out of Scope（显式边界声明）

- **缩容**：admission 已挡（只增不减），执行面永不传 `--shrink`；不实现缩容。
- **在线 / 热扩容**：仅冷扩容（VM stopped/defined 才执行），热扩容按项目既定 deferred。
- **换池**：`Volume.poolRef` immutable，换池走未来 Job spec（note #29），不在 update。
- **status 容量字段**：不加（决策 3），live 容量唯一权威是 qcow2，展示走未来 metrics / 节点汇报。
- **卷新增 / 给 VM 加盘**：root 卷新建已在 create-only slice 完成（不在本刀）；
  「给已有 VM 加盘」（VM.volumeRefs 成员变更 + argv 重建）属于刀 6 冷配置改，不在刀 5。
- **data 卷扩容的控制器路径**：本刀以 root volume 为主（VolumeController 现有路径即 root
  volume）；data volume 扩容若执行面一致则自然覆盖，但 e2e 只验 root（与现有 e2e 拓扑一致）。

## 9. 刀 5 / 刀 6 边界

- **刀 5 只做 Volume.capacityBytes 冷扩容**——单字段 cold-mutable，最小验证「cold-mutable
  字段变更 → 控制器 cold gate → 落 live 资源」完整闭环。
- **刀 5 给刀 6 立的模板**：cold gate（注入 vmm + vmIsCold）+ 声明式强制收敛（C′）+
  A2 失败语义。刀 6（冷配置改：VM memoryMiB/vcpus + 增删 volumeRefs/nicRefs + argv 重建）
  复用同一 cold gate 模式，落点在 VM 控制器、动作是「重建 argv + 重新 Create/Start」，
  复杂度高于单字段 resize。
- **不在刀 5 做**：VM 自身规格变更、增删硬件、argv 重建——全部刀 6。

## 10. 复用的既有铁律

- 上下一致：driver 读 live qcow2 virtual size 作幂等权威；准入读 live 物理占用仅观测不门禁。
- 显式优于隐式：`ResizeVolumeRequest` 所有字段必填。
- 积木式：controller → service → pool → driver → qemuimg 逐层只依赖下层契约。
- errors.Join：记账 + resize 的多错误合并保全。
- 单写者 + no-op status guard：防 reconcile 反馈环。
