# Task Phase One Design

**日期**：2026-06-12  
**状态**：已确认设计  
**上位设计**：`docs/superpowers/specs/2026-06-12-controlplane-controller-manager-design.md`

## 1. 目标

阶段一只建立 Task 最小闭环：Task 成为内部一等资源，control plane 能生成并推进 `Node` / `Cluster` 两类 Task，`govirtlet` 只 watch 分配给自己的 `Node` Task 并 patch Task status，control plane 直接执行 `Cluster` Task。阶段一不迁移任何现有业务控制器，`internal/node/controllers` 下的 StoragePool/Image/Volume/Network/NIC/VM/Snapshot 控制器继续按现状运行。

验收目标：

- `Task` 有强类型 API 契约与 `TaskScope=Node|Cluster`。
- `/apis/Task` 可被内部组件 `GET/List/Watch/PATCH status`。
- 用户不能通过 `POST apply` 或 `PUT replace` 声明式创建/替换 Task。
- `govirtad` 的 control-plane controller-manager 可生成 `NodeTask` 并等待节点 status，也可直接执行 `ClusterTask`。
- `govirtlet` 只 watch 自己的 `NodeTask`，执行 no-op TaskExecutor 后 patch `Task.status`。

## 2. 非目标

- 不迁移 StoragePool/Image/Volume/Network/NIC/VM/Snapshot 业务控制器。
- 不删除或停用现有 `govirtlet` 业务资源 watch/reconcile 路径。
- 不实现真实 host side effect task，例如 qemu-img、QMP、netlink、nftables、CoreDHCP 或 VMM 操作。
- 不引入 informer、watch cache、leader election、RBAC 或认证授权。
- 不把 Task 作为用户可声明式管理的业务资源暴露给 `govirtctl apply`。

## 3. 方案选择

采用方案 A：`Task` 进入现有 `/apis/{kind}` HTTP 面，但创建与替换只允许 control-plane 内部写 store。

原因：

- 复用现有 `store.Store`、list-then-watch、nodeName routing、status patch、fake/etcd 一致语义。
- `govirtlet` 可以复用 `client.WatchSource`、`client.PatchStatus` 和 node controller-manager 薄框架。
- 验收链路经过真实 HTTP watch/status，而不是绕过分布式 spine 的 fake-only 闭环。

被排除方案：

- 新增专用 Task endpoint：会重复实现 watch/status 路由，并绕开已验证的 `/apis/{kind}` 语义。
- 只做 store 内部 fake executor：无法证明 `govirtlet watch → execute → patch status`。

## 4. Task API 契约

新增 `pkg/apis/task/v1alpha1`，并在 `pkg/apis/meta/v1alpha1` 增加 `KindTask`。

`Task` 使用项目现有 API envelope：

```text
Task
  typeMeta.apiVersion
  typeMeta.kind = Task
  metadata.name
  metadata.uid
  metadata.nodeName
  metadata.resourceVersion
  spec.scope
  spec.ownerKind
  spec.ownerName
  spec.ownerUID
  spec.operation
  spec.input
  status.phase
  status.observed
  status.errorClass
  status.message
  status.startedAt
  status.completedAt
```

阶段一约束：

- `TaskScope` 是专用类型，合法值只有 `Node` 和 `Cluster`。
- `TaskOperation` 是专用类型；阶段一只定义 no-op 操作，例如 `NoopNode` 与 `NoopCluster`。
- `Node` scope 必须带 `metadata.nodeName`，节点 watch 使用现有 `nodeName` 过滤。
- `Cluster` scope 必须不带 `metadata.nodeName`，避免被任何 `govirtlet` watch 到。
- `spec.ownerKind/name/uid` 必填，阶段一可由 no-op owner 测试资源语境填入；后续业务迁移再绑定真实资源。
- `spec.input` 使用强类型输入 envelope；阶段一 no-op 输入也要显式存在，不能用空值代表默认行为。
- `status.phase` 是专用类型：`Pending`、`Running`、`Succeeded`、`Failed`。
- `status.observed` 存放执行方回报的显式 observed JSON；阶段一 no-op 至少回报 executor identity 与 completion marker。

阶段一不引入 Task finalizer。Task 是执行记录，不在此阶段承载本地 teardown 语义；后续删除/撤销类 task 设计时再引入 finalizer。

## 5. Apiserver 边界

`/apis/Task` 支持内部读写所需的通用 API 语义：

- `GET /apis/Task/{name}`：读取 Task。
- `GET /apis/Task`：列出 Task。
- `GET /apis/Task?watch=true&nodeName=<node>`：节点 watch 自己的 `Node` scope Task。
- `PATCH /apis/Task/{name}/status`：执行方 patch Task status。

`POST /apis/Task/{name}` 与 `PUT /apis/Task/{name}` 必须拒绝，避免用户或 `govirtctl apply` 直接声明式管理 Task。control-plane controller-manager 通过 store/internal client 创建和更新 Task spec/status，仍使用确定性 key、resourceVersion CAS 和 typed validation。

Task status patch 继续复用 apiserver 的 status subresource 语义：只替换 `status` 字段，不允许改 spec 或 metadata。Task status admission 只校验 bare status body 与 phase/error 字段，不做业务 owner 推进。

## 6. Control-plane Controller-manager

新增 `internal/controlplane/controller` 作为 control-plane 控制器框架。阶段一保持薄实现：

- `Manager.Run(ctx)` 与 apiserver 同生命周期运行。
- 框架提供 controller 注册、watch/list 事件接入、dedup queue、requeue、ctx cancellation。
- 写 Task 使用内部 `TaskClient`，该 client 只依赖 `store.Store`，不调用 apiserver handler 私有函数。
- `TaskClient` 提供 typed `CreateOrUpdateTask`、`GetTask`、`PatchTaskStatus` 或等价方法，内部执行 marshal/unmarshal、validate、CAS。

阶段一内置两个 no-op 控制器/执行路径：

1. `NodeTask` 生成器：创建一个确定性 name 的 `Node` scope no-op Task，`metadata.nodeName` 来自显式配置中的节点名。
2. `ClusterTask` executor：创建或读取一个确定性 name 的 `Cluster` scope no-op Task，并由 control-plane 内部 executor 将 status 推进到 `Succeeded`。

这两个路径只证明 Task 生命周期，不接入任何业务 API 资源。

## 7. Govirtlet TaskExecutor

`govirtlet` 在保留现有 7 个业务控制器的同时，新增一个 Task controller/executor：

1. `WatchSource.Watch(ctx, "Task", startRevision)` 连接 `/apis/Task?watch=true&nodeName=<node>`。
2. 只处理 `spec.scope=Node` 且 `metadata.nodeName` 等于本节点名的 Task。
3. 对 `Pending` Task patch `Running`，执行 no-op handler，再 patch `Succeeded`。
4. 对已终态 `Succeeded` / `Failed` Task no-op，避免 status feedback loop。
5. 遇到不支持 operation、scope 不匹配、输入非法时 patch `Failed`，带稳定 `errorClass` 与 message。

TaskExecutor 不读取 StoragePool/Image/Volume/Network/NIC/VM/Snapshot 业务对象，不 patch 业务资源 status/finalizers，不推断默认值。

## 8. 数据流

### NodeTask

```text
control-plane controller
  -> store.Put /govirta/Task/<node-noop-task>
  -> apiserver watch /apis/Task?nodeName=node0
  -> govirtlet TaskExecutor
  -> PATCH /apis/Task/<name>/status Running
  -> no-op handler
  -> PATCH /apis/Task/<name>/status Succeeded
  -> control-plane controller observes Task success
```

### ClusterTask

```text
control-plane controller
  -> store.Put /govirta/Task/<cluster-noop-task>
  -> control-plane executor reads Task
  -> internal TaskClient patches status Running
  -> no-op handler
  -> internal TaskClient patches status Succeeded
```

`ClusterTask` 不带 `metadata.nodeName`，因此不会出现在任何 nodeName-filtered watch 中。

## 9. 错误处理与可观测性

- 所有 Task phase、scope、operation、errorClass 都使用专用 typed constants。
- 执行错误必须回写 Task status；禁止只 log 不回报。
- retry 只在框架层重新入队，不在 executor 内部无限循环。
- 日志字段复用项目词汇表：`component`、`operation`、`node_id`、`outcome`、`error`。
- no-op handler 也记录 start/result，确保后续真实 handler 沿用同一模式。

## 10. 测试策略

单元测试：

- `pkg/apis/task/v1alpha1`：roundtrip、Validate、scope/operation/phase typed constants。
- apiserver：`POST/PUT /apis/Task` 拒绝；`GET/List/Watch/PATCH status` 可用；NodeTask 只被匹配 nodeName 的 watch 看到；ClusterTask 不被节点 watch 看到。
- control-plane controller：NodeTask deterministic create/update；ClusterTask 直接推进到 `Succeeded`；status no-op guard 避免反馈循环。
- govirtlet TaskExecutor：watch 到 NodeTask 后 patch Running/Succeeded；终态 no-op；非法 operation patch Failed。

集成测试：

- fake store + apiserver + node WatchSource：`NodeTask` 从 control-plane 写入后被指定 node watch 到并 patch status。
- fake store + control-plane manager：`ClusterTask` 不经 govirtlet 直接完成。

最终本地验证命令：

```bash
go test ./...
```

## 11. 文件体量预判

本阶段会新增多个小文件，避免单文件承担过多职责：

- `pkg/apis/task/v1alpha1/types.go`：预计 150-250 行。
- `internal/controlplane/controller/*`：按 manager、queue、task client、no-op controller/executor 拆分，单文件预计低于 250 行。
- `internal/node/controllers/task.go`：预计 150-250 行。
- apiserver 仅补 kind 分支与 admission 分支，不做大规模重排。

预计不会新增或大幅修改超过 500 行的源文件；不需要硬上限豁免。

## 12. 验收边界

阶段一完成时，只能声明以下能力成立：

- Task 是内部 API 一等资源。
- Task 的 Node/Cluster 执行路径可闭环。
- govirtlet 可作为 TaskExecutor 执行节点分配的 Task。
- control plane 可直接执行 ClusterTask。

不得声明业务控制器已上移；不得删除或削弱现有 node 业务资源控制器。

官方文档引用：不涉及第三方 SDK 或外部 API 选型；本设计基于 Govirta 现有 apiserver/store/watch/controller 边界。
