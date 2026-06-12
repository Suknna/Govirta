# API 写入并发语义 — apply / replace / conditional delete 设计

**日期**：2026-06-12  
**状态**：设计确认稿  
**作者**：brainstorming 协作产出

## 1. 背景与问题

生命周期后半与冷操作 1-6 刀完成后，Govirta 已具备真实的 create / update / delete
闭环：资源对象经 `govirtctl` 写入 apiserver，node controller reconcile 到 live 资源，
再经 status / finalizers 子资源回写控制面。

当前仍有一个控制面写入语义缺口：`POST/PUT /apis/{kind}/{name}` 都绑定到同一个
`Apply` handler，最终 `store.Put(..., expectedVersion="")` 无条件覆盖。它适合静态
manifest 的声明式提交，但不适合表达“我基于某个版本做了一次完整对象替换”：两个运维
或自动化同时改同一对象时，后写者可能无意覆盖先写者的 spec 变更。

同时，finalizers 子资源的真删收口仍用无条件 `store.Delete(ctx, key)`。当最后一个
finalizer 被移除后，handler 从 store 读到版本 `N` 并准备删除；若 Get→Delete 间对象被
并发写成版本 `N+1`，无条件 delete 会盲删新版本。

这两个问题都不是 rollback 问题。`metadata.resourceVersion` 的核心价值是：

- **乐观并发控制**：防止 lost update；
- **watch 续传**：节点/控制器从某个版本后继续 watch；
- **finalizer 收口一致性**：确认删除的是刚才读到的那个版本。

虚拟机是重资产，不能随意删建，反而更需要避免旧 manifest 盲覆盖新 spec。

本设计把日常声明式 apply 与并发安全 replace 分开：`POST apply` 继续服务静态 manifest，
`PUT replace` 提供 Kubernetes-style `resourceVersion` CAS 更新。

## 2. 已核实的当前实现

已通过源码核实：

- `metadata.resourceVersion` 字段已存在于 `pkg/apis/meta/v1alpha1.ObjectMeta`。
- `GET /apis/{kind}/{name}` 当前返回 raw stored JSON，并只在 `X-Resource-Version`
  header 携带 store RV；body 通常没有 `metadata.resourceVersion`。
- `apply` create 会注入 `govirta.io/node-teardown` finalizer。
- `apply` update 已保留 server-owned metadata：
  `resourceVersion/deletionTimestamp/finalizers`，VM 还保留 `nodeName`。
- `apply` update 已保留旧 status（Knife 5 修复），不会清空 node-owned status projection。
- `ReferenceValidator` 已拒绝引用带 `deletionTimestamp` 的删除中对象，Knife 1 note #31
  的 apply-side 悬挂引用窗口已关闭。
- DELETE 打戳路径已用 CAS `store.Put(..., raw.ResourceVersion)`。
- finalizers 回写路径已用 CAS `store.Put(..., raw.ResourceVersion)`。
- finalizers 真删路径仍是无条件 `store.Delete(ctx, key)`。

## 3. 外部参考

Kubernetes API Concepts（官方文档，2026-03-30 更新，
`https://kubernetes.io/docs/reference/using-api/api-concepts/`）说明：

- HTTP PUT update 由客户端提交完整对象；客户端负责带上从对象读取到的
  `metadata.resourceVersion`；若提交版本过期，apiserver 返回 `409 Conflict`。
- PATCH / Server-Side Apply 是另一套机制；Server-Side Apply 依赖 managed fields / field
  ownership，不应在 Govirta 当前阶段冒充。
- 资源删除是 finalization → removal 两阶段，最后一个 finalizer 移除后对象才从存储中删除。

Govirta 本刀只采用 PUT update 的乐观并发控制语义，不实现 Server-Side Apply。

## 4. API 语义

### 4.1 POST apply：声明式无条件提交

`POST /apis/{kind}/{name}` 保持当前 `apply` 语义：

- create 或 update 都允许；
- 不要求 manifest 带 `metadata.resourceVersion`；
- 最终写 store 仍是无条件 `Put(..., expectedVersion="")`；
- 保留 server-owned metadata/status；
- 继续运行现有 validating admission 与 mutating admission（MAC 分配、VM scheduling、
  Snapshot nodeName 派生、finalizer 注入等）。

这是给静态 manifest / e2e bring-up / 声明式提交用的轻量路径。它不承诺防 lost update；
需要并发安全更新的调用方必须使用 `replace`。

### 4.2 PUT replace：只更新已存在对象的 RV-CAS 完整替换

`PUT /apis/{kind}/{name}` 改为新的 `Replace` handler。

规则：

1. 目标对象必须已存在；不存在返回 `404 Not Found`。
2. body 必须带 `metadata.resourceVersion`；缺失返回 `400 Bad Request`。
3. body 的 `metadata.resourceVersion` 必须等于当前 store 版本；不匹配返回
   `409 Conflict`。
4. 成功时返回 `200 OK` + stored object body，body 内带新的
   `metadata.resourceVersion`。
5. replace 仍是完整对象替换，仍走同一套 admission 与 server-owned 字段保护；它只比
   apply 多一个 RV-CAS 前置条件，不放开 server-owned 字段。

### 4.3 server-owned 字段保护

replace 保持 apply update 的保护规则：

- `status` 由 node `PATCH .../status` 独占，replace 保留旧 status；
- `metadata.finalizers`、`metadata.deletionTimestamp`、`metadata.resourceVersion` 由 server
  管理；replace 以提交的 RV 做 CAS，但持久化对象里的这些字段来自旧对象 / 新 store RV；
- VM `metadata.nodeName` 由 scheduler / bind 语义管理，replace 保留旧 nodeName，不能变成
  隐式迁移；
- Snapshot `nodeName` 仍由 target VM 的 nodeName 派生；
- NIC 空 MAC update 继承旧 MAC；显式改 MAC 继续由 immutable validator 拒绝；
- immutable/cold-mutable/reference/power validators 全部继续生效。

## 5. GET body 注入 resourceVersion

为了支持 Kubernetes-style `get → edit → replace`，single-object GET 必须把 store RV 注入
body：

```http
GET /apis/VM/vm-e2e
200 OK
X-Resource-Version: 123

{
  "metadata": {
    "name": "vm-e2e",
    "uid": "vm-e2e-001",
    "resourceVersion": "123"
  },
  ...
}
```

要求：

- header `X-Resource-Version` 保留，兼容现有客户端；
- body `metadata.resourceVersion` 必须与 header 一致；
- 注入只改变响应体，不改变 store 中的 raw value；
- 本刀只保证 single GET。List 仍可保持 raw array pass-through，不引入 `ListMeta`。

实现上新增 kind-agnostic JSON helper：解析顶层 `metadata` object，写入
`resourceVersion` 字段后重新编码。若对象 malformed，返回 5xx，因为 store 中对象应已由
write path 保证合法。

## 6. Store conditional delete

新增显式接口：

```go
DeleteIfVersion(ctx context.Context, key string, expectedVersion string) error
```

语义：

- key 不存在：返回 `nil`（保持现有 Delete 的幂等语义）；
- key 存在且当前 version == expectedVersion：删除成功，发出 DELETED watch event；
- key 存在但当前 version != expectedVersion：返回 `store.ErrRevisionConflict`，不删除；
- expectedVersion 为空：返回 `store.ErrRevisionConflict`，不删除。空字符串没有可匹配版本，
  且调用方不得用空版本调用该方法。

finalizers 真删收口改为：

```go
if len(finalizers)==0 && deletionTimestamp!="" {
    store.DeleteIfVersion(ctx, key, raw.ResourceVersion)
}
```

并发写发生时返回 409，让 node/controller 后续 watch/reconcile 重试，禁止盲删新版本。

## 7. govirtctl UX

新增命令：

```bash
govirtctl replace --server <url> -f <object.json>
```

行为：

- 读取完整对象；
- 要求 `kind`、`metadata.name`、`metadata.resourceVersion` 均存在；
- 调 `PUT /apis/{kind}/{name}`；
- 成功输出 `<Kind>/<name> replaced`；
- 404 提示对象不存在，应先 `apply` 创建；
- 400 提示缺 RV 或 manifest 非法；
- 409 提示版本冲突，应重新 `get` 最新对象再编辑/replace。

推荐运维流程：

```bash
govirtctl get --server http://127.0.0.1:8080 VM vm-e2e > vm.json
# 编辑 vm.json 的 spec
govirtctl replace --server http://127.0.0.1:8080 -f vm.json
```

`govirtctl apply -f` 不变，继续用于静态声明式提交；`replace` 才表示“我基于某个版本安全更新”。

## 8. 验证策略

### Store contract

- `DeleteIfVersion` 匹配版本删除成功；
- 过期版本返回 `ErrRevisionConflict` 且对象仍存在；
- 缺对象返回 nil；
- fake 与 etcd 实现共用 `RunStoreContract`。

### Apiserver

- GET body 注入 `metadata.resourceVersion`，header/body 一致；
- PUT replace missing RV → 400；
- PUT replace stale RV → 409；
- PUT replace missing object → 404；
- PUT replace matching RV → 200，body 携带新 RV；
- replace 保留 status/finalizers/deletionTimestamp/nodeName；
- replace 仍运行 validators，例如 immutable 改动仍 409；
- finalizers 真删走 `DeleteIfVersion`，并发写导致 409 而非盲删。

### govirtctl / e2e

在 `TestDistributedSpineClosure` 中增加轻量 replace 场景：

1. `govirtctl get VM vm-e2e` 输出 body 含 `metadata.resourceVersion`；
2. 修改一个安全 spec 字段并 `govirtctl replace` 成功；
3. 用旧 RV 再 replace 一次，验证 409；
4. 不把现有 e2e bring-up 全部改成 replace，保持 apply/replace 两条路径都被覆盖。

## 9. Out of Scope

- 不实现 Server-Side Apply / managedFields / field ownership。
- 不把 `apply -f` 改成强制 RV-CAS。
- 不实现 interactive `govirtctl edit`。
- 不给 List 引入 ListMeta / collection resourceVersion。
- 不做多对象事务；Govirta 和 Kubernetes 一样，单资源 verb 只处理一个对象。
