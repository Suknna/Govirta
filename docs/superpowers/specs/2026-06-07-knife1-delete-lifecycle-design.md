# 刀 1 设计：Delete 生命周期（Finalizer 两阶段 + 反向引用保护式拒绝）

**日期**：2026-06-07
**状态**：设计已锁，待写实现计划
**上游**：`docs/superpowers/specs/2026-06-07-lifecycle-coldops-overview-design.md`（总览，5 决策 + 7 资源模型）
**本刀定位**：生命周期后半的第一刀，横切全资源的删除基础。Update / 冷操作（7-9）都隐含"拆除"能力，必须最先做。

---

## 1. 目标与范围

### 做什么

让 6 类节点资源（StoragePool / Image / Volume / Network / NIC / VM）可被安全删除：

1. **Finalizer 两阶段删除**：DELETE 只打 `deletionTimestamp`，node 拆净 live 资源后摘除 finalizer，apiserver 见 finalizers 清空才真正 `store.Delete`。
2. **反向引用保护式拒绝**：删被引用对象返回 409，用户必须先删上层，逆序由用户显式驱动。
3. **控制器接 EventDeleted 拆除**：6 个控制器的 no-op DELETE 分支改为"见删除戳就拆 live 资源"，复用已存在的下层 `Delete*`。
4. **finalizer 专用端点**：node 通过窄端点摘除自己的 finalizer。
5. **govirtctl delete** 命令 + 三节点 e2e 逆向拆除闭环。

### 不做什么（明确 out of scope）

- VM stop/start（刀 2）、Update（刀 3）、冷操作 7-9（刀 4-6）。
- 级联删除（cascade）——总览已定保护式拒绝，绝不连带删。
- ownerReferences / 垃圾回收机制——不引入。
- 换池 / 迁移（未来 Job spec，memory 832 / note #29）。

---

## 2. 架构原则（继承总览，本刀相关的 4 条）

1. **声明式 level-triggered**：删除是"改 etcd 期望态（打 deletionTimestamp）→ node watch → reconcile 拆除 → 摘 finalizer"，无命令式同步删除。
2. **上下一致 / live 实况是唯一权威**：拆除以 live 资源为准，幂等；拆净判定由 node 持有，不由 apiserver 投影决定。
3. **显式优于隐式**：finalizer admission 注入（有条件，仅 finalizers 为空时）；删被引用对象边界拒绝，不偷偷级联。
4. **finalizer 保证下层先拆净、上层才消失**：拆除失败 → 保留 finalizer → 对象卡在"删除中"持续重试，绝不"对象消失但资源泄漏"。

---

## 3. API 契约新增（`pkg/apis/meta/v1alpha1`）

`ObjectMeta` 现状字段：`Name` / `UID` / `ResourceVersion` / `NodeName` / `Labels`。新增两字段：

```go
type ObjectMeta struct {
	Name            string            `json:"name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	NodeName        string            `json:"nodeName,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	// 新增：
	DeletionTimestamp string   `json:"deletionTimestamp,omitempty"` // RFC3339；空=未删
	Finalizers        []string `json:"finalizers,omitempty"`        // 拆净前阻止真删的守卫列表
}
```

- `DeletionTimestamp` 用 `string`（RFC3339），空串表示未删。不引入 `*time.Time` 以保持 JSON 往返简单、与现有字段风格一致。
- `Finalizers` 为 `[]string`，本刀只用一个常量 finalizer：`metav1.FinalizerNodeTeardown = "govirta.io/node-teardown"`（命名常量，项目铁律：no bare string）。
- `Validate()` 不强制要求这两字段（删除前为空是正常态）。
- 往返测试：扩展现有六对象 round-trip，覆盖带 finalizer + deletionTimestamp 的序列化。

---

## 4. Finalizer Admission 注入（apiserver apply 路径）

### 机制（复用 MAC / 调度先例）

与 `applyNIC`（MAC 注入）、`bindVM`（调度注入）同构——在 `store.Put` **之前**改写对象。新增 `injectFinalizer`：

```
apply 任意节点资源
  → validateObject（已有）
  → 若 obj.Finalizers 为空：obj.Finalizers = ["govirta.io/node-teardown"]   ← 注入
  → （NIC: MAC 注入 / VM: 调度注入，已有）
  → store.Put（带 finalizer 的对象）
```

- **有条件注入**：仅当 `Finalizers` 为空时设默认值，已有 finalizer 的对象尊重原值（与 MAC/调度有条件注入一致，为未来多 finalizer 留口）。
- 注入对所有 6 类节点资源生效（当前全部是节点资源，都有 live 资源要拆）。
- 注入位置：`handler_apply.go` 的 kind 分发分支，put 之前。集中一处，不散落。

### 关键正确性

对象一落 etcd 就带 finalizer，消除"finalizer 还没加就被 DELETE 真删 → 节点已建部分资源 → 泄漏"的竞态窗口（这是选 apiserver 注入而非 node 自注册的核心理由）。

---

## 5. DELETE 请求状态机（apiserver，严格 k8s 语义）

新增路由：`DELETE /apis/{kind}/{name}` → `Server.Delete` handler。

```
DELETE /apis/{kind}/{name} 到达：

状态1：对象不存在
  → 404 NotFound

状态2：对象存在、无 deletionTimestamp（首次删除）
  → 反向引用扫描（§6）：
      被引用 → 409 Conflict（"still referenced by <kind>/<name>"）
      未引用 → 打 deletionTimestamp（RFC3339 now）+ 确保 finalizer 在
             → store.Put（CAS，带 resourceVersion 防并发覆盖）
             → 202 Accepted

状态3：对象存在、已有 deletionTimestamp（重复删除 / 删除进行中）
  → 反向引用重扫（防打戳后被新对象引用的竞态）：
      被引用 → 409 Conflict
      未引用 → 幂等返回 202（删除已在进行）

finalizers 清空那一刻（由 §7 的 finalizer 端点触发）：
  → 真正 store.Delete → 对象消失
```

- 反向引用扫描**每次** DELETE 都做（状态2 + 状态3），因为 finalizer 删除是异步的，打戳到真删之间有时间窗，期间可能被新对象引用。
- 打 deletionTimestamp 用 CAS（`store.Put` 带 expectedVersion）防并发覆盖。
- 时间戳来源：`time.Now().UTC().Format(time.RFC3339)`。

---

## 6. 反向引用扫描（实时 List，强一致）

### 引用图与标识符（关键：标识符不统一，必须按正确字段匹配）

核实真实字段后的引用关系（`[已验证]` 直接读 apis types）：

| 删除目标 | 扫描下游 kind | 下游引用字段 | 匹配标识符 |
| --- | --- | --- | --- |
| StoragePool | Volume | `spec.poolRef` | Pool **对象名** |
| StoragePool | Volume | `spec.imageFilePoolRef` | Pool **对象名**（root volume 的镜像文件池） |
| Network | NIC | `spec.networkRef` | Network **对象名** |
| Image | Volume | `spec.imageRef` | Image **对象名** |
| Volume | VM | `spec.volumeRefs[]` | Volume **对象名** |
| NIC | VM | `spec.nicRefs[]` | NIC **对象名** |
| VM | （无下游） | — | 直接放行 |

**陷阱（spec 必须明示，否则扫不中）**：
- `VM.spec.volumeRefs[]` / `nicRefs[]` 用的是**对象名**（被删 Volume/NIC 的 `metadata.name`）。
- 但 `Volume.spec.vmRef` / `NIC.spec.vmRef` 用的是 **VM uid**（不是 name）——本刀不靠它做引用扫描（删 VM 无下游），但实现时不可混淆 name/uid。
- 删 Pool 要扫 Volume 的**两个**字段（`poolRef` 块池 + `imageFilePoolRef` 镜像文件池），任一命中即被引用。

### 实现

```
referenceGuard(ctx, kind, name):
  switch kind:
    Pool    → List(Volume)，任一 vol.poolRef==name || vol.imageFilePoolRef==name → 被引用
    Network → List(NIC)，任一 nic.networkRef==name → 被引用
    Image   → List(Volume)，任一 vol.imageRef==name → 被引用
    Volume  → List(VM)，任一 vm.volumeRefs 含 name → 被引用
    NIC     → List(VM)，任一 vm.nicRefs 含 name → 被引用
    VM      → 永不被引用，放行
  返回 (referencedBy string, ok bool)
```

- 用现有 kind-agnostic `store.List(prefix)` + 按 kind 解码下游对象的最小投影（只解 ref 字段，类似 `nodeNameSelector` 模式）。
- etcd List 是线性读，结果强一致，无 ledger 漂移（与 MAC 分配选扫描而非 ledger 一致）。
- 被引用时返回第一个引用者的 `kind/name` 用于 409 消息。

---

## 7. Finalizer 专用端点 + node client（窄接口）

### apiserver 端点

node 当前只有 `PatchStatus`（改 status 子资源），无法改 `metadata.finalizers`。新增窄端点：

```
PATCH /apis/{kind}/{name}/finalizers
  body: { "remove": "govirta.io/node-teardown" }   // 只能移除指定 finalizer
  → read-modify-write（CAS）：从 metadata.finalizers 移除指定项
  → 若移除后 finalizers 为空 且 deletionTimestamp 非空：
        store.Delete（真删，对象消失）
  → 否则：store.Put 回写（finalizer 列表缩短）
  → 返回 200 + 当前对象（或删除后的空响应）
```

- **最小权限**：node 对 master 的写能力精确限定为 `PatchStatus`（报实况）+ `RemoveFinalizer`（声明拆净），碰不到 spec / metadata 其他字段。
- "finalizers 清空则真删"的收口逻辑集中在 apiserver 一处（和打 deletionTimestamp 同层），node 只声明"我拆完了"，真删时机由 apiserver 决定。
- 与现有 status 子资源端点对称（k8s subresource 模型）。

### node client 方法

`internal/node/client/client.go` 新增：

```go
func (c *Client) RemoveFinalizer(ctx context.Context, kind, name, finalizer string) error
```

- 复用现有 client 的 named-return + `closeBody` 模式（memory：Plan 3 Task 4 修过的 close-error 传播）。

---

## 8. 控制器拆除收敛（6 控制器，level-triggered 重试）

### 统一拆除流程

6 个控制器的 `EventDeleted` no-op 分支 + ADDED/MODIFIED 路径统一改为"见 deletionTimestamp 就拆"：

```
controller.Reconcile(ctx, ev):
  decode obj
  若 obj.metadata.deletionTimestamp 非空（删除中）：
    → teardown(ctx, obj)：调用下层 Delete*（幂等）
        失败 → 返回 error（保留 finalizer）→ 框架 requeue 重试
        成功 → client.RemoveFinalizer(kind, name, "govirta.io/node-teardown")
             → 返回 nil（不 requeue）
    return
  否则（ADDED/MODIFIED 无删除戳）：现有 ensure 路径（不变）
```

- **拆除失败保留 finalizer + requeue**（总览决策）：finalizer 留着 → apiserver 不真删 → 对象卡"删除中"持续重试直到 live 资源真拆净。
- `EventDeleted`（对象已从 store 消失的事件）此时仅作日志——真正的拆除由"带 deletionTimestamp 的 MODIFIED"驱动，因为对象要等拆净才消失，正常路径不会先收到 DELETED。

### 各控制器拆除映射 + 幂等核实（实现时逐一验证）

| 控制器 | 拆除调用（下层已存在） | 幂等性待核实点 |
| --- | --- | --- |
| StoragePool | `pool.Service` 注销池 | 删不存在的池是否幂等成功 |
| Image | `ImageService.DeleteImage` | 删不存在镜像的行为（memory 357：无引用计数） |
| Volume | `VolumeService.DeleteVolume` | 删不存在卷、qcow2 文件已删的幂等性 |
| Network | `netpool.DeleteNetwork(masqueradeRef, forwardRef)` | 需要 firewall RuleRef，从哪取（见下） |
| NIC | `netpool.DeleteNIC(antiSpoofRef)` | 需要 anti-spoof RuleRef，从哪取（见下） |
| VM | `vmm.Delete(uuid)` | 进程已死 / 已删的幂等性 |

**Network/NIC 拆除的 RuleRef 来源问题**（实现时确认）：`netpool.DeleteNetwork` / `DeleteNIC` 需要 firewall `RuleRef`（masquerade / forward / anti-spoof）。e2e 现有 teardown 通过 `firewall.ListRules` 解析 ref 后再删（见 AGENTS.md flow-guest-egress）。控制器拆除同样需要先 list 解析 ref，或从 netpool 注册态取——实现时核实 netpool 是否已持有这些 ref，避免在控制器层重新发明 ref 解析。

---

## 9. govirtctl delete 命令

```
govirtctl delete <kind> <name>   → DELETE /apis/{kind}/{name}
```

- 沿用现有 `apply -f` / `get <kind> <name>` 的 kind-agnostic 模式 + flag-after-subcommand 解析（memory：e2e 踩过 flag 顺序坑）。
- 退出码：202→0；404→非零并提示"not found"；409→非零并打印"still referenced by <kind>/<name>"。
- `internal/govirtctl/client.go` 新增 `Delete(kind, name)`；`command.go` 新增 delete 分发。

---

## 10. e2e 验证（三节点 closure 追加逆向拆除）

在现有正向 closure（apply 7 类 → reconcile ready → VM running）后追加逆向段：

```
逆向拆除（按依赖逆序，用户显式驱动）：
  0. 前置：VM 已 running（正向段产物）
  1. delete VM       → 202 → node 拆 QEMU/QMP → 摘 finalizer → 对象消失
  2. delete NIC      → 202 → node 拆 TAP + anti-spoof nftables → 消失
     delete Volume   → 202 → node 拆 qcow2 → 消失
  3. delete Network  → 202 → node 拆 bridge + masquerade/forward → 消失
     delete Image    → 202 → node 拆镜像文件 → 消失
  4. delete Pool(×2) → 202 → 注销池 → 消失

保护式拒绝验证：
  - VM running 时 delete 它引用的 Volume → 期望 409 "still referenced by vm/vm-e2e"
  - delete Pool 时仍有 Volume 引用 → 期望 409

finalizer 两阶段验证：
  - delete 后对象短暂可 GET 到（带 deletionTimestamp，finalizer 未摘）
  - 拆净后 GET 返回 404（真消失）

上下一致 live 核查（host 侧实际检查，非只看 API）：
  - VM 删后：QEMU 进程消失、QMP socket 清理
  - NIC 删后：ip link 无该 TAP、nft list 无 anti-spoof 规则
  - Network 删后：ip link 无该 bridge、nft list 无 masquerade/forward 规则
  - Volume 删后：qcow2 文件不存在
  - 确认无孤儿资源残留
```

- 证据级别对齐现有 e2e：host 侧 `ip link` / `ls` / `nft list` 实测，不只看 API 返回码。
- 复用 `//go:build e2e` + `scripts/e2e.sh` 三节点编排（etcd 容器 + host govirtad + Lima guest govirtlet）。

---

## 11. 验证策略

- **单元**：apis round-trip（带 finalizer/deletionTimestamp）；apiserver DELETE 状态机三分支 + 反向引用扫描各 kind（fake store）；finalizer 端点 read-modify-write + 清空真删；6 控制器 teardown 路径（拆除成功摘 finalizer / 拆除失败保留 + requeue，fake 下层）；govirtctl delete 退出码。
- **跨平台**：node 控制器拆除逻辑 darwin 可测（fake 下层），Linux 拆除走真实路径靠 e2e。
- **e2e**：§10 逆向拆除 + 保护拒绝 + finalizer 两阶段 + live 核查。
- **回归**：现有正向 closure + verify.sh 全绿。
- **铁律**：所有拆除错误 `errors.Join` 传播（netpool 逆序已有先例）；幂等拆除核实写进实现计划逐项验证（memory 799：先读下层真实行为再写 fake）。

---

## 12. 影响的调用关系

```
govirtctl delete
  → DELETE /apis/{kind}/{name}
  → apiserver.Server.Delete
       → referenceGuard（store.List 下游 kind）
       → 打 deletionTimestamp（store.Put CAS）
  → node watch MODIFIED（带删除戳）
  → controller.Reconcile → teardown
       → 下层 Delete*（pool/image/volume/netpool/vmm）
       → client.RemoveFinalizer
  → PATCH /apis/{kind}/{name}/finalizers
  → apiserver：finalizers 清空 → store.Delete → 对象消失
```

---

## 13. 开放实现点（计划阶段细化，非架构岔路）

1. Network/NIC 拆除的 firewall RuleRef 来源（netpool 注册态 vs ListRules 解析）——§8 已标，实现时核实 netpool 现状。
2. 各下层 `Delete*` 的幂等性逐一核实（§8 表）——memory 799：先读真实行为再定 fake。
3. `DeletionTimestamp` 用 string(RFC3339) 已定；若后续需要排序/比较再评估。
4. 文件行数：apiserver handler_apply.go 已较大，DELETE handler 应独立 `handler_delete.go`；finalizer 端点独立 `handler_finalizers.go`（第十八章：计划阶段设计文件拆分）。
