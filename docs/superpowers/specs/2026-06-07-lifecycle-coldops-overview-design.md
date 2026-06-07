# 生命周期后半 + 冷操作 — 总览设计

**日期**：2026-06-07
**状态**：总览设计（统一原则与契约骨架；不展开各刀实现细节）
**作者**：brainstorming 协作产出

## 1. 背景与目标

当前 Govirta 已打通 create-only walking skeleton：七个 manifest 经 `govirtctl` →
master apiserver/etcd → node 六控制器 reconcile → 真 daemonized QEMU guest 到达
`running`，并由 `TestDistributedSpineClosure` e2e 验证。

但资源管理目前是「只进不退、只建不改」的单向 create 闭环：

- **Delete**：6 个控制器的 `EventDeleted` 全是 no-op（执行面 Delete 已就绪但未接线）。
- **Update / 改规格**：apiserver 不区分 create vs update，无字段可变性概念。
- **VM 电源**：spec 无电源意图字段，控制器"建了就 start 冲到 running"，用户无法关机。
- **冷操作（roadmap 7-9）**：快照 / 扩容 / 配置改未实现（执行面 qemu-img
  snapshot/resize、vmm Stop/Kill 已就绪但未接线）。

本设计补齐生命周期的另一半（stop/delete/update）+ 冷操作（roadmap 7-9），定义
**统一原则与契约骨架**，供后续 6 刀逐刀细化实现。

### 执行面就绪度（已验证，直接读源码）

这批工作的真实性质是 **API 契约扩展 + apiserver 路由/校验 + 控制器接线**，
不是建执行引擎——下层执行面大量已就绪：

| 能力 | 执行面状态 | 证据 |
| --- | --- | --- |
| VM stop/start | ✅ 已建 | `vmm.Stop`(QMP powerdown)、`Kill`(quit+SIGKILL)、`Start`、`Discover`、`Reattach`，6 phase 完整 |
| Delete（全资源） | ✅ 已建 | `VolumeService.DeleteVolume`、`ImageService.DeleteImage`、`pool.DeleteVolume/DeleteImage`、`netpool.DeleteNIC/DeleteNetwork`、`vmm.Delete`、`store.Delete` |
| 冷快照 | ✅ 执行面在 | `pkg/virt/qemuimg/snapshot`（Path/Name/Do） |
| 冷扩容 | ✅ 执行面在 | `pkg/virt/qemuimg/resize`（Path/SizeBytes/Do） |
| 冷配置改 | ⚠️ 部分 | argv builder 在，"改后重建 argv"路径未接 |

缺口集中在上三层：API 契约（无 finalizer/deletionTimestamp、apply 不分 create/update）、
apiserver（无 DELETE 路由、无字段校验）、控制器（EventDeleted 全 no-op）。

## 2. 产品哲学（贯穿全部 6 刀）

**架构参考 k8s，用户体验对齐业界标杆 ESXi。这是私有云，不是公有云——
不搬 OpenStack/公有云那套（如单磁盘独立快照资源）。**

遇到设计岔路时，先问「ESXi 怎么做」，而不是「k8s/OpenStack 怎么做」。
k8s 影响的是控制面机制（声明式、level-triggered、watch/reconcile），
ESXi 影响的是用户操作模型（Power On/Off、整机快照、加盘/拔卡）。

## 3. 统一架构原则（六刀共同遵循）

1. **声明式 level-triggered**：所有变更都是"改 etcd 期望态（spec /
   deletionTimestamp / powerState）→ node watch → reconcile 收敛 → 回报 live status"。
   无命令式动作端点（DELETE 这个 HTTP 动词本身除外）。
2. **上下一致 / live 实况是唯一权威**：所有"能不能做"的门禁（stopped 判定、
   引用扫描）以 live 资源为准；status 只是投影，不参与权威决策。
3. **显式优于隐式**：字段可变性显式分级；immutable 改动在 apiserver 边界拒绝；
   危险操作（换池/迁移）显式划到未来 Job spec，绝不让系统偷做。
4. **finalizer 保证下层先拆净、上层才消失**：删除两阶段，etcd 对象只在真实资源
   拆除完成后才真正消失。
5. **blast radius 最小**：一次操作只动一个对象；删除被引用对象直接拒绝，不级联。

## 4. 资源模型（k8s 式独立资源 + VM ref 组合）

所有可挂载/可操作的东西都是独立一等公民资源，VM 通过 ref 组合它们。

```
独立一等公民资源（各自 apply、各自生命周期）：
  StoragePool · Image · Volume · NIC · Network · VM · Snapshot(新增, 第7个)

VM 通过 ref 组合（一对多）：
  VM.spec.volumeRefs []string  → 多块盘
  VM.spec.nicRefs    []string  → 多张卡

Snapshot 指向 VM（整机快照, ESXi 式）：
  Snapshot.spec.vmRef string   → 目标 VM
```

用户流程与 k8s 一致：先 apply Volume/NIC（独立资源），再 apply VM 引用它们的名字。
Volume / NIC 独立资源 + ref 引用是**现状已实现并 e2e 验证**的模型，本设计确认保持不变。

## 5. 删除模型（Finalizer 两阶段 + 保护式拒绝）

### API 契约新增（`pkg/apis/meta`）

- `ObjectMeta.DeletionTimestamp *string`（RFC3339，nil = 未删）
- `ObjectMeta.Finalizers []string`（如 `"govirta.io/node-teardown"`）

### 删除流程

```
1. govirtctl DELETE /apis/{kind}/{name}
2. apiserver：反向引用扫描 → 被引用则 409 拒绝（"still referenced by X"）
3. apiserver：未被引用 → 不删，只打 deletionTimestamp（对象仍在 etcd）
4. node watch 收到 MODIFIED（带删除戳）→ 控制器拆除 live 资源
   （调用已存在的 DeleteVolume/DeleteImage/DeleteNIC/DeleteNetwork/vmm.Delete）
5. 控制器拆净 → 移除自己的 finalizer（PATCH）
6. apiserver：finalizers 空 → 真正 store.Delete → 对象消失
```

### 反向引用规则（保护式拒绝的扫描表）

| 删除目标 | 扫描是否被引用 |
| --- | --- |
| StoragePool | 有无 Volume/Image 的 poolRef 引用它 |
| Network | 有无 NIC 引用它 |
| Image | 有无 Volume 的 imageRef 引用它 |
| Volume | 有无 VM 的 volumeRefs 引用它 |
| NIC | 有无 VM 的 nicRefs 引用它 |
| VM | 无下游，直接放行 |

扫描模式复用现有先例（MAC 分配时扫描 NIC 对象占用），apiserver 在删除边界做。
逆序删除是用户显式操作序列（先删 VM 再删 Volume/NIC 再删 Pool），意图清晰可审计。

## 6. VM 电源意图 + stop/start（刀 2）

### API 契约新增（`pkg/apis/vm`）

- `VM.spec.powerState PowerState`（强类型枚举 + 命名常量）：
  `PowerStateOn` / `PowerStateOff`
- 创建语义：apply 时必须**显式**给 `powerState`（显式铁律，不默认 On）。

### VM 控制器电源收敛（用 live phase，非 status 投影）

```
desired = spec.powerState,  live = vmm.Status().Phase
On  + 进程死(Stopped/Failed/Defined) → vmm.Start  → 回报 Starting→Running
Off + 进程活(Running/Starting)       → vmm.Stop   → 回报 Stopping→Stopped
On  + Running / Off + Stopped        → no-op（已收敛）
```

- `Stop` 用已存在的 `vmm.Stop`（QMP system_powerdown 优雅停）。
- 收敛中的 transient phase（Starting/Stopping）requeue 自驱到终态（现有机制）。
- `Kill`（QMP quit + SIGKILL 兜底）暂不接入声明式路径，留作未来强停 Job / 超时兜底。

## 7. Update 字段分级（横切全资源，ESXi-aligned）

每个资源 spec 字段显式归类。**immutable 由 apiserver 写时拒绝**（纯契约规则，
与 live 状态无关）；**cold-mutable 由 node 用 live phase 门禁**（沿用第 3 原则）。

| 资源 | immutable（拒绝改） | cold-mutable（stopped 才生效） |
| --- | --- | --- |
| VM | arch、name/uid | memoryMiB、vcpus、volumeRefs（增删盘）、nicRefs（增删卡） |
| Volume | poolRef、imageRef、role、format | sizeBytes（只增不减，冷扩容） |
| NIC | mac、networkRef、vmRef | （无；改 MAC = 删旧建新 NIC，ESXi 式硬件绑定） |
| Network | bridge 语义字段、egressInterface | （本期无） |
| Image | 全部（id/format/source） | （无；镜像不可变） |
| StoragePool | 全部（backend/storageRoot/capacity） | （本期无） |

`VM.spec.powerState` 是 live-mutable（运行中可改，正是 stop/start 的触发），单独归类。

### 校验分层

- **immutable 改动** → apiserver update 时对比旧 spec，变了就 4xx 拒绝。
- **cold-mutable 改动** → apiserver **接受**写入；node reconcile 时若 live phase ≠
  Stopped 则不执行、status 记原因，等停机后下次 reconcile 自然生效（声明式收敛）。

ESXi 行为对齐说明：ESXi 灰掉运行中 VM 的配置项；本系统是声明式，改运行中 VM 的
cold-mutable 字段不会立刻报错，而是"接受但暂不生效 + status 显示原因"，停机后自动应用。

## 8. 冷操作 7-9（复用 cold-mutable 门禁 + 已就绪执行面）

冷操作不是新机制，而是 cold-mutable 字段变更 / 独立资源 reconcile 的具体实例。

- **冷快照（刀 4，roadmap 7）**：`Snapshot` 第 7 个独立资源，`spec.vmRef` 指向 VM
  （整机快照，ESXi 式）。它是**一个对象**（apply 一个 Snapshot = 一次整机快照意图）——
  不违反"一次操作只动一个对象"：被操作的就是这个 Snapshot，其 reconcile **内部**
  为该 VM 的所有 volumeRefs 各做一次 `qemu-img snapshot`，对外仍是单一资源的单一意图。
  门禁：reconcile 时检查 `vmRef` 的 live phase == Stopped。
  **明确不做**（OpenStack 排除）：不暴露单卷/单磁盘快照 API，不做卷级 snapshot 资源。
  用户视角只有"给这台 VM 打快照"，和 ESXi 一致。
- **冷扩容（刀 5，roadmap 8）**：`Volume.sizeBytes` 增大（只增不减）→ node 在 VM
  stopped 时 `qemu-img resize`。纯 cold-mutable 实例，执行面已就绪。
- **冷配置改（刀 6，roadmap 9）**：`VM.memoryMiB/vcpus/volumeRefs/nicRefs` 变更 →
  node 在 stopped 时重建 argv + 重新 Create/Start。执行面 argv builder 已就绪，
  需接"改后重建"路径。

## 9. 分刀路线图（依赖序，一刀一 spec→plan→e2e 闭环）

```
刀 1：Delete 生命周期（finalizer + 反向引用拒绝 + 6 控制器接 EventDeleted 拆除）
        └─ 横切基础，必须最先做（update/冷操作都隐含"拆"能力）
刀 2：VM stop/start（powerState 字段 + 控制器电源收敛）
        └─ 冷操作的 stopped 门禁前提
刀 3：Update 字段分级（apiserver immutable 拒绝 + node cold-mutable 门禁框架）
        └─ 依赖刀 1（部分 update = 删建）+ 刀 2（stopped 判定）
刀 4：冷快照（roadmap 7，Snapshot 第 7 资源 + spec.vmRef 整机 + 多卷扇出 + stopped 门禁）
        └─ 依赖刀 2（stopped 门禁）
刀 5：冷扩容（roadmap 8，Volume.sizeBytes cold-mutable + qemu-img resize）
        └─ 依赖刀 2 + 刀 3（cold-mutable 框架）
刀 6：冷配置改（roadmap 9，VM 内存/CPU/增删硬件 + argv 重建）
        └─ 依赖刀 2 + 刀 3
```

每刀独立 brainstorm（细化各自 API/控制器）→ spec → plan → 执行 → e2e 闭环，
互不阻塞、可独立审阅。本总览只锁统一原则与契约骨架。

## 10. Out of Scope（显式边界声明）

- **换 pool 池 / 存储迁移 / 冷热计算迁移** → 未来独立 **Job spec**（含冷热计算迁移、
  存储迁移等命令式一次性动作），不属于 update（memory 832 / note #29）。
- **热操作**（hot-plug、live migration）→ 按项目既定 deferred，不在本设计。
- **动态节点注册 / 心跳 / 真实调度 / 多节点 e2e** → 不在本生命周期范围，
  是另一条"成为真集群"的独立线。
- **单卷/单磁盘独立快照资源**（OpenStack/公有云模型）→ 明确排除；快照只在 VM 整机粒度。
