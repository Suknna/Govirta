# storage pool and block volume design

## 背景

Govirta 当前处于单节点冷操作闭环阶段。现有 `internal/virt/qemuimg`
负责离线 qcow2 操作，`internal/virt/qemu` 负责 typed QEMU argv 构建，二者不能互相依赖。
本次需要新增项目内部存储抽象，让上层只调用 Go API，不关心第一版 local qcow2 或未来
NFS、Ceph RBD 的具体实现。

本设计借鉴 Kubernetes CSI 的分层、能力声明、幂等和 publish 语义，但不实现 CSI
gRPC/protobuf 协议，不作为独立服务运行，也不引入 Kubernetes PV、PVC、StorageClass、
CSIDriver 等 API 对象。Govirta 的存储接口是单体项目内的 `internal/storage` Go 包。

当前已有 `internal/store` 是早期为 SQL/状态存储预留的声明，不作为本次存储抽象基础。
实现阶段应直接移除旧 `internal/store` 包及其测试，不保留兼容层。

## 目标

1. 新增 `internal/storage` 及其子包，提供 pool、volume、block driver 的内部 Go API。
2. 第一版只实现 local qcow2 block volume 后端，并通过现有 `internal/virt/qemuimg` 创建、删除、查询镜像。
3. 引入 `Pool` 概念，第一版由装配代码显式注册 `default` pool；所有资源操作必须显式传入 `poolName`。
4. `Pool` 作为一等资源，持有 pool 配置、内存 VolumeIndex、容量统计和已绑定 pool 配置的 `BlockDriver`。
5. 提供 OpenStack 风格的 `VolumeService`，让 VM 编排层只依赖 `internal/storage` 暴露的块卷能力。
6. `PoolService` 可被单独调用，用于存储池注册、查询、容量和管理；它不是 `VolumeService` 的私有实现细节。
7. 明确 `PublishedVolume` / `VolumeAttachment` 的返回契约，说明上层如何传递给 VM runtime 使用。
8. 第一版元数据仅在内存中存在，不持久化、不扫描磁盘、不自动恢复。
9. 所有 `context.Context` 必须从调度/编排入口向下透传；中间禁止创建新的 root context。
10. 所有错误必须返回或使用 `%w` / `errors.Join` 保留后向上传递，禁止吞错。

## 非目标

- 不实现 CSI 兼容驱动、gRPC、protobuf、sidecar 或插件注册。
- 不引入 Kubernetes PV/PVC/StorageClass/CSIDriver/VolumeAttachment 对象。
- 不实现独立存储服务进程。
- 不持久化 Pool、Volume 元数据。
- 不扩展旧 `internal/store`；旧包在实现阶段删除。
- 不扫描 pool root 自动恢复已有 qcow2 文件。
- 不实现 `ImportVolume`、`RegisterExistingVolume`、image catalog 或远程镜像下载。
- 不实现跨 pool 自动调度。
- 不允许空 `poolName` fallback 到 `default`。
- 不实现 per-pool overcommit ratio；第一版固定常量 `1.5`。
- 不实现 shared read-write、online resize、snapshot/resize 的 local 后端动作。
- 不让 VM 编排层依赖 `internal/virt/qemu`、`internal/virt/qemuimg`、RBD 或 NFS 细节。
- 不实现镜像仓库；后续镜像仓库依托独立 file pool / image pool 设计，与本次 block volume pool 分离。
- 不在本次 volume API 中接收 base image 路径；镜像仓库和镜像派生卷后续单独设计。

## 包结构

`internal/storage` 根包只保留聚合入口，不承载 pool、volume、driver 具体实现：

```text
internal/storage
  ├── service.go              # VolumeService；VM 编排层入口
  ├── pool/
  │   ├── service.go          # PoolService
  │   ├── pool.go             # Pool / PoolConfig / PoolUsage / PoolType
  │   └── errors.go
  ├── volume/
  │   ├── volume.go           # Volume / VolumeSpec / PublishedVolume
  │   ├── attachment.go       # VolumeAttachment
  │   └── errors.go
  ├── block/
  │   └── driver.go           # BlockDriver 接口与后端无关请求结构
  └── local/
      └── driver.go           # local qcow2 BlockDriver 实现
```

测试文件：

```text
internal/storage/pool/service_test.go
internal/storage/service_test.go
internal/storage/local/driver_test.go
```

旧包删除：

```text
internal/store/store.go       # 删除
internal/store/store_test.go  # 删除
```

## 依赖方向

```text
VM 编排层
  └─ internal/storage only
      └─ storage.VolumeService
          └─ pool.PoolService
              └─ pool.Pool
                  └─ block.BlockDriver
                      └─ local.Driver
                          └─ qemuimg

VM Runtime 层
  ├─ internal/storage   # 读取 PublishedVolume / VolumeAttachment 契约
  └─ internal/virt/qemu # 启动 QEMU，转换 VolumeAttachment
```

包依赖规则：

- `internal/storage` 根包定义 `VolumeService`，可依赖 `pool`、`volume`、`block`，但不依赖 `internal/virt/qemu`、`qemuimg`、`qmp`、`network/bridge`。
- `internal/storage/pool` 依赖 `internal/storage/block` 和 `internal/storage/volume`，不依赖 qemu、qemuimg、local 实现。
- `internal/storage/volume` 只定义 volume/attachment 类型和 volume 错误，不依赖 `pool`、`block`、qemu 或 qemuimg。
- `internal/storage/block` 只定义 driver 接口和后端无关请求结构，可依赖 `volume`，不依赖 qemu/qemuimg。
- `internal/storage/local` 依赖 `pool`、`volume`、`block` 的公开类型和 `internal/virt/qemuimg`。
- `internal/virt/qemu` 不依赖 `internal/storage`。
- `internal/virt/qemuimg` 不依赖 `internal/storage`。
- VM 编排层只依赖 `internal/storage`。
- VM runtime 层可以同时依赖 `internal/storage` 和 `internal/virt/qemu`，负责把 `VolumeAttachment` 转换为 QEMU typed config。

## 命名

- `VolumeService`：OpenStack volume 风格的块卷服务，位于根包 `internal/storage`，面向 VM 编排层。
- `PoolService`：可单独调用的存储池管理 API，负责 pool 注册、查询、容量和索引。
- `BlockDriver`：底层块卷后端接口。第一版 local qcow2 实现该接口，未来 RBD block volume 也实现该接口。
- `VolumeAttachment`：storage 发布后的块卷使用描述，不使用 `BlockdevSource` 命名，避免绑定 QEMU `blockdev` 概念。

`Service` 表示 Go 包内应用服务对象，不是独立进程、daemon 或 gRPC service。

## PoolService 与 Pool 模型

PoolService 后续可以被单独调用作为池管理 API，类似虚拟化系统里的 pool 管理概念；本项目仍然永久不引入 libvirt 或 libvirt 派生依赖。

```go
package pool

type BackendType string

const (
    BackendLocalBlock BackendType = "local-block"
    BackendNFSBlock   BackendType = "nfs-block"
    BackendRBDBlock   BackendType = "rbd-block"
)

const DefaultOvercommitRatio = 1.5

type PoolType string

const (
    PoolTypeBlock PoolType = "block"
    PoolTypeFile  PoolType = "file"
)

type PoolConfig struct {
    Name          string
    Type          PoolType
    Backend       BackendType
    StorageRoot   string
    CapacityBytes int64
}

type Pool struct {
    Config PoolConfig
    Driver block.BlockDriver

    volumes map[string]volume.Volume
}
```

规则：

- 本次只实现 `PoolTypeBlock`。
- `PoolConfig.Type` 显式表明该 pool 的类型，例如 `block` 或未来 `file`。
- 后续镜像仓库依托 `PoolTypeFile` 或独立 image/file pool 设计；不混入本次 block volume pool。
- `PoolConfig.Name` 必填。
- `PoolConfig.Type` 必填，第一版必须为 `block`。
- `PoolConfig.Backend` 必填。
- local block pool 的 `StorageRoot` 必填。
- `CapacityBytes > 0`。
- `DefaultOvercommitRatio` 固定为 `1.5`。
- 第一版不支持 per-pool ratio。
- VolumeIndex 只属于 `Pool`，不属于 `PoolService` 或 driver。
- `PoolService` 不自动创建 `default` pool。
- 即使使用名为 `default` 的 pool，也必须由装配代码显式注册，并由调用方显式传入 `poolName: "default"`。

`PoolService` API：

```go
type Service struct {
    pools map[string]*Pool
}

func NewService() *Service
func (s *Service) RegisterPool(pool *Pool) error
func (s *Service) GetPool(name string) (*Pool, error)
func (s *Service) GetPoolUsage(ctx context.Context, poolName string) (Usage, error)
func (s *Service) ListPools(ctx context.Context) ([]Pool, error)
```

`RegisterPool` 不隐式覆盖同名 pool。重复注册同名 pool 返回 `ErrPoolAlreadyExists`。

## 容量模型

Pool 同时暴露声明分配容量和真实占用容量：

```go
type Usage struct {
    PoolName               string
    Type                   PoolType
    Backend                BackendType
    CapacityBytes          int64
    OvercommitRatio        float64
    AllocationLimitBytes   int64
    AllocatedBytes         int64
    ActualUsedBytes        int64
    AvailableForAllocation int64
}
```

语义：

- `Type`：pool 类型，第一版为 `block`。
- `CapacityBytes`：pool 声明基准容量。
- `OvercommitRatio`：第一版固定 `1.5`。
- `AllocationLimitBytes`：`CapacityBytes * 1.5`。
- `AllocatedBytes`：pool 内所有 `Volume.CapacityBytes` 之和。
- `ActualUsedBytes`：local 后端统计出来的真实文件占用。
- `AvailableForAllocation`：`AllocationLimitBytes - AllocatedBytes`。

准入只看声明分配容量：

```text
AllocatedBytes + RequestedBytes <= AllocationLimitBytes
```

`ActualUsedBytes` 只用于观测，不参与第一版创建准入。

## BlockDriver 接口

`BlockDriver` 是底层块存储后端接口，不叫 `VolumeDriver`。Pool 层调用 driver 时使用明确的请求结构。由于 local 路径包含 VM 语义，`CreateRequest` 必须携带 `VMID`、`VMName` 和显式 `DiskIndex`；这些字段来自 `VolumeService.CreateVolume` 的请求，不由 storage 内部推断。

```go
package block

type Driver interface {
    DriverInfo(ctx context.Context) (DriverInfo, error)

    Create(ctx context.Context, req CreateRequest) (volume.Volume, error)
    Delete(ctx context.Context, vol volume.Volume) error
    GetActualUsedBytes(ctx context.Context) (int64, error)

    Publish(ctx context.Context, vol volume.Volume, req PublishRequest) (volume.PublishedVolume, error)
    Unpublish(ctx context.Context, vol volume.Volume, req UnpublishRequest) error

    Snapshot(ctx context.Context, vol volume.Volume, req SnapshotRequest) (volume.Snapshot, error)
    Resize(ctx context.Context, vol volume.Volume, req ResizeRequest) (volume.Volume, error)
}

type CreateRequest struct {
    Name          string
    PoolName      string
    VMID          string
    VMName        string
    VolumeID      volume.ID
    DiskIndex     int
    CapacityBytes int64
    ReadOnly      bool
}

type PublishRequest struct {
    VolumeID volume.ID
    VMID     string
    ReadOnly bool
}

type UnpublishRequest struct {
    VolumeID volume.ID
    VMID     string
}

type SnapshotRequest struct {
    Name string
}

type ResizeRequest struct {
    CapacityBytes int64
}
```

规则：

- `BlockDriver` 实例化时已经绑定具体 pool 配置。
- `BlockDriver` 不维护 VolumeIndex。
- `BlockDriver` 不做 pool 容量准入。
- `BlockDriver` 负责后端动作，例如 local driver 调用 `qemuimg`。
- local 第一版实现 create/delete/actual usage/publish/unpublish。
- local 第一版的 `Snapshot` 和 `Resize` 返回 `ErrUnsupported`。
- driver 不得自行生成或递增 `DiskIndex`。
- driver 不得自行推断 `PoolName`、`VMID` 或 `VMName`。

## VolumeService 与 Volume 模型

`VolumeService` 是 VM 编排层使用的主要入口，位于根包 `internal/storage/service.go`。
它对齐 OpenStack volume 风格：上层创建、发布、取消发布、删除 volume，而不是操作 disk manager。
`internal/storage/volume` 子包只保存 volume 数据类型，避免 `pool` 与 `volume` 出现 Go import cycle。

```go
package volume

type ID string

type State string

const (
    StateAvailable State = "available"
    StatePublished State = "published"
)

type Role string

const (
    RoleRoot Role = "root"
    RoleData Role = "data"
)

type Volume struct {
    ID            ID
    Name          string
    VMID          string
    VMName        string
    PoolName      string
    Role          Role
    DiskIndex     int
    Backend       string
    CapacityBytes int64
    State         State

    Attachment *AttachmentState
    Context    map[string]string
}

type AttachmentState struct {
    VMID       string
    ReadOnly   bool
    Attachment Attachment
}
```

第一版 volume 只表示块卷本身，不携带镜像来源。创建 volume 时只创建空 qcow2。
基于镜像创建 root volume、镜像仓库、镜像到卷的派生流程后续单独设计，并依托
file pool / image pool；本次不在 `VolumeService` 中预留镜像来源字段。

`VolumeService` API：

```go
package storage

type Service struct {
    pools *pool.Service
}

func NewService(pools *pool.Service) *Service
func (s *Service) CreateVolume(ctx context.Context, req CreateRequest) (Volume, error)
func (s *Service) CreateRootVolume(ctx context.Context, req CreateRootRequest) (Volume, error)
func (s *Service) CreateDataVolume(ctx context.Context, req CreateDataRequest) (Volume, error)
func (s *Service) PublishVolume(ctx context.Context, req PublishRequest) (PublishedVolume, error)
func (s *Service) UnpublishVolume(ctx context.Context, req UnpublishRequest) error
func (s *Service) DeleteVolume(ctx context.Context, req DeleteRequest) error
```

请求：

```go
type Spec struct {
    Name          string
    Role          Role
    DiskIndex     int
    CapacityBytes int64
    ReadOnly      bool
}

type CreateRequest struct {
    VMID     string
    VMName   string
    PoolName string
    Spec     Spec
}

type PublishRequest struct {
    VolumeID ID
    VMID     string
    PoolName string
    ReadOnly bool
}
```

规则：

- `PoolName == ""` 返回 `ErrPoolRequired`。
- `VMID == ""` 返回 `ErrInvalidRequest`。
- `VMName == ""` 返回 `ErrInvalidRequest`。
- `DiskIndex < 0` 返回 `ErrInvalidRequest`。
- `CapacityBytes <= 0` 返回 `ErrInvalidRequest`。
- 第一版 root/data volume 都创建为空 qcow2。
- 镜像派生 root volume 不在本次接口范围内。
- `CreateRootVolume` / `CreateDataVolume` 是便利包装，内部调用 `CreateVolume`。
- `VolumeService` 不 import qemu，不启动 VM。

## PublishedVolume 与 VolumeAttachment 契约

```go
type PublishedVolume struct {
    VolumeID   ID
    VMID       string
    PoolName   string
    Attachment Attachment
}

type AttachmentKind string

const (
    AttachmentFile        AttachmentKind = "file"
    AttachmentBlockDevice AttachmentKind = "blockDevice"
    AttachmentDirectory   AttachmentKind = "directory"
    AttachmentNetwork     AttachmentKind = "network"
)

type DiskFormat string

const (
    DiskFormatQCOW2 DiskFormat = "qcow2"
    DiskFormatRaw   DiskFormat = "raw"
)

type Attachment struct {
    Kind       AttachmentKind
    Format     DiskFormat
    Path       string
    ReadOnly   bool
    Attributes map[string]string
}
```

第一版 local qcow2 返回：

```go
Attachment{
    Kind:   AttachmentFile,
    Format: DiskFormatQCOW2,
    Path:   "/var/lib/govirta/storage/pool/default/<vm-id>/<vm-name>-disk-0.qcow2",
}
```

使用规则：

- `PublishedVolume` 只能由 `VolumeService.PublishVolume` 返回。
- VM 编排层不能自己构造 `PublishedVolume`。
- VM 编排层不能解析 `Attributes` 做后端判断。
- VM 编排层只把 `PublishedVolume` 作为“可用于启动 VM 的卷发布结果”传递给 VM runtime。
- VM runtime 负责将 `Attachment` 转为 QEMU 参数。
- 如果 VM runtime 不支持某种 `AttachmentKind`，返回 runtime 层错误。

## local 路径规则

local block volume 路径按照 VM 语义生成：

```text
<StorageRoot>/pool/<PoolName>/<VMID>/<SafeVMName>-disk-<DiskIndex>.qcow2
```

示例：

```text
/var/lib/govirta/storage/pool/default/vm-123/cirros-test-disk-0.qcow2
/var/lib/govirta/storage/pool/default/vm-123/cirros-test-disk-1.qcow2
```

规则：

- `DiskIndex` 必须由调用方显式传入，不由 `VolumeService` 自动递增。
- `VMName` 必须已经是安全文件名；第一版只校验，不做隐式转换。
- `PoolName`、`VMID`、`VMName` 禁止包含路径分隔符或 `..` 注入。
- 最终路径必须位于 `<StorageRoot>/pool/<PoolName>/<VMID>/` 下。
- local driver 只能删除 Govirta 管理路径内的 qcow2。

安全字符规则：

```text
PoolName: [A-Za-z0-9._-]+
VMID:     [A-Za-z0-9._-]+
VMName:   [A-Za-z0-9._-]+
```

不符合规则时返回 `ErrInvalidRequest`。第一版不做 slug 化、大小写转换或自动替换字符，避免隐藏命名决策。

路径检查：

```text
filepath.Clean
filepath.Rel(root, target)
rel 不得以 ".." 开头
target 不得等于 root
```

## local driver 实现策略

配置：

```go
type Config struct {
    PoolName    string
    StorageRoot string
    QEMUImg     qemuimg.Client
}
```

`Create`：

- 调用 `qemu-img create -f qcow2 <path> <size>` 创建空 qcow2。
- 第一版不接收 base image path，不创建 backing file overlay。
- 创建前检查 `ctx.Err()`。
- 创建目录失败必须返回错误。
- qemu-img 失败必须返回带上下文的错误。
- qemu-img 失败后若 cleanup 目录也失败，必须使用 `errors.Join(primaryErr, cleanupErr)`。

`Delete`：

- 从 `Volume.Context["path"]` 读取路径。
- 校验路径位于该 pool 管理目录内。
- 调用 `qemuimg.QCOW2().Remove().Path(path).Do(ctx)`。
- 删除目录。
- `Remove` 或目录删除失败都必须返回错误；如果同时有主错误和 cleanup 错误，使用 `errors.Join`。

`Publish`：

- 从 `Volume.Context["path"]` 获取 qcow2 路径。
- 调用 `qemu-img info -f qcow2 --output=json` 做轻量校验。
- 返回 `AttachmentFile` + `qcow2` + path。

`Unpublish`：

- local 第一版 no-op，但必须检查 `ctx.Err()`。
- 状态切换由 `Pool` 负责。

`GetActualUsedBytes`：

- 遍历 `StorageRoot/pool/<PoolName>`。
- Unix 上优先使用 `syscall.Stat_t.Blocks * 512`。
- 如果不可用或为 0，可 fallback 到 `Size()`。
- pool root 不存在时返回错误，表示装配或环境问题。

## 数据流

### CreateVolume

```text
VM 编排层
  -> VolumeService.CreateVolume(ctx, req)
      -> 校验 VMID / VMName / PoolName / Spec
      -> PoolService.GetPool(req.PoolName)
      -> Pool.CreateVolume(ctx, block.CreateRequest)
          -> 容量准入
          -> Pool.Driver.Create(ctx, block.CreateRequest)
              -> local.Driver
                  -> qemuimg.QCOW2().Create().Do(ctx)
          -> Pool 写入 VolumeIndex
      -> 返回 Volume
```

如果 driver 创建失败，不写入 `VolumeIndex`。如果后续状态写入失败，第一版应尝试回滚已创建 volume；回滚失败必须用 `errors.Join(primaryErr, cleanupErr)` 返回。

### PublishVolume

```text
VM 编排层
  -> VolumeService.PublishVolume(ctx, req)
      -> PoolService.GetPool(req.PoolName)
      -> Pool.PublishVolume(ctx, volumeID, publishReq)
          -> 检查 Volume.State
          -> Pool.Driver.Publish(ctx, volume, publishReq)
          -> 更新 Volume.State = published
          -> 保存 Volume.Attachment
      -> 返回 PublishedVolume
```

### UnpublishVolume

```text
VM 编排层
  -> VolumeService.UnpublishVolume(ctx, req)
      -> PoolService.GetPool(req.PoolName)
      -> Pool.UnpublishVolume(ctx, volumeID, unpublishReq)
          -> 已 available 时幂等成功
          -> Pool.Driver.Unpublish(ctx, volume, req)
          -> 清空 Volume.Attachment
          -> Volume.State = available
```

### DeleteVolume

```text
VM 编排层
  -> VolumeService.DeleteVolume(ctx, req)
      -> PoolService.GetPool(req.PoolName)
      -> Pool.DeleteVolume(ctx, req.VolumeID)
          -> published 返回 ErrVolumeInUse
          -> Pool.Driver.Delete(ctx, volume)
          -> 从 VolumeIndex 删除
```

### GetPoolUsage

```text
PoolService.GetPoolUsage(ctx, poolName)
  -> GetPool(poolName)
  -> Pool 计算 AllocatedBytes
  -> Pool.Driver.GetActualUsedBytes(ctx)
  -> 返回 Usage
```

## 幂等性与冲突

- 同 pool 内 volume name 不允许重复。
- 同名 volume 不存在时创建。
- 同名 volume 已存在且规格完全一致时返回已有 volume，视为幂等成功。
- 同名 volume 已存在但容量或后端参数不同，返回 `ErrVolumeConflict`。
- 重复 publish 同一 volume 到同一 VM 且语义一致，返回已有 `PublishedVolume`。
- publish 到冲突 VM，返回 `ErrVolumeInUse`。
- 第一版不支持多写。
- 第一版不支持共享只读复用。
- 重复 `UnpublishVolume` 已存在但未 published 的 volume，返回成功。
- 删除 published volume，返回 `ErrVolumeInUse`。

## context 传播规则

所有阻塞、I/O、driver 调用、qemu-img 调用都接收并透传同一个调度/编排入口传入的 `ctx` 或其派生 context：

```text
调度 / 编排入口 ctx
  -> VolumeService
      -> PoolService
          -> Pool
              -> BlockDriver
                  -> local.Driver
                      -> qemuimg
```

禁止：

```go
context.Background()
context.TODO()
```

允许在确有需要时从父 ctx 派生：

```go
childCtx, cancel := context.WithTimeout(ctx, timeout)
defer cancel()
```

第一版如果没有明确超时需求，优先直接传入原始 `ctx`。每个入口和外部动作前应检查 `ctx.Err()`；如果已取消，应返回 `ctx.Err()` 并停止后续动作。

## 错误模型

```go
var (
    ErrInvalidRequest = errors.New("invalid storage request")
    ErrUnsupported    = errors.New("storage operation unsupported")

    ErrPoolRequired         = errors.New("pool name is required")
    ErrPoolNotFound         = errors.New("pool not found")
    ErrPoolAlreadyExists    = errors.New("pool already exists")
    ErrPoolCapacityExceeded = errors.New("pool capacity exceeded")

    ErrVolumeNotFound      = errors.New("volume not found")
    ErrVolumeAlreadyExists = errors.New("volume already exists")
    ErrVolumeConflict      = errors.New("volume conflict")
    ErrVolumeInUse         = errors.New("volume in use")
    ErrVolumeNotPublished  = errors.New("volume not published")
)
```

错误治理规则：

- 禁止 `_ = err`。
- 禁止 `_, _ = ...`。
- 禁止覆盖 `err` 后不检查。
- goroutine 内错误不能只 log 不回传。
- cleanup 失败不能静默吞掉。
- 底层错误加上下文时使用 `fmt.Errorf("...: %w", err)`。
- 主错误和 cleanup 错误同时存在时使用 `errors.Join(primaryErr, cleanupErr)`。
- 上层必须能通过 `errors.Is` / `errors.As` 分类错误。

## 内存元数据语义

第一版元数据仅保存在进程内：

- `PoolService` 的 pool 注册表只在内存中。
- `Pool` 的 VolumeIndex 只在内存中。
- 不写 `volume.json`。
- 不写数据库。
- 不扩展旧 `internal/store`。

重启语义：

- 进程重启后，所有 `Pool` 必须由启动装配代码重新显式注册。
- 进程重启后，所有 VolumeIndex 为空。
- 已经存在于磁盘上的 qcow2 文件不会自动重新纳管。
- 第一版不扫描 pool root，不自动重建 volume 元数据。
- 如果未来要重新使用已有文件，需要显式设计 `ImportVolume` 或 `RegisterExistingVolume`；第一版不做。

## 测试策略

`PoolService` 测试：

- 注册 nil pool 返回 `ErrInvalidRequest`。
- 重复注册返回 `ErrPoolAlreadyExists`。
- 空 poolName 返回 `ErrPoolRequired`。
- 未注册 pool 返回 `ErrPoolNotFound`。
- `ListPools` 返回已注册 pool 的副本。
- `GetPoolUsage` 正确计算 capacity、ratio、allocation limit、allocated、actual used 和 available。

容量准入测试：

```text
CapacityBytes = 100
OvercommitRatio = 1.5
AllocationLimit = 150

已有 allocated = 100
请求 50 -> 允许
请求 51 -> ErrPoolCapacityExceeded
```

`VolumeService` 测试：

- `PoolName` 必填。
- `VMID` / `VMName` 必填。
- `DiskIndex >= 0`。
- `CreateRootVolume` / `CreateDataVolume` 内部调用 `CreateVolume`。
- 创建成功后 `Pool.VolumeIndex` 一致。
- `PublishVolume` 返回 `PublishedVolume.Attachment`。
- 重复 publish 同一 volume 幂等成功。
- publish 到冲突 VM 返回 `ErrVolumeInUse`。
- published volume 不允许 delete。
- ctx 取消后不调用 driver。
- 创建失败不写 index。
- 创建后续失败触发 cleanup，cleanup error 通过 `errors.Join` 返回。

local driver 测试：

- 渲染 qemu-img create 空盘。
- 不包含 backing file overlay 或镜像来源测试。
- qemu-img create 失败返回错误。
- ctx canceled 时不调用 runner。
- 路径格式为 `<StorageRoot>/pool/<PoolName>/<VMID>/<SafeVMName>-disk-<DiskIndex>.qcow2`。
- 路径注入被拒绝。
- `Publish` 调用 info 校验并返回 file/qcow2 attachment。
- `Delete` 调用 remove。
- 删除路径不在 pool root 下返回 `ErrInvalidRequest`。
- `Snapshot` 返回 `ErrUnsupported`。
- `Resize` 返回 `ErrUnsupported`。
- `GetActualUsedBytes` 返回非负值或正确处理 root 不存在错误。
- 所有错误路径没有 `_ = err` 式吞错。

旧包删除测试：

- 删除 `internal/store` 后运行 `go test ./...`，确认无生产包依赖旧 store。

## 验证

实现后最小验证：

```bash
gofmt -l .
go test ./...
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl
```

或运行本地 CI 等价入口：

```bash
scripts/verify.sh
```

如果实现中引入并发锁或 goroutine，需要补充：

```bash
go test -race ./...
```

## 验收链路关系

本设计本身不直接启动 VM，但支撑后续验收链路：

```text
empty local qcow2 block volume
  -> VolumeService.CreateRootVolume(poolName="default")
  -> VolumeService.PublishVolume
  -> VM Runtime 获取 PublishedVolume.Attachment
  -> Runtime 转成 QEMU blockdev/device
  -> QEMU 启动 CirrOS
```

最终仍按项目验收方式在 `192.168.139.206` 上启动 VM，注入 TAP，guest 识别链路通过。

## 官方文档引用

- CSI 官方规范仓库：https://github.com/container-storage-interface/spec
- CSI 规范文档：https://github.com/container-storage-interface/spec/blob/master/spec.md
- CSI protobuf 定义：https://github.com/container-storage-interface/spec/blob/master/csi.proto
- Kubernetes CSI driver 开发文档：https://kubernetes-csi.github.io/docs/developing.html
- Kubernetes Volumes 文档：https://kubernetes.io/docs/concepts/storage/volumes/
- Kubernetes CSIDriver API：
  https://kubernetes.io/docs/reference/kubernetes-api/config-and-storage-resources/csi-driver-v1/
- OpenStack Cinder block storage service 概念文档：
  https://docs.openstack.org/cinder/latest/

本设计仅借鉴上述文档中的分层、能力声明、幂等、publish 和 OpenStack volume 命名语义，不实现 CSI 协议、Kubernetes API 对象或 OpenStack API。
