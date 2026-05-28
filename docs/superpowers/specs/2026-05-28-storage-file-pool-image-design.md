# storage file pool and image-derived root volume design

## 背景

Govirta 已有 `internal/storage` 的 block pool / block volume 抽象：
`VolumeService` 面向 VM 编排层，`pool.Service` 负责显式 pool 注册、容量准入和内存索引，
`block.Driver` 负责后端块卷生命周期，`local` 后端基于 local qcow2 与 `qemu-img`。

当前阶段需要进入 file pool + 镜像派生 root volume：控制面能够把一个显式注册的镜像文件放入 file pool，
再从 file pool 读取镜像字节，完整拷贝到 block pool 中形成 VM root volume。该 root volume 是完整独立副本，
不使用 qcow2 backing file 链接；原始镜像删除不会影响已有 VM。

本设计仍保持 Govirta 单体内部 Go 包形态，不引入 CSI sidecar、gRPC、libvirt、qemu-nbd、
qemu-storage-daemon 或 qemu-io。持久化和 catalog 属于更上层控制面；storage 层只维护进程内索引，
重启后必须由控制面重新注册 pool 与镜像元数据。

## 目标

1. 新增 file pool 能力，让 `pool.Service` 能注册 `PoolTypeFile` 类型的 pool。
2. 新增 `internal/storage/image` 子包，定义与 `block.Driver` 并列的 `image.Driver`。
3. 新增 `ImageService`，提供 image 字节读写与删除入口，但不承担控制面 catalog 职责。
4. 新增强类型镜像格式枚举，支持显式 `qcow2` 与 `raw` 输入，禁止裸 `string` 格式字段。
5. 新增 local file image driver，第一版在本地文件系统中保存 qcow2/raw 镜像字节。
6. 扩展 `block.Driver` 与 `VolumeService`，允许从 `io.Reader` 创建完整拷贝的 root volume。
7. 明确 file pool 与 block pool 互相独立，跨 pool 编排由控制面完成。
8. 明确所有错误必须返回上层，禁止 `_ = err` 或静默丢弃 cleanup error。
9. 明确 `context.Context` 必须从控制面/调度入口一路向下透传，禁止中途断链。

## 非目标

- 不实现 qcow2 backing file overlay；所有 VM root 盘都是完整拷贝。
- 不实现 image catalog 持久化、数据库 schema、控制面 API 或调度器选择逻辑。
- 不允许 storage 层从调用方接收宿主机镜像路径；镜像如何落盘是 driver 内部实现。
- 不让 `ImageService` 注册镜像元数据到控制面；注册由更上层调度或控制面完成。
- 不让 `ImageService` 依赖 `VolumeService`，也不让 `VolumeService` 依赖 `ImageService`。
- 不实现引用计数或 image in-use 检查；删除原始镜像不影响已完整拷贝出的 root volume。
- 不实现 NFS、Ceph、RBD、file pool 远程下载、镜像校验和签名、镜像格式自动探测。
- 不支持 qcow2/raw 以外格式；未来增加格式必须扩展强类型常量与显式校验。
- 不引入新进程或 shell 命令拼接；需要外部命令时沿用 `qemuimg` runner 边界。
- 不在 storage root、pool、image、block、localfile、local driver 或 qemuimg 调用链中创建新的 root context。

## 包结构

在现有 `internal/storage` 基础上增加纯类型包、image driver 包和 local file 后端：

```text
internal/storage
  ├── service.go                 # VolumeService；新增 CreateRootVolumeFromReader
  ├── image_service.go           # ImageService；独立于 VolumeService
  ├── diskformat/
  │   └── format.go              # 强类型 Format 常量：qcow2/raw
  ├── image/
  │   └── driver.go              # image.Driver / ImageWriter / 请求结构
  ├── block/
  │   └── driver.go              # block.Driver；新增 CreateFromReader
  ├── pool/
  │   ├── pool.go                # Pool / PoolConfig / PoolType；支持 file pool
  │   ├── service.go             # 注册、查询、容量、block/image 生命周期入口
  │   └── errors.go
  ├── local/                     # 已有 local qcow2 block driver；扩展 reader 输入
  └── localfile/                 # 新增 local file image driver
```

依赖方向：

```text
storage root ──> pool ──> block ──> volume
     │            │       └──────> diskformat
     │            └────> image ──> diskformat
     │
     ├────> image
     ├────> block
     ├────> volume
     └────> diskformat

local      ──> block / volume / diskformat / qemuimg
localfile  ──> image / diskformat
```

`image` 与 `block` 互不依赖。`diskformat` 是纯类型包，避免把共享格式枚举放在 root 包后形成子包反向依赖。

## 磁盘格式类型

格式必须是强类型常量，不使用裸 `string`：

```go
package diskformat

// Format 表示 storage 层可处理的磁盘镜像字节格式。
type Format string

const (
    // FormatQCOW2 表示 qcow2 镜像格式。
    FormatQCOW2 Format = "qcow2"
    // FormatRaw 表示 raw 镜像格式。
    FormatRaw Format = "raw"
)

// Valid 报告格式是否为当前 storage 层支持的显式格式。
func (f Format) Valid() bool {
    switch f {
    case FormatQCOW2, FormatRaw:
        return true
    default:
        return false
    }
}
```

所有接收镜像字节格式的请求字段都使用 `diskformat.Format`。调用方必须显式传
`diskformat.FormatQCOW2` 或 `diskformat.FormatRaw`；storage 层不得通过文件扩展名、magic bytes 或 qemu-img 探测隐式推断格式。

## Context 传播

所有 storage 调用必须使用调用方传入的 `ctx`，并从控制面/调度入口一路透传到最底层 I/O 或 runner：

```text
control-plane / scheduler ctx
  -> ImageService / VolumeService
  -> pool.Service
  -> image.Driver / block.Driver
  -> localfile filesystem I/O / local block filesystem I/O
  -> qemuimg builder Do(ctx)
  -> imgexec.Runner.Run(ctx, binary, args)
```

硬约束：

- 禁止在中间层使用 `context.Background()` 或 `context.TODO()` 创建新 root context。
- 禁止为了方便清理、转换或文件复制而丢弃父 ctx。
- 如果需要超时，只能使用 `context.WithTimeout(parentCtx, timeout)` 或等价的父 ctx 派生形式；派生 ctx 必须 `defer cancel()`。
- 派生 ctx 只允许缩短生命周期，不允许绕过父 ctx 的取消。
- `PutImage`、`ImageWriter.Close`、`ImageWriter.Cancel`、`GetImage`、`DeleteImage`、`CreateRootVolumeFromReader`、raw 转 qcow2、resize、cleanup 前都必须在可阻塞边界检查或传递 ctx。
- `qemuimg` 调用必须继续走 `Do(ctx)` / `Runner.Run(ctx, ...)`，不能在 storage driver 内直接 `os/exec` 后丢失 ctx。
- 对 `io.Reader` / `io.Writer` 的标准接口本身无法强制取消时，storage 层必须在开始 copy 前检查 `ctx.Err()`，copy 后再次检查 `ctx.Err()`；若 reader/writer 由上层提供，上层负责让其遵循同一个 ctx 生命周期。

错误语义：

- 父 ctx 取消时，下层应尽快返回 `context.Canceled` 或 `context.DeadlineExceeded`，并保持 `errors.Is` 可识别。
- 如果 ctx 取消后 cleanup 又失败，返回 `errors.Join(ctxErr, cleanupErr)`，不得吞掉任一错误。

## image.Driver

`image.Driver` 是 file pool 的后端接口，与 block pool 的 `block.Driver` 平级：

```go
package image

type Driver interface {
    Info(ctx context.Context) (DriverInfo, error)
    Put(ctx context.Context, req PutRequest) (ImageWriter, error)
    Get(ctx context.Context, req GetRequest) (io.ReadCloser, error)
    Delete(ctx context.Context, req DeleteRequest) error
    GetActualUsedBytes(ctx context.Context) (int64, error)
}

type ImageWriter interface {
    io.Writer
    Close() error
    Cancel() error
}

type PutRequest struct {
    ImageID           string
    Format            diskformat.Format
    DeclaredSizeBytes int64
}

type GetRequest struct {
    ImageID string
}

type DeleteRequest struct {
    ImageID string
}
```

语义：

- `ImageID` 由调用方显式提供；同一 file pool 内重复 ID 返回 `ErrImageExists`，不覆盖、不幂等。
- `DeclaredSizeBytes` 用于 file pool 容量准入；必须为正数。
- `Put` 返回 `ImageWriter`，调用方通过标准 `io.Writer` 写入字节。
- `Close` 表示提交写入；`Cancel` 表示放弃并清理临时数据。
- `Get` 返回 `io.ReadCloser`，调用方必须关闭并处理关闭错误。
- `Delete` 直接删除 image 字节；不检查 block volume 引用。
- `GetActualUsedBytes` 用于观测真实磁盘占用，不参与准入。

所有方法入口先检查 `ctx.Err()`，并把同一个 ctx 继续传给下层可取消操作。所有临时文件清理、close、rename、remove 失败都必须返回或用 `errors.Join` 合并返回。

## ImageService

`ImageService` 是 storage root 包的 file image 入口，但不做 catalog 持久化：

```go
type ImageService struct {
    pools *pool.Service
}

func NewImageService(pools *pool.Service) (*ImageService, error)

func (s *ImageService) PutImage(ctx context.Context, req PutImageRequest) (image.ImageWriter, error)
func (s *ImageService) GetImage(ctx context.Context, req GetImageRequest) (io.ReadCloser, error)
func (s *ImageService) DeleteImage(ctx context.Context, req DeleteImageRequest) error
```

请求结构：

```go
type PutImageRequest struct {
    PoolName          string
    ImageID           string
    Format            diskformat.Format
    DeclaredSizeBytes int64
}

type GetImageRequest struct {
    PoolName string
    ImageID  string
}

type DeleteImageRequest struct {
    PoolName string
    ImageID  string
}
```

`ImageService` 只通过 `pool.Service` 找到显式 file pool 并调用该 pool 的 `image.Driver`。
它不提供 `ListImages` / `GetImageInfo`，因为 catalog 与持久化由上层控制面负责。

## PoolService 扩展

`pool.PoolType` 增加 file pool：

```go
type PoolType string

const (
    PoolTypeBlock PoolType = "block"
    PoolTypeFile  PoolType = "file"
)
```

`RegisterPool` 根据 `PoolConfig.Type` 校验 driver 类型：

- `PoolTypeBlock` 必须绑定 `block.Driver`。
- `PoolTypeFile` 必须绑定 `image.Driver`。
- 空 type、未知 type、driver 类型不匹配都返回错误。

容量准入：

- block pool 沿用 `DefaultOvercommitRatio = 1.5`。
- file pool 使用固定 `DefaultFileOvercommitRatio = 1.0`。
- file pool 的 `AllocatedBytes` 来自已提交 image 的 `DeclaredSizeBytes` 之和。
- pending image 的 `DeclaredSizeBytes` 也必须计入 `AllocatedBytes`，防止并发上传在提交前绕过容量准入。
- `ActualUsedBytes` 来自 `image.Driver.GetActualUsedBytes(ctx)`。

`Pool` 继续持有内存索引。file pool 需要新增 image index，用于 ID 冲突检测、容量释放和重启后由控制面重新注册。
第一版不扫描磁盘恢复 index。

block 生命周期方法只允许 block pool 调用，file pool 调用返回 `volume.ErrUnsupported` 或 pool 层等价错误。
image 生命周期方法只允许 file pool 调用，block pool 调用返回 `volume.ErrUnsupported` 或 pool 层等价错误。

## localfile driver

`internal/storage/localfile` 是第一版 file pool 后端。它只知道自己被绑定到某个 file pool，
不暴露宿主机路径给上层。

建议路径布局：

```text
<StorageRoot>/pool/<PoolName>/images/<ImageID>/<ImageID>.<format>
```

路径安全：

- `PoolName`、`ImageID` 必须通过与 local block driver 一致的安全名称校验。
- 禁止空字符串、`.`、`..`、路径分隔符、控制字符。
- 文件扩展名只由 `diskformat.Format` 决定，不从 caller path 获取。

`Put` 流程：

1. 校验 `ctx.Err()`、`ImageID`、`Format.Valid()`、`DeclaredSizeBytes > 0`。
2. 检查目标 image 是否已存在；存在则返回 `ErrImageExists`。
3. 创建 image 目录和临时文件，例如 `<ImageID>.<format>.tmp`。
4. 返回 `ImageWriter`。
5. `ImageWriter.Close` 先关闭临时文件，再原子 rename 到目标文件；任一失败都返回错误。
6. `ImageWriter.Cancel` 关闭临时文件并删除临时文件；cleanup 错误必须返回。

driver 返回的 writer 只负责后端文件提交。`ImageService` / `pool.Service` 必须再包一层 writer：

- `PutImage` 在 pool lock 下预留 image ID 和声明容量，状态为 pending。
- 包装后的 `Close` 先调用 driver writer 的 `Close`；成功后在 pool lock 下把 index 状态切换为 ready。
- 包装后的 `Close` 如果失败，必须释放 pending 容量并移除 pending index，同时把 driver 错误与释放/清理错误通过 `errors.Join` 返回。
- 包装后的 `Cancel` 调用 driver writer 的 `Cancel`，然后释放 pending 容量并移除 pending index；所有错误必须返回。
- 调用方必须最终调用 `Close` 或 `Cancel`。如果调用方泄漏 writer，pending 容量会保留到进程重启；控制面负责用上下文和上层超时约束上传生命周期。

`Get` 直接打开目标文件并返回 `io.ReadCloser`。`Delete` 删除目标文件并尝试删除空 image 目录；目录删除失败若不是非空场景，必须返回。

## 从 Reader 创建完整 root volume

`block.Driver` 增加 reader 输入方法：

```go
type Driver interface {
    Info(ctx context.Context) (DriverInfo, error)
    Create(ctx context.Context, req CreateRequest) (volume.Volume, error)
    CreateFromReader(ctx context.Context, req CreateFromReaderRequest) (volume.Volume, error)
    Delete(ctx context.Context, vol volume.Volume) error
    GetActualUsedBytes(ctx context.Context) (int64, error)
    Publish(ctx context.Context, vol volume.Volume, req PublishRequest) (volume.PublishedVolume, error)
    Unpublish(ctx context.Context, vol volume.Volume, req UnpublishRequest) error
    Snapshot(ctx context.Context, vol volume.Volume, req SnapshotRequest) (volume.Snapshot, error)
    Resize(ctx context.Context, vol volume.Volume, req ResizeRequest) (volume.Volume, error)
}

type CreateFromReaderRequest struct {
    Reader    io.Reader
    Format    diskformat.Format
    PoolName  string
    VMID      string
    VMName    string
    DiskIndex int
    Spec      volume.Spec
}
```

`VolumeService` 增加专用方法：

```go
func (s *VolumeService) CreateRootVolumeFromReader(ctx context.Context, req CreateRootVolumeFromReaderRequest) (volume.Volume, error)
```

该方法只接收 `io.Reader` 和显式 `Format`，不知道 reader 来自 `ImageService`、HTTP、控制面缓存还是其它来源。
它必须要求 `volume.RoleRoot`，并保持已有显式 `PoolName`、`VMID`、`VMName`、`DiskIndex` 规则。

## local block driver 的 CreateFromReader

`internal/storage/local` 的 `CreateFromReader` 生成独立 qcow2 root volume，不创建 backing file：

1. 校验 `ctx.Err()`、安全名称、`Format.Valid()`、`Reader != nil`、目标容量和 root volume 语义。
2. 计算目标路径，沿用 VM 语义路径：
   `<StorageRoot>/pool/<PoolName>/<VMID>/<VMName>-disk-<DiskIndex>.qcow2`。
3. 写入临时文件，避免半成品暴露为正式 volume。
4. 若输入为 `diskformat.FormatQCOW2`：完整复制 reader 字节到临时 qcow2 文件，再 rename 为目标 qcow2。
5. 若输入为 `diskformat.FormatRaw`：完整复制 reader 字节到临时 raw 文件，再调用 `qemu-img convert -f raw -O qcow2 tmp target`。
6. 如果 `Spec.CapacityBytes` 大于镜像虚拟大小，调用 qemu-img resize 增长到目标容量。
7. 不支持缩小镜像；如果目标容量小于输入镜像虚拟大小，返回错误并清理临时文件。
8. 任一步失败都清理临时文件、目标文件和空目录；主错误与 cleanup 错误用 `errors.Join` 返回。

为保持显式格式，raw 转 qcow2 的 qemu-img 调用必须显式传 `-f raw -O qcow2`。qcow2 输入路径不使用 `qemu-img create -b`、`qemu-img rebase` 或任何 backing file 选项。

现有 `qemuimg.QCOW2().Convert()` 只固定 `-f qcow2 -O qcow2`，不能直接满足 raw 输入转换。
实现阶段应新增通用/typed convert builder，或在 `qemuimg` 中扩展 convert 请求以显式接收输入格式；不得在 `local` driver 中拼 shell 字符串或直接 `os/exec`。
同理，若当前 `qemuimg` 还没有 resize builder，必须新增 typed resize builder，用 runner fake 测试 argv，
再由 local block driver 调用；禁止在 local block driver 中绕过 qemuimg runner 边界。

## 控制面数据流

控制面负责编排 image pool 到 block pool 的跨 pool 流程：

```go
func createRootFromImage(ctx context.Context, imageSvc *storage.ImageService, volumeSvc *storage.VolumeService, req CreateRootFromImageRequest) (vol volume.Volume, err error) {
    src, err := imageSvc.GetImage(ctx, storage.GetImageRequest{
        PoolName: req.ImagePoolName,
        ImageID:  req.ImageID,
    })
    if err != nil {
        return volume.Volume{}, err
    }

    defer func() {
        closeErr := src.Close()
        if closeErr != nil {
            err = errors.Join(err, closeErr)
        }
    }()

    vol, err = volumeSvc.CreateRootVolumeFromReader(ctx, storage.CreateRootVolumeFromReaderRequest{
        PoolName:  req.BlockPoolName,
        VMID:      req.VMID,
        VMName:    req.VMName,
        DiskIndex: req.DiskIndex,
        Reader:    src,
        Format:    req.Format,
        Spec: volume.Spec{
            Role:          volume.RoleRoot,
            CapacityBytes: req.CapacityBytes,
        },
    })
    if err != nil {
        return volume.Volume{}, err
    }

    return vol, nil
}
```

示例中所有错误都显式返回或通过 `errors.Join` 透传；禁止 `_ = err`。

## 错误模型

新增或复用以下 sentinel errors：

- `volume.ErrPoolRequired`：空 pool name。
- `volume.ErrInvalidVolume`：VMID/VMName/DiskIndex/Spec/Reader/Format 非法时使用或包裹。
- `volume.ErrUnsupported`：pool 类型与操作不匹配，或 driver 不支持某操作。
- `image.ErrImageExists`：同一 file pool 内 ImageID 已存在。
- `image.ErrImageNotFound`：ImageID 不存在。
- `image.ErrInvalidImage`：ImageID、Format、DeclaredSizeBytes 非法。

错误传递规则：

- 不允许 `_ = err`。
- 不允许吞掉 close、remove、rename、mkdir、copy、qemu-img 调用错误。
- 主操作失败后 cleanup 又失败时，返回 `errors.Join(primaryErr, cleanupErr)`。
- 包装错误必须使用 `%w`，使上层可以 `errors.Is` / `errors.As` 分类。
- `context.Canceled` / `context.DeadlineExceeded` 必须透传，不改写成业务错误。
- 禁止用新 root context 包裹或替换上层传入的 ctx；ctx 派生只能来自父 ctx。

## 并发与原子生命周期

file pool 的 `PutImage` 必须在 pool lock 下完成容量准入和 image index 预留，防止并发 Put 同 ID 或并发超卖。
写入字节发生在 driver writer 上，不能长期持有 pool lock。提交语义分两阶段：

1. `PutImage` 在 pool lock 下预留 image ID 和声明容量，状态为 pending；pending 容量计入 `AllocatedBytes`。
2. `ImageWriter.Close` 成功后，pool index 状态切换为 ready。
3. `ImageWriter.Cancel` 或 `Close` 失败后释放预留容量并移除 pending index。

如果实现阶段认为 pending 状态过重，可选择在 `PutImage` 中持锁创建 writer 并只在 `Close` 时登记 ready，
但必须证明不会出现同 ID 并发写入、容量超卖或 writer 成功后 index 缺失。优先推荐显式 pending 状态。

block pool 的 `CreateRootVolumeFromReader` 必须保持已有原子生命周期要求：容量准入、driver 创建、VolumeIndex 写入要么整体成功，
要么释放容量并清理 driver 产物。并发 create/publish/unpublish/delete 继续使用 pool lock 保护 index 与 lifecycle 状态。

## 测试策略

单元测试不得依赖真实 QEMU、qemu-img、TAP 或远程主机。新增测试建议：

1. `diskformat`：`Format.Valid()` 接受 qcow2/raw，拒绝空值和未知值。
2. `image`：接口类型编译期约束与错误 sentinel 分类。
3. `pool`：注册 file pool、拒绝 driver 类型不匹配、file pool 容量准入 ratio=1.0、重复 ImageID 返回 `ErrImageExists`、pending close/cancel 释放容量。
4. `storage`：`ImageService` 要求显式 pool name，拒绝 block pool；`VolumeService.CreateRootVolumeFromReader` 要求 root role、显式 format、错误透传。
5. `localfile`：Put/Close/Get/Delete 成功路径；Cancel 清理；重复 Put 拒绝；`.` / `..` / 路径分隔符拒绝；close/remove/rename 错误返回。
6. `local`：qcow2 reader 完整拷贝；raw reader 调用 qemu-img convert runner；失败 cleanup 用 `errors.Join`；目标容量小于输入镜像返回错误。
7. ctx 传播：取消父 ctx 后，ImageService、PoolService、localfile、local block driver、qemuimg runner fake 都能观察到取消并返回可 `errors.Is` 识别的 context 错误；测试中不得出现 `context.Background()` / `context.TODO()` 断链。
8. 全量：`go test ./...`，涉及并发容量和 pending 状态时运行 `go test -race ./internal/storage/...`。

需要新增 qemu-img convert builder 测试，确保 raw 输入转换 argv 是 `convert -f raw -O qcow2 <source> <target>`，
并继续通过 runner fake 断言，不直接调用 `os/exec`。

## 官方文档依据

- QEMU `qemu-img` 官方文档（QEMU 11.0.50 master docs）：`qemu-img` 可离线 create/convert/modify 镜像，并警告不要修改运行中 VM 使用的镜像；`convert` 支持显式 `-f FMT` 和 `-O OUTPUT_FMT`；`resize` 用于改变镜像大小；raw/qcow2 是 QEMU 支持的磁盘镜像格式。本设计据此要求离线转换、显式格式和完整 root volume 创建。
- Go 标准库 `io` 官方文档：`Reader`/`Writer`/`Closer`/`ReadCloser` 是基础 I/O 接口；`Reader` 调用方必须先处理 `n > 0` 字节再处理 error；`Writer` 在短写时必须返回非 nil error；`Closer.Close` 返回 error。本设计据此要求 `ImageService` 暴露标准 `io.Reader`/`io.Writer` 语义并透传 close/copy/write 错误。

## 验收标准

1. 控制面可以显式注册一个 `PoolTypeFile` localfile pool 和一个 `PoolTypeBlock` local block pool。
2. 调用方通过 `ImageService.PutImage` 写入一个显式 `diskformat.FormatQCOW2` 或 `diskformat.FormatRaw` 的镜像。
3. 同一 file pool 内重复 `ImageID` 返回 `ErrImageExists`，不覆盖已有镜像。
4. 控制面通过 `ImageService.GetImage` 得到 `io.ReadCloser`，并通过 `VolumeService.CreateRootVolumeFromReader` 创建独立 root volume。
5. root volume 的目标文件是完整 qcow2 文件，不依赖原始 image 文件或 backing chain。
6. 删除原始 image 后，已有 root volume 的 publish/VM runtime 输入不受影响。
7. 所有存储操作都显式传 pool name、format、VMID、VMName、DiskIndex；无隐藏默认值。
8. `scripts/verify.sh` 通过；新增并发路径通过 `go test -race ./internal/storage/...`。
