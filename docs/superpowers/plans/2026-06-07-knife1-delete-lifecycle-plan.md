# 刀 1 实现计划：Delete 生命周期（Finalizer 两阶段 + 反向引用保护式拒绝）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 6 类节点资源可被安全删除——finalizer 两阶段（拆净才真删）+ 反向引用保护式拒绝（删被引用对象返回 409）+ 6 控制器接 EventDeleted 拆 live 资源 + govirtctl delete + 三节点 e2e 逆向拆除闭环。

**Architecture:** 删除是声明式 level-triggered：DELETE 只打 `deletionTimestamp`（不真删），node watch 到带删除戳的 MODIFIED 后调下层已存在的 `Delete*` 拆 live 资源，拆净后通过窄 finalizer 端点摘除 `govirta.io/node-teardown`，apiserver 见 finalizers 清空才 `store.Delete`。finalizer 由 apiserver 在 apply 时 admission 注入（复用 MAC/调度先例），消除"未加 finalizer 就被删"的泄漏竞态。network/NIC 的 firewall rule-ref 解析收进 service 内部（修正泄漏的边界抽象，符合积木式分层铁律）。

**Tech Stack:** Go 1.26 + etcd store + 自建 controller-manager + zerolog + 现有 hostnet/storage/vmm 执行面 Delete*。

---

## 文件结构（第十八章体量预判）

**新建文件**（独立职责，避免堆进已大的 handler_apply.go 411 行）：
- `internal/controlplane/apiserver/handler_delete.go` — DELETE 状态机（~120 行）
- `internal/controlplane/apiserver/handler_delete_test.go` — 状态机三分支测试
- `internal/controlplane/apiserver/reference_guard.go` — 反向引用扫描（~110 行）
- `internal/controlplane/apiserver/reference_guard_test.go` — 各 kind 引用扫描测试
- `internal/controlplane/apiserver/handler_finalizers.go` — finalizer 子资源端点（~90 行）
- `internal/controlplane/apiserver/handler_finalizers_test.go` — 移除/清空真删测试
- `internal/node/controllers/teardown.go` — 6 控制器共享拆除 helper（~80 行）

**修改文件**（均在安全区，无接近 800 硬上限）：
- `pkg/apis/meta/v1alpha1/types.go`（66→~85）— 加 2 字段 + finalizer 常量
- `internal/controlplane/apiserver/handler_apply.go`（411→~445）— finalizer 注入
- `internal/controlplane/apiserver/server.go`（141→~155）— 注册 DELETE + finalizers 路由
- `internal/node/client/client.go`（158→~195）— RemoveFinalizer
- 6 个控制器各 +30~50 行拆除分支，调用共享 teardown helper
- `internal/network/service.go` + `nic_service.go` — Delete* 去掉 rule-ref 参数（前置任务 Task 1）
- `internal/storage/pool/service.go`（加 `UnregisterPool`）+ `errors.go`（加 `ErrPoolNotEmpty`）— 前置任务 Task 1b
- `internal/govirtctl/client.go` + `command.go` — delete 命令
- `test/acceptance/network_egress_test.go` — 同步 service 签名变更
- `test/e2e/closure_test.go` + manifests — 逆向拆除段

---

## Task 1: network service 内部自解析 rule-ref（前置任务，修正泄漏抽象）

**背景**：`NetworkService.DeleteNetwork(ctx, name, masqueradeRef, forwardRef)` / `NICService.DeleteNIC(ctx, networkName, vmID, antiSpoofRef)` 要求调用方传 firewall rule-ref。控制器只持有 service 句柄、无 firewall manager，无法反查 ref。决策 A：把 `ListRules → 反查 ref → DeleteXxx` 收进 service 内部，签名去掉 ref 参数（积木式分层铁律：firewall rule handle 是 netpool 内部细节，不该泄漏到控制器）。

**Files:**
- Modify: `internal/network/netpool/orchestrate.go:169` (DeleteNIC) `:200` (DeleteNetwork)
- Modify: `internal/network/service.go:43` (NetworkService.DeleteNetwork)
- Modify: `internal/network/nic_service.go:38` (NICService.DeleteNIC)
- Modify: `test/acceptance/network_egress_test.go`（删 lowestHandleRuleRef 解析段，直接调新签名）
- Test: `internal/network/netpool/orchestrate_test.go`（如存在）或新增拆除测试

- [ ] **Step 1: 确认目标与验收**

Goal: `netpool.Service.DeleteNetwork(ctx, name)` 与 `DeleteNIC(ctx, networkName, vmID)` 不再要求调用方传 firewall RuleRef；内部用 `s.firewall.ListRules` + 现有 `firewallRule`/`nicAntiSpoofingRule` helper 反查后删除。
验收证据：
- `go test ./internal/network/...` 通过
- service 层 DeleteNetwork/DeleteNIC 签名无 `firewall.RuleRef` 参数
- e2e teardown 不再含 `lowestHandleRuleRef`

- [ ] **Step 2: 读现有 ref 解析逻辑**

读 `internal/network/netpool/orchestrate.go` 的 `firewallRule`（:335）、`nicAntiSpoofingRule`（:354），以及 `EnsureNetwork`/`EnsureNIC` 里如何构造 masquerade/forward/anti-spoof 的 filter。拆除时复用同样的 filter 构造逻辑反查 ref。读 `test/acceptance/network_egress_test.go:340 lowestHandleRuleRef` 理解 "lowest handle" 选择语义（多规则时取最低 handle）。

- [ ] **Step 3: 改 netpool.Service.DeleteNetwork / DeleteNIC 内部自解析**

`DeleteNetwork(ctx, name)`：内部用 network 注册态构造 masquerade filter（GuestCIDR + EgressInterface）+ forward filter，各 `ListRules` 取 lowest-handle ref，再执行原拆除逻辑。`DeleteNIC(ctx, networkName, vmID)`：内部构造 anti-spoof filter（owner + TAP/MAC/IP）反查 ref。ref 未找到（规则已不存在）视为幂等：跳过该规则删除，不报错（拆除幂等性，§8 要求）。错误用 `errors.Join` 合并（逆序拆除已有先例）。

- [ ] **Step 4: 改 NetworkService / NICService 转发签名**

`NetworkService.DeleteNetwork(ctx, name)` / `NICService.DeleteNIC(ctx, networkName, vmID)` 去掉 ref 参数，直接转发到 netpool 新签名。

- [ ] **Step 5: 更新 e2e teardown**

`test/acceptance/network_egress_test.go`：删除 `lowestHandleRuleRef` 调用与 firewall filter 构造段，cleanup 直接 `nicSvc.DeleteNIC(ctx, egressNetwork, egressVM)` / `netSvc.DeleteNetwork(ctx, egressNetwork)`。保留 NIC-before-Network 的逆序（DeleteNetwork 仍因 NIC 计数非零而拒绝的语义不变）。

- [ ] **Step 6: 验证**

Run: `go test ./internal/network/...`
Expected: PASS
Run: `gofmt -l internal/network/ test/acceptance/`
Expected: 无输出

- [ ] **Step 7: 提交**

```bash
git add internal/network/ test/acceptance/network_egress_test.go
git commit -m "refactor(network): service internalizes firewall rule-ref resolution on delete"
```

---

## Task 1b: pool.Service.UnregisterPool（前置任务，存储层补注销能力）

**背景**：`pool.Service` 当前只有 `RegisterPool` / `DeleteVolume` / `DeleteImage`，**无注销池方法**（`[已验证]` 直接读源码）。StoragePool 控制器拆除时需要把内存注册态移除，使 live 态与 etcd 态一致（上下一致铁律）。决策 A：给 `pool.Service` 加 `UnregisterPool`，拒绝注销非空池（仍有 volume/image 时返回错误，呼应 apiserver 反向引用拒绝的同一保护语义在执行层的兜底）。

**Files:**
- Modify: `internal/storage/pool/service.go`（加 `UnregisterPool` 方法）
- Modify: `internal/storage/pool/errors.go`（加 `ErrPoolNotEmpty` sentinel）
- Test: `internal/storage/pool/service_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: `pool.Service.UnregisterPool(name string) error` 从 `s.pools` 移除指定池；池不存在返回 `ErrPoolNotFound`；池仍含 volume 或 image 返回新增的 `ErrPoolNotEmpty`（不移除）；空池移除成功返回 nil。
验收证据：
- `go test ./internal/storage/pool/...` 通过
- 注销空池成功、注销含卷池返回 `ErrPoolNotEmpty`、注销不存在池返回 `ErrPoolNotFound`

- [ ] **Step 2: 加 ErrPoolNotEmpty sentinel**

`internal/storage/pool/errors.go` 在现有 sentinel 后追加：

```go
	// ErrPoolNotEmpty marks unregister requests for a pool that still holds volumes or images.
	ErrPoolNotEmpty = errors.New("pool not empty")
```

- [ ] **Step 3: 实现 UnregisterPool**

`internal/storage/pool/service.go`（`Service.pools map[string]*Pool` + `mu sync.RWMutex`，`Pool.volumes map[volume.ID]volume.Volume` / `Pool.images map[string]ImageRecord`，键为 `p.Config.Name`）：

```go
// UnregisterPool 移除一个已注册的存储池。池不存在返回 ErrPoolNotFound；
// 池仍持有 volume 或 image 返回 ErrPoolNotEmpty（拒绝注销，防止丢失对在用资源的核算）。
// 这是 RegisterPool 的反向操作，使内存注册态可随 StoragePool 对象删除而清理（上下一致）。
func (s *Service) UnregisterPool(name string) error {
	if name == "" {
		return ErrPoolRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pools[name]
	if !ok {
		return ErrPoolNotFound
	}
	p.mu.RLock()
	nonEmpty := len(p.volumes) > 0 || len(p.images) > 0
	p.mu.RUnlock()
	if nonEmpty {
		return fmt.Errorf("%w: %q", ErrPoolNotEmpty, name)
	}
	delete(s.pools, name)
	return nil
}
```

- [ ] **Step 4: 测试**

`internal/storage/pool/service_test.go`：注册空 block 池 → `UnregisterPool` 成功、再 `GetPoolUsage` 返回 `ErrPoolNotFound`；注册池 + 建一个 volume → `UnregisterPool` 返回 `ErrPoolNotEmpty`、池仍在；`UnregisterPool("不存在")` 返回 `ErrPoolNotFound`。

- [ ] **Step 5: 验证**

Run: `go test ./internal/storage/pool/...`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/storage/pool/service.go internal/storage/pool/errors.go internal/storage/pool/service_test.go
git commit -m "feat(pool): add UnregisterPool with non-empty guard"
```

---

## Task 2: API 契约 — ObjectMeta 加 finalizer + deletionTimestamp

**Files:**
- Modify: `pkg/apis/meta/v1alpha1/types.go:48-54`（ObjectMeta）
- Test: `pkg/apis/meta/v1alpha1/types_test.go`（如存在）+ 各资源 round-trip 测试

- [ ] **Step 1: 确认目标与验收**

Goal: `ObjectMeta` 含 `DeletionTimestamp string` + `Finalizers []string`；包级常量 `FinalizerNodeTeardown Finalizer = "govirta.io/node-teardown"`（强类型，非 bare string）。
验收证据：
- `go test ./pkg/apis/...` 通过
- 带 finalizer + deletionTimestamp 的对象 JSON round-trip 字段不丢

- [ ] **Step 2: 加字段 + 常量**

`pkg/apis/meta/v1alpha1/types.go`：

```go
// Finalizer 是阻止对象在 live 资源拆净前被真正删除的守卫标识。
// 状态机式标识符，专用类型 + 命名常量（项目铁律：no bare string）。
type Finalizer string

const (
	// FinalizerNodeTeardown 表示该对象有 node 侧 live 资源待拆除；
	// node 拆净后通过 finalizers 端点摘除它，apiserver 见 finalizers 清空才真删。
	FinalizerNodeTeardown Finalizer = "govirta.io/node-teardown"
)

type ObjectMeta struct {
	Name            string            `json:"name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	NodeName        string            `json:"nodeName,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	// DeletionTimestamp 非空表示对象处于删除中（RFC3339）；空=未删。
	DeletionTimestamp string `json:"deletionTimestamp,omitempty"`
	// Finalizers 是拆净前阻止真删的守卫列表；空且 DeletionTimestamp 非空时 apiserver 真删。
	Finalizers []Finalizer `json:"finalizers,omitempty"`
}
```

- [ ] **Step 3: round-trip 测试扩展**

在现有六对象 round-trip 测试里，给至少一个对象的 ObjectMeta 填 `DeletionTimestamp: "2026-06-07T10:00:00Z"` + `Finalizers: []Finalizer{FinalizerNodeTeardown}`，marshal→unmarshal 后断言两字段相等。

- [ ] **Step 4: 验证**

Run: `go test ./pkg/apis/...`
Expected: PASS

- [ ] **Step 5: 确认 Validate 不强制新字段**

`ObjectMeta.Validate()` 不得要求 DeletionTimestamp/Finalizers 非空（创建时为空是正常态）。读现有 Validate 确认无需改动。

- [ ] **Step 6: 提交**

```bash
git add pkg/apis/meta/v1alpha1/
git commit -m "feat(apis): add deletionTimestamp + finalizers to ObjectMeta"
```

---

## Task 3: Finalizer admission 注入（apply 路径）

**Files:**
- Modify: `internal/controlplane/apiserver/handler_apply.go`（kind 分发分支，put 之前）
- Test: `internal/controlplane/apiserver/handler_apply_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: apply 任意 6 类节点资源时，若 `obj.Finalizers` 为空，apiserver 在 `store.Put` 前注入 `[FinalizerNodeTeardown]`；已有 finalizer 的对象尊重原值（有条件注入，与 MAC/调度一致）。
验收证据：
- apply 一个无 finalizer 的 StoragePool → GET 回来 `metadata.finalizers == ["govirta.io/node-teardown"]`
- apply 一个已带自定义 finalizer 的对象 → 原值保留

- [ ] **Step 2: 加 injectFinalizer helper + 接入各分支**

`handler_apply.go` 加：

```go
// injectFinalizer 在对象持久化前注入默认 node-teardown finalizer（仅当 Finalizers 为空），
// 与 MAC/调度的 admission 注入同模式：落 etcd 的对象一定带 finalizer，消除"未加 finalizer 就被删"
// 的泄漏竞态。有条件注入为未来多 finalizer 留口。
func injectFinalizer(meta *metav1.ObjectMeta) {
	if len(meta.Finalizers) == 0 {
		meta.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	}
}
```

在每个 kind 分支的 `validateObject` 之后、`put`/`applyNIC`/`bindVM` 之前调用 `injectFinalizer(&obj.ObjectMeta)`。（NIC 走 applyNIC、VM 走 bindVM，注入仍在它们之前。）

- [ ] **Step 3: 测试**

```go
func TestApplyInjectsNodeTeardownFinalizer(t *testing.T) {
	// apply StoragePool with empty finalizers → stored object has FinalizerNodeTeardown
}
func TestApplyPreservesExistingFinalizers(t *testing.T) {
	// apply object already carrying a finalizer → preserved, not overwritten
}
```

- [ ] **Step 4: 验证**

Run: `go test ./internal/controlplane/apiserver/ -run TestApply -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/controlplane/apiserver/handler_apply.go internal/controlplane/apiserver/handler_apply_test.go
git commit -m "feat(apiserver): inject node-teardown finalizer on apply"
```

---

## Task 4: 反向引用扫描（reference_guard.go）

**Files:**
- Create: `internal/controlplane/apiserver/reference_guard.go`
- Test: `internal/controlplane/apiserver/reference_guard_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: `referenceGuard(ctx, kind, name) (referencedBy string, referenced bool, err error)` 用 `store.List` 扫描下游 kind，按正确标识符匹配（VM.volumeRefs/nicRefs=对象名，Volume.poolRef/imageFilePoolRef/imageRef=对象名，NIC.networkRef=对象名）。
验收证据：
- 删被 VM 引用的 Volume → referenced=true, referencedBy="VM/<name>"
- 删无引用的 Volume → referenced=false
- 删 Pool 时 Volume 的 imageFilePoolRef 命中也算被引用

- [ ] **Step 2: 实现 referenceGuard**

按引用图实现。用最小投影解码（类似 handler_watch.go 的 `nodeNameSelector` 模式，只解 ref 字段）：

```go
// referenceGuard 报告 kind/name 是否被任一下游对象引用，返回首个引用者的 "Kind/Name"。
// 引用图（标识符均为对象 name）：
//   Pool    ← Volume.poolRef | Volume.imageFilePoolRef
//   Network ← NIC.networkRef
//   Image   ← Volume.imageRef
//   Volume  ← VM.volumeRefs[]
//   NIC     ← VM.nicRefs[]
//   VM      ← 无下游
// 用线性读 store.List，无 ledger 漂移（与 MAC 分配选扫描一致）。
func (s *Server) referenceGuard(ctx context.Context, kind, name string) (string, bool, error) { ... }
```

每个分支：`s.store.List(ctx, prefix(downstreamKind))` → 遍历解码 ref 投影 → 命中返回。

- [ ] **Step 3: 测试各 kind 分支（fake store）**

覆盖：Pool 被 poolRef 引用、Pool 被 imageFilePoolRef 引用、Network 被 networkRef、Image 被 imageRef、Volume 被 volumeRefs、NIC 被 nicRefs、VM 永不被引用、无引用时 referenced=false。

- [ ] **Step 4: 验证**

Run: `go test ./internal/controlplane/apiserver/ -run TestReferenceGuard -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/controlplane/apiserver/reference_guard.go internal/controlplane/apiserver/reference_guard_test.go
git commit -m "feat(apiserver): reverse-reference scan for delete protection"
```

---

## Task 5: DELETE 状态机（handler_delete.go）

**Files:**
- Create: `internal/controlplane/apiserver/handler_delete.go`
- Test: `internal/controlplane/apiserver/handler_delete_test.go`
- Modify: `internal/controlplane/apiserver/server.go`（注册路由）

- [ ] **Step 1: 确认目标与验收**

Goal: `DELETE /apis/{kind}/{name}` 严格 k8s 语义——不存在→404，无删除戳→引用扫描后打 deletionTimestamp（CAS）→202，已有删除戳→重扫后幂等 202，被引用→409。
验收证据：
- DELETE 不存在对象 → 404
- DELETE 被引用对象 → 409 + "still referenced by"
- DELETE 未引用对象 → 202，GET 回来带 deletionTimestamp，对象仍在
- 重复 DELETE → 202 幂等

- [ ] **Step 2: 实现 Server.Delete**

```go
// Delete 实现 finalizer 两阶段删除的第一阶段 + 重复删除幂等。
// 不真删：仅打 deletionTimestamp（CAS）；真删由 finalizers 端点在 finalizers 清空时触发。
func (s *Server) Delete(w http.ResponseWriter, r *http.Request) {
	// kind/name 解析 → store.Get（404 if ErrNotFound）
	// referenceGuard（409 if referenced，状态2/3 都查）
	// 无 deletionTimestamp：set RFC3339 now + 确保 finalizer 在 → store.Put(CAS) → 202
	// 已有 deletionTimestamp：幂等 202
}
```

时间戳：`time.Now().UTC().Format(time.RFC3339)`。CAS：`store.Put(ctx, key, data, raw.ResourceVersion)`，`ErrRevisionConflict` → 409/重试语义（返回 409 让客户端重试）。

- [ ] **Step 3: 注册路由**

`server.go` Handler()：`mux.HandleFunc("DELETE /apis/{kind}/{name}", s.Delete)`。

- [ ] **Step 4: 测试三分支（fake store）**

覆盖 404 / 409 被引用 / 202 首删打戳 / 202 重复幂等 / 打戳后被新引用重扫 409。

- [ ] **Step 5: 验证**

Run: `go test ./internal/controlplane/apiserver/ -run TestDelete -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/controlplane/apiserver/handler_delete.go internal/controlplane/apiserver/handler_delete_test.go internal/controlplane/apiserver/server.go
git commit -m "feat(apiserver): DELETE handler with finalizer two-phase + reference guard"
```

---

## Task 6: Finalizer 子资源端点（handler_finalizers.go）

**Files:**
- Create: `internal/controlplane/apiserver/handler_finalizers.go`
- Test: `internal/controlplane/apiserver/handler_finalizers_test.go`
- Modify: `internal/controlplane/apiserver/server.go`（注册路由）

- [ ] **Step 1: 确认目标与验收**

Goal: `PATCH /apis/{kind}/{name}/finalizers` body `{"remove":"govirta.io/node-teardown"}` → read-modify-write 移除该 finalizer；若移除后 finalizers 空且 deletionTimestamp 非空 → `store.Delete`（真删）；否则 Put 回写。
验收证据：
- 移除最后一个 finalizer（对象带 deletionTimestamp）→ 对象真消失，后续 GET 404
- 移除一个（还剩其他 finalizer）→ 对象仍在，finalizers 缩短

- [ ] **Step 2: 实现 Server.PatchFinalizers**

```go
// PatchFinalizers 是 node 摘除自己 finalizer 的窄端点（最小权限：只能移除指定 finalizer，
// 碰不到 spec/其他 metadata）。"finalizers 清空则真删" 的收口逻辑集中在此处（与打 deletionTimestamp 同层）。
func (s *Server) PatchFinalizers(w http.ResponseWriter, r *http.Request) {
	// decode {"remove": "<finalizer>"}
	// store.Get → 从 Finalizers 移除 remove 项
	// if len(Finalizers)==0 && DeletionTimestamp!="" : store.Delete → 200 空响应
	// else : store.Put(CAS) → 200 + 当前对象
}
```

移除用 kind-agnostic 投影：只需读/改 `metadata.finalizers`，但回写要保持整个对象——用 `map[string]json.RawMessage` 或解码到 metav1 envelope 后改 metadata 再 marshal。实现时选最稳妥：解码到一个只含 `{"metadata": ObjectMeta, ...rest}` 的结构保留 spec/status 原样。

- [ ] **Step 3: 注册路由**

`server.go`：`mux.HandleFunc("PATCH /apis/{kind}/{name}/finalizers", s.PatchFinalizers)`。

- [ ] **Step 4: 测试**

覆盖：移除最后 finalizer + 带 deletionTimestamp → 真删 404；移除但还剩 finalizer → 保留；移除不存在的 finalizer → 幂等无变化；无 deletionTimestamp 时清空 finalizer → 不真删（只回写，因为没删除意图）。

- [ ] **Step 5: 验证**

Run: `go test ./internal/controlplane/apiserver/ -run TestPatchFinalizers -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/controlplane/apiserver/handler_finalizers.go internal/controlplane/apiserver/handler_finalizers_test.go internal/controlplane/apiserver/server.go
git commit -m "feat(apiserver): finalizers subresource endpoint with empty-then-delete收口"
```

---

## Task 7: node client RemoveFinalizer

**Files:**
- Modify: `internal/node/client/client.go`
- Test: `internal/node/client/client_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: `Client.RemoveFinalizer(ctx, kind, name, finalizer string) error` PATCH `/apis/{kind}/{name}/finalizers` body `{"remove":finalizer}`；非 2xx 返回带响应体的 wrapped error。
验收证据：
- 测试用 httptest server 断言 PATCH path + body 正确，2xx 返回 nil，非 2xx 返回含状态码的 error

- [ ] **Step 2: 实现（复用 named-return + closeBody 模式）**

```go
// RemoveFinalizer 摘除对象的一个 finalizer（node 声明 "live 资源已拆净"）。复用 closeBody
// named-return 模式，确保 close 错误也并入返回（memory: Plan3 Task4 修过的 close-error 传播）。
func (c *Client) RemoveFinalizer(ctx context.Context, kind, name, finalizer string) (err error) {
	path := "/apis/" + url.PathEscape(kind) + "/" + url.PathEscape(name) + "/finalizers"
	body, merr := json.Marshal(map[string]string{"remove": finalizer})
	if merr != nil {
		return fmt.Errorf("node/client: marshal remove-finalizer for %s/%s: %w", kind, name, merr)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+path, bytes.NewReader(body))
	// ... Content-Type json, Do, defer closeBody(&err), read, non-2xx → wrapped error
}
```

- [ ] **Step 3: 测试（httptest）**

断言 method=PATCH、path、body `{"remove":"govirta.io/node-teardown"}`；2xx→nil；404→error 含状态码。

- [ ] **Step 4: 验证**

Run: `go test ./internal/node/client/ -run TestRemoveFinalizer -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/node/client/
git commit -m "feat(node/client): RemoveFinalizer narrow finalizers endpoint method"
```

---

## Task 8: 共享拆除 helper + 各下层 Delete* 幂等核实（teardown.go）

**Files:**
- Create: `internal/node/controllers/teardown.go`
- Test: `internal/node/controllers/teardown_test.go`
- 调研: 各下层 Delete* 真实幂等行为

- [ ] **Step 1: 确认目标与验收（memory 799：先读真实行为再写 fake）**

Goal: 提供共享判定 `isDeleting(meta) bool`（deletionTimestamp 非空）；逐一核实 6 个下层 Delete* 的幂等性（删不存在/已删资源是否报错），把结论写进 teardown.go 注释。
验收证据：
- 读 `pool.Service` 注销、`ImageService.DeleteImage`、`VolumeService.DeleteVolume`、`netpool.DeleteNetwork/DeleteNIC`（Task1 后新签名）、`vmm.Delete` 源码，确认各自删缺失资源的行为
- 不幂等的 Delete* 在 teardown helper 里加 NotFound 容错（视为拆净成功）

- [ ] **Step 2: 读下层 Delete* 真实幂等行为**

逐个读源码记录：
- `vmm.Delete(uuid)` — `[已验证]` running VM 返回 `ErrConflict`（"cannot delete running vm"）；进程已死/状态文件已删时 `loadState` 报错。故 VM teardown 必须先停（Task 9 Step 6 决策 A：先 Stop/Kill 等进程死再 Delete），不能直接 Delete。
- `pool.Service.UnregisterPool`（Task 1b 新增）— 非空池返回 `ErrPoolNotEmpty`；不存在的池幂等成功（视为已注销）。
- `VolumeService.DeleteVolume` / `pool.DeleteVolume` — 卷不存在 / qcow2 已删
- `ImageService.DeleteImage` — 镜像不存在（memory 357：无引用计数）
- `netpool.DeleteNetwork/DeleteNIC` — 规则已不存在（Task 1 已加 ref-not-found 幂等）
- 把每个的幂等结论写进 teardown.go 文件顶部注释。

- [ ] **Step 3: 实现 isDeleting + finalizer 摘除 helper**

```go
// isDeleting 报告对象是否处于删除中（带 deletionTimestamp）。
func isDeleting(meta metav1.ObjectMeta) bool { return meta.DeletionTimestamp != "" }

// removeTeardownFinalizer 在 live 资源拆净后摘除 node-teardown finalizer。
func removeTeardownFinalizer(ctx context.Context, c FinalizerRemover, kind, name string) error {
	return c.RemoveFinalizer(ctx, kind, name, string(metav1.FinalizerNodeTeardown))
}

// FinalizerRemover 是控制器对 master 摘 finalizer 的窄依赖。
type FinalizerRemover interface {
	RemoveFinalizer(ctx context.Context, kind, name, finalizer string) error
}
```

- [ ] **Step 4: 测试 helper**

`isDeleting` 空/非空；`removeTeardownFinalizer` 透传到 fake remover。

- [ ] **Step 5: 验证**

Run: `go test ./internal/node/controllers/ -run TestTeardown -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/node/controllers/teardown.go internal/node/controllers/teardown_test.go
git commit -m "feat(controllers): shared teardown helper + downstream Delete idempotency notes"
```

---

## Task 9: 6 控制器接拆除分支

**Files:**
- Modify: `internal/node/controllers/{storagepool,image,volume,network,nic,vm}.go`（各加 deletionTimestamp 拆除分支）
- Modify: 各对应 `*_test.go`

**统一改法**（每个控制器 Reconcile 解码 obj 后、ensure 路径前插入）：

```go
if isDeleting(obj.ObjectMeta) {
	if err := c.teardown(ctx, obj); err != nil {
		// 拆除失败保留 finalizer → 对象卡"删除中" → requeue 重试（总览决策）
		return true, fmt.Errorf("<kind> controller: teardown %q: %w", obj.Name, err)
	}
	if err := removeTeardownFinalizer(ctx, c.master, c.Kind(), obj.Name); err != nil {
		return true, fmt.Errorf("<kind> controller: remove finalizer %q: %w", obj.Name, err)
	}
	return false, nil
}
```

- [ ] **Step 1: StoragePool 拆除**

teardown 调 `pool.Service.UnregisterPool(name)`（Task 1b 新增）。幂等容错：`ErrPoolNotFound`（池已注销）视为拆净成功，继续摘 finalizer；`ErrPoolNotEmpty`（仍有卷/镜像）视为真失败，返回 error → requeue 保留 finalizer（正常情况下不会发生，因 apiserver 反向引用拒绝已挡在前面，这里是执行层兜底）。加测试：带 deletionTimestamp 的 StoragePool → `UnregisterPool` 成功 + 摘 finalizer；`UnregisterPool` 返回 `ErrPoolNotFound` → 仍摘 finalizer（幂等）；`ErrPoolNotEmpty` → requeue 保留 finalizer。

- [ ] **Step 2: Image 拆除**

teardown 调 `ImageService.DeleteImage`（构造 DeleteImageRequest，需 PoolName + ImageID + Format，从 spec 取）。删不存在镜像按 Step8 核实结论容错。测试覆盖成功摘 finalizer / 失败 requeue。

- [ ] **Step 3: Volume 拆除**

teardown 调 `VolumeService.DeleteVolume`（DeleteVolumeRequest 需 PoolName + VolumeID，从 spec/status 取）。测试同上。

- [ ] **Step 4: Network 拆除**

teardown 调 `NetworkService.DeleteNetwork(ctx, name)`（Task 1 新签名，无 ref 参数）。测试同上。

- [ ] **Step 5: NIC 拆除**

teardown 调 `NICService.DeleteNIC(ctx, networkName, vmID)`（Task 1 新签名）。networkName/vmID 从 spec 取（networkRef + vmRef）。测试同上。

- [ ] **Step 6: VM 拆除**

teardown 调 VM 停机+删除两步（决策 A：`vmm.Delete` 拒绝删 running VM，已读源码确认 `if alive { return ErrConflict }`，所以删前必须先停）：

```
vmTeardown(ctx, obj):
  uuid = obj.metadata.uid   // VM 控制器现有 uuid 来源，核实 vm.go 现状确认
  live = vmm.Status(uuid).Phase
  若 live ∈ {Running, Starting}:
    → vmm.Stop(ctx, uuid)            // QMP system_powerdown 优雅停（已存在）
    → 返回 requeue=true（等进程退到 Stopped，下次 reconcile 再删）
  若 live == Stopping:
    → 返回 requeue=true（停机进行中，等终态）
  若 live ∈ {Stopped, Failed, Defined} 或 Status 返回 not-found:
    → vmm.Delete(ctx, uuid)          // 进程已死，安全删除
    → 删成功/已删（幂等）→ 摘 finalizer
```

- 这是"先停后删"而非刀 2 的声明式 powerState 流——用已存在的 `vmm.Stop`/`Delete` 执行面能力，删除即"停机+清理"一气呵成（ESXi 式"删 VM 自动先关"）。`Kill`（quit+SIGKILL 兜底）本刀不接入，留作未来强停超时兜底。
- e2e 删 running VM 时：第一次 reconcile 触发 Stop + requeue，进程退到 Stopped 后下次 reconcile 执行 Delete + 摘 finalizer。
- 测试覆盖：running → Stop+requeue（不摘 finalizer）；Stopped → Delete+摘 finalizer；Stopping → requeue；Delete 失败 → requeue 保留 finalizer。

- [ ] **Step 7: 各控制器验证**

Run: `go test ./internal/node/controllers/ -v`
Expected: PASS

- [ ] **Step 8: 提交（按控制器分多个提交或一个，单一逻辑变更）**

```bash
git add internal/node/controllers/
git commit -m "feat(controllers): 6 controllers tear down live resources on deletionTimestamp"
```

---

## Task 10: govirtctl delete 命令

**Files:**
- Modify: `internal/govirtctl/client.go`（加 Delete）
- Modify: `internal/govirtctl/command.go`（加 delete 分发）
- Test: `internal/govirtctl/client_test.go` + `command_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: `govirtctl delete <kind> <name>` → DELETE /apis/{kind}/{name}；退出码 202→0、404→非零 "not found"、409→非零 "still referenced by ..."。
验收证据：
- httptest 模拟 202/404/409 → 对应退出码 + stderr 消息

- [ ] **Step 2: client.Delete**

```go
func (c *Client) Delete(ctx context.Context, kind, name string) error {
	// DELETE /apis/{kind}/{name}；202→nil；404→wrapped not-found；409→wrapped referenced（含响应体）
}
```

- [ ] **Step 3: command.go delete 分发**

沿用 flag-after-subcommand 解析（memory: e2e flag 顺序坑）。`delete <kind> <name>` + `--server`。

- [ ] **Step 4: 测试**

client 三状态码映射；command 解析 + 退出码。

- [ ] **Step 5: 验证**

Run: `go test ./internal/govirtctl/ -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/govirtctl/
git commit -m "feat(govirtctl): delete <kind> <name> command"
```

---

## Task 11: e2e 逆向拆除闭环

**Files:**
- Modify: `test/e2e/closure_test.go`（追加逆向段）
- Modify: `scripts/e2e.sh`（如需 host 侧 live 核查命令）

- [ ] **Step 1: 确认目标与验收**

Goal: 正向 closure 后追加逆向拆除——按依赖逆序 delete VM→NIC/Volume→Network/Image→Pool，验证 202、finalizer 两阶段、保护式 409、host 侧 live 无孤儿。
验收证据：
- `scripts/e2e.sh` 全程绿，逆向段每个 delete 后 GET 最终 404，host 侧 `ip link`/`ls`/`nft list` 无残留

- [ ] **Step 2: 写逆向拆除段**

按 spec §10：delete VM → 摘 finalizer → 404；保护拒绝（VM running 时 delete Volume → 409）；finalizer 两阶段（delete 后短暂带 deletionTimestamp，拆净后 404）。

- [ ] **Step 3: host 侧 live 核查**

VM 删后 QEMU 进程消失；NIC 删后无 TAP + 无 anti-spoof 规则；Network 删后无 bridge + 无 masquerade/forward；Volume 删后 qcow2 不存在。用 `ssh`/Lima exec 跑 `ip link`/`ls`/`nft list`。

- [ ] **Step 4: 跑 e2e（真实三节点）**

Run: `scripts/e2e.sh`
Expected: 正向 + 逆向全绿，无孤儿。
（注意 memory: e2e.sh shell 变量作用域坑、host/guest 嵌套引号坑、govirtctl flag 顺序坑——复用已修版本。）

- [ ] **Step 5: 提交**

```bash
git add test/e2e/ scripts/e2e.sh
git commit -m "test(e2e): reverse teardown closure with finalizer two-phase + live orphan check"
```

---

## Task 12: 全量验证

- [ ] **Step 1: gofmt + build + 单元 + race**

```bash
gofmt -l .
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl
go test ./...
go test -race ./...
```
Expected: 全绿

- [ ] **Step 2: 跨平台（Linux 控制器拆除路径）**

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go vet ./...
```
Expected: 0 退出（memory 700）

- [ ] **Step 3: scripts/verify.sh**

```bash
scripts/verify.sh
```
Expected: 全绿

- [ ] **Step 4: e2e（已在 Task 11）**

确认 Task 11 e2e 通过即可，不重复。

- [ ] **Step 5: 最终提交（如有 verify 修复）**

```bash
git add -A
git commit -m "chore: knife 1 delete lifecycle full verification"
```

---

## 验证策略总览

- **单元**：apis round-trip（finalizer/deletionTimestamp）；apiserver DELETE 三分支 + 引用扫描各 kind + finalizer 端点清空真删（fake store）；6 控制器 teardown 成功摘/失败 requeue（fake 下层）；client RemoveFinalizer；govirtctl delete 退出码。
- **跨平台**：控制器拆除 darwin 可测（fake 下层），Linux 真实路径靠 e2e（memory 700）。
- **e2e**：逆向拆除 + 保护拒绝 409 + finalizer 两阶段 + host 侧 live 无孤儿核查。
- **铁律**：拆除错误 `errors.Join` 传播；幂等核实先读下层真实行为再写 fake（memory 799）。
