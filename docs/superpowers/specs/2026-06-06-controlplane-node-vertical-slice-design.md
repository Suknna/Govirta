# 控制面 / 节点垂直切片设计（VM create-only walking skeleton）

**日期**: 2026-06-06
**状态**: 设计已确认，待写实现计划
**前置**: `internal/storage`、`internal/network`、`internal/vmm`、`pkg/hostnet/*` 执行面均已真实落地并有 Lima 验收覆盖；分布式骨架（apiserver/scheduler/controlplane/node agent）当前全为 no-op 占位。

## 1. 目标与范围

打通一条**最薄但结构正确**的端到端链路（walking skeleton），点亮 Govirta 的 master/node 分布式脊柱，并复用已做扎实的节点本地执行面。

第一刀终点：`govirtctl` 显式提交 StoragePool / Image / Volume / Network / NIC / VM 六个资源对象 → master 存 etcd → 节点上的多控制器循环按依赖链各自 reconcile（真跑执行面）→ VM 控制器启动真实 daemonized QEMU → 持续上报 live `Phase` → master 向 etcd reconcile → `govirtctl get vm` 查到 `Running`。

**结构形态（本切片确立的方向）**：
- 两层传输：master↔etcd 走 etcd gRPC Watch；master↔node 走 k8s 式 HTTP watch（node 主动拨号、下行推期望状态）+ 独立上行 HTTP（`PATCH .../status`）。
- 资源模型：六个一等公民对象共享 k8s 式信封 `TypeMeta + ObjectMeta + Spec + Status`，`ResourceVersion = etcd revision`。
- node = controller-manager：自建轻量框架托管六个独立控制器，每个 `watch → workqueue → reconcile → status`，按 operator 形态依赖链推进。
- 全显式提交：每个对象身份与 spec 由控制面/用户显式给出，无级联派生、无自动生成身份。
- 镜像分发遵循 etcd-only：镜像字节（GB 级 blob）绝不进 etcd；etcd 只存 Image 元数据，字节由节点 Image 控制器从外部源拉进本地 file pool（`ImageService.PutImage`）。

## 2. 已锁定的核心决策

| # | 决策点 | 选择 | 理由 |
| --- | --- | --- | --- |
| 1 | 推进风格 | A 垂直切片（walking skeleton） | 最早暴露脊柱设计真实问题，立即复用执行面 |
| 2 | 传输层 | 两层：master↔etcd gRPC Watch；master↔node k8s 式 HTTP watch（下行）+ 独立上行 PATCH status | 贴合真 k8s 架构，上下一致铁律天然落地 |
| 3 | 资源模型 | k8s 信封 `TypeMeta+ObjectMeta+Spec+Status`，`ResourceVersion=etcd revision` | spec/status 分离承载期望/现实双轴；两层 watch 共用版本游标 |
| 4 | 第一刀范围 | create-only；固定 node 身份；NoopScheduler 选 node[0] | 薄但结构正确 |
| 5 | master watch 数据流 | A 直通 etcd（无内部缓存） | 单一事实源在代码里是结构性保证，非约定 |
| 6 | node 内部形态 | controller-manager + 六个一等公民控制器（operator 形态） | 与一等公民判据严丝合缝 |
| 7 | 共享循环框架 | B 自建轻量框架 | 贴合自定义 HTTP watch 契约，最小化外部依赖，积木式可换 |
| 8 | 资源创建模型 | A 全显式提交 | 与全项目「显式优于隐式、身份由调用方提供」铁律一致 |

## 3. 资源契约（六个一等公民，共享信封）

包路径 `pkg/apis/<resource>/v1alpha1/`（放 `pkg/` 以便未来 client 包 import）。

全部共享信封：

```go
type TypeMeta struct {
    APIVersion string // govirta.io/v1alpha1
    Kind       string // StoragePool / Image / Volume / Network / NIC / VM
}

type ObjectMeta struct {
    Name            string
    UID             string // 调用方提供（一等公民判据 + 显式铁律），绝不自动生成
    ResourceVersion string // = etcd revision，watch 增量 + 乐观并发游标
    NodeName        string // 本地资源由 govirtctl 显式提交；VM 由 scheduler 绑定；node watch 按此过滤
    Labels          map[string]string
}
```

| 对象 | Spec（期望，显式） | Status（现实，node 写） | 依赖引用 |
| --- | --- | --- | --- |
| StoragePool | backend kind + 容量参数 | Phase + 实际容量 | — |
| Image | poolRef(file pool) + 外部源(本地文件路径 / HTTP URL，显式) + 显式 format + caller-provided ID | Phase(pending/ready/deleting) + 本地字节大小 | → StoragePool(file) |
| Volume | poolRef(block pool) + imageRef + imageFilePoolRef（root 卷）+ 磁盘规格(role/diskIndex) | Phase + 卷路径 | → StoragePool, Image |
| Network | bridge/subnet/forwarding 声明 | Phase + 观测网络态 | — |
| NIC | networkRef + tap 规格（MAC 由平台分配，见下） | Phase + 观测 NIC 态 | → Network |
| VM | volumeRefs + nicRefs + CPU/内存/argv 基础参数 | Phase（live 派生）+ 运行信息 | → Volume + NIC |

除 MAC 外，所有 behavior-affecting 字段（pool / 磁盘号 / owner 引用 / UID / 名称）由 `govirtctl` 显式提供，apiserver 不补默认。镜像源字节格式（format）的权威是 `Image.Spec.Format`，Volume 不另带源 format 字段。`Phase` 等状态/生命周期值用专属 Go 类型 + 命名常量（不用裸 string）。

**MAC 分配（平台职责，非用户注入）**：私有云平台的 NIC MAC 由平台内部计算，不由用户提供。NIC 对象创建时 `Spec.MAC` 留空，由 **apiserver 准入期**从配置的平台 MAC 池（locally-administered 单播范围）同步分配一个唯一 MAC，写入 `Spec.MAC` 后再落 etcd——与 k8s apiserver 准入期分配 Service ClusterIP 同构（强一致单点 + 即时不变式，依赖 etcd 事务防冲突）。分配器做成可替换的 `MACAllocator` 接口（积木式），占用状态存 etcd。

- 此举不违反 #698：#698 要求 MAC 由**控制面**提供、编排/hostnet 层绝不自行生成；apiserver 正是控制面组件，分配后 MAC 成为 `NIC.Spec.MAC` 的显式值，原样穿到 TAP + DHCP binding + 反欺骗，编排层「显式 MAC 贯穿、自己绝不生成」的铁律继续成立。
- **阶段性约束（已存 memory 820 / note #25）**：本切片 apiserver 是单点，可作为 MAC 分配唯一源；后期 apiserver 演进为多副本时，MAC 分配必须归入控制平面的专属控制器组件（kubevirt `kubemacpool` 形态），不再留在 apiserver 准入期。`MACAllocator` 接口边界为这次迁移预留。
- IP 分配是同构问题，本切片不展开（NIC 仍走现有 hostnet 静态 DHCP binding，IP 由 Network spec 的池显式约束）；未来与 MAC 一并归入控制平面分配器组件。

**镜像分发（Image 第六对象，etcd-only 下 blob 走带外）**：镜像字节动辄 GB，绝不进 etcd（#652）——etcd 只存 Image 元数据（caller-provided ID / 外部源 / pending|ready|deleting status）。Image 是 AGENTS.md 既有一等公民，本切片提升为第六个 watched API 对象，节点复用现有 `ImageService`（byte-stream 本地 file pool 存储）。

- `Image.Spec` 携带一个**显式外部源**：第一刀只支持本地文件路径（`file://` 验收用）与 `http(s)://` URL 两种显式源，不做 registry / 容器镜像。
- 节点 Image 控制器 watch 到 Image 对象后，从外部源把字节拉进自己的 file pool（复用 `ImageService.PutImage`），ready 后上报 `status=ready`——证明 etcd-only 下「元数据进 etcd、blob 带外分发」的关键架构约束在代码里成立（贴 kubevirt CDI / OpenStack Glance 形态）。
- Volume 控制器的 image 引用改为引用 Image 对象 ID；Volume 派生 root 卷前 gate「被引用 Image 对象 `status=ready`」。
- **演进方向（已存 memory 821）**：镜像分发与网络一样，后期作为控制平面控制器组件下的子组件统一治理（registry 拉取、缓存、GC、跨节点分发）；本切片的节点本地拉取是最薄起点。

## 4. 包布局

```
pkg/apis/{storagepool,image,volume,network,nic,vm}/v1alpha1/types.go   # 共享信封 + 各资源类型 + Phase 常量

internal/controlplane/
  apiserver/
    store/                       # Store 接口（积木式边界）
      etcd/                      # etcd 实现
      fake/                      # 单测用内存实现
    handler_apply.go             # govirtctl 写入任意资源 → Store.Put(etcd)
    handler_watch.go             # node 下行 watch：etcd Watch(过滤 nodeName+kind) → HTTP chunked JSON 事件流
    handler_status.go            # node 上行 PATCH .../status → reconcile 写回 etcd
  scheduler/                     # 现 NoopScheduler：VM 绑 NodeName=node[0]
  reconcile.go                   # master 侧：status 上报 → Store.Put(etcd status)

internal/node/
  controller/                    # 自建轻量框架
    manager.go                   # controller-manager：托管 N 个控制器，统一启动/停止
    queue.go                     # 去重 workqueue
    loop.go                      # 通用循环：watch 事件 → 入队 → reconcile → 失败重入队
  controllers/
    storagepool_controller.go    # 复用 internal/storage pool.Service
    image_controller.go          # 复用 ImageService（从外部源拉字节进 file pool）
    volume_controller.go         # 复用 VolumeService（镜像派生独立 root 卷）
    network_controller.go        # 复用 NetworkService
    nic_controller.go            # 复用 NICService（显式 MAC 贯穿）
    vm_controller.go             # 复用 VMMService（真 daemonized QEMU + live Phase 上报）
  watchclient.go                 # node 主动拨号 master 的 HTTP watch client
```

## 5. 端到端数据流（第一刀）

```
govirtctl apply pool/image/volume/network/nic/vm  → apiserver handler_apply → Store.Put(etcd)   [期望状态]
  · pool/image/volume/network/nic 由 govirtctl 显式带 NodeName 提交（节点本地资源，显式铁律）
  · VM 提交时不带 NodeName，由 scheduler 放置
scheduler 给 VM 绑 NodeName=node[0]（NoopScheduler）

node controller-manager 启动 → watchclient 拨号 master
  → 每个控制器 GET ../watch?nodeName=N&kind=<K> (HTTP 长连接, chunked JSON 事件流)
  → master handler_watch: etcd Watch(过滤 nodeName+kind) → 翻译 HTTP 事件下推    [第2层 watch，直通 etcd]

各控制器收到自己 kind 的 ADDED 事件，按依赖链 gate 推进:
  StoragePool 控制器          → pool.Service 注册                → PATCH status Ready
  Image 控制器(等 poolRef Ready) → 从外部源拉字节 → ImageService.PutImage(file pool) → PATCH status Ready(pending→ready)
  Volume 控制器(等 poolRef+imageRef Ready) → 镜像派生独立 qcow2 root 卷 → PATCH status Ready
  Network 控制器              → EnsureNetwork                    → PATCH status Ready
  NIC 控制器(等 networkRef Ready) → EnsureNIC(平台已分配 MAC 原样贯穿) → PATCH status Ready
  VM 控制器(等 volumeRefs+nicRefs Ready) → builder→Create→Start 真 QEMU
    → 读 live Phase → PATCH vm/status {Phase: Running}   [持续上报，如 kubelet]

每次 PATCH status → master reconcile → Store.Put(etcd status)  [上下一致: 向 node 现实对齐]
govirtctl get vm → 读 etcd → Running
```

依赖未 Ready 时控制器重入队等待（最薄重试，无退避优化）。

## 6. 铁律落地

- **单一事实源 / 上下一致**：master 直通 etcd 无内部缓存；etcd 中对象 = 期望轴权威，node 上报 live status = 运行现实权威，master 只做 reconcile 写回，绝不反向。
- **积木式可替换**：`Store` 接口让 etcd↔fake 可换（单测不需要真 etcd）；controller 框架自建、各层是自有可替换边界；六个控制器全部复用现有执行面 service，零重写。
- **显式优于隐式**：全显式提交，无级联派生；身份（UID/名称/引用）由调用方显式提供，apiserver 不补默认。唯一例外是 NIC 的 MAC——私有云平台的 MAC 必须平台内部分配（非用户注入），由 apiserver 准入期通过可替换 `MACAllocator` 从配置的 locally-administered MAC 池同步分配（与 k8s apiserver 准入期分配 Service ClusterIP 同构），分配后即 NIC.Spec.MAC 的显式值，原样穿到 TAP+DHCP+反欺骗（#698 编排层仍不生成 MAC）。
- **ctx 端到端**：HTTP 请求 ctx → controller loop → storage/network/vmm service 全链路透传，无 orphan `context.Background()`。
- **持续上报**：VM 控制器周期 reconcile 回传 live `Phase`（「存活虚拟机循环」）。
- **etcd-only**（#652）：master 持久化只用 etcd，不引入任何其他元数据库。
- **错误传播**：所有错误向上传播；清理/回滚多错误用 `errors.Join`。
- **强类型状态**：`Phase` / backend kind / role 等用专属类型 + 命名常量。

## 7. 验证策略

- **单测**：`Store` 用 fake 内存实现；apiserver handler、controller 框架（fake watch 源 + fake 执行面）、各控制器依赖 gate 逻辑，均不碰真 etcd / QEMU。覆盖 ctx 取消行为。
- **acceptance（三节点真分布式拓扑）**：
  - **etcd** → OrbStack Docker 容器启动，master 经 localhost gRPC 连接。
  - **控制面 govirtad** → macOS 本地直接运行，连本机 etcd，监听 node 拨入的 HTTP watch/status。
  - **计算节点 govirtlet** → Lima VM（带 KVM）内运行，真起 QEMU。
  - 跨边界连通性：传输模型为 node 主动拨号 master（node-initiated），只需 Lima guest → macOS host 单向可达（经 host gateway），无需 master 反向入站 Lima，也无需 node 直连 etcd（仅 master 碰 etcd）。
  - 跑完整六对象 `apply → 各 Ready → VM Running` 闭环，作为脊柱真实内核网关。
- etcd 客户端依赖版本：进入 plan/实现阶段用子代理联网 + ctx7 核实最新稳定版再锁定（第十一/十七章），本设计不锁版本。

## 8. 明确不做（留给后续切片，已存 memory 819）

- VM stop / delete 生命周期（期望状态下发 + status 回传）
- 控制器退避 / 限速优化
- Node 注册对象 + Lease 心跳（第一刀用固定 node 身份）
- 断线重连恢复 / reconcile 全量补偿
- master informer / watch cache（规模化优化）
- scheduler 真实放置策略（第一刀 NoopScheduler 选 node[0]）
- client-go 式客户端包
- master↔etcd 之外的任何持久化

## 9. 后续切片预告（顺序非承诺）

1. 本切片：create-only 全链路打通
2. VM stop/delete 生命周期 + 控制器退避
3. Node 注册对象 + Lease 心跳 + scheduler 真实策略
4. 断线重连恢复 + reconcile 补偿
5. client-go 式客户端包 + master watch cache（规模化）
