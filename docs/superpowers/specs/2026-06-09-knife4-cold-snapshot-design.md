# 刀 4：冷快照（Snapshot 第 7 类资源）— 详细设计

**日期**：2026-06-09
**状态**：详细设计（brainstorming 协作产出，已逐节确认）
**上游**：`docs/superpowers/specs/2026-06-07-lifecycle-coldops-overview-design.md`（§8 冷操作、§9 刀 4）
**依赖**：刀 2（VM stop/start，stopped 门禁前提）、刀 3（admission 框架 + 反向引用扫描）

## 1. 背景与范围

总览把冷快照定为刀 4（roadmap 7）：`Snapshot` 第 7 个独立一等公民资源，
`spec.vmRef` 指向 VM = 整机快照（ESXi 式）。一个 Snapshot 对象 = 一次整机快照意图；
其 reconcile 内部为该 VM 的所有 `volumeRefs` 各做一次 qcow2 内部快照，对外仍是
单一资源单一意图——不违反"一次操作只动一个对象"。

### 本刀范围 = 建 + 删（声明式纯净）

- **建**：apply Snapshot → 整机打快照（`qemu-img snapshot -c`）。
- **删**：DELETE Snapshot → 删 qcow2 内部快照（`qemu-img snapshot -d`）。
- **回滚（revert）不在本刀的声明式 API**：回滚本质是命令式动作（"把磁盘回退到某个
  快照点"），声明式资源模型无法干净表达，与"换池/迁移"同类，已划到未来 Job spec
  （memory 1042 / note #33）。但**执行面回滚能力本刀就绪并测试覆盖**，只差上层接线。

### 明确不做（OpenStack/公有云模型排除）

- 不暴露单卷/单磁盘快照 API，不做卷级 snapshot 资源。
- 用户视角只有"给这台 VM 打快照"，与 ESXi 一致。

### 执行面就绪度（已验证，直接读源码）

| 能力 | 状态 | 证据 |
| --- | --- | --- |
| `qemu-img snapshot -c`（建） | ✅ 已建 | `pkg/virt/qemuimg/snapshot`（Path/Name/Do） |
| `qemu-img snapshot -d`（删） | ❌ 缺 | 本刀补齐 |
| `qemu-img snapshot -a`（回滚） | ❌ 缺 | 本刀补齐（执行面就绪，不接声明式 API） |
| `qemu-img snapshot -l`（列） | ❌ 缺 | 本刀补齐（回滚/管理辅助） |
| `local.Driver.Snapshot` | ⚠️ 返回 `ErrUnsupported` | 本刀接线到执行面 |
| admission 框架 / 反向引用扫描 | ✅ 已建（刀 3） | `internal/controlplane/apiserver/admission` |
| 冷门禁（live phase，精化自 stopped 门禁） | ✅ 已建（刀 2/3） | VM 控制器 live phase 判定；本刀精化为 `vmIsCold` |

## 2. 产品哲学

架构参考 k8s（声明式、独立资源 + ref、level-triggered、finalizer），用户体验对齐
ESXi（整机快照、快照不能脱离 VM 独立存在）。这是私有云，不搬 OpenStack 单磁盘快照那套。

## 3. API 契约（新增 `pkg/apis/snapshot/v1alpha1`）

```go
type SnapshotSpec struct {
    VMRef string `json:"vmRef"` // 指向 VM 的 metadata.name（整机快照目标）
}

// SnapshotPhase 是 Snapshot 生命周期状态（状态机枚举，专用类型 + 命名常量）。
type SnapshotPhase string

const (
    SnapshotPhasePending  SnapshotPhase = "Pending"  // 等待 VM stopped 或扇出进行中
    SnapshotPhaseReady    SnapshotPhase = "Ready"    // 所有盘快照已建立
    SnapshotPhaseDeleting SnapshotPhase = "Deleting" // 删除中（等 VM stopped / 扇出删除中）
    SnapshotPhaseFailed   SnapshotPhase = "Failed"   // 扇出失败（已回滚已建的，可重试）
)

// DiskSnapshotState 是单块盘的快照结果（状态机枚举）。
type DiskSnapshotState string

const (
    DiskSnapshotStateCreated DiskSnapshotState = "Created"
    DiskSnapshotStateFailed  DiskSnapshotState = "Failed"
)

type DiskSnapshotResult struct {
    VolumeRef string            `json:"volumeRef"`
    Result    DiskSnapshotState `json:"result"`
}

type SnapshotStatus struct {
    Phase         SnapshotPhase        `json:"phase"`
    DiskSnapshots []DiskSnapshotResult `json:"diskSnapshots,omitempty"`
    Message       string               `json:"message,omitempty"`
}
```

- 新增 `metav1.KindSnapshot Kind = "Snapshot"`（第 7 个 Kind 常量）。
- `SnapshotSpec.Validate()`：`vmRef` 非空。
- `SnapshotStatus.Validate()` + `SnapshotPhase.Valid()` + `DiskSnapshotState.Valid()`
  （契约层校验方法，刀 3 admission 依赖；与其他资源同构）。
- 沿用共享 `TypeMeta + ObjectMeta` envelope，仅依赖 stdlib，不 import internal/。

### 字段可变性：全 spec immutable

Snapshot 是不可变实体（与 Image 同类——快照一旦建立就是固定时间点）。
`vmRef` 改了就是另一台 VM 的快照，没有意义。apiserver update 时改任何 spec
字段都 4xx 拒绝（复用刀 3 `FieldPolicyValidator` 的 immutable 表，新增 Snapshot 行）。

### 内部快照命名

所有盘统一用 **Snapshot 的 UID** 作为 qcow2 内部快照名。VM 有 N 块盘 → N 个 qcow2
文件里都创建名为 `<snapshot-uid>` 的内部快照。整机快照的本质是"所有盘的同一逻辑
时间点"，统一 name 最自然地表达这个语义；删除/回滚时对每块盘用同一个 name 操作，
无需记录 per-disk 映射。qcow2 内部快照名在单个文件内唯一即可，跨文件同名无冲突。

## 4. apiserver / admission

### 4.1 nodeName 注入（第三个 admission mutation 先例）

继 MAC 分配（`applyNIC`）、VM 调度（`bindVM`）、finalizer 注入之后，Snapshot 的
nodeName 由 admission 期从 `vmRef` 解析注入：

```
apply Snapshot → 读 spec.vmRef 对应的 VM 对象 → 取 VM.metadata.nodeName
              → 注入 Snapshot.metadata.nodeName
```

用户不填、也不应该填 nodeName——它是 vmRef 的 VM 落点的确定性派生（单一事实源）。
node watch 用 `?nodeName=` 过滤到自己。这个 vmRef→VM 的查询与 ReferenceValidator
（aply 侧校验 vmRef 存在）是同一次 store 读，可复用。

注入逻辑放在 apply handler 的 mutation 阶段（与 NIC MAC / VM bind 同位置），
**不在 admission validator 内**（admission 只校验不变更，刀 3 已确立的边界）。

### 4.2 引用完整性（apply 侧，复用刀 3 `ReferenceValidator`）

`vmRef` 指向的 VM 必须存在且未在删除中（`deletionTimestamp` 空），否则拒绝：
- VM 不存在 → 400 BadRequest。
- VM 在删除中 → 409 Conflict（不允许给删除中的 VM 建快照，与现有 deleting-target 守卫同构）。

Snapshot 的 `vmRef` 是 VM 的 **name**（不是 UID）——与 `VM.volumeRefs`/`nicRefs`
一致（那些也是 name）。注意区别于 `Volume.vmRef`/`NIC.vmRef`（那两个是 VM UID）。

### 4.3 反向引用扫描（DELETE 侧，复用刀 3 `ReverseReferenceValidator`）

扫描表新增一行：

| 删除目标 | 扫描是否被引用 |
| --- | --- |
| VM | `Snapshot.spec.vmRef`（按 VM name） |

删 VM 前必须先删其所有 Snapshot，否则 409 "still referenced by Snapshot/x"。

**这修正刀 3"VM 是删除树顶点"的结论**：当时没有任何资源指向 VM，所以 VM 是顶点；
Snapshot 是第一个合法的 VM 下游引用，扩展反向扫描表加入这条边是自然演进。
与刀 1/3 的保护式拒绝完全同构，blast radius 最小（不级联）。

### 4.4 finalizer 两阶段删除

Snapshot 沿用 `FinalizerNodeTeardown`：
- apply 时 admission 注入 finalizer（与其他资源同）。
- DELETE 只打 `deletionTimestamp`（先过反向引用扫描——Snapshot 无下游，直接放行）。
- node 删完 qcow2 内部快照后才 RemoveFinalizer。
- apiserver 见 finalizers 空 → 真 store.Delete。

## 5. 节点控制器：`SnapshotController`

第 7 个 per-resource 控制器（`internal/node/controllers/snapshot.go`），沿用现有
框架（watch→workqueue→reconcile→PATCH status + RemoveFinalizer），`Kind()` 返回
`metav1.KindSnapshot`，在 `node.Agent` 装配时注册。

### 5.0 冷门禁定义（cold gate，精化自「stopped 门禁」）

qemu-img snapshot 的安全硬约束是**没有 QEMU 进程打开这个 qcow2**——这是「进程死」而非字面 `PhaseStopped`。VM live phase 中进程死的态有三个：`PhaseStopped`（启动后停止）、`PhaseDefined`（`powerState=Off` 从未启动）、`PhaseFailed`（intent=running 但异常退出）。

**冷门禁 = 进程死 且 非 running 意图**，落到 phase 即 **`PhaseStopped` 或 `PhaseDefined`**：

- **允许 `PhaseStopped` + `PhaseDefined`**：两者 intent 都不是 running，VM 控制器不会在快照期间擅自重启（无竞争）；且进程死，qcow2 无人持有，qemu-img 安全。`PhaseDefined` 必须放行——否则 `powerState=Off` 新建 VM 必须先 On→Off 一次才能打快照（接近 bug 的 UX）。
- **排除 `PhaseFailed`**：它 intent=running，VM 控制器下次 reconcile 可能重新 Start，快照期间存在进程重启竞争——不安全，门禁不放行（视为「未冷」，requeue 等待）。
- **`vmm.ErrNotFound`（runtime 不存在）视为已冷**：runtime `vm.json` 不存在意味着没有任何进程持有 qcow2，等价进程死；尤其删除路径上 VM 对象在但 runtime 已没，必须放行而非永久 requeue。

实现用一个共享判定 `vmIsCold(phase)`（建/删两路共用一处定义），`vmm.ErrNotFound` 也归入 cold。下文「冷门禁通过」即指此判定为真。

### 5.1 建快照路径（无 deletionTimestamp）

```
1. decode → 若 status.phase == Ready 提前返回（level-triggered 幂等，沿用刀 1/3 防 PATCH 自环）
2. 解析 spec.vmRef → 经 master client 读 VM 对象 → 取 spec.volumeRefs
3. 冷门禁：查 vmRef 的 VM live phase（vmm.Status；ErrNotFound 视为已冷）
   - 未冷（PhaseFailed/PhaseStarting/PhaseRunning/PhaseStopping）→ status=Pending
     + message "waiting for VM cold (stopped/defined)" + RequeueAfter
4. 冷门禁通过 → 逐块 volumeRef：经 storage 边界解析 qcow2 path → snapshot create <snapshot-uid>
5. 全有或全无：
   - 中途某块失败 → 回滚已建的（snapshot delete <uid>）→ status=Failed
     + diskSnapshots（已建的标 Created、失败的标 Failed）+ message + RequeueAfter
   - 回滚也失败 → errors.Join 合并上报，status=Failed
6. 全部成功 → status=Ready + diskSnapshots 全 Created（不 requeue，已收敛）
```

### 5.2 删快照路径（deletionTimestamp 非空 / EventDeleted）

```
1. 冷门禁：查 vmRef 的 VM live phase（vmm.Status；ErrNotFound 视为已冷）
   - 未冷 → 不摘 finalizer + status=Deleting + message "waiting for VM cold" + RequeueAfter
   - VM 对象本身已不存在（master ErrNotFound）→ qcow2 随 VM 一起没了，直接 RemoveFinalizer
2. 冷门禁通过 → 逐块 volumeRef：snapshot delete <snapshot-uid>
   - 幂等：内部快照不存在视为已删（不报错）——delete 前 list 一次，缺失则跳过
3. 全部删净 → RemoveFinalizer → apiserver 见 finalizers 空 → 真 store.Delete
4. 中途失败 → 保留 finalizer + status=Deleting + message + RequeueAfter（level-triggered 重试）
```

### 5.3 门禁与收敛纪律

- 冷门禁两路都用 **live phase**（非 status 投影），与刀 3 cold-mutable 门禁同构
  （上下一致铁律：live 实况是唯一权威）。`vmIsCold` 是建/删共享的单一判定。
- 门禁未过 / 扇出失败 → `RequeueAfter` 延迟轮询（刀 2 引入），不紧打转。
- 所有错误向上传播，多块盘失败 / 回滚失败用 `errors.Join` 合并（项目铁律）。
- 删除幂等：删不存在的内部快照不得报错（list-before-delete 或 sentinel 容忍），
  否则 teardown 会因「重复删 / 创建中途失败留下的部分盘」永久卡住 finalizer。
- status PATCH 前比对：与期望 status 相同则跳过 PATCH（沿用刀 1/3 防自环）。

## 6. 执行面补齐（`pkg/virt/qemuimg/snapshot`）

当前只有 `-c`（create）。本刀补齐为完整健全的快照生命周期：

| 子命令 | 方法 | 本刀用途 |
| --- | --- | --- |
| `-c` create | 已有 `Builder`（Path/Name/Do） | 建快照 |
| `-d` delete | 新增 | 删快照（声明式 DELETE 路径） |
| `-a` apply/revert | 新增 | 回滚（执行面就绪 + 单测；本刀不接声明式 API，待 Job） |
| `-l` list | 新增 | 列快照（回滚/管理辅助查询） |

- 每个子命令独立 builder，与现有 `-c` 同构（binary + runner 注入，`Do(ctx)` 构造
  `[]string` argv 交 runner）。
- 全部带单元测试（fake runner 验证 argv 构造 + ctx 取消行为）。
- 回滚 `-a` 的健全性由单测证明（argv 正确 + 可执行边界），满足"底层回滚功能健全"要求。
- `-l` 返回结构化快照列表（解析 qemu-img 输出），供回滚/管理。

## 7. 存储层接线

`local.Driver.Snapshot` 当前返回 `volume.ErrUnsupported`。本刀接线到 qemuimg 执行面：
- create-snapshot：解析 qcow2 path（沿用 `local.PathKey`，刀 1 已导出）→ `snapshot -c`。
- delete-snapshot：新增 driver 契约方法 → `snapshot -d`（幂等）。
- 控制器经 storage 边界调用，**不直接碰 qemuimg**（积木式分层铁律：上层依赖
  `block.Driver` 抽象，不依赖具体实现）。
- 现有 `block.SnapshotRequest` 扩展或新增 delete 契约（实现时按最小契约决定）。

存储层不持久化快照元数据（沿用现有 in-memory 原则）：快照的权威事实源是 qcow2
文件内部的快照（`qemu-img snapshot -l` 可读），不是上层账本（上下一致铁律）。

## 8. govirtctl

无需新子命令（kind-agnostic CLI 已支持）：
- `govirtctl apply -f snapshot.json` → 建快照。
- `govirtctl get snapshot <name>` → 看 phase + diskSnapshots。
- `govirtctl delete snapshot <name>` → 删快照（finalizer 两阶段）。

## 9. e2e 验证（扩展 `TestDistributedSpineClosure`）

在现有闭环（VM 到 Off/stopped）之后插入快照场景：

```
1. VM 已 stopped → apply Snapshot(vmRef=vm-e2e)
   → status.phase=Ready，diskSnapshots 全 Created
2. host 侧 live 实况验证：qemu-img snapshot -l <vol-qcow2> 确认内部快照 <snapshot-uid> 真的建了
   （验证 live 实况，非 status 投影 — 上下一致）
3. DELETE Snapshot（VM stopped）→ finalizer 两阶段 → GET 404
4. host 侧：qemu-img snapshot -l 确认内部快照真的删了
5. 删 VM 前先建一个残留 Snapshot → DELETE VM 返回 409 "still referenced by Snapshot/x"
   （反向引用验证）→ 先删 Snapshot → 再删 VM 成功
```

可选额外场景：VM running 时 apply Snapshot → 卡 Pending "waiting for VM stopped"
（stopped 门禁验证）。

## 10. 单元测试最小覆盖

- `SnapshotSpec.Validate` / `SnapshotStatus.Validate` / `SnapshotPhase.Valid` /
  `DiskSnapshotState.Valid`（契约层）。
- apiserver：nodeName 注入（从 vmRef 解析）、Snapshot immutable 拒绝、引用完整性
  （vmRef 不存在 400 / vmRef 删除中 409）、反向引用（删 VM 被 Snapshot 挡 409）。
- 执行面：`-c`/`-d`/`-a`/`-l` 四个 builder 的 argv 构造 + ctx 取消（fake runner）。
- 存储层：`local.Driver` create/delete snapshot 接线（fake runner）。
- SnapshotController：建路径（stopped 门禁未过 Pending / 全成功 Ready / 中途失败回滚
  Failed）、删路径（VM 运行中保留 finalizer / stopped 删净摘 finalizer / 删除幂等）、
  status 防自环、未知 phase 处理。

## 11. Out of Scope（显式边界）

- **回滚声明式 API** → 执行面就绪，声明式接线归未来 Job spec（memory 1042 / note #33）。
- **单卷/单磁盘快照资源**（OpenStack 模型）→ 明确排除，快照只在 VM 整机粒度。
- **热快照**（运行中 VM 快照）→ qemu-img 对运行中镜像不安全，硬约束排除；快照只在
  VM stopped 时做（建和删都是）。
- **快照链/快照树管理 UI**、**快照导出/克隆** → 不在本刀。

## 12. 关键决策回顾（brainstorming 锁定）

1. 范围 = 建 + 删（声明式纯净），回滚归 Job backlog，执行面就绪。
2. 删除引用 = `VM ← Snapshot.vmRef` 保护式拒绝（修正刀 3 VM 顶点结论）。
3. nodeName = apiserver admission 从 vmRef 解析注入（第三个 mutation 先例）。
4. 内部快照命名 = 所有盘统一用 Snapshot UID。
5. 扇出原子性 = 全有或全无（失败回滚已建的，status=Failed 可重试）。
6. 冷门禁 = 建和删都要求 VM 进程死且非 running 意图（`PhaseStopped`/`PhaseDefined`，
   `vmm.ErrNotFound` 视为已冷；排除 `PhaseFailed`）——精化自「stopped 门禁」，详见 §5.0。
7. 执行面 = 补齐 `-c`/`-d`/`-a`/`-l`（回滚就绪待 Job）。
8. status = 结构化 per-disk 结果（phase + diskSnapshots + message）。
9. 字段可变性 = 全 spec immutable（与 Image 同类）。
