# 垂直切片 Plan 3：node 侧（controller-manager + 六控制器 + master client）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现计算节点 `govirtlet` 的 controller-manager 形态：自建轻量控制循环框架 + master HTTP client（watch 下行 / get / PATCH status 上行）+ 确定性底层身份派生 + 六个一等公民控制器（StoragePool / Image / Volume / Network / NIC / VM），每个走最薄 create+status 路径，复用既有 `internal/storage`、`internal/network`、`internal/vmm` 执行面。产出：一个能 watch master 派给本节点的资源对象、按依赖链 reconcile 到真实主机资源（真 QEMU）、再上报 live status 的 node agent。

**Architecture:** 三层。(1) 自建控制框架 `watch → workqueue → reconcile → PATCH status`，每个控制器 watch 自己的 kind（Plan 2 的下行 watch），失败重入队，无退避优化。(2) master HTTP client：watch（断线 resume 复用 Plan 2 已验证的 `resourceVersion → startRevision` 语义）+ get（依赖 gating 用 live 读）+ PATCH status（上报 live 现实）。(3) 六个控制器把 apis Spec 映射到执行面 Register+Ensure 请求类型，底层 nftables/TAP 内核身份由控制器**确定性派生**（决策 A：API Spec 不含内核身份），依赖链通过「被引用对象 live Status=Ready」gate。单一事实源：观测状态永远 live 读执行面，不缓存。

**Tech Stack:** Go 1.26，stdlib `net/http` client，复用 `internal/storage`（VolumeService/ImageService/pool.Service）、`internal/network`（NetworkService/NICService over netpool）、`internal/vmm`（VMMService）、`pkg/virt/qemu`（Builder），import `pkg/apis`。

---

## 关键设计决策（来自已确认 spec + brainstorming，写进实现）

- **node = controller-manager**：一个 manager 托管六个独立控制器，共享一套循环机制。每个控制器 `watch(kind, nodeName) → workqueue → Reconcile → PATCH status`。
- **自建框架（B 方案）**：不引入 client-go。轻量 `Controller` 接口 + 去重 `workqueue` + 通用 `loop`，贴合 Plan 2 的 k8s 式 HTTP watch 契约，无重依赖。
- **确定性底层身份派生（决策 A）**：API Spec 只含语义意图；nftables 表名/链/优先级、TAP owner UID/GID、VNetHeader 由 node 控制器从资源稳定身份（network name / VM uid）**确定性纯函数**派生后填进 `netpool.NetworkDefinition`/`NICDefinition`。这不违反「编排层不生成」——netpool 仍收到调用方（=控制器，控制面组件）的显式值；与 MAC 分配同构（apiserver 算 MAC、node 控制器算 firewall/TAP 身份）。MAC 例外：MAC 已由 apiserver 准入期分配进 `NIC.Spec.MAC`，控制器原样透传，绝不重新生成。
- **依赖链 gating（单一事实源）**：控制器推进前 gate「被引用对象 live Status=Ready」——通过 master client `GET` 读被引用对象的当前 status，不信任本地缓存。未 Ready 则重入队等待（最薄重试，无退避）。
  - Volume 控制器 gate：被引用 StoragePool（block + image file pool）+ Image 都 Ready。
  - NIC 控制器 gate：被引用 Network Ready。
  - VM 控制器 gate：被引用所有 Volume + NIC 都 Ready。
- **观测状态 live 读**：每个控制器上报 status 前，从执行面 live 读真实资源态（`GetNetworkStatus`/`GetNICStatus`/`vmm.Status`/pool usage），不缓存 drift-prone 状态（上下一致铁律）。
- **watch resume**：watchclient 断线后用上次见到的 `resourceVersion` 作为 `?resourceVersion=` 重连，复用 Plan 2 已在真 etcd 验证的 resume 契约（fake/etcd 同序回放、保留事件类型）。
- **node 身份**：第一刀 node 名由 cmd flag 显式注入（`--node-name`），与 master 静态 node 列表一致；不做 Node 注册对象 + Lease（留给后续切片）。
- **执行面真实签名（已确认，零未定义引用）**：
  - `pool.Service.RegisterPool(*Pool) error`、`GetPoolUsage(ctx, name) (Usage, error)`
  - `VolumeService.CreateRootVolumeFromReader(ctx, CreateRootVolumeFromReaderRequest{VMID,VMName,PoolName,Name,DiskIndex,CapacityBytes,ReadOnly,Reader,Format}) (volume.Volume, error)`、`PublishVolume(ctx, PublishVolumeRequest{VolumeID,VMID,PoolName,ReadOnly}) (volume.PublishedVolume, error)`
  - `ImageService.PutImage(ctx, PutImageRequest{PoolName,ImageID,Format,DeclaredSizeBytes}) (image.ImageWriter, error)`、`GetImage(ctx, GetImageRequest{PoolName,ImageID}) (io.ReadCloser, error)`
  - `NetworkService.RegisterNetwork(netpool.NetworkDefinition) error`、`EnsureNetwork(ctx, NetworkName) (NetworkStatus, error)`、`GetNetworkStatus(ctx, NetworkName) (NetworkStatus, error)`
  - `NICService.RegisterNIC(netpool.NICDefinition) error`、`EnsureNIC(ctx, NetworkName, VMID) (NICStatus, error)`、`GetNICStatus(ctx, NetworkName, VMID) (NICStatus, error)`
  - `vmm.NewVMMService(runtimeRoot, proc.ProcessController, QMPFactory) (*VMMService, error)`、`Create(ctx, CreateRequest{UUID,Builder *qemu.Builder,Spec SpecSummary}) (VM, error)`、`Start(ctx, uuid) (VM, error)`、`Status(ctx, uuid) (VM, error)`
- **apis Status 字段（已确认，控制器写回目标）**：`StoragePoolStatus{Phase,AllocatedBytes,Message}`、`ImageStatus{Phase,LocalSizeBytes,Message}`、`VolumeStatus{Phase,VolumePath,Message}`、`NetworkStatus{Phase,Message}`、`NICStatus{Phase,TapName,Message}`、`VMStatus{Phase,Message}`。

---

## 文件结构

```
internal/node/
  controller/
    controller.go        # Controller 接口 + Event 类型
    queue.go             # 去重 workqueue（key 去重 + 失败重入队）
    queue_test.go
    manager.go           # Manager：托管 N 个控制器，统一启动/停止
    manager_test.go
    loop.go              # 通用循环：watch 事件 → 入队 → Reconcile → 重入队
    loop_test.go
  client/
    client.go            # master HTTP client：Get / List / PatchStatus
    client_test.go
    watch.go             # 流式 watch client + resourceVersion resume
    watch_test.go
  identity/
    identity.go          # 确定性派生 nftables/TAP 身份（纯函数）
    identity_test.go
  controllers/
    storagepool.go       # StoragePool 控制器（复用 pool.Service）
    storagepool_test.go
    image.go             # Image 控制器（外部源拉取 → ImageService.PutImage）
    image_test.go
    volume.go            # Volume 控制器（gate pool+image → CreateRootVolumeFromReader）
    volume_test.go
    network.go           # Network 控制器（派生身份 → RegisterNetwork+EnsureNetwork）
    network_test.go
    nic.go               # NIC 控制器（gate network → RegisterNIC+EnsureNIC）
    nic_test.go
    vm.go                # VM 控制器（gate volume+nic → Builder → vmm.Create+Start）
    vm_test.go
  agent.go               # 重写：组装 controller-manager（替换 no-op skeleton）
  agent_test.go

cmd/govirtlet/main.go    # 显式 flag 配置（master URL / node name / runtime root / 执行面根）
```

设计依据（来自已确认 spec `2026-06-06-controlplane-node-vertical-slice-design.md` + brainstorming 决策）：node = controller-manager；自建框架；确定性身份派生；依赖链 live gating；观测 live 读；resume 复用 Plan 2 契约。

---

## Task 1: `Controller` 接口 + Event 类型

**Files:**
- Create: `internal/node/controller/controller.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 定义控制器框架的核心抽象。`Controller` 是每个 kind 的 reconcile 单元；`Event` 是 watch 下发的资源变更。框架对六个具体 kind 无感知（积木式），控制器自己解码 apis 对象。

Acceptance evidence:
- `go build ./internal/node/controller/`
- 接口签名 review：`Reconcile` 接受 ctx + Event，返回 `(requeue bool, err error)`

- [ ] **Step 2: 写实现**

```go
// Package controller is govirtlet's self-built controller-manager framework. It
// is a lightweight watch → workqueue → reconcile → status loop, deliberately not
// k8s client-go: it binds to the project's own master HTTP watch contract and
// pulls in no heavy dependencies. The framework is kind-agnostic; each Controller
// decodes its own apis object from the raw event bytes.
package controller

import "context"

// EventType is the kind of change observed on a watched resource. State-machine
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

// Event is one resource change delivered to a Controller. Object is the resource
// object's raw JSON (the framework never parses it); Key is the dedup identity
// (the object's name within its kind).
type Event struct {
	Type   EventType
	Key    string
	Object []byte
}

// Controller reconciles one resource kind toward its spec. Kind names the apis
// kind this controller watches (used to build the watch URL). Reconcile drives
// one object toward its desired state and reports status; returning requeue=true
// asks the loop to retry later (e.g. a dependency is not Ready yet). An error is
// logged and also triggers a requeue.
type Controller interface {
	// Kind is the apis kind this controller watches, e.g. "VM".
	Kind() string
	// Reconcile processes one event. requeue=true means retry later (dependency
	// not ready); err means the attempt failed and should be retried.
	Reconcile(ctx context.Context, ev Event) (requeue bool, err error)
}
```

- [ ] **Step 3: 验证**
- `go build ./internal/node/controller/`
- `gofmt -l internal/node/controller/`

---

## Task 2: 去重 workqueue

**Files:**
- Create: `internal/node/controller/queue.go`
- Test: `internal/node/controller/queue_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 一个并发安全的去重 workqueue。同 key 多次入队只保留一份待处理（最新 Event 覆盖旧的）；支持失败重入队；`Get` 阻塞直到有项或关闭；`Shutdown` 解除阻塞。

Acceptance evidence:
- `go test -race ./internal/node/controller/ -run TestQueue -v` 通过
- 覆盖：同 key 入队去重；Get 返回入队项；失败 re-Add 可再次取出；Shutdown 后 Get 返回 closed

- [ ] **Step 2: 写实现**

要点（实现细节）：
- `type Queue struct{ mu sync.Mutex; items map[string]Event; order []string; notify chan struct{}; closed bool }`。
- `New() *Queue`：初始化 map + buffered(1) notify channel。
- `Add(ev Event)`：持锁，若 key 不在 order 则 append；`items[key]=ev`（最新覆盖，去重）；非阻塞 notify。
- `Get() (Event, bool)`：阻塞 select notify / closed；持锁弹出 order[0] 对应 item，返回；closed 且空返回 `(_, false)`。
- `Shutdown()`：持锁 closed=true，close(notify-equivalent)；幂等。
- goroutine 纪律：Get 的阻塞用 `for { select{<-notify / <-done} }`，无 fire-and-forget。

- [ ] **Step 3: 写测试**

```go
func TestQueueDedupsSameKey(t *testing.T) {
	q := New()
	q.Add(Event{Type: EventAdded, Key: "a", Object: []byte(`{"v":1}`)})
	q.Add(Event{Type: EventModified, Key: "a", Object: []byte(`{"v":2}`)})
	ev, ok := q.Get()
	if !ok || ev.Key != "a" || string(ev.Object) != `{"v":2}` {
		t.Fatalf("dedup: got %+v ok=%v, want latest v:2 for key a", ev, ok)
	}
	// Only one item should have been queued.
	q.Shutdown()
	if _, ok := q.Get(); ok {
		t.Fatalf("expected queue drained after single dedup'd item")
	}
}

func TestQueueShutdownUnblocksGet(t *testing.T) {
	q := New()
	done := make(chan struct{})
	go func() {
		_, ok := q.Get()
		if ok {
			t.Errorf("Get after shutdown: ok=true, want false")
		}
		close(done)
	}()
	q.Shutdown()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Shutdown did not unblock Get")
	}
}
```

- [ ] **Step 4: 验证**
- `go test -race ./internal/node/controller/ -run TestQueue`
- `gofmt -l`

---

## Task 3: Manager + 通用 loop

**Files:**
- Create: `internal/node/controller/manager.go`
- Create: `internal/node/controller/loop.go`
- Test: `internal/node/controller/manager_test.go`
- Test: `internal/node/controller/loop_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: `Manager` 托管 N 个控制器，每个控制器跑一条 loop：从 watch 源拿 Event → 入队 → 调 `Reconcile` → requeue/error 则重入队。`Manager.Run(ctx)` 启动全部 loop，`ctx.Done()` 时统一停止（关闭所有 queue、等所有 loop 退出，无泄漏）。

Acceptance evidence:
- `go test -race ./internal/node/controller/ -run 'TestManager|TestLoop' -v` 通过
- 覆盖：loop 把 watch 事件喂给 Reconcile；Reconcile 返回 requeue=true 时该 key 被再次处理；ctx 取消后 Run 返回且所有 goroutine 退出（WaitGroup join）

- [ ] **Step 2: 写实现**

要点：
- `EventSource` 接口：`Watch(ctx, kind, startRevision string) (<-chan Event, error)`（Task 4 的 watchclient 实现它；测试用 fake）。
- `Manager struct{ source EventSource; controllers []Controller }` + `NewManager(source, controllers)`。
- `Run(ctx)`：每个控制器起一条 goroutine 跑 `runController`，`sync.WaitGroup` join；ctx.Done 时 return `ctx.Err()`。无 fire-and-forget。
- `runController(ctx, c)`：
  - 起一个 feeder goroutine：`source.Watch(ctx, c.Kind(), lastRV)` → range channel → `queue.Add(ev)`；记录 `lastRV`（断线 resume）；watch channel 关闭则用 lastRV 重连（最小 resume，无退避）。
  - 主循环：`queue.Get()` → `c.Reconcile(ctx, ev)` → `requeue || err` 则 `queue.Add(ev)` 重入队（最薄重试）。
  - ctx.Done 时 `queue.Shutdown()` 退出。
- `loop.go` 放 `runController` + feeder；`manager.go` 放 Manager/NewManager/Run。

- [ ] **Step 3: 写测试**

要点：用 fake EventSource（预置事件 channel）+ fake Controller（记录收到的 Event，可配置返回 requeue 一次后成功）。断言：事件到达 Reconcile；requeue 的 key 被重处理；ctx 取消后 Run 返回、无 goroutine 泄漏（用 `runtime.NumGoroutine` 前后对比或 WaitGroup 完成信号）。

- [ ] **Step 4: 验证**
- `go test -race ./internal/node/controller/`
- `gofmt -l`

---

## Task 4: master HTTP client（Get / List / PatchStatus）

**Files:**
- Create: `internal/node/client/client.go`
- Test: `internal/node/client/client_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: node 访问 master apiserver 的 HTTP 客户端。`Get(ctx, kind, name)` 读单对象（依赖 gating 用）；`List(ctx, kind)`；`PatchStatus(ctx, kind, name, statusJSON)` 上报 status。caller 显式传 master base URL（无默认）。

Acceptance evidence:
- `go test ./internal/node/client/ -run TestClient -v` 通过（httptest server）
- 覆盖：Get 命中返回对象字节 + 404 映射 `ErrNotFound`；PatchStatus 发 PATCH 到 `/apis/<kind>/<name>/status` 并在非 2xx 返回错误；List 返回数组

- [ ] **Step 2: 写实现**

要点：
- `Client struct{ baseURL string; http *http.Client }` + `New(baseURL string, hc *http.Client) *Client`（hc 为 nil 用 `http.DefaultClient`——这是 client 行为旋钮非业务默认，可接受；baseURL 必须显式）。
- `ErrNotFound = errors.New("node/client: object not found")`。
- `Get(ctx, kind, name) ([]byte, error)`：`GET {base}/apis/{kind}/{name}`；404→ErrNotFound；2xx 读 body；其他状态码 `%w` 错误。
- `List(ctx, kind) ([]byte, error)`：`GET {base}/apis/{kind}`。
- `PatchStatus(ctx, kind, name string, status []byte) ([]byte, error)`：`PATCH {base}/apis/{kind}/{name}/status` body=status；非 2xx 返回错误（含 body 文案）。
- 所有请求 `http.NewRequestWithContext(ctx, ...)`，ctx 端到端。错误 `%w` 包裹。

- [ ] **Step 3: 写测试**

要点：`httptest.NewServer` 挂一个 mux 模拟 apiserver 路由；断言 Get 命中/404、PatchStatus 的 method+path+body、List 数组。

- [ ] **Step 4: 验证**
- `go test ./internal/node/client/ -run TestClient`
- `gofmt -l`

---

## Task 5: watch client（流式 + resume）

**Files:**
- Create: `internal/node/client/watch.go`
- Test: `internal/node/client/watch_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 实现 `controller.EventSource`。`Watch(ctx, kind, startRevision)` 向 master 发 `GET /apis/{kind}?watch=true&nodeName={node}&resourceVersion={startRevision}`，流式解析 newline-delimited JSON 事件，翻译成 `controller.Event` 推到 channel。复用 Plan 2 已验证的 resume 语义（resourceVersion → startRevision）。

Acceptance evidence:
- `go test -race ./internal/node/client/ -run TestWatch -v` 通过（httptest 流式 server）
- 覆盖：watch 解析多行事件并翻译成 controller.Event；nodeName + resourceVersion 作为 query 发出；ctx 取消后 channel 关闭、goroutine 退出；每个事件 Key = 对象 metadata.name

- [ ] **Step 2: 写实现**

要点：
- watch client 持有 `Client` 的 baseURL/http + 注入的 `nodeName`。
- `Watch(ctx, kind, startRevision) (<-chan controller.Event, error)`：构造 URL（含 `watch=true&nodeName=&resourceVersion=`）→ `http.NewRequestWithContext` → Do；非 200 返回错误。
- 起一个 goroutine：`bufio.Scanner`/`json.Decoder` 逐行 decode `{type, object}` → 取 object 的 `metadata.name` 作 Key + `metadata.resourceVersion` 供 manager 记录 resume 点 → 翻译 EventType → 推 channel；`defer close(out)`；select ctx.Done。无 fire-and-forget（owner=caller ctx）。
- 事件 wire 结构：`struct{ Type controller.EventType; Object json.RawMessage }`；Key 提取用最小结构 `struct{ Metadata struct{ Name, ResourceVersion string } }`。
- resourceVersion 暴露：Event 需带 RV 供 manager resume——在 `controller.Event` 加 `ResourceVersion string` 字段（回到 Task 1 补；或 watch 包内单独传递）。**实现选定**：Task 1 的 `Event` 加 `ResourceVersion string` 字段，manager feeder 记录最后一个非空 RV 作下次 resume 点。

- [ ] **Step 3: 写测试**

要点：`httptest.NewServer` 返回 chunked newline JSON 流（flush 多行）；断言收到的 Event 序列、Key、RV；ctx 取消后 channel 关闭。

- [ ] **Step 4: 验证**
- `go test -race ./internal/node/client/ -run TestWatch`
- `gofmt -l`

---

## Task 6: 确定性身份派生（纯函数）

**Files:**
- Create: `internal/node/identity/identity.go`
- Test: `internal/node/identity/identity_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 把资源的稳定身份（network name、VM uid）确定性派生成执行面需要的底层内核身份：nftables 表名/链名/优先级、TAP 名、TAP owner UID/GID、VNetHeader 模式。纯函数、可复现、无 etcd / 无副作用。这是决策 A 的落点：API Spec 不含内核身份，控制器据此算出后填进 netpool 定义。

Acceptance evidence:
- `go test ./internal/node/identity/ -run TestIdentity -v` 通过
- 覆盖：同 network name 永远派生同一组 firewall 身份（确定性）；不同 name 派生不同表/链名；TAP 名从 VM uid + 索引确定性派生且长度 ≤ Linux IFNAMSIZ-1（15 字节）；owner UID/GID 用注入的 caller 值（不硬编码 root）

- [ ] **Step 2: 写实现**

要点：
- `NetworkIdentity struct{ FirewallTable firewall.TableName; MasqueradeChain, ForwardChain firewall.ChainName; RuleOwner firewall.RuleOwner; MasqueradePriority, ForwardPriority firewall.Priority }`。
- `DeriveNetworkIdentity(networkName string) NetworkIdentity`：表名固定 `govirta`（项目自有单表），链名 `gv-masq-{name}` / `gv-fwd-{name}`，owner `govirta/{name}`，优先级用固定常量。确定性。
- `NICIdentity struct{ TapName link.Name; AntiSpoofTable firewall.TableName; AntiSpoofChain firewall.ChainName; AntiSpoofPriority firewall.Priority; VNetHeader link.VNetHeaderMode }`。
- `DeriveNICIdentity(vmUID string, nicIndex int) NICIdentity`：TAP 名 `gv{hash(vmUID)[:8]}.{nicIndex}` 确保 ≤15 字节（Linux IFNAMSIZ）；反欺骗链 `gv-as-{tap}`；VNetHeader 默认 enabled（virtio 性能）。
- TAP owner UID/GID 不在此派生——由 cmd 注入（运行 QEMU 的用户），控制器从配置取，纯函数只算名字/链/优先级。
- 包只 import `pkg/hostnet/{link,firewall}` 的类型 + stdlib（`crypto/sha256`/`hash/fnv` 做稳定短哈希）。不 import etcd/apis。

- [ ] **Step 3: 写测试**

要点：表驱动断言确定性（同输入同输出）、唯一性（不同 name 不同链）、TAP 名长度边界（长 uid 也 ≤15 字节）。

- [ ] **Step 4: 验证**
- `go test ./internal/node/identity/`
- `gofmt -l`

---

## Task 7: StoragePool 控制器

**Files:**
- Create: `internal/node/controllers/storagepool.go`
- Test: `internal/node/controllers/storagepool_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: StoragePool 控制器 watch StoragePool 对象，把 `StoragePoolSpec`（backend/type/storageRoot/capacity）映射成 `pool.Pool` 调 `pool.Service.RegisterPool`，注册成功后 live 读 `GetPoolUsage` 上报 `StoragePoolStatus{Phase:ready, AllocatedBytes}`。

Acceptance evidence:
- `go test ./internal/node/controllers/ -run TestStoragePool -v` 通过（fake pool.Service + fake master client）
- 覆盖：合法 StoragePool ADDED → RegisterPool 调用 + status patched ready；RegisterPool 失败 → status failed + requeue；ctx 取消传播

- [ ] **Step 2: 写实现**

要点：
- `StoragePoolController struct{ pools PoolRegistrar; client StatusReporter }`，`Kind() string { return "StoragePool" }`。
- 定义窄接口（积木式 + 可测）：`PoolRegistrar interface{ RegisterPool(*pool.Pool) error; GetPoolUsage(ctx, name) (pool.Usage, error) }`（`*pool.Service` 满足）；`StatusReporter interface{ PatchStatus(ctx, kind, name string, status []byte) ([]byte, error) }`（client 满足）。
- `Reconcile`：解码 StoragePool 对象 → 构造 `pool.Pool`（backend/type/root/capacity 映射，强类型枚举转换）→ `RegisterPool`（已注册视为幂等成功）→ `GetPoolUsage` live 读 → marshal `StoragePoolStatus{Phase:ready, AllocatedBytes:usage.AllocatedBytes}` → `PatchStatus`。失败 patch `Phase:failed,Message` + 返回 requeue。
- DELETED 事件第一刀 no-op（不做删除，留后续切片），记录日志。

- [ ] **Step 3: 写测试**

要点：fake PoolRegistrar 记录 RegisterPool 调用 + 可注入错误；fake StatusReporter 捕获 patch 的 status JSON；断言 ready/failed 路径 + requeue。

- [ ] **Step 4: 验证**
- `go test ./internal/node/controllers/ -run TestStoragePool`
- `gofmt -l`

---

## Task 8: Image 控制器

**Files:**
- Create: `internal/node/controllers/image.go`
- Test: `internal/node/controllers/image_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: Image 控制器 watch Image 对象，从 `ImageSpec.Source`（`file://` 本地路径或 `http(s)://` URL）把字节拉进本地 file pool（`ImageService.PutImage` 写 + 复制字节），ready 后 live 读大小上报 `ImageStatus{Phase:ready, LocalSizeBytes}`。证明 etcd-only 下 blob 带外分发。

Acceptance evidence:
- `go test ./internal/node/controllers/ -run TestImage -v` 通过（fake ImageService + 本地临时文件源）
- 覆盖：`file://` 源 → PutImage writer 收到字节 + status ready + LocalSizeBytes>0；不支持的源 scheme → status failed（无 requeue，配置错误重试无意义）；PutImage 失败 → failed + requeue；ctx 取消传播

- [ ] **Step 2: 写实现**

要点：
- `ImageController struct{ images ImagePutter; client StatusReporter; httpc *http.Client }`，`Kind() { return "Image" }`。
- `ImagePutter interface{ PutImage(ctx, storage.PutImageRequest) (image.ImageWriter, error) }`（`*ImageService` 满足）。
- `Reconcile`：解码 Image → 按 `Source.Type` 打开 reader（`ImageSourceFile`→`os.Open`（校验路径在允许根内）；`ImageSourceHTTP`→`http.Get` with ctx）→ `PutImage(PutImageRequest{PoolName:spec.FilePoolRef, ImageID:meta.Name, Format:spec.Format, DeclaredSizeBytes:spec.DeclaredSizeBytes})` 拿 writer → `io.Copy(writer, reader)` → `writer.Close()`（commit pending→ready）→ patch `ImageStatus{Phase:ready, LocalSizeBytes:copied}`。
- 不支持 scheme / 解析失败 → patch failed，**不 requeue**（配置错误）。传输失败 → failed + requeue。
- 源字节格式权威 = `Image.Spec.Format`（决策已定），透传给 PutImage。

- [ ] **Step 3: 写测试**

要点：fake ImagePutter 返回一个 buffer-backed ImageWriter 记录写入字节数；用 `t.TempDir()` 造 `file://` 源文件；断言 ready+size、bad-scheme failed-no-requeue。

- [ ] **Step 4: 验证**
- `go test ./internal/node/controllers/ -run TestImage`
- `gofmt -l`

---

## Task 9: Volume 控制器

**Files:**
- Create: `internal/node/controllers/volume.go`
- Test: `internal/node/controllers/volume_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: Volume 控制器 watch Volume 对象，gate「被引用 block StoragePool + image file pool + Image 都 live Ready」后，用 `ImageService.GetImage` 拿镜像字节流喂 `VolumeService.CreateRootVolumeFromReader`（format 权威 = 被引用 Image 的 status/spec format），派生独立 qcow2 root 卷，上报 `VolumeStatus{Phase:ready, VolumePath}`。

Acceptance evidence:
- `go test ./internal/node/controllers/ -run TestVolume -v` 通过（fake VolumeService + fake ImageService + fake master client）
- 覆盖：依赖全 Ready → CreateRootVolumeFromReader 调用 + status ready + VolumePath；被引用 Image 未 Ready → requeue（不调 create）；被引用 pool 未 Ready → requeue；create 失败 → failed + requeue

- [ ] **Step 2: 写实现**

要点：
- `VolumeController struct{ volumes RootVolumeCreator; images ImageGetter; client DependencyReader }`，`Kind() { return "Volume" }`。
- `DependencyReader interface{ Get(ctx, kind, name) ([]byte, error); PatchStatus(...) }`（client 满足）——gate 用 `Get` 读被引用对象 status。
- gate 逻辑：`Get(ctx, "StoragePool", spec.PoolRef)` 解 Phase；`Get("StoragePool", spec.ImageFilePoolRef)`；`Get("Image", spec.ImageRef)` —— 任一非 ready → 返回 `requeue=true, nil`（不报 failed，是等待非错误）。
- 推进：`GetImage(GetImageRequest{PoolName:spec.ImageFilePoolRef, ImageID:spec.ImageRef})` 拿 reader → `CreateRootVolumeFromReader(CreateRootVolumeFromReaderRequest{VMID:spec.VMRef,VMName:spec.VMName,PoolName:spec.PoolRef,Name:meta.Name,DiskIndex:spec.DiskIndex,CapacityBytes:spec.CapacityBytes,Reader:reader,Format:imageFormat})` → patch `VolumeStatus{Phase:ready, VolumePath:vol.Path}`。
- imageFormat 来源：从 gate 时读到的 Image 对象 `spec.format` 解出（权威 = Image.Spec.Format）。
- DELETED no-op 第一刀。

- [ ] **Step 3: 写测试**

要点：fake DependencyReader 可配置每个 ref 的 Phase（ready/pending）；断言未 ready→requeue 且 create 未被调用；全 ready→create 调用参数正确 + status ready。

- [ ] **Step 4: 验证**
- `go test ./internal/node/controllers/ -run TestVolume`
- `gofmt -l`

---

## Task 10: Network 控制器

**Files:**
- Create: `internal/node/controllers/network.go`
- Test: `internal/node/controllers/network_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: Network 控制器 watch Network 对象，用 `identity.DeriveNetworkIdentity` 派生底层 firewall 身份，把 `NetworkSpec`（语义意图）+ 派生身份组装成 `netpool.NetworkDefinition`，调 `NetworkService.RegisterNetwork` + `EnsureNetwork`，live 读 `GetNetworkStatus` 上报 `NetworkStatus{Phase}`。

Acceptance evidence:
- `go test ./internal/node/controllers/ -run TestNetwork -v` 通过（fake NetworkService）
- 覆盖：合法 Network ADDED → Register+Ensure 调用 + 派生身份正确填入 def + status ready；EnsureNetwork 失败 → failed + requeue；同 network 重复 reconcile 幂等

- [ ] **Step 2: 写实现**

要点：
- `NetworkController struct{ networks NetworkEnsurer; client StatusReporter; ouwnerUID link.UID; ... }`，`Kind() { return "Network" }`。
- `NetworkEnsurer interface{ RegisterNetwork(netpool.NetworkDefinition) error; EnsureNetwork(ctx, netpool.NetworkName) (netpool.NetworkStatus, error); GetNetworkStatus(ctx, netpool.NetworkName) (netpool.NetworkStatus, error) }`。
- `Reconcile`：解码 Network → 解析 Subnet/GatewayCIDR（`netip.ParsePrefix`）+ DHCP 池/DNS/lease → `id := identity.DeriveNetworkIdentity(meta.Name)` → 组装 `netpool.NetworkDefinition{Name, BridgeName, Subnet, GatewayCIDR, Pool, EgressIface, DHCPServerID, Router, DNS, LeaseDuration, FirewallTable:id.FirewallTable, MasqueradeChain:id.MasqueradeChain, ForwardChain:id.ForwardChain, RuleOwner:id.RuleOwner, MasqueradePriority:id.MasqueradePriority, ForwardPriority:id.ForwardPriority}` → `RegisterNetwork`（幂等）→ `EnsureNetwork` → live status → patch。
- BridgeMAC/BridgeMTU 等：从 spec 或确定性默认（MTU 1500 是合理常量非业务旋钮）；BridgeName 从 spec.BridgeName。
- 失败 → failed + requeue。

- [ ] **Step 3: 写测试**

要点：fake NetworkEnsurer 捕获传入的 NetworkDefinition；断言派生的 firewall 身份字段非空且确定性、Ensure 被调、status patched。

- [ ] **Step 4: 验证**
- `go test ./internal/node/controllers/ -run TestNetwork`
- `gofmt -l`

---

## Task 11: NIC 控制器

**Files:**
- Create: `internal/node/controllers/nic.go`
- Test: `internal/node/controllers/nic_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: NIC 控制器 watch NIC 对象，gate「被引用 Network live Ready」后，用 `identity.DeriveNICIdentity` 派生 TAP/反欺骗身份，把 `NICSpec`（含 apiserver 已分配的 `MAC`，原样透传）+ 派生身份组装 `netpool.NICDefinition`，调 `RegisterNIC` + `EnsureNIC`，live 读 `GetNICStatus` 上报 `NICStatus{Phase, TapName}`。

Acceptance evidence:
- `go test ./internal/node/controllers/ -run TestNIC -v` 通过（fake NICService）
- 覆盖：Network Ready → Register+Ensure + MAC 原样透传（不重新生成）+ status ready + TapName；Network 未 Ready → requeue（不调 Ensure）；EnsureNIC 失败 → failed + requeue

- [ ] **Step 2: 写实现**

要点：
- `NICController struct{ nics NICEnsurer; client DependencyReader; ownerUID link.UID; ownerGID link.GID }`，`Kind() { return "NIC" }`。
- `NICEnsurer interface{ RegisterNIC(netpool.NICDefinition) error; EnsureNIC(ctx, netpool.NetworkName, netpool.VMID) (netpool.NICStatus, error); GetNICStatus(ctx, netpool.NetworkName, netpool.VMID) (netpool.NICStatus, error) }`。
- gate：`Get(ctx, "Network", spec.NetworkRef)` → Phase != ready → `requeue=true, nil`。
- 推进：`mac, err := net.ParseMAC(spec.MAC)`（apiserver 已分配，必非空；空则 failed-no-requeue 配置错误）→ `id := identity.DeriveNICIdentity(spec.VMRef, 0)` → `netpool.NICDefinition{NetworkName, VMID, TapName:id.TapName, MAC:mac, IP:netip.MustParseAddr(spec.IP), TapMTU, VNetHeader:id.VNetHeader, OwnerUID:c.ownerUID, OwnerGID:c.ownerGID, Hostname, AntiSpoofTable:id.AntiSpoofTable, AntiSpoofChain:id.AntiSpoofChain, AntiSpoofPriority:id.AntiSpoofPriority}` → `RegisterNIC` → `EnsureNIC(networkName, vmID)` → live status → patch `NICStatus{Phase:ready, TapName:id.TapName}`。
- MAC 铁律：原样透传 spec.MAC，控制器绝不生成（#698 + 决策）。

- [ ] **Step 3: 写测试**

要点：fake NICEnsurer 捕获 NICDefinition；断言 MAC == spec 提供值（透传）、TapName 来自派生、Network 未 ready→requeue 且 Ensure 未调。

- [ ] **Step 4: 验证**
- `go test ./internal/node/controllers/ -run TestNIC`
- `gofmt -l`

---

## Task 12: VM 控制器

**Files:**
- Create: `internal/node/controllers/vm.go`
- Test: `internal/node/controllers/vm_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: VM 控制器 watch VM 对象，gate「被引用所有 Volume + NIC live Ready」后，从依赖对象的 status 取 VolumePath（root 磁盘）+ TapName，组装 `qemu.Builder`（arch/vcpus/memory/disk/tap），调 `vmm.Create` + `vmm.Start` 启真 daemonized QEMU，live 读 `vmm.Status` 上报 `VMStatus{Phase}`（持续 reconcile 回传 live Phase——存活虚拟机循环）。

Acceptance evidence:
- `go test ./internal/node/controllers/ -run TestVM -v` 通过（fake VMM + fake master client）
- 覆盖：依赖全 Ready → Builder 组装（disk path 来自 Volume status、tap 来自 NIC status）+ Create+Start 调用 + status 上报 live Phase；任一 Volume/NIC 未 Ready → requeue（不调 Create）；vmm.Create 失败 → failed + requeue；已 running 的 VM 再 reconcile → 仅 live Status 重报（幂等，不重复 Create）

- [ ] **Step 2: 写实现**

要点：
- `VMController struct{ vmm VMRunner; client DependencyReader; ... qemu 配置注入 }`，`Kind() { return "VM" }`。
- `VMRunner interface{ Create(ctx, vmm.CreateRequest) (vmm.VM, error); Start(ctx, uuid string) (vmm.VM, error); Status(ctx, uuid string) (vmm.VM, error) }`。
- gate：对每个 `spec.VolumeRefs` `Get("Volume", ref)` 取 Phase+VolumePath；每个 `spec.NICRefs` `Get("NIC", ref)` 取 Phase+TapName；任一非 ready → `requeue=true, nil`。
- 幂等检查：先 `vmm.Status(ctx, meta.UID)`——若已存在且 running，直接 live status 重报（不重复 Create+Start）。
- 推进：用 `pkg/virt/qemu` 构造 `*qemu.Builder`（arch from spec.Arch, vcpus, memory, root disk = Volume status.VolumePath, tap = NIC status.TapName）→ `vmm.Create(CreateRequest{UUID:meta.UID, Builder:builder, Spec:vmm.SpecSummary{Arch,VCPUs,MemoryMiB,DiskPaths,TapNames}})` → `vmm.Start(meta.UID)` → `vmm.Status` live → patch `VMStatus{Phase:mapPhase(vm.Phase)}`。
- vmm.Phase → apis VMPhase 映射（defined/starting/running/stopping/stopped/failed 同名映射）。
- 持续上报：VM 对象每次 reconcile（包括 watch MODIFIED 或周期 resync）都 live 读 Status 重报，实现「存活虚拟机循环」。

- [ ] **Step 3: 写测试**

要点：fake VMRunner 记录 Create/Start 调用 + 可配置 Status 返回的 Phase；fake DependencyReader 配置 Volume/NIC 的 ready+path/tap；断言依赖 gate、Builder 组装的 disk/tap 来自依赖 status、幂等（已 running 不重复 Create）、live Phase 上报。

- [ ] **Step 4: 验证**
- `go test ./internal/node/controllers/ -run TestVM`
- `gofmt -l`

---

## Task 13: node agent 组装 + cmd/govirtlet 接线

**Files:**
- Modify: `internal/node/agent.go`（重写：组装 controller-manager 替换 no-op skeleton）
- Test: `internal/node/agent_test.go`
- Modify: `cmd/govirtlet/main.go`（显式 flag 配置）

- [ ] **Step 1: 确认目标与验收标准**

Goal: `Agent` 从 no-op skeleton 重写为组装 controller-manager：构造 master client + watch source + 六个控制器（注入真实执行面 service）+ Manager，`Run(ctx)` 跑 Manager。配置（master URL、node name、runtime root、storage root、MAC owner UID/GID、egress iface 等）由 `cmd/govirtlet/main.go` 显式 flag 注入（显式铁律）。

Acceptance evidence:
- `go test ./internal/node/ -v` 通过（用 fake 执行面 + fake master 注入，不碰真 QEMU/etcd）
- `go build ./cmd/govirtlet`
- `scripts/verify.sh` 全绿
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`（memory 700）

- [ ] **Step 2: 写实现**

要点：
- `Config struct{ MasterURL, NodeName, RuntimeRoot, StorageRoot string; OwnerUID link.UID; OwnerGID link.GID; EgressIface firewall.InterfaceName; ... }`。
- `NewAgent(cfg Config) (*Agent, error)`：构造 `client.New(cfg.MasterURL, ...)` → watch source（注入 nodeName）→ 真实执行面 service（`pool.Service`、`VolumeService`/`ImageService`、`netpool.NewService` + real hostnet managers、`vmm.NewVMMService`）→ 六个控制器（注入对应 service + client）→ `controller.NewManager(source, controllers)`。
- `Run(ctx)`：`manager.Run(ctx)`。
- 第一刀执行面 host 原语：真实 netlink/nftables/CoreDHCP（Linux）——但单测用 fake 注入。Linux-only 构建标签注意（memory 700）：真实 manager 在 linux 文件，darwin build 用 noop（agent 组装层保持可跨平台编译，真实注入在 build-tagged 装配点）。
- `cmd/govirtlet/main.go`：flag 解析 `--master-url`/`--node-name`/`--runtime-root`/`--storage-root`/`--owner-uid`/`--owner-gid`/`--egress-iface`，全部必填无默认，构造 Config → NewAgent → Run。

- [ ] **Step 3: 写测试**

要点：`agent_test.go` 用 fake 执行面 + fake master client 注入（拆 `newAgentWithDeps` 内部构造器），断言 Agent 组装六个控制器、Run 启动 Manager、ctx 取消退出。不碰真 QEMU/etcd/netlink。

- [ ] **Step 4: 验证**
- `scripts/verify.sh`
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`

---

## Task 14: 全量验证 + node 侧集成冒烟

**Files:** 无新代码（验证收尾）

- [ ] **Step 1: 全量自动验证**
- `gofmt -l .`（排除 `.lima`/`.worktrees`/缓存）
- `go build ./...`
- `go test ./...`（fake 路径全绿）
- `go test -race ./internal/node/...`
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`

- [ ] **Step 2: node ↔ master 集成冒烟（macOS master + 真 etcd，无真 QEMU）**

证明 node 控制器能 watch master、reconcile、上报 status（用 fake/noop 执行面，不需真 QEMU——真 QEMU 留给 Plan 4 的 Lima 端到端）：
- 起 etcd 容器 + `go run ./cmd/govirtad`（Plan 2 的 master）
- `go run ./cmd/govirtlet --master-url=... --node-name=node0 ...`（注入 noop/fake 执行面的变体，或 build tag）
- `curl` apply 一个 StoragePool（绑 node0）到 master → 观察 node 日志 watch 到该对象 + reconcile + PATCH status → `curl` get 确认 status 被 node 写回 ready
- 记录命令 + 真实输出到交付物（证据导向 + memory 822：不接受未验证的成功声明）；清理容器/进程（写 `.tmp/`）

- [ ] **Step 3: 证据要求**

实际命令 + 真实输出贴进交付报告；「node watch 到对象 → reconcile → status 写回 master」这条 node 侧脊柱闭环必须有可复现命令证明。真 QEMU 端到端不在 Plan 3 范围（Plan 4 的 Lima 三节点验收做）。

---

## 验证总览

| 层 | 命令 | 依赖 |
| --- | --- | --- |
| 单元（全 fake） | `go test ./internal/node/...` | 无 |
| 并发 | `go test -race ./internal/node/...` | 无 |
| 跨平台 | `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` | 无 |
| 本地 CI | `scripts/verify.sh` | 无 |
| node↔master 冒烟 | `cmd/govirtad` + `cmd/govirtlet` + curl | docker etcd |

## 明确不做（留给 Plan 4 / 后续切片）

- 三节点 Lima 端到端真 QEMU 闭环（Plan 4：`govirtctl apply → 各 Ready → VM Running`）
- `govirtctl` CLI（Plan 4）
- DELETED 事件的真实资源回收（第一刀 DELETED no-op）
- 退避限速、watch cache、Node 注册对象 + Lease 心跳、断线重连的完整恢复（仅最小 resume）
- 多 NIC per VM（第一刀单 NIC，nicIndex 固定 0）

## Self-review

- [x] 每个 task 有 Goal + Acceptance evidence（可复现命令）
- [x] 控制器框架积木式：Controller/EventSource/窄接口边界，fake 可注入
- [x] 自建框架（B 方案），不引入 client-go
- [x] 确定性身份派生（决策 A）：identity 纯函数派生 nftables/TAP 身份，API Spec 不含内核身份
- [x] MAC 铁律：apiserver 分配、控制器原样透传、绝不生成（#698）
- [x] 依赖链 live gating（单一事实源）：gate 用 master client GET 读被引用对象 live status，不缓存
- [x] 观测 live 读：status 上报前从执行面 live 读真实资源态
- [x] resume 复用 Plan 2 已验证契约（resourceVersion → startRevision）
- [x] 显式铁律：所有配置（master URL/node name/root/owner/egress）caller 显式传，无隐藏默认
- [x] ctx 端到端：framework/client/controllers 全链路 ctx 首参
- [x] 错误传播 + requeue 语义；无 `_ = err`
- [x] 第十八章：六控制器各独立文件、框架按 manager/queue/loop/controller 拆，预判均 < 500 软上限
- [x] 跨平台 build（memory 700）：Linux-only host 原语在 build-tagged 装配点，agent 组装层跨平台可编译
- [x] memory 822 教训：Task 14 强制贴真实命令输出证明 node 侧闭环
- [x] 复用既有执行面（storage/network/vmm），零重写
