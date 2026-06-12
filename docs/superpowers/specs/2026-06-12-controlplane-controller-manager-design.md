# Control-plane Controller Manager 上移总设计

**日期**：2026-06-12
**状态**：设计草案
**作者**：brainstorming 协作产出

## 1. 背景与目标

Govirta 已完成 apiserver 写并发、`metadata.resourceVersion`、`PUT replace`、status patch、delete/finalizer 收口等 API 基础语义。当前剩余的结构性问题是：`internal/node/controllers` 同时承担了两类职责。

- 控制面职责：依赖判断、状态机推进、重试策略、finalizer drain、冷操作门禁、资源编排顺序。
- 节点职责：真实 host 操作，例如 storage/qemu-img、bridge/TAP、nftables、CoreDHCP、VMM/QEMU/QMP。

这让 `govirtlet` 实际上既是 node executor，又是分布式系统的大脑。目标是把大脑上移到 `govirtad`：在 control plane 内引入 controller-manager，由它 watch API 资源、生成任务、推进状态机、处理依赖和 finalizer；`govirtlet` 收敛为只执行明确任务的 `TaskExecutor`，执行 live host 操作并回报 live observed result。

本设计只写总方案，不实现代码。

## 2. 非目标

- 不做真调度：继续使用现有 `Scheduler`/`NoopScheduler` 和 `metadata.nodeName` 绑定语义。
- 不做 leader election：本阶段假定单 `govirtad` 实例。
- 不做 RBAC、租户、认证授权。
- 不做完整 informer、watch cache、shared indexer 或 Kubernetes client-go 风格框架。
- 不改变 etcd-only 控制面持久化决策。
- 不把 host storage/network/vmm 真实操作搬进 control plane。
- 不实现 hot-plug、live migration、运行中 snapshot/resize/config 修改。

## 3. 当前职责拆分依据

已核实的现状：

- `internal/controlplane/apiserver` 已拥有 HTTP route、kind dispatch、admission、GET/List/Watch、status/finalizers 子资源、CAS 写入语义。它应继续只作为 API 边界。
- `internal/controlplane/store.Store` 已提供 `Put`、`Get`、`List`、`Watch`、`DeleteIfVersion` 等 swappable store 边界，可支撑轻量控制器循环。
- `internal/node/controller` 已有 watch → queue → reconcile 的薄框架，但当前运行在 node 内。
- `internal/node/controllers` 的每个 reconciler 都是 decode → dependency/cold gate → domain work → status/finalizer patch，其中 dependency/cold gate/status/finalizer 属于 control-plane 编排，domain work 属于 node live 执行。
- `pkg/apis/meta/v1alpha1.ObjectMeta` 已有 `ResourceVersion`、`NodeName`、`DeletionTimestamp`、`Finalizers`，可承载控制面状态推进与节点路由。

## 4. 方案比较

### 4.1 方案 A：把现有 node controllers 原样搬到 govirtad

优点是改动表面最小，复用现有 Reconcile 结构。缺点是会把 storage/network/vmm concrete dependency 一起带入 control plane，破坏 host side effect 边界，也会让 `govirtad` 直接依赖 node-local runtime。该方案不采用。

### 4.2 方案 B：control plane controller 生成持久化 Task，node 执行 Task

control-plane controllers 只读取 API 对象、依赖对象和 task 状态，做状态机和依赖编排；需要触碰 host 时创建或更新显式 Task。`govirtlet` watch 分配给自己的 Task，按 Task 内的显式参数执行 live host 操作，然后回写 Task status。控制面再根据 Task status 推进资源 status/finalizers。

优点：边界清晰、符合显式参数铁律、可复用 list-then-watch/resourceVersion/finalizer 语义、节点不需要读取业务资源依赖。缺点：需要新增 Task API/存储与一层 task 状态机。本设计采用此方案。

### 4.3 方案 C：control plane 通过同步 RPC 直接调用 node executor

优点是对象少，链路短。缺点是任务不持久，`govirtad` 重启、node 断线、长操作超时和幂等重试都会变复杂，并绕开已完成的 watch/resourceVersion 机制。该方案不采用。

## 5. 总架构

迁移后的控制流分三层：

```text
API client
  -> govirtad apiserver
     -> etcd API resources
     -> control-plane controller-manager
        -> Task objects
        -> govirtlet TaskExecutor
           -> live host resources
           -> Task status
        -> resource status/finalizers
```

职责边界：

| 层 | 负责 | 禁止 |
| --- | --- | --- |
| apiserver | HTTP API、admission、resourceVersion、replace、status/finalizers、watch/list | reconcile、依赖推进、host side effect、任务调度循环 |
| control-plane controllers | 依赖图、状态机、任务生成、任务去重、重试、finalizer drain、资源 status | qemu-img、QMP、netlink、nftables、CoreDHCP、读取节点文件系统 |
| node TaskExecutor | 执行 Task 中已解析的 live host 操作，回报 observed result/error | 读取业务 API 依赖、推断默认值、决定资源状态机、移除资源 finalizer |

## 6. Apiserver 边界

apiserver 继续保持纯 API 层：

- `POST apply`：声明式 create/update，保留现有 server-owned metadata/status 保护。
- `PUT replace`：带 `resourceVersion` 的完整对象 CAS 替换。
- `PATCH /status`：只替换 status，CAS retry，禁止改 spec。
- `DELETE`：只打 `deletionTimestamp` 并保留 finalizers。
- `PATCH /finalizers`：只处理 finalizer 子资源，最后一个 finalizer 移除后用 `DeleteIfVersion` 真删。
- `GET/List/Watch`：提供 controller-manager 与 node executor 所需的对象观察能力。

apiserver 不内嵌 controller 逻辑。即使 controller-manager 与 apiserver 在同一个 `govirtad` 进程中，它们也通过 `store.Store`/受限 client 边界交互，不直接调用 handler 私有函数制造捷径。

## 7. Control-plane controller-manager

### 7.1 位置与组合

新增 `internal/controlplane/controller` 作为控制面控制器框架。`internal/controlplane.Service` 在组装 `store`、`mac allocator`、`scheduler`、`apiserver` 后，再组装 controller-manager。

`Service.Run(ctx)` 同时运行：

- apiserver HTTP loop；
- control-plane controller-manager；
- 共享同一个 root `ctx` 与 zerolog context；
- 关闭时先让 manager 停止取新 watch/reconcile，再关闭 store。

### 7.2 轻量 watch/queue

本阶段不引入完整 informer。controller-manager 使用项目自有最小机制：

- 每个 controller watch 自己关心的 kind；
- 初次连接使用 list-then-watch，避免漏掉已存在对象；
- 断线重连使用最后处理过的 `resourceVersion`；
- queue 按 resource key 去重，保持 level-triggered 语义；
- reconcile 失败按 controller 返回的 retry decision 重新入队；
- status 写入前做 desired/current 比较，避免 status feedback loop。

这等价于把当前 `internal/node/controller` 的薄框架思想上移，但不直接把 node 依赖带入 control plane。

### 7.3 写入边界

control-plane controllers 写入两类对象：

- 资源 status/finalizers：通过与 apiserver status/finalizers 同等语义的内部 client 或 store helper，保持 CAS 与 server-owned 字段保护。
- Task 对象：通过 store CAS create/update，Task 自身也有 `metadata.resourceVersion`。

控制器不得无条件覆盖资源 spec。所有基于旧对象的状态推进必须携带预期 `resourceVersion`；冲突时重新 get/list/watch 后重算。

## 8. Task 作为唯一节点执行契约

### 8.1 Task API 形态

引入内部 API kind `Task`，类型放在 `pkg/apis/task/v1alpha1`。Task 走 typed API object 形态，原因是 Task 需要被 store/watch/status 复用，且 node executor 需要稳定解码契约；它仍是内部执行契约，不作为用户可 apply 的业务资源暴露。

Task 基本字段：

```text
Task
  metadata.name
  metadata.uid
  metadata.nodeName
  metadata.resourceVersion
  metadata.deletionTimestamp
  metadata.finalizers
  spec.ownerKind
  spec.ownerName
  spec.ownerUID
  spec.operation
  spec.attempt
  spec.input
  status.phase
  status.observed
  status.errorClass
  status.message
  status.startedAt
  status.completedAt
```

约束：

- `metadata.nodeName` 必填，node watch 只看自己的 Task。
- `spec.ownerKind/name/uid` 必填，用于控制器确认 Task 仍属于当前资源版本语境。
- `spec.operation` 使用专用 typed constant，不使用裸字符串。
- `spec.input` 必须是按 operation 强类型解码的显式参数结构，不允许 map 任意拼装。
- Task name 由控制面确定性生成，例如 `<ownerKind>.<ownerUID>.<operation>` 或加上 disk/nic 子标识；同一 owner/operation 收敛到同一个 Task，避免重复创建。
- Task 是内部执行契约，不作为用户声明式资源暴露给 `govirtctl apply`。

### 8.2 Task phase

Task phase 使用专用类型：

- `Pending`：控制面已生成，等待 node 获取。
- `Running`：node 已开始执行。
- `Succeeded`：node 已完成并提交 live observed result。
- `Failed`：node 执行失败，带可分类错误。
- `Deleting`：控制面要求 executor 撤销或清理 Task 关联的本地执行痕迹。

Task 失败不直接等同资源失败。控制面 controller 根据 operation、错误类别、资源当前 spec/status 和 retry policy 决定资源 status 是保持、Failed、还是重试。

### 8.3 Task 幂等性

每个 Task operation 必须是幂等或可安全重试：

- create/ensure 类：重复执行收敛到同一 live resource。
- delete/teardown 类：目标不存在视为已完成。
- start/stop/kill 类：以 live QEMU/QMP 状态判断当前态。
- snapshot fan-out：必须携带 snapshot name 与 disk mapping；已存在同名 disk snapshot 时按 operation 语义判定成功或冲突。
- resize：携带绝对目标容量，不携带增量。

node executor 只做幂等 host 收敛，不决定上层资源 phase。

## 9. Task 输入显式性

Task input 是控制面到 node 的完整执行说明。凡影响行为的字段必须由控制面提前解析并写入 Task，node 不得再读取业务 API 对象推断。

示例输入族：

- `RegisterStoragePoolInput`：pool name、backend、pool type、storage root、capacity bytes。
- `PutImageInput`：pool name、image id、format、source type、source path/URL、expected size 信息。
- `CreateVolumeInput`：pool name、volume id、role、vm id、vm name、disk index、capacity、source image/file pool/format。
- `EnsureNetworkInput`：network name、bridge name、subnet、gateway、MTU、DHCP server id、egress interface、DNS/router option 模式。
- `EnsureNICInput`：nic name、vm identity、network identity、TAP name、bridge name、MAC、IP、hostname、anti-spoof rule identity。
- `DefineVMInput`：vm id、vm name、arch、vCPU、memory、explicit volume attachments、explicit NIC attachments、QMP socket path、pidfile path、runtime root。
- `PowerVMInput`：desired power state、explicit power-off mode（ACPI vs kill）、timeout policy。
- `SnapshotVMInput`：snapshot name、target VM identity、per-volume pool/name/snapshot tag。
- `ResizeVolumeInput`：pool name、volume id、absolute capacity bytes、owning VM identity。
- `RedefineVMInput`：完整 desired VMM spec summary 与依赖 attachments。
- `TeardownInput`：资源类型对应的完整 delete 参数。

Task input 不允许空值代表默认行为。没有某个行为时使用显式 disabled/none typed mode。

## 10. Node TaskExecutor

### 10.1 govirtlet 新形态

`govirtlet` 启动后仍构造 host dependencies：storage service、network service、VMM service、hostnet managers、qemu-img runner、QMP client 等。但它不再为 7 个业务资源启动 reconcilers。

它只启动 TaskExecutor：

1. watch `/apis/Task?nodeName=<node>` 或等价内部 task watch；
2. 解码 Task；
3. 按 `spec.operation` 分派到对应 executor handler；
4. 执行 live host 操作；
5. patch Task status；
6. 对 deleting Task 执行本地清理后移除 Task finalizer 或回报完成。

### 10.2 executor 边界

TaskExecutor 保留：

- storage pool 注册/取消、本地容量读取；
- image byte pull/write/delete；
- qcow2 create/delete/snapshot/resize；
- bridge/TAP/route readiness/firewall/DHCP ensure/delete；
- VMM define/redefine/discover/reattach；
- QEMU start/ACPI shutdown/kill/delete；
- QMP live status 查询。

TaskExecutor 禁止：

- watch `StoragePool/Image/Volume/Network/NIC/VM/Snapshot` 业务对象；
- 根据 API 依赖自行判断顺序；
- 自行改业务资源 status 或 finalizers；
- 为缺失字段选择默认值；
- 吞错或只 log 不回报。

### 10.3 状态回报

Task status 必须携带 live observed result，而不是控制面投影：

- storage：真实 pool usage、volume path/size、image ready 信息；
- network：真实 bridge/TAP/firewall/DHCP observed info；
- VM：真实 QEMU/QMP power/status、pid、QMP socket、argv summary；
- snapshot/resize/redefine：真实 qemu-img/VMM 操作结果。

错误回报包含稳定错误分类和 message；控制面根据错误分类决定是否重试。cleanup/rollback 多错误通过 `errors.Join` 保留，Task status 至少保留主错误分类和可读 message。

## 11. Resource controllers 上移映射

### 11.1 StoragePool controller

控制面职责：根据 StoragePool spec 生成 `RegisterStoragePool` Task；等待 Task succeeded 后把 pool usage 写入 StoragePool status；删除时生成 unregister/delete Task，成功后移除 StoragePool finalizer。

节点职责：注册本地 pool、读取真实 usage、执行本地 teardown。

### 11.2 Image controller

控制面职责：确认目标 pool ready，生成 `PutImage` Task；根据 Task result 设置 Image phase；删除时生成 `DeleteImage` Task 并 drain finalizer。

节点职责：从显式 source 拉取 bytes，写入本地 file pool，删除本地 image bytes。

### 11.3 Volume controller

控制面职责：门禁 pool/image readiness；为 root/data volume 生成 `CreateVolume` Task；ready 后写 Volume status；当 ready volume 的 desired capacity 变大时，先确认 owning VM cold observed，再生成 `ResizeVolume` Task；删除时生成 `DeleteVolume` Task。

节点职责：执行 qcow2 create/from-reader/resize/delete，返回真实 volume observed info。

### 11.4 Network controller

控制面职责：生成确定性 bridge/firewall/DHCP identity 与 `EnsureNetwork` Task；根据 Task result 写 Network status；删除时生成 `DeleteNetwork` Task。

节点职责：ensure bridge、check IPv4 forwarding、ensure masquerade/forward-accept、start DHCP，或反向删除。

### 11.5 NIC controller

控制面职责：门禁 Network ready；使用已分配 MAC 与确定性 TAP identity 生成 `EnsureNIC` Task；根据 Task result 写 NIC status；删除时生成 `DeleteNIC` Task。

节点职责：ensure TAP、apply DHCP binding、ensure anti-spoofing，或反向删除。

### 11.6 VM controller

控制面职责：门禁 Volume/NIC ready；生成 `DefineVM`/`RedefineVM`/`PowerVM` Task；根据 Task result 推进 VM phase、observed power、transition；冷配置变更只在 node 回报 VM cold 时生成 redefine task。关机和断电继续明确区分：ACPI shutdown 是优雅关机，kill 是断电。

节点职责：discover/reattach live QEMU，define/redefine vm.json/argv，start QEMU，执行 ACPI shutdown 或 kill，查询 QMP live status。

### 11.7 Snapshot controller

控制面职责：解析 target VM、门禁 VM cold observed、解析 per-volume mapping，生成 `SnapshotVM` 或 per-volume snapshot Task；根据 Task result 写 Snapshot disk status；删除时生成 delete snapshot Task 并 drain finalizer。

节点职责：执行 qemu-img internal snapshot fan-out/delete，返回每个 disk 的真实结果。

## 12. Finalizer 语义

资源 finalizer 由 control-plane controllers drain，不再由 node 直接移除。

删除流程：

1. apiserver DELETE 给资源打 `deletionTimestamp`，保留 finalizers。
2. control-plane controller 观察到 deleting resource。
3. controller 生成 teardown Task，并确认 Task owner/resourceVersion 仍匹配。
4. node TaskExecutor 执行本地 cleanup，Task status `Succeeded`。
5. controller 根据 Task success 移除资源 finalizer。
6. apiserver finalizers 子资源在最后一个 finalizer 移除后真删资源。
7. controller 再删除或 finalize Task 本身。

node 不再调用业务资源 `PATCH .../finalizers`。它最多只更新 Task status 或 Task finalizer。

## 13. 状态机与依赖

控制面 controller 负责所有业务资源 phase。原则：

- spec 是 desired state；status 是控制面根据 Task live result 写入的 observed projection。
- 依赖不满足时不生成 host Task，只写 pending/blocked 类状态或保持现状并重试。
- 运行中冷操作不生成执行 Task；必须先通过 VM live observed cold 状态门禁。
- Task 成功后 controller 重新读取 owner resource，确认 UID/resourceVersion 语境仍有效，再写 status。
- 若 owner spec 已变化，旧 Task result 不直接推进新 spec；controller 删除/废弃旧 Task 并按新 spec 生成新 Task。
- `Failed` VM 不被假定为 cold；必须重新查询 live runtime 或等待 executor 回报。

## 14. 与 node 上下一致的关系

上移 controller 不改变单一事实源原则。真实事实仍是 live resource：qcow2、image file、bridge/TAP、nftables、DHCP runtime、QEMU/QMP。差异只是：

- 以前 node controller 读取 live resource 后直接 patch 业务 status；
- 以后 node TaskExecutor 读取 live resource 后 patch Task status；control-plane controller 再把 Task observed result 投影到业务 status。

业务 status 仍不得成为独立事实源。任何重启恢复或 drift 处理都必须通过 TaskExecutor 的 live inspect/discover 类 task 获取事实。

## 15. 任务恢复与重启

### 15.1 govirtad 重启

Task 存在 etcd 中。control-plane controller-manager 重启后 list-then-watch API resources 和 Task objects，重新计算：

- Pending/Running Task 是否仍匹配 owner；
- Succeeded Task 是否已投影到 owner status；
- Failed Task 是否要重试；
- deleting owner 是否仍需要 teardown Task。

### 15.2 govirtlet 重启

TaskExecutor 重启后 watch 自己的 Task。对于 Running Task，它不能信任内存状态，必须按 operation 做 live inspect/ensure：

- 已完成则回报 Succeeded；
- 部分完成则继续幂等收敛；
- 无法判断则回报 Failed + 可分类错误，交由控制面重试或标记资源失败。

### 15.3 QEMU 进程生存

迁移不改变 VMM 铁律：orchestrator 崩溃不得杀死 running QEMU。TaskExecutor 仍通过 discover/reattach/QMP 查询已有进程，而不是依赖父子进程关系。

## 16. 并发控制

- 所有资源状态推进基于 `resourceVersion` CAS；冲突时重新读取并重算。
- Task create/update 使用确定性 key + CAS，避免同一资源/operation 重复任务。
- 多个 controller 观察同一依赖变化时，只允许 owner controller 修改 owner status。
- node 只能 patch Task status，不允许 patch owner resource status。
- Task status patch 也应有 no-op guard，避免 Task watch feedback loop。
- controller 不依赖事件 exactly-once；所有 reconcile 都按 level-triggered 目标状态重算。

## 17. 观测性

新增控制器与 TaskExecutor 必须沿用统一字段词汇：

- `process`：`govirtad` 或 `govirtlet`；
- `component`：`controlplane-controller-manager`、`task-executor`、具体 controller/executor；
- `operation`：Task operation 或 reconcile operation；
- `node_id`、`vm_id`、`pool`、`volume`、`image`、`network`、`nic`；
- `outcome`：`success`/`failure`。

每个资源 reconcile 和每个 Task execution 至少有 start/result 事件。错误分类应复用现有 sentinel 分类，未来接入 metrics/traces 时同样复用这些字段。

## 18. 迁移阶段

### 18.1 阶段一：Task 契约与框架

新增 Task 类型、Task store/watch/status 语义、control-plane controller-manager 薄框架、govirtlet TaskExecutor 薄框架。先实现无 host side effect 的 fake/no-op task 闭环。

### 18.2 阶段二：Storage/Image/Volume 上移

先迁 storage 族，因为它们依赖清晰且已有较强单元测试。Volume resize 的 cold gate 改为依赖 VM live observed Task result。

### 18.3 阶段三：Network/NIC 上移

迁 network 族，确保 MAC、TAP、bridge、firewall identity 都由控制面显式写入 Task input。

### 18.4 阶段四：VM/Snapshot 上移

迁 VM power/config 与 Snapshot cold fan-out。保留 ACPI shutdown 与 kill 的显式差异；Snapshot 必须继续冷门禁。

### 18.5 阶段五：移除旧 node controllers

当所有业务资源都由 control-plane controllers 驱动后，删除 `govirtlet` 中 7 个业务 resource controllers 的 wiring。`internal/node/controllers` 可被拆分为 TaskExecutor handlers 或删除旧实现，不保留新老架构长期并存。

## 19. 验证策略

### 19.1 单元测试

- control-plane controller-manager queue/list-watch/retry/resourceVersion conflict。
- Task deterministic key、phase transition、owner UID/resourceVersion mismatch 处理。
- 每个 resource controller：依赖不满足不生成 Task；依赖满足生成显式 Task；Task success 才写 status；Task failure 按错误类别处理。
- TaskExecutor handler：每个 operation 的 input validation、ctx cancellation、idempotent retry、错误分类。

### 19.2 集成测试

- fake store 下 API resource → controller → Task → fake executor → Task status → resource status。
- finalizer：DELETE resource → teardown Task → Task success → controller remove finalizer → apiserver true delete。
- conflict：controller 基于旧 RV 写 status 时遇到 409，重新读取并重算。

### 19.3 E2E

保持 `scripts/e2e.sh full` 为最终验收：etcd + govirtad + Lima govirtlet + lifecycle/cold-operation checks。迁移过程中每阶段只替换一族资源的执行路径，确保 distributed spine 持续闭环。

## 20. 风险与约束

- Task 类型会增加中间状态复杂度；必须靠确定性 key、owner UID、operation typed constants 控制复杂度。
- Task input 结构会比现有 controller 参数更细；宁可显式冗长，也不能让 node executor 回头读取业务 API 对象或推断默认。
- control-plane controller-manager 与 apiserver 同进程，但必须保持包边界，不能把 handler 变成 controller helper。
- 没有 leader election 意味着当前仅支持单 `govirtad` 写控制循环；多实例 HA 必须另行设计。
- 不做完整 informer 意味着本阶段不提供复杂本地 index；跨资源依赖查询直接通过 store/client Get/List 完成，规模化性能问题另行设计 index。

## 21. 交付边界

本 spec 确认的是总架构，不是实现计划。后续实施计划应按阶段拆分，并在每阶段明确：

- 新增/迁移哪些 controller；
- 新增哪些 Task operation/input/status；
- 哪些旧 node controller 分支被删除；
- 对应最小测试和 e2e 验证命令。

官方文档引用：不涉及第三方 SDK 或外部 API 选型；本设计基于 Govirta 现有 apiserver/store/controller/VMM/hostnet 边界。
