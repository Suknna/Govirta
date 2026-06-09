# 刀 3：Validating Admission Controller — 设计说明

## 1. 背景与目标

刀 1 已完成 finalizer 两阶段删除，刀 2 已完成 VM `powerState` 声明式电源生命周期。按照生命周期总览，下一步原本是 **Update 字段分级**：在 update 时拒绝 immutable 字段变化，并为 cold-mutable 冷操作建立门禁地基。

本刀把这个目标下沉为更基础的控制面能力：在项目内部建立 **Admission Controller** 概念，且本刀只实现 **Validating Admission**，不实现 Mutating Admission 框架。所有会修改 etcd 对象的 HTTP 入口都必须经过 validating admission：

- Apply create/update：`POST/PUT /apis/{kind}/{name}`
- DELETE：`DELETE /apis/{kind}/{name}`
- status PATCH：`PATCH /apis/{kind}/{name}/status`
- finalizers PATCH：finalizer 子资源入口

本刀完成后，apiserver handler 的职责应收敛为 HTTP 解包、kind dispatch decode、调用 admission、执行已有 mutation、写 store；所有 validating policy 从 handler 中迁入 `internal/controlplane/apiserver/admission`。

## 2. 明确边界

### 2.1 In Scope

- 新建 `internal/controlplane/apiserver/admission` 子包。
- 定义 validating admission 的 `Request` / `Operation` / `Subresource` / `Validator` / `Chain` / registry。
- 所有 etcd-mutating entrypoints 接入 admission chain。
- Apply admission 接管所有现有 spec validation，并新增 update 字段分级、引用完整性、VM powerState 规则等 validators。
- DELETE admission 接管保护式反向引用扫描。
- status PATCH admission 校验 patch shape 与 status enum contract。
- finalizers PATCH admission 使用白名单 finalizer 模型。

### 2.2 Out of Scope

- 不实现 Mutating Admission 框架。
- 不迁移现有 mutation：MAC 分配、finalizer 注入、VM scheduling / nodeName binding、VM update server-owned metadata preservation 继续留在现有 handler 路径。
- 不执行冷扩容、冷配置改、增删硬件，不重建 argv，不调用 qemu-img resize。
- 不实现 Job spec；换 pool、存储迁移、冷热计算迁移继续属于未来 Job spec。
- 不引入认证授权或 node identity。本刀 finalizers PATCH 使用白名单模型，未来再用 authz 精化。

## 3. 包结构与核心抽象

Admission 框架位于 apiserver 子包内：

```text
internal/controlplane/apiserver/
├── handler_apply.go
├── handler_delete.go
├── handler_status.go
├── handler_finalizers.go
└── admission/
    ├── request.go
    ├── chain.go
    ├── registry.go
    ├── apply.go
    ├── delete.go
    ├── status.go
    └── finalizers.go
```

核心类型：

```go
type Operation string

const (
    OperationCreate          Operation = "Create"
    OperationUpdate          Operation = "Update"
    OperationDelete          Operation = "Delete"
    OperationStatusPatch     Operation = "StatusPatch"
    OperationFinalizersPatch Operation = "FinalizersPatch"
)

type Subresource string

const (
    SubresourceNone       Subresource = ""
    SubresourceStatus     Subresource = "status"
    SubresourceFinalizers Subresource = "finalizers"
)

type Request struct {
    Operation   Operation
    Subresource Subresource
    Kind        metav1.Kind
    Name        string

    OldRaw []byte
    NewRaw []byte

    OldObject any
    NewObject any
}

type Validator interface {
    Name() string
    Validate(ctx context.Context, req Request) error
}
```

`Request` 同时携带 raw bytes 和 typed object：handler 在进入 admission 前完成 kind dispatch decode，validator 通过 type switch 使用 typed object；raw bytes 保留给通用 shape 校验、日志和错误上下文。

Apply 有两个 validating 阶段：

1. **pre-mutation admission**：验证用户请求、old/new diff、引用完整性。
2. **post-mutation admission**：验证 mutation path 产生的最终入库对象仍满足契约，例如 NIC MAC allocator 填充后的 `spec.mac` 合法。

这两个阶段都只做 validating，不执行 mutation；mutation 仍由 handler 现有路径完成。

## 4. 错误模型

Admission 返回结构化错误，不直接写 HTTP response。

```go
type ErrorReason string

const (
    ReasonBadRequest ErrorReason = "BadRequest"
    ReasonConflict   ErrorReason = "Conflict"
    ReasonInternal   ErrorReason = "Internal"
)

type Error struct {
    Validator string
    Reason    ErrorReason
    Err       error
}
```

HTTP 映射由 handler 完成：

- `ReasonBadRequest` → 400：请求 shape、enum、spec validate 失败；引用目标不存在也属于请求引用了非法对象。
- `ReasonConflict` → 409：immutable 变更、被引用删除、引用 deletionTimestamp 对象、finalizer precondition 失败。
- `ReasonInternal` → 500：old object decode 损坏、store read 失败、内部类型断言失败。

handler/store/scheduler 仍保留 admission 之外的错误映射：目标对象不存在的 404、unknown kind 沿用各 handler 现有映射、调度无节点的 503 等不强行塞进 Admission `ErrorReason`。

Admission error 必须 `%w` 包裹原始错误，保留 `errors.Is` 能力。`Validator` 名称必须进入错误，便于定位是哪条 validating policy 拒绝。

## 5. Apply Admission

Apply handler 构造：

```go
admission.Request{
    Operation: Create or Update,
    Subresource: admission.SubresourceNone,
    Kind: kind,
    Name: name,
    OldRaw: oldRaw,
    NewRaw: requestBody,
    OldObject: oldTypedObject,
    NewObject: newTypedObject,
}
```

Create 时 `OldRaw` / `OldObject` 为空；update 时必须能 decode old object。old object decode 失败是控制面内部数据损坏，返回 500。

### 5.1 Apply validators 顺序

固定顺序如下：

1. **Envelope validator**
   - `apiVersion` / `kind` 必须正确。
   - URL `{name}` 必须等于 `metadata.name`。
   - `metadata.uid` 必须显式提供。
   - update 时 `metadata.uid` 不可变。
   - user body 不能伪造或改写 server-owned metadata：`resourceVersion`、`deletionTimestamp`、`finalizers`。
   - create 时也不允许用户提交 finalizers；默认 `FinalizerNodeTeardown` 由现有 mutating path 注入。
   - `nodeName` 的 VM-specific 规则由 VM validator 处理。

2. **Spec validator**
   - 所有 `XxxSpec.Validate()` 迁入 admission。
   - `handler_apply.go` 不再直接承载 spec validating policy。

3. **Apply operation validator**
   - create 确认 old object 缺失。
   - update 确认 old object 存在且 decode 成功。
   - 分类只用于 admission；store write 仍保持现有 unconditional put 语义。

4. **Immutable field validator**
   - update 时对比 old/new spec。
   - immutable 字段变化直接拒绝 409。

5. **Cold-mutable classifier validator**
   - update 时识别 cold-mutable 字段变化。
   - 不因为运行中而拒绝，不读 status 作为事实源。
   - 只校验变化方向是否合法，例如 `Volume.capacityBytes` 只能增大不能缩小。

6. **Reference integrity validator**
   - 新对象或更新对象引用的目标必须存在。
   - 引用目标不能带 `deletionTimestamp`。
   - 关闭刀 1 遗留的打删除戳到真删之间新引用窗口。

7. **VM powerState validator**
   - create 只允许 `On` / `Off`。
   - update 允许 `On` / `Shutdown` / `Off`。
   - `nodeName` update：body 为空则现有 mutation path 保留旧 binding；body 与旧 nodeName 不同则拒绝 409；body 与旧 nodeName 相同则允许。

8. **NIC final MAC validator**
   - MAC allocation 仍是现有 mutation path。
   - 该 validator 属于 post-mutation admission：MAC 分配完成后、store write 前验证最终 `NIC.spec.mac` 合法。

### 5.2 字段分级初版

| 资源 | immutable | cold-mutable | live-mutable |
| --- | --- | --- | --- |
| VM | `arch` | `memoryMiB`、`vcpus`、`volumeRefs`、`nicRefs` | `powerState` |
| Volume | `poolRef`、`vmRef`、`vmName`、`diskIndex`、`role`、`imageRef`、`imageFilePoolRef` | `capacityBytes`（只增不减） | 无 |
| NIC | `networkRef`、`vmRef`、`mac` | 无 | 无 |
| Network | 全部 spec 字段 | 无 | 无 |
| Image | 全部 spec 字段 | 无 | 无 |
| StoragePool | 全部 spec 字段 | 无 | 无 |

Admission 只验证字段分级合法性。cold-mutable 是否实际执行，必须留给 node controller 基于 live resource phase 判断；不能用 apiserver status projection 当作 stopped gate。

## 6. DELETE Admission

DELETE handler 继续负责状态机：不存在返回 404；存在且没有删除戳则打 `deletionTimestamp`；finalizers 清空时真删；重复 DELETE 保持幂等 202。

Admission validators：

1. **Existing object decode validator**
   - old object 必须能 decode 成对应 kind。
   - decode 失败返回 500。

2. **Protective reference validator**
   - 被其它对象引用则拒绝 409。
   - 迁入 admission 后保持刀 1 guard 的保护式拒绝语义，并补齐 VM 反向引用扫描。

3. **Deleting object idempotence validator**
   - 已有 `deletionTimestamp` 的对象再次 DELETE 仍重扫引用。
   - 防止删除窗口中新引用漏进来。

引用图：

| 删除对象 | 必须扫描的引用 |
| --- | --- |
| StoragePool | `Volume.spec.poolRef`、`Image.spec.filePoolRef`、`Volume.spec.imageFilePoolRef` |
| Image | `Volume.spec.imageRef` |
| Network | `NIC.spec.networkRef` |
| Volume | `VM.spec.volumeRefs`（按 Volume object name） |
| NIC | `VM.spec.nicRefs`（按 NIC object name） |
| VM | `Volume.spec.vmRef`、`NIC.spec.vmRef`（按 VM UID） |

## 7. status PATCH Admission

status PATCH handler 继续负责 read-modify-write CAS。Admission 校验 patch body 和目标对象。

Validators：

1. **Patch shape validator**
   - 现有协议是裸 status JSON：request body 本体就是对应 kind 的 Status 对象。
   - body 不是完整资源对象，也不是 `{ "status": ... }` wrapper。
   - 如果 body 形状像完整对象并夹带 `spec` / `metadata`，必须拒绝。

2. **Status type validator**
   - request body 必须能 decode 成对应 kind 的 Status 类型。
   - 所有 status phase enum 必须合法。
   - VM status 额外校验 `ObservedPowerState` 与 `PowerTransition`。
   - `message` 允许为空。

3. **Target object validator**
   - 目标对象必须存在并能 decode。
   - 对象有 `deletionTimestamp` 且 finalizers 未清空时允许 status patch，用于 teardown 进度/失败回报。
   - 对象已进入 finalizers 为空的真删收口窗口时拒绝 409，避免写回僵尸状态。

## 8. finalizers PATCH Admission

finalizers handler 继续负责 finalizers 清空后的真删收口。Admission 只验证请求是否可以修改 finalizers。

Validators：

1. **Patch shape validator**
   - 现有协议是单字段 remove 请求，例如 `{"remove":"govirta.io/node-teardown"}`。
   - 不设计通用 JSON Patch / merge patch，也不提交新的 `metadata.finalizers` 集合。
   - 拒绝空 `remove`、未知字段、以及任何试图夹带 spec/status/其它 metadata 的 body。

2. **Whitelist finalizer validator**
   - 现阶段只允许移除 `metav1.FinalizerNodeTeardown`。
   - 不允许移除其它 finalizer。

3. **Deletion precondition validator**
   - 目标对象必须已有 `deletionTimestamp`。
   - 没有删除戳时拒绝移除 finalizer。

4. **Decode / existence validator**
   - 目标对象不存在的 404 仍由 handler 处理。
   - 目标对象 decode 失败返回 500。

## 9. Handler 迁移策略

迁移按以下顺序执行：

1. 新建 admission 框架。
2. Apply 接入 admission，并迁移现有 validating logic。
3. Apply 新增 immutable / cold-mutable / reference-to-deleting validators。
4. DELETE 保护式引用扫描迁入 admission。
5. status PATCH 接入 admission。
6. finalizers PATCH 接入 admission。
7. 保持 mutation 和 store write 路径在 handler，避免本刀引入 Mutating Admission。

最终 handler 结构应类似：

```text
HTTP write handler
  -> parse route/body
  -> decode typed old/new objects
  -> build pre-mutation admission.Request
  -> admission.Chain.Validate(ctx, preMutationReq)
  -> existing mutation path
  -> admission.Chain.Validate(ctx, postMutationReq) // apply only, validates final object contract
  -> store write/CAS/delete
```

## 10. 测试与验收

### 10.1 admission 包单元测试

- chain 顺序执行、遇错短路、validator name 出现在错误中。
- 每类 operation 都能构造 Request。
- Apply validators 覆盖 6 kind。
- DELETE reference validators 覆盖完整引用图。
- status validators 覆盖所有 phase enum。
- finalizers validators 覆盖 whitelist / no-add / deletion precondition。

### 10.2 apiserver handler 测试

- apply create/update 现有行为不漂移。
- 所有 `XxxSpec.Validate()` 失败都经 admission 返回 400。
- immutable update 返回 409。
- cold-mutable update 被 apiserver 接受，不因 status/running 被拒绝。
- apply 引用 `deletionTimestamp` 对象返回 409。
- DELETE 被引用对象返回 409。
- status PATCH 非法 phase 返回 400。
- status PATCH 夹带 spec/metadata 返回 400。
- finalizers PATCH 非删除中对象返回 409。
- finalizers PATCH 夹带新增/替换 finalizers 字段或删除非白名单 finalizer 返回 400/409。
- old object decode 损坏返回 500。

### 10.3 验证命令

```bash
scripts/verify.sh
go test -race ./internal/controlplane/apiserver/...
go test -race ./...
scripts/e2e.sh full
```

## 11. 完成判据

- 所有 etcd-mutating entrypoints 都调用 admission。
- Admission 框架位于 `internal/controlplane/apiserver/admission`。
- Apply admission 包含所有 spec validation。
- DELETE / status PATCH / finalizers PATCH 都有业务 validators。
- 没有新增 Mutating Admission framework。
- 现有 mutation 行为保持原位且行为不漂移。
- 冷操作执行面不在本刀实现。
- Job spec / pool migration / compute migration 继续 out of scope。
