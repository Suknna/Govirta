# Image Controller Migration Design

**日期**：2026-06-13  
**状态**：已实现，本地验证通过
**上位设计**：`docs/superpowers/specs/2026-06-12-controlplane-controller-manager-design.md`、`docs/superpowers/specs/2026-06-12-task-phase-one-design.md`

## 1. 目标

本轮正式迁移只处理 Image 控制器，把镜像控制面从当前 node-local `ImageController` 和 Volume/root-disk 派生路径中拆出。Image 成为“集群镜像目录 + 每节点缓存”的独立控制对象：control plane 负责上传入口、元数据校验、版本/哈希、目标节点集合、`CacheImage` Task 生成、Task 终态聚合、finalizer；node 只执行镜像缓存 Task，把权威镜像字节拉到本节点 cache 目录并校验。

验收目标：

- Image 控制逻辑上移到 control plane controller-manager。
- 用户创建的 Image 资源具备完整 CRUD：上传/创建、查询/list、替换元数据或新版本、删除。
- `Task` 扩展出强类型 `CacheImage` / `DeleteCachedImage` NodeTask 操作。
- `govirtlet` 不再用业务 Image watch 直接 patch `Image.status`；它只执行 Image cache Task 并 patch `Task.status`。
- control plane 聚合每个节点的缓存结果，只有所有目标节点缓存当前版本后才推进 `Image.status.phase=ready`。
- `pkg/virt/qemu` 新增 CD/DVD typed 封装，支持 ISO 作为 `media=cdrom` 或等价 CD-ROM backend 挂载，不依赖 generic `AddArgument` 绕过策略。
- `VM.spec` 新增显式 CD/DVD 镜像引用字段，使 ISO Image 能从 node image cache 进入 VMM/QEMU argv。
- Volume/root disk 不完整迁移，但做最小消费边界调整：root Volume 从本节点 image cache materialize，不能再拉取外部镜像源。
- 现有 distributed spine VM 启动 E2E 保留 root disk 启动目标，但前置链路改为 ImageController cache ready 后再创建 root Volume。

## 2. 非目标

- 不迁移 StoragePoolController 或 VolumeController 的完整状态机；VolumeController 只允许为消费本节点 image cache 做最小必要调整。
- 不实现 Ceph/RBD、S3/RGW、NFS 后端驱动；只定义后续边界。
- 不把镜像字节存入 etcd；etcd 只存 Image 元数据与状态。
- 不让 master/control-plane 把镜像字节 fan-out 推送给所有节点。
- 不引入 backing-file chain、linked root disk 或写时克隆语义；当前项目仍保持 VM root disk 独立副本约束。
- 不支持任意未声明格式字符串；新增格式必须是 typed constant、校验和执行路径同步扩展。
- 不实现 ISO 安装操作系统的 guest 内闭环；本轮只验证 ISO 作为只读 CD/DVD 介质进入运行中 QEMU argv。
- 不把现有 VM 启动 E2E 改成 ISO 安装/启动；当前 E2E 继续验证 qcow2/raw root disk 启动，并新增独立 CD/DVD argv 断言。

## 3. 核心决策

采用“权威 ImageStore + 节点 pull cache”的模型：

```text
govirtctl image upload / external source
  -> ImageStore 权威镜像库
  -> etcd Image metadata
  -> control-plane ImageController
  -> CacheImage NodeTask per node
  -> govirtlet pulls into node image cache
  -> Task.status observed
  -> Image.status nodeCaches aggregated
```

关键约束：

- master 可以接收一次上传，但不负责把字节逐个推到所有节点。
- 节点按 Task 自己 pull，便于失败重试、限速、恢复和局部缓存重建。
- ImageStore 是权威源；node image cache 是派生缓存，可删除并由 Task 重建。
- VM 创建只依赖目标节点本地 cache；cache 不存在或版本不匹配时，control plane 必须先等待/生成 `CacheImage` Task。
- ISO 介质同样只引用本节点 cache，不允许 VM controller 直接读取外部 source 或 ImageStore。

## 4. 目录布局

### 4.1 Node runtime root

`govirtlet --runtime-root` 是节点运行时父目录。镜像缓存目录和 VM 运行时目录必须位于同一个父级目录下，避免缓存、VM runtime、QMP socket、pidfile 分散到多个隐式位置。

建议布局：

```text
<runtimeRoot>/
  vms/
    <vmUID>/
      vm.json
      qmp.sock
      qemu.pid
  image-cache/
    <imageName>/
      <version>/
        image
        metadata.json
```

阶段落地方式：

- 保留现有 `--runtime-root` flag 作为父目录。
- 新增 node 配置字段可显式命名 cache 子目录，例如 `ImageCacheDirName=image-cache`；默认值只允许在 `cmd/govirtlet` composition root 暴露，不允许埋入内部包。
- 后续 VM runtime 若当前直接写在 `<runtimeRoot>/<vmUID>`，迁移计划里必须显式处理到 `<runtimeRoot>/vms/<vmUID>`，但本轮 Image 迁移不强行搬动既有 VM runtime 行为。

### 4.2 Master-local ImageStore

第一版 ImageStore 使用 `govirtad` 本地目录作为权威镜像库，由 `govirtad --image-store-root` 显式指定。
节点需要通过 HTTP 从 `govirtad` 拉取 upload source 镜像，因此还必须由 `govirtad --image-store-public-url` 显式指定节点可访问的 ImageStore 下载基址；不能从 `--listen` 或请求 Host 头隐式推断。

建议布局：

```text
<imageStoreRoot>/
  images/
    <imageName>/
      <version>/
        image
        metadata.json
        upload.tmp
```

约束：

- `--image-store-root` 必填；不能隐式落到当前目录、`/tmp` 或 etcd 数据目录。
- `--image-store-public-url` 必填；例如 e2e 中应显式传 `http://host.lima.internal:<port>`，本机单进程测试可传 `http://127.0.0.1:<port>`。
- 上传先写 `upload.tmp`，校验 size/sha256/format 后原子提交为 `image`。
- `metadata.json` 是 ImageStore 内部字节清单，不是控制面事实源；etcd 中的 `Image` 对象仍是控制面元数据事实源。
- 后续 NFS/S3/RGW ImageStore 替换时，上层只依赖 `ImageStore` 接口，不依赖本地路径布局。

## 5. Image API 契约

`Image` 表示集群可用的镜像模板或安装介质，而不是 VM root volume。

### 5.1 Spec

现有 `ImageSpec` 需要从“节点直接 fetch 外部 source 到 file pool”演进为“权威 source + 版本化 content identity”：

```text
Image.spec
  format            qcow2 | raw | iso
  source.type       upload | http
  source.location   ImageStore object ref 或 external URL
  version           显式版本，例如 v1 或内容哈希派生版本
  declaredSizeBytes 上传/登记时声明
  sha256            内容哈希
```

说明：

- `qcow2`、`raw`、`iso` 是第一批合法格式。
- `upload` 表示字节已进入 master-local ImageStore。
- `http` 表示节点可直接从外部 URL pull；control plane 仍保存 size/sha256/format 并生成 cache Task。
- 不再要求 `filePoolRef` 作为 Image 的必填字段；Image 不属于 Volume block/file pool。
- 如果保留兼容字段，必须在迁移中明确废弃路径；不允许同时让 `filePoolRef` 和新 ImageStore 语义并存为两个事实源。

### 5.2 Status

Image status 显式记录当前版本的分发状态和 Task 引用：

```text
Image.status
  phase              pending | caching | ready | deleting | failed
  observedVersion
  observedSHA256
  observedSizeBytes
  nodeCaches[]
    nodeName
    phase            pending | caching | ready | deleting | failed
    taskRef{name,uid}
    cachedPath       node-local observed path
    sizeBytes
    sha256
    message
  message
```

聚合规则：

- 所有目标节点对 `observedVersion/observedSHA256` 都 ready，Image 才能 ready。
- 任一节点 failed，Image 可进入 failed 或保持 caching 并记录失败节点；第一版采用 fail-closed：进入 failed，等待用户 replace 新版本或删除重建。
- control plane 只接受匹配 `taskRef` 的 Task 结果，避免旧 Task 污染新版本状态。

## 6. Upload 与分发流程

### 6.1 Upload source

```text
govirtctl image upload --name ubuntu --version v1 --format qcow2 --file ubuntu.qcow2
  -> CLI 计算 size/sha256
  -> POST/PUT upload bytes to govirtad ImageStore endpoint
  -> govirtad 校验 size/sha256/format
  -> govirtad 原子提交 ImageStore object
  -> govirtad 创建或更新 Image metadata
```

约束：

- CLI 必须显式传 `name/version/format/file`。
- `sha256` 与 size 可以由 CLI 计算并随请求发送；govirtad 必须在写入后重新校验，不能只信任客户端。
- 上传成功只是 ImageStore ready，不代表节点 cache ready。

### 6.2 External HTTP source

```text
govirtctl apply Image(source.type=http, source.location=https://...)
  -> control plane 校验 spec 显式字段
  -> ImageController 为每个 node 创建 CacheImage Task
  -> node 直接 pull HTTP URL 到本地 cache
```

约束：

- HTTP source 必须显式 `format/declaredSizeBytes/sha256`。
- node 下载后必须校验 size/sha256。
- 第一版不实现鉴权 HTTP、重定向策略、断点续传；这些能力后续单独设计。

### 6.3 Image CRUD

用户创建的 Image 资源必须具备完整增删改查能力：

- Create：`govirtctl image upload` 创建 upload source Image；`govirtctl apply Image` 创建 external HTTP source Image。
- Read/List：`govirtctl get Image <name>` 和 `govirtctl get Image` 返回 spec、status、每节点 cache 状态。
- Update：`govirtctl replace Image <name>` 只能替换显式可变字段或提交新 `version/sha256/size/source`；replace 必须走 `metadata.resourceVersion` CAS，不允许隐式覆盖。
- Delete：`govirtctl delete Image <name>` 触发 control-plane finalizer，等待所有节点 `DeleteCachedImage` Task 成功后再删除 ImageStore object 和 etcd 记录。

约束：

- 不提供兼容别名或静默迁移旧字段；旧 `filePoolRef` 语义必须在 admission 中 fail-closed 或通过明确迁移步骤移除。
- 新版本不是原地覆盖旧字节；`name + version + sha256` 是内容身份。
- 删除必须先清 node cache，再删 master-local ImageStore object，最后删除 etcd 记录。

## 7. Task 扩展

新增强类型操作：

- `TaskOperationCacheImageNode`
- `TaskOperationDeleteCachedImageNode`

`CacheImageInput`：

```text
imageName
imageUID
version
format
source.type       upload | http
source.location   ImageStore pull URL 或 external URL
declaredSizeBytes
sha256
cacheRoot
```

`CacheImageObserved`：

```text
nodeName
imageName
version
format
cachedPath
sizeBytes
sha256
```

Task 规则：

- `CacheImage` 是 NodeTask，必须带 `metadata.nodeName`。
- Task name 必须确定性生成，例如 `cache-image-<imageName>-<version>-<nodeName>`，并用 UID/spec hash 防止同名旧 Task 被误用。
- node executor 对非匹配 node、非支持 operation、非法 input 一律 fail-closed。

## 8. Control-plane ImageController

职责：

1. watch/list `Image` 对象。
2. 校验 spec 中格式、版本、size、sha256、source 的显式性。
3. 根据 control-plane 已知 node 列表，为每个节点创建或复用匹配当前版本的 `CacheImage` Task。
4. 读取 Task 终态并聚合到 `Image.status.nodeCaches`。
5. 所有节点 ready 后推进 `Image.status.phase=ready`。
6. 删除 Image 时先生成/等待 `DeleteCachedImage` Task，再删除 ImageStore object，最后移除 finalizer。

不做：

- 不读取或写入节点文件系统。
- 不把镜像字节写入 etcd。
- 不直接推送字节到 node。
- 不执行 qemu-img convert；格式处理留给 node 或后续 Volume materialization。

## 9. Node ImageTaskExecutor

职责：

1. watch 分配给本节点的 `Task`。
2. 对 `CacheImage` Task patch `Running`。
3. 从 ImageStore pull URL 或 external HTTP URL 读取字节。
4. 写入 `<runtimeRoot>/image-cache/<imageName>/<version>/image.tmp`。
5. 校验 size、sha256、format。
6. 原子 rename 到 `image` 并写 `metadata.json`。
7. patch Task `Succeeded`，observed 返回 cached path 与内容身份。

失败处理：

- 下载、校验、路径安全、原子提交失败都 patch Task `Failed`，带稳定 errorClass。
- 已存在相同 version/sha256 的 cache 时 idempotent success。
- 已存在同名 version 但 sha256 不同必须 fail-closed，不允许覆盖。

## 10. Volume 与 VM 消费边界

本轮不迁 VolumeController 的完整状态机，但必须调整 root Volume 对 Image 的消费边界：

- Volume 不再负责从外部 source 拉镜像。
- 创建 VM root disk 前，control plane 必须确保目标节点已有对应 Image cache。
- `qcow2/raw`：从 cached image copy/convert 成 VM 独立 root disk；当前 distributed spine E2E 的 `Image -> root Volume -> VM` 启动链路必须接入这个路径。
- `iso`：作为 CD/DVD 介质挂载；同时创建空白 root disk 承载安装结果。
- ISO 不允许被当作 root disk bytes 直接 convert 成系统盘。

VM API 增加显式 CD/DVD 引用字段，建议命名：

```text
VM.spec.cdromImageRefs[]
  imageRef
  bootIndexMode   unset | index
  bootIndex       *int；仅 bootIndexMode=index 时必须存在，unset 时必须省略
```

约束：

- `cdromImageRefs` 只允许引用 `Image.spec.format=iso` 且本节点 cache ready 的 Image。
- `volumeRefs` 仍只表达 root/data disk；不得把 ISO 塞进 Volume。
- `bootIndexMode` 必须显式表达是否设置 boot index；`index` 模式必须携带 `bootIndex` 字段，即使值为 0；`unset` 模式必须省略 `bootIndex`，不能用空整数隐式表示默认值。
- VMM `SpecSummary` 需要新增 `CDROMs []CDROMSpec`，由 VM controller 从 `cdromImageRefs` + Image cache status 派生。

现有 E2E 链路调整：

```text
旧：03-image(file/filePoolRef) -> node ImageController -> file pool -> 04-volume(root) -> VM boot
新：Image upload/apply -> control-plane ImageController -> CacheImage Task -> node image-cache -> 04-volume(root) -> VM boot
```

启动目标不变：VM 仍通过 `volumeRefs` 引用 root Volume 启动；测试仍证明 qcow2/raw root disk 可启动，而不是改成 ISO 启动。

QEMU 依据：QEMU 文档将 `raw/qcow2` 定义为磁盘镜像格式；CD-ROM 应通过 `-cdrom` 或 `media=cdrom` 挂载。管理工具应显式指定 block format，避免不可信镜像头自动推断。

## 11. QEMU CD/DVD 封装

当前 `pkg/virt/qemu` 只有 typed `-blockdev`、virtio block device、netdev、chardev、monitor、serial、firmware 等封装，没有 CD/DVD 或 `media=cdrom` 的 typed API。本轮必须补齐 ISO 消费所需的最小 QEMU 封装。

设计要求：

- 新增 CD-ROM backend/device typed builder，优先使用现有稳定 `-blockdev` + `-device` 模型，而不是直接开放 `-cdrom` shortcut。
- 输入必须显式包含 image path、format/source kind、readonly、device id、boot index 或显式不设置 boot index。
- ISO 路径必须作为只读介质渲染；不允许以 writable disk 方式挂载 ISO。
- `AddArgument` 仍不得允许 `-cdrom`、`-drive` 等绕过 typed policy 的快捷参数。
- 测试需要覆盖 ISO argv golden case、空路径拒绝、非只读拒绝、generic argument bypass 拒绝。

说明：

- QEMU 文档支持 `-cdrom file` 和 `-drive file=...,media=cdrom`。Govirta 作为管理工具应优先走显式 blockdev/device 表达，保持现有 typed builder 约束。
- 本轮必须打通 `VM.spec.cdromImageRefs -> Image cache -> vmm.SpecSummary.CDROMs -> QEMU argv`，但不做 guest OS 安装流程。

## 12. 错误处理与可观测性

- Task errorClass 增加或复用稳定分类：`DependencyNotReady`、`BackendUnsupported`、`ValidationFailed`、`ChecksumMismatch`、`TransientIO`。
- 日志字段复用项目词汇表：`component=image-controller|image-task-executor`、`operation=cache_image|delete_cached_image|upload_image`、`image`、`node_id`、`outcome`、`error`。
- control plane 记录每次 Task 生成与聚合结果。
- node executor 记录下载开始、校验结果、原子提交结果。
- 指标/追踪后续接入时必须以 live cache 文件和 ImageStore object 为事实源，不从临时状态推断。

## 13. 测试策略

单元测试：

- `pkg/apis/image/v1alpha1`：`iso/qcow2/raw` 格式校验、source/version/sha256/size 必填、status node cache 聚合校验。
- `pkg/apis/task/v1alpha1`：`CacheImage` / `DeleteCachedImage` typed input/observed 校验。
- ImageStore local 实现：临时写入、size/sha256 校验、原子提交、拒绝同版本不同哈希。
- control-plane ImageController：为每个 node 生成 CacheImage Task；只接受匹配 taskRef 的结果；所有节点 ready 后 Image ready。
- node ImageTaskExecutor：upload source pull、HTTP source pull、idempotent cache、checksum mismatch fail、同版本不同 hash fail。
- `pkg/virt/qemu`：CD/DVD typed argv rendering、ISO readonly、禁止 generic `-cdrom` bypass。
- `pkg/apis/vm/v1alpha1`：`cdromImageRefs` 字段校验、显式 boot index 模式校验。
- `internal/vmm`：`SpecSummary.CDROMs` 派生 CD/DVD argv。
- govirtctl/apiserver：Image create/get/list/replace/delete CRUD 路径覆盖。

集成测试：

- fake store + apiserver + control-plane ImageController + node TaskExecutor：Image 从 pending/caching 到 ready。
- 两节点 fake executor：一个节点失败时 Image fail-closed，两个节点成功时 Image ready。

E2E：

- 调整现有 distributed spine closure：`03-image` 不再走旧 `filePoolRef` 直拉路径，而是先经 ImageStore/ImageController/CacheImage Task 让 node image-cache ready。
- 保留 `04-volume -> 07-vm` root disk 启动断言，证明 cached qcow2/raw image 能 materialize 成独立 root Volume 并启动 VM。
- 新增一个 CD/DVD e2e 断言：应用 ISO Image 并在 VM manifest 中声明 `cdromImageRefs`，等待 VM running 后检查运行中 QEMU argv 含只读 cdrom 介质；不做 guest 内安装闭环。

最终验证：

```bash
go test ./...
go test -race -count=1 ./internal/controlplane ./internal/controlplane/controller ./internal/node/...
scripts/verify.sh
```

## 14. 文件体量预判

预计新增/修改文件会按职责拆分，避免单文件过大：

- `pkg/apis/image/v1alpha1/types.go`：会增长但预计低于 300 行。
- `pkg/apis/task/v1alpha1/types.go`：新增 storage image task typed payload，若接近 500 行应拆出 `image_task.go`。
- `internal/controlplane/imagestore/*`：local ImageStore 独立包，每文件预计低于 250 行。
- `internal/controlplane/controller/image*.go`：controller、task builder、status aggregation 拆分。
- `internal/node/controllers/task_image.go` 或等价 executor 文件：只放 Image task execution，不混入 Volume/StoragePool。
- `pkg/virt/qemu/device/cdrom.go` 或等价文件：只放 CD/DVD typed device，不混入 virtio block disk。

本设计不需要硬上限豁免。

## 15. 官方文档引用

- QEMU Disk Images：`https://www.qemu.org/docs/master/system/images.html`。用于确认 `raw/qcow2` 是磁盘镜像格式，且 QEMU 支持多种显式 image format。
- QEMU Invocation：`https://www.qemu.org/docs/master/system/invocation.html`。用于确认 CD-ROM 可通过 `-cdrom` 或 `media=cdrom` 挂载，并且 block device format 应显式指定。

## 16. 验收边界

本阶段完成后只能声明：

- Image control plane 已从 node business controller 迁出。
- 用户 Image CRUD 已闭环。
- Image 字节有权威 ImageStore 和每节点 cache 的明确边界。
- Image cache 分发通过 Task 闭环完成。
- QEMU builder 具备 CD/DVD typed argv 封装。
- `VM.spec.cdromImageRefs` 能把 ISO Image 作为只读 CD/DVD 介质接入运行中 QEMU argv。
- Volume root disk 已通过最小调整消费 Image cache，distributed spine E2E 仍能启动 VM。

不得声明：

- StoragePool/Volume 控制器已完成 control-plane 迁移。
- Ceph/RBD、NFS、S3/RGW 后端已经实现。
- ISO guest 内安装流程已完成，除非后续计划显式实现并验证。

## 17. 实施结果摘要

Tasks 1-12 已完成实现，当前落地形态与本设计保持一致：

- ImageStore 成为 `govirtad` 本地权威镜像字节库；Image 元数据和状态仍以 etcd 中的 Image 对象为事实源。
- control-plane ImageController 聚合 Image cache 状态，并按节点生成 `CacheImage` / `DeleteCachedImage` Task；`govirtlet` TaskController 只执行节点本地 cache/delete 任务并 patch `Task.status`。
- root Volume 从目标节点本地 image-cache materialize 独立根盘，不再通过旧 ImageController 从 file pool 读取镜像源。
- `VM.spec.cdromImageRefs` 引用 ISO Image cache，VMM `CDROMs` 派生为 typed QEMU CD-ROM argv；QEMU builder 保持禁止 generic `-cdrom`/`-drive` bypass。
- `govirtad` 显式新增 `--image-store-root`、`--image-store-public-url`、`--image-cache-root`、`--image-controller-sync-period`；`govirtlet` 使用 sibling/default image cache root，并移除旧 `--image-source-root`。
- distributed spine E2E 路径改为通过 `govirtctl image upload` 上传 CirrOS/root image 和 ISO，再等待 Image cache ready 后创建 root Volume 和启动 VM。

未改变的边界：StoragePool/Volume 控制器未完整迁移；ISO guest 内安装流程未实现；完整 Lima E2E/acceptance 仍需在最终验收阶段按需运行。
