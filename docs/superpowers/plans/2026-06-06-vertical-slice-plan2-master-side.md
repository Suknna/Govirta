# 垂直切片 Plan 2：master 侧（apiserver + Store/etcd + MAC 分配 + scheduler）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现控制面 master 侧：一个能存六类资源对象、按 k8s 式 HTTP watch 下行推送、接收 node 上行 `PATCH .../status` 并向 etcd reconcile 的真实 HTTP server，含 etcd 持久化、单测用 fake store、apiserver 准入期 MAC 分配、以及消费 apis VM 对象的 scheduler。产出：无需 node 即可用 `curl` 验证「apply → 存 etcd → watch 下发 → status 回写」的真 master。

**Architecture:** 三层。(1) `Store` 接口 = 裸字节键值 + watch 边界（积木式，对六个具体资源类型无感知；etcd 实现与 fake 内存实现可互换）。(2) apiserver = 四个 HTTP handler（apply / get-list / watch / status）+ 准入期 `MACAllocator`，handler 把 apis 对象 JSON 编解码后落到 Store。(3) scheduler 重构为消费 `apis/vm` 对象给 VM 绑 `NodeName`。master 直通 etcd 不缓存（单一事实源铁律：etcd 是期望权威，node 上报 live status 是现实权威，master 只 reconcile 写回不反向）。

**Tech Stack:** Go 1.26，stdlib `net/http`，`go.etcd.io/etcd/client/v3 v3.6.12`（已验证：module path `go.etcd.io/etcd/client/v3`，包名 `clientv3`，Go 1.26.1 实测 `go build`/`go vet` 通过，引入约 76 个传递 module — memory 823），import `pkg/apis`。

---

## 关键设计决策（来自已确认 spec + brainstorming，写进实现）

- **Store 形态**：裸字节 + 信封键。key = `/govirta/<kind>/<name>`，value = 资源对象 JSON，`resourceVersion = etcd ModRevision`。Store 不知道六个具体类型（积木式）；`nodeName` / `kind` 过滤在 watch handler 侧 Go 端做。Store 接口方法用 `context.Context` 端到端。
- **单一事实源**：master 不持有内存缓存或 informer。每个 node watch 请求直接开一个 etcd Watch（A-直通，spec 第 6 问已定）。
- **MAC 分配（A 方案 + 三层唯一性，spec 第 65-69 行 + brainstorming 已定）**：
  1. `mac.Pool` = 配置的 locally-administered 单播范围，确定性枚举候选（纯函数，可单测）。
  2. 分配时对 etcd 做 linearizable list 所有 NIC 对象，占用集合 = 全部非空 `Spec.MAC`。NIC 对象是 MAC 占用唯一事实源（无独立账本）。
  3. allocator 进程内 `sync.Mutex` 串行化「list 占用 → 挑第一个空闲 → 写入新 NIC」整段，单点 apiserver 下堵死并发竞态。
  - 池耗尽返回显式 `ErrMACPoolExhausted`，不随机重试、不静默复用。
  - 边界声明：唯一性依赖 apiserver 单点；多副本时须迁移到控制平面专属控制器（memory 820）。`MACAllocator` 接口为迁移预留。
- **etcd 测试策略（第十四章 Docker 优先于 mock）**：`Store` 单测用 fake 内存实现（不碰 etcd）；etcd 实现的集成测试用环境变量 `GOVIRTA_ETCD_ENDPOINTS` 指向真实 etcd（OrbStack docker 启动），未设则 `t.Skip`——与现有 `GOVIRTA_QEMU_INTEGRATION` 模式一致。**不为 etcd 写 mock。**
- **scheduler 重构**：删除旧 `internal/types` 的 `VirtualMachine/Node`（fast-iteration 阶段直接替换，无兼容层 — 反模式铁律）。新 `Scheduler` 消费 `apis/vm` 对象 + 可用 node 名列表，给 VM 绑 `NodeName`。第一刀仍是 noop 策略（选 `nodes[0]`），但类型对齐 apis。
- **Backlog 收口（note #26）**：Plan 1 审阅留的 Network 范围自洽校验（`LeaseSeconds>=0`、`DHCPRangeStart<=End`、子网包含）归属在此定：属 **apiserver 准入期校验**（apply handler 落库前），不在 apis 契约层、也不在 node controller。本 plan Task 7 落地。

---

## 文件结构

```
internal/controlplane/
  store/
    store.go            # Store 接口 + RawObject + WatchEvent + sentinel 错误
    store_test.go       # 接口契约共享测试（对 fake 跑）
    fake/
      fake.go           # 内存 Store 实现（map + revision 计数 + watch fan-out）
      fake_test.go
    etcd/
      etcd.go           # clientv3 实现
      etcd_test.go      # 集成测试（GOVIRTA_ETCD_ENDPOINTS 未设则 skip）
  mac/
    pool.go             # mac.Pool 确定性候选枚举（纯函数）
    pool_test.go
    allocator.go        # MACAllocator 接口 + etcd-backed 实现（mutex + list NIC 派生占用）
    allocator_test.go   # 用 fake store 测分配/唯一性/耗尽
  apiserver/
    server.go           # 替换 NoopServer：真实 http.Server 组装 + 路由
    server_test.go
    handler_apply.go    # PUT/POST  /apis/<kind>/<name>      → 校验 + (NIC) MAC 分配 + Store.Put
    handler_get.go      # GET       /apis/<kind>[/<name>]    → Store.Get / List
    handler_watch.go    # GET       /apis/<kind>?watch=true&nodeName=N → etcd Watch → chunked JSON 事件流
    handler_status.go   # PATCH     /apis/<kind>/<name>/status → reconcile 写回 etcd
    handlers_test.go    # httptest + fake store
  scheduler/
    scheduler.go        # 重构：消费 apis/vm，给 VM 绑 NodeName
    scheduler_test.go
  service.go            # NewService 重构：组装 Store + apiserver + scheduler
  service_test.go

internal/types/         # 删除旧 VirtualMachine/Node（如无其他引用则整包评估删除）
```

---

## Task 1: `Store` 接口 + 错误 + 事件类型

**Files:**
- Create: `internal/controlplane/store/store.go`
- Test: `internal/controlplane/store/store_test.go`（接口契约测试，Task 2 fake 落地后跑）

- [ ] **Step 1: 确认目标与验收标准**

Goal: 定义对六资源类型无感知的裸字节 KV + watch 边界。Store 操作全部接受 caller `ctx`。`resourceVersion` 用字符串承载 etcd ModRevision。

Acceptance evidence:
- 包编译通过：`go build ./internal/controlplane/store/`
- 接口方法签名 review 通过：Put/Get/List/Delete/Watch 均含 `ctx context.Context` 首参

- [ ] **Step 2: 写实现**

```go
// Package store defines the control-plane persistence boundary. It is a raw
// key/value + watch contract that is intentionally unaware of the six concrete
// resource types (积木式): the etcd implementation and the in-memory fake are
// interchangeable, and kind/nodeName filtering happens in the apiserver layer.
package store

import (
	"context"
	"errors"
)

// Stable sentinel errors so callers classify with errors.Is.
var (
	// ErrNotFound is returned when a key has no stored object.
	ErrNotFound = errors.New("store: object not found")
	// ErrRevisionConflict is returned when a compare-and-swap precondition fails.
	ErrRevisionConflict = errors.New("store: resource version conflict")
	// ErrClosed is returned when the store has been closed.
	ErrClosed = errors.New("store: closed")
)

// EventType is the kind of change observed on a watched key. State-machine-like
// discriminator, so it is a dedicated type with named constants (项目铁律).
type EventType string

const (
	// EventAdded means the object was created.
	EventAdded EventType = "ADDED"
	// EventModified means the object was updated.
	EventModified EventType = "MODIFIED"
	// EventDeleted means the object was removed.
	EventDeleted EventType = "DELETED"
)

// RawObject is a stored object's bytes plus its store-assigned revision. The
// bytes are the resource object's JSON; the store never parses them.
type RawObject struct {
	// Key is the full store key, /govirta/<kind>/<name>.
	Key string
	// Value is the resource object JSON.
	Value []byte
	// ResourceVersion carries the etcd ModRevision as a string.
	ResourceVersion string
}

// WatchEvent is a single change delivered on a Watch channel.
type WatchEvent struct {
	// Type is ADDED/MODIFIED/DELETED.
	Type EventType
	// Object is the post-change object (for DELETED, Key is set and Value may be empty).
	Object RawObject
}

// Store is the raw persistence + watch boundary.
type Store interface {
	// Put stores value at key. When expectedVersion is non-empty, the write is a
	// compare-and-swap that returns ErrRevisionConflict if the current version
	// differs. Returns the new RawObject (with assigned ResourceVersion).
	Put(ctx context.Context, key string, value []byte, expectedVersion string) (RawObject, error)
	// Get returns the object at key or ErrNotFound.
	Get(ctx context.Context, key string) (RawObject, error)
	// List returns all objects whose key starts with prefix, sorted by key.
	List(ctx context.Context, prefix string) ([]RawObject, error)
	// Delete removes key. Deleting a missing key is not an error (idempotent).
	Delete(ctx context.Context, key string) error
	// Watch streams events for keys under prefix starting after startRevision
	// ("" = current). The channel closes when ctx is done or the store closes.
	Watch(ctx context.Context, prefix string, startRevision string) (<-chan WatchEvent, error)
	// Close releases store resources.
	Close() error
}
```

- [ ] **Step 3: 接口契约测试（共享，Task 2 后启用）**

写 `store_test.go` 内一个导出的 `RunStoreContract(t *testing.T, newStore func() Store)`，覆盖：Put 新键返回非空 version；Get 命中/ErrNotFound；Put CAS 版本不符返回 ErrRevisionConflict；List 前缀 + 排序；Delete 幂等；Watch 收到 ADDED/MODIFIED/DELETED；ctx 取消后 Watch channel 关闭。Task 2 的 fake 与 Task 3 的 etcd 都调用此契约测试（同一套断言验证可替换性）。

- [ ] **Step 4: 验证**
- `go build ./internal/controlplane/store/`
- `gofmt -l internal/controlplane/store/`

---

## Task 2: fake 内存 Store

**Files:**
- Create: `internal/controlplane/store/fake/fake.go`
- Test: `internal/controlplane/store/fake/fake_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 纯内存 `Store` 实现，供上层（allocator / handlers / server）单测，无外部依赖。`map[string]RawObject` + 单调递增 revision 计数 + per-watch channel fan-out。并发安全（`sync.Mutex`）。

Acceptance evidence:
- `go test ./internal/controlplane/store/fake/ -run TestFakeStoreContract -v` 通过（调用 Task 1 的 `RunStoreContract`）
- watch fan-out 测试：两个 watcher 都收到同一事件；ctx 取消后对应 channel 关闭

- [ ] **Step 2: 写实现**

要点（实现细节，非逐字代码）：
- `New() *Store` 返回内存实现。
- revision：进程内 `int64` 计数器，每次成功 Put/Delete 自增；`ResourceVersion = strconv.FormatInt(rev, 10)`。
- CAS：`expectedVersion != ""` 时比对当前存储 version，不符返回 `store.ErrRevisionConflict`。
- Watch：注册一个 buffered channel 到 prefix 订阅列表；Put/Delete 后向所有匹配 prefix 的 watcher 投递 `WatchEvent`；`startRevision` 非空时先补发 revision 之后的当前快照（最小实现：仅支持 "" = 从当前开始 + 已存在对象作为 ADDED 重放，保证 node 重连不丢已存在对象）。
- goroutine 纪律：每个 watcher 的投递在持锁快照后异步发送，监听 `ctx.Done()` 关闭 channel，无 fire-and-forget（owner = Watch 调用者的 ctx）。
- `Close()` 关闭所有 watcher channel，后续操作返回 `store.ErrClosed`。

- [ ] **Step 3: 验证**
- `go test -race ./internal/controlplane/store/fake/`（watch fan-out 并发敏感，必须 -race）
- `gofmt -l`

---

## Task 3: etcd Store 实现

**Files:**
- Create: `internal/controlplane/store/etcd/etcd.go`
- Test: `internal/controlplane/store/etcd/etcd_test.go`
- Modify: `go.mod` / `go.sum`（`go get go.etcd.io/etcd/client/v3@v3.6.12`）

- [ ] **Step 1: 确认目标与验收标准**

Goal: 用 `clientv3` 实现 `Store`。Put/Get/List/Delete 走 KV API，CAS 用 etcd `Txn` + `Compare(ModRevision)`，Watch 用 `clientv3.Watch(ctx, prefix, WithPrefix(), WithRev(...))`。`ResourceVersion = ModRevision`。

Acceptance evidence:
- `GOVIRTA_ETCD_ENDPOINTS` 已设时：`go test ./internal/controlplane/store/etcd/ -run TestEtcdStoreContract -v` 通过（调用 `RunStoreContract`）
- 未设时：测试 `t.Skip("set GOVIRTA_ETCD_ENDPOINTS to run")`，不失败
- `go vet ./internal/controlplane/store/etcd/` 干净

- [ ] **Step 2: 写实现**

要点：
- `New(ctx, cfg clientv3.Config) (*Store, error)`：构造 client；caller 传 endpoints + dialTimeout，不在包内补默认（显式铁律）。
- Put CAS：
  ```go
  // expectedVersion=="" → 无条件 Put；否则 Txn 比对 ModRevision
  txn := cli.Txn(ctx)
  if expectedVersion != "" {
      rev, _ := strconv.ParseInt(expectedVersion, 10, 64)
      txn = txn.If(clientv3.Compare(clientv3.ModRevision(key), "=", rev))
  }
  resp, err := txn.Then(clientv3.OpPut(key, string(value))).Else(clientv3.OpGet(key)).Commit()
  // !resp.Succeeded && expectedVersion!="" → ErrRevisionConflict
  // 读回新 ModRevision：OpPut 后用 OpGet 或单独 Get 取 ModRevision
  ```
- List：`cli.Get(ctx, prefix, clientv3.WithPrefix())`，每个 KV 的 `ModRevision` 映射成 `ResourceVersion`，按 Key 排序。
- Linearizable read（MAC 分配需要）：List/Get 默认 linearizable（clientv3 默认即线性一致，不加 `WithSerializable()`）。
- Watch：`cli.Watch(ctx, prefix, clientv3.WithPrefix(), clientv3.WithRev(startRev+1))`；翻译 `mvccpb.PUT`→ADDED/MODIFIED（用 `IsCreate()` 区分），`mvccpb.DELETE`→DELETED；range watch channel 直到 ctx done。
- 错误：clientv3 错误 `%w` 包裹后返回；不吞错。
- `Close()` 调 `cli.Close()`。

- [ ] **Step 3: 验证**
- 本机起 etcd：`docker run -d --name govirta-etcd -p 2379:2379 quay.io/coreos/etcd:v3.6.12 etcd --advertise-client-urls http://0.0.0.0:2379 --listen-client-urls http://0.0.0.0:2379`（OrbStack docker）
- `GOVIRTA_ETCD_ENDPOINTS=localhost:2379 go test ./internal/controlplane/store/etcd/`
- 清理：`docker rm -f govirta-etcd`

---

## Task 4: `mac.Pool` 确定性候选枚举

**Files:**
- Create: `internal/controlplane/mac/pool.go`
- Test: `internal/controlplane/mac/pool_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 把配置的 locally-administered 单播 MAC 范围表示成确定性候选序列（纯函数，无 etcd）。给定 OUI 前缀 + 后缀区间 `[start, end]`，按序产出候选 `net.HardwareAddr`。

Acceptance evidence:
- `go test ./internal/controlplane/mac/ -run TestPool -v` 通过
- 覆盖：枚举顺序确定；候选都是 locally-administered（第 2 个 nibble bit1=1）且单播（bit0=0）；非法前缀（multicast/global）构造时拒绝；区间 start>end 拒绝

- [ ] **Step 2: 写实现**

要点：
- `type Pool struct{ ... }` + `NewPool(prefix net.HardwareAddr, start, end uint32) (*Pool, error)`。
- 构造期校验：prefix 必须是 locally-administered + 单播（`prefix[0]&0x02==0x02 && prefix[0]&0x01==0`），否则返回错误（显式拒绝非法池）。
- `Candidates() iter.Seq[net.HardwareAddr]`（Go 1.23+ range-over-func）或返回 `[]net.HardwareAddr`：prefix 拼接 3 字节后缀，从 start 到 end 顺序产出。确定性、可复现。
- `Contains(mac net.HardwareAddr) bool`：判断 mac 是否落在本池（allocator 释放/校验用）。
- 纯函数包，不 import etcd / apis。

- [ ] **Step 3: 验证**
- `go test ./internal/controlplane/mac/`
- `gofmt -l`

---

## Task 5: `MACAllocator` 接口 + etcd-backed 实现

**Files:**
- Create: `internal/controlplane/mac/allocator.go`
- Test: `internal/controlplane/mac/allocator_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 实现 spec 第 65-69 行 + brainstorming 三层唯一性。`Allocate` 从 `mac.Pool` 挑一个未被任何 NIC 对象占用的 MAC，进程内 mutex 串行化「list NIC 派生占用 → 挑空闲 → caller 写 NIC」。allocator 不直接写 NIC（写在 apply handler 的同一事务语义内），而是返回挑中的 MAC + 占用快照；或提供 `Allocate(ctx) (net.HardwareAddr, error)` 在锁内 list 并返回候选，由 handler 立即 Put 后释放锁。**实现选定：allocator 持锁完成 list→挑选→返回，handler 在锁释放前完成 Put**——即 allocator 暴露 `WithAllocation(ctx, func(mac) error) error`，把 handler 的 NIC Put 闭包在锁内执行，保证「挑中→写入」原子。

Acceptance evidence:
- `go test -race ./internal/controlplane/mac/ -run TestAllocator -v` 通过（用 fake store）
- 覆盖：空占用时分配池首个；已有 NIC 占用 M1 时跳过 M1；并发 N 个 `WithAllocation` 分到 N 个互不相同的 MAC（-race + 串行性断言）；池耗尽返回 `ErrMACPoolExhausted`

- [ ] **Step 2: 写实现**

```go
// ErrMACPoolExhausted is returned when every MAC in the pool is already
// occupied by an existing NIC object.
var ErrMACPoolExhausted = errors.New("mac: pool exhausted")

// MACAllocator allocates a unique MAC from the platform pool. The first-slice
// implementation relies on a single-process apiserver (process-mutex
// serialization); multi-replica deployments must migrate allocation to a
// dedicated control-plane controller (memory 820). This interface is the seam
// for that migration.
type MACAllocator interface {
	// WithAllocation picks a MAC not occupied by any existing NIC object and
	// invokes commit(mac) while holding the allocation lock, so the caller's NIC
	// Put is atomic with selection. commit's error aborts allocation.
	WithAllocation(ctx context.Context, commit func(mac net.HardwareAddr) error) error
}
```

实现 `etcdAllocator`：
- 字段：`pool *Pool`、`store store.Store`、`mu sync.Mutex`、NIC key 前缀 `/govirta/NIC/`。
- `WithAllocation`：`mu.Lock(); defer mu.Unlock()` → `store.List(ctx, "/govirta/NIC/")`（linearizable）→ 解析每个 NIC 对象 JSON 取 `Spec.MAC` 建占用 set（这里需 import `apis/nic/v1alpha1` 解析 — allocator 在 internal 层，允许 import apis）→ 遍历 `pool.Candidates()` 找第一个不在 set 的 → 调 `commit(mac)`（handler 在此闭包内 Put NIC）→ commit 返回后释放锁。无空闲返回 `ErrMACPoolExhausted`。
- 占用解析只看非空 `Spec.MAC`（NIC 对象是唯一事实源，无独立账本 — A 方案）。
- 错误全部 `%w` 传播。

- [ ] **Step 3: 验证**
- `go test -race ./internal/controlplane/mac/`
- 并发测试用 fake store + N goroutine 调 `WithAllocation`，断言分到的 MAC 集合大小 == N（无重复）

---

## Task 6: scheduler 重构（消费 apis/vm）

**Files:**
- Modify: `internal/controlplane/scheduler/scheduler.go`
- Create: `internal/controlplane/scheduler/scheduler_test.go`
- Evaluate-delete: `internal/types`（旧 `VirtualMachine`/`Node`，若无其他引用）

- [ ] **Step 1: 确认目标与验收标准**

Goal: 重构 `Scheduler` 接口消费 `apis/vm` 对象。删除旧 `internal/types.VirtualMachine/Node` 依赖（fast-iteration 无兼容层）。第一刀仍是 noop 策略（选 `nodeNames[0]`），但返回的是要写入 `VM.Spec.NodeName` 的 node 名。

Acceptance evidence:
- `go test ./internal/controlplane/scheduler/ -v` 通过
- 覆盖：有 node 时绑第一个；空 node 列表返回显式 `ErrNoNodes`；ctx 取消返回 `ctx.Err()`

- [ ] **Step 2: 写实现**

```go
// Scheduler decides which node a VM is placed on by returning the node name to
// write into VM.Spec.NodeName.
type Scheduler interface {
	Schedule(ctx context.Context, vm vmv1.VM, nodeNames []string) (string, error)
}
```
- `ErrNoNodes = errors.New("scheduler: no nodes available")`。
- `NoopScheduler.Schedule`：ctx 检查 → 空列表返回 `ErrNoNodes` → 返回 `nodeNames[0]`。
- 删除旧 `internal/types` 引用前，先 `grep` 确认无其他引用方；若 `internal/types` 整包仅剩这两个类型且无引用，评估删整包（反模式铁律：不留死代码，但仅删本次替换掉的）。

- [ ] **Step 3: 验证**
- `go build ./internal/controlplane/...`
- `go test ./internal/controlplane/scheduler/`

---

## Task 7: apply handler（校验 + MAC 分配 + Put）

**Files:**
- Create: `internal/controlplane/apiserver/handler_apply.go`
- Test: `internal/controlplane/apiserver/handlers_test.go`（apply 部分）

- [ ] **Step 1: 确认目标与验收标准**

Goal: `POST/PUT /apis/<kind>/<name>` 接收资源对象 JSON，按 kind 解码到对应 apis 类型，调其 `Validate()`，对 NIC 额外做准入期 MAC 分配（`Spec.MAC` 为空时），落 Store。收口 note #26：Network 范围自洽校验在此 handler 做（apiserver 准入期）。

Acceptance evidence:
- `go test ./internal/controlplane/apiserver/ -run TestApply -v` 通过（httptest + fake store）
- 覆盖：合法 StoragePool/Image/Volume/Network/NIC/VM apply 成功落库；非法 spec（如空 Name、Volume root 缺 ImageRef）返回 400 + 错误体；NIC 空 MAC 被分配非空 MAC 后落库；NIC 已带 MAC 时原样保留（用户显式不该发生但要确定性）；Network `LeaseSeconds<0` / `DHCPRangeStart>End` / 网关不在子网 → 400

- [ ] **Step 2: 写实现**

要点：
- kind→类型分发：用一个 `map[v1alpha1.Kind]func() apiObject` 注册表，`apiObject` 接口含 `Validate() error` + `GetObjectMeta()`。六类型各实现（apis 已有 Validate；GetObjectMeta 返回内嵌 ObjectMeta — 可能需在 apis 加该方法，若缺则在 handler 侧用类型 switch，避免改 apis 范围）。**实现选定**：handler 侧 `switch kind` 解码，避免触碰 apis 包（Plan 1 已合并冻结）。
- 流程：读 body → `switch kind` 解码到具体类型 → `obj.Validate()` 失败返回 400 → kind==NIC 且 MAC 空：`allocator.WithAllocation(ctx, func(mac){ nic.Spec.MAC=mac.String(); return store.Put(...) })` → kind==Network：额外 `validateNetworkAdmission(spec)`（范围自洽，note #26）→ 其他 kind：`store.Put(ctx, key, json, "")` → 返回 201 + 落库后对象（含 ResourceVersion）。
- key = `/govirta/<kind>/<meta.Name>`。
- Network 准入校验函数 `validateNetworkAdmission`：`LeaseSeconds>=0`；DHCP 范围 `start<=end`；gateway/DHCP 范围 ∈ subnet（用 `net/netip` 的 `Prefix.Contains`）。这是 apiserver 职责，不下放 apis/node。
- 错误体：统一 JSON `{"error": "..."}`，4xx/5xx 分类。

- [ ] **Step 3: 验证**
- `go test ./internal/controlplane/apiserver/ -run TestApply`

---

## Task 8: get/list handler

**Files:**
- Create: `internal/controlplane/apiserver/handler_get.go`
- Test: `handlers_test.go`（get 部分）

- [ ] **Step 1: 确认目标与验收标准**

Goal: `GET /apis/<kind>/<name>` 返回单对象；`GET /apis/<kind>` 返回列表。直通 Store.Get/List，不缓存。

Acceptance evidence:
- `go test ./internal/controlplane/apiserver/ -run TestGet -v` 通过
- 覆盖：get 命中返回 200 + 对象 JSON；get 缺失返回 404；list 返回数组（空时 `[]` 非 null）；list 按 name 排序

- [ ] **Step 2: 写实现**
- get：`store.Get` → ErrNotFound 映射 404 → 200 + raw value（已是 JSON，直接透传，附 ResourceVersion 到 header 或重新编码进 metadata）。
- list：`store.List(ctx, "/govirta/<kind>/")` → 拼成 JSON 数组返回。
- 不解析对象（透传裸 JSON），保持 handler 对类型无感（除 apply/watch 的 nodeName 过滤需要）。

- [ ] **Step 3: 验证**
- `go test ./internal/controlplane/apiserver/ -run TestGet`

---

## Task 9: watch handler（k8s 式下行事件流）

**Files:**
- Create: `internal/controlplane/apiserver/handler_watch.go`
- Test: `handlers_test.go`（watch 部分）

- [ ] **Step 1: 确认目标与验收标准**

Goal: `GET /apis/<kind>?watch=true&nodeName=N` 开一个 etcd Watch（spec 第 6 问 A-直通），翻译成 chunked newline-delimited JSON 事件流推给 node。`nodeName` 过滤在 Go 端做（解析对象的 `Spec.NodeName` / `ObjectMeta.NodeName`）。

Acceptance evidence:
- `go test ./internal/controlplane/apiserver/ -run TestWatch -v` 通过（用 fake store 的 Watch + httptest）
- 覆盖：watch 收到匹配 nodeName 的 ADDED 事件并 flush；不匹配 nodeName 的对象被过滤掉；client 断开（ctx done）后 handler 退出不泄漏 goroutine；事件格式 = 每行一个 `{"type":...,"object":...}` JSON

- [ ] **Step 2: 写实现**

要点：
- 解析 query：`watch=true` 进入流式；`nodeName` 必填（node 拨号必带，缺失返回 400 — 显式）；可选 `resourceVersion` 作为 startRevision（断线 resume，对应 clientv3 `WithRev`）。
- `store.Watch(ctx, "/govirta/<kind>/", startRev)` 拿 channel。
- 对每个 event：解析 object JSON 取 nodeName 字段（VM 用 `Spec.NodeName`；pool/image/volume/network/nic 用 `ObjectMeta.NodeName` — spec 第 97 行：本地资源由 govirtctl 显式带 NodeName 提交）→ 不匹配则跳过 → 匹配则写一行 JSON + `flusher.Flush()`。
- `http.Flusher` 做 chunked；`ctx = r.Context()`，client 断开自动 cancel watch。
- 无 fire-and-forget：watch range 在 handler goroutine 内，ctx done 即 return。
- nodeName 字段提取：handler 侧最小解析结构 `struct{ Metadata struct{ NodeName string }; Spec struct{ NodeName string } }`，按 kind 取对应位置。

- [ ] **Step 3: 验证**
- `go test -race ./internal/controlplane/apiserver/ -run TestWatch`（goroutine 泄漏敏感）

---

## Task 10: status handler（上行 reconcile）

**Files:**
- Create: `internal/controlplane/apiserver/handler_status.go`
- Test: `handlers_test.go`（status 部分）

- [ ] **Step 1: 确认目标与验收标准**

Goal: `PATCH /apis/<kind>/<name>/status` 接收 node 上报的 status，merge 进 etcd 中对象的 `.status` 字段后写回（上下一致：master 向 node 现实 reconcile，只动 status 不动 spec）。

Acceptance evidence:
- `go test ./internal/controlplane/apiserver/ -run TestStatus -v` 通过
- 覆盖：status patch 成功更新对象 status、保留 spec 不变；对象不存在返回 404；并发 patch 用 CAS（version 不符重试或返回 409）

- [ ] **Step 2: 写实现**
- 流程：`store.Get(key)` 拿当前对象 + version → 解析为 `map[string]json.RawMessage` → 替换 `"status"` 字段为 body → `store.Put(key, merged, version)` CAS → 冲突返回 409（node 重试）或 handler 内重读重试一次。
- **只允许改 status**：spec 字段原样保留（reconcile 铁律：master 不改期望，只接收现实）。
- key 同 apply。

- [ ] **Step 3: 验证**
- `go test ./internal/controlplane/apiserver/ -run TestStatus`

---

## Task 11: apiserver server 组装 + 路由

**Files:**
- Modify: `internal/controlplane/apiserver/server.go`（替换 NoopServer）
- Test: `internal/controlplane/apiserver/server_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 用真实 `http.Server` 替换 `NoopServer`，路由四个 handler，注入 `Store` + `MACAllocator` + `Scheduler`。`Run(ctx)` 启动监听，`ctx.Done()` 时 graceful shutdown。

Acceptance evidence:
- `go test ./internal/controlplane/apiserver/ -run TestServer -v` 通过
- 覆盖：Run 启动后路由可达（httptest server 或真实 listener + 随机端口）；ctx 取消触发 `srv.Shutdown`；路由分发正确（apply/get/watch/status 各打到对应 handler）

- [ ] **Step 2: 写实现**
- `Server` 结构持有 `store.Store`、`mac.MACAllocator`、`scheduler.Scheduler`、`addr string`（caller 显式传监听地址，不补默认）。
- 路由：`http.ServeMux` 或手写 path 解析 `/apis/<kind>[/<name>[/status]]` + method/query 分发。
- `Run(ctx)`：起 `http.Server`，goroutine 监听 ctx.Done 调 `Shutdown`；listener error 向上返回。
- scheduler 接入：apply VM 对象时，若 `Spec.NodeName` 为空，调 scheduler 绑定（第一刀 node 列表来源 = 配置注入的静态 node 名，spec 已定固定 node 身份）。**实现选定**：VM 绑定放在 apply handler VM 分支（Task 7 已留），server 注入 scheduler + 静态 nodeNames。
- 保留 `apiserver.Server` 接口契约（`Run(ctx) error`），让 controlplane 组装层无感替换（积木式）。

- [ ] **Step 3: 验证**
- `go test ./internal/controlplane/apiserver/`

---

## Task 12: controlplane service 组装重构

**Files:**
- Modify: `internal/controlplane/service.go`
- Test: `internal/controlplane/service_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: `NewService` 从「注入 NoopServer」重构为「组装 etcd Store + MACAllocator + Scheduler + 真实 apiserver」。配置（etcd endpoints、监听地址、MAC 池、静态 node 名）由 caller 显式传入（显式铁律），`cmd/govirtad/main.go` 提供。

Acceptance evidence:
- `go test ./internal/controlplane/ -v` 通过（用 fake store 注入，不碰真 etcd）
- `go build ./cmd/govirtad`
- `scripts/verify.sh` 全绿

- [ ] **Step 2: 写实现**
- `NewService(cfg Config) (*Service, error)`，`Config` 含 etcd endpoints / listen addr / MAC pool 参数 / node names。
- 组装：`etcd.New` → `mac.NewPool` + `mac.NewAllocator(pool, store)` → `scheduler.NewNoopScheduler()` → `apiserver.NewServer(store, allocator, scheduler, addr, nodeNames)`。
- `Service.Run` 调 `apiServer.Run(ctx)`（签名不变，积木式）。
- 为单测可注入：`NewService` 接受已构造的 `Store` 或拆一个 `newServiceWithStore` 内部构造器，让 service_test 用 fake。
- `cmd/govirtad/main.go`：从 flag / 配置文件读 etcd endpoints 等，构造 `Config`，调 `NewService`。配置项显式，无隐藏默认。

- [ ] **Step 3: 验证**
- `scripts/verify.sh`

---

## Task 13: 全量验证 + master 侧手动冒烟

**Files:** 无新代码（验证收尾）

- [ ] **Step 1: 全量自动验证**
- `gofmt -l .`（空输出）
- `go build ./...`
- `go test ./...`（fake store 路径全绿）
- `go test -race ./internal/controlplane/...`
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`（确认无 darwin-only 假设，memory 700）

- [ ] **Step 2: etcd 集成验证（OrbStack docker）**
- 起 etcd 容器（Task 3 命令）
- `GOVIRTA_ETCD_ENDPOINTS=localhost:2379 go test ./internal/controlplane/store/etcd/`
- 清理容器

- [ ] **Step 3: master 端到端手动冒烟（无 node，证明 Plan 2 产出可用）**
- 起 etcd 容器 + `go run ./cmd/govirtad`（配置指向 localhost etcd + 监听端口）
- `curl -X POST .../apis/StoragePool/pool1 -d @pool.json` → 201
- `curl -X POST .../apis/NIC/nic0 -d @nic.json`（MAC 留空）→ 201，返回体 `Spec.MAC` 非空（验证准入分配）
- `curl .../apis/NIC/nic0` → 200，MAC 与上一步一致
- `curl -N '.../apis/VM?watch=true&nodeName=node0'` 后台开着 → 另一终端 apply 一个绑 node0 的 VM → watch 流出 ADDED 事件（验证下行 watch + nodeName 过滤）
- `curl -X PATCH .../apis/VM/vm0/status -d '{"phase":"running"}'` → 200，再 get 确认 status 更新、spec 不变（验证上行 reconcile）
- 记录冒烟命令 + 真实输出到交付物（证据导向铁律）；清理容器与进程（写 `.tmp/`，不污染 /tmp）

- [ ] **Step 2/3 证据要求**：实际命令 + 真实输出贴进交付报告；NIC MAC 分配、watch nodeName 过滤、status reconcile 三个关键不变式必须有可复现命令证明（memory 822 教训：不接受未验证的成功声明）。

---

## 验证总览

| 层 | 命令 | 依赖 |
| --- | --- | --- |
| 单元（全 fake） | `go test ./internal/controlplane/...` | 无 |
| 并发 | `go test -race ./internal/controlplane/...` | 无 |
| etcd 集成 | `GOVIRTA_ETCD_ENDPOINTS=localhost:2379 go test ./internal/controlplane/store/etcd/` | OrbStack docker etcd |
| 跨平台 | `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` | 无 |
| 本地 CI | `scripts/verify.sh` | 无 |
| master 冒烟 | `cmd/govirtad` + curl 序列 | docker etcd |

## 明确不做（留给 Plan 3/4）

- node 侧 controller-manager + 六控制器 + watchclient（Plan 3）
- `govirtctl apply/get` CLI（Plan 4）
- 三节点 Lima 端到端闭环（Plan 4）
- watch cache / informer、退避限速、断线重连的完整恢复语义（仅最小 resume）
- IP 分配、Node 注册对象 + Lease 心跳

## Self-review

- [x] 每个 task 有明确 Goal + Acceptance evidence（可复现命令）
- [x] Store 积木式边界：fake / etcd 共用 `RunStoreContract` 证明可替换
- [x] 单一事实源：master 直通 etcd 无缓存；status reconcile 只动 status 不动 spec
- [x] MAC 三层唯一性（pool 确定性枚举 + linearizable list 派生占用 + mutex 串行化）+ 单点边界声明 + 接口迁移预留
- [x] etcd 不写 mock，用 docker 真实依赖 + 环境变量 skip 模式（第十四章）
- [x] note #26 Network 范围校验归属 apiserver 准入期，已落 Task 7
- [x] 显式铁律：所有配置（endpoints/addr/pool/node names）caller 显式传，无隐藏默认
- [x] ctx 端到端：Store/handler/allocator 全链路 ctx 首参
- [x] 错误传播 + `errors.Join`（CAS 冲突等）；无 `_ = err`
- [x] fast-iteration：旧 `internal/types` 直接替换无兼容层
- [x] memory 822 教训：Task 13 强制贴真实命令输出证明三个关键不变式
- [x] 跨平台 build 检查（memory 700）
