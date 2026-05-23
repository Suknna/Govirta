# qemu-img qcow2 client design

## 背景

`internal/virt/qemuimg` 负责封装离线镜像管理能力。当前阶段只支持
`qcow2`，不支持 `raw` 或其它镜像格式。该包应提供类似 Kubernetes
`client-go` 的链式客户端风格，让上层通过资源入口构建请求，并在
`Do(ctx)` 中完成校验、命令构造和执行。每个 `qemu-img` 子命令必须作为
独立 Go 子包存在；该子命令相关的 builder、结果结构、argv 构造和校验逻辑
也必须留在对应子包内。

本设计直接替换历史 `CreateRequest`、`ResizeRequest`、`Info(path)` 风格，
不保留兼容层。

## 目标

实现 6 项 qcow2 能力：

1. 通过 base 卷创建虚拟机磁盘。
2. 获取 qcow2 镜像元数据。
3. 格式转换，输出固定为 qcow2。
4. 创建内部快照。
5. 执行 qcow2 检查。
6. 删除磁盘文件。

## 非目标

- 不支持 `raw`、`vmdk` 或其它格式。
- 不封装 `qemu-nbd`、`qemu-storage-daemon`、`qemu-io`。
- 不创建目录、bridge、tap 或其它宿主机资源。
- 不保留旧 API 的兼容层。

## API 形态

客户端入口：

```go
client := qemuimg.NewClient(qemuimg.Config{
    Binary: "/usr/bin/qemu-img",
})
```

调用形态：

```go
err := client.QCOW2().
    Create().
    Target("vm-root.qcow2").
    FromBase("base.qcow2").
    SizeBytes(10 * 1024 * 1024 * 1024).
    Do(ctx)

info, err := client.QCOW2().Info().Path("vm-root.qcow2").Do(ctx)

err := client.QCOW2().Convert().Source("src.qcow2").Target("dst.qcow2").Do(ctx)

err := client.QCOW2().Snapshot().Path("vm-root.qcow2").Name("before-upgrade").Do(ctx)

check, err := client.QCOW2().Check().Path("vm-root.qcow2").Do(ctx)

err := client.QCOW2().Remove().Path("vm-root.qcow2").Do(ctx)
```

每个 verb 返回对应子包内定义的独立 builder。builder 只暴露该命令需要的选项，
并在 `Do(ctx)` 中完成参数校验。

## 包结构

`internal/virt/qemuimg` 根包只保留公共入口和共享边界：

- `Client` / `ExecClient`：提供 `QCOW2()` 资源入口。
- `Config`：配置 `qemu-img` 二进制路径，空值默认 `qemu-img`。
- `ErrInvalidRequest`：公共错误分类，便于调用方用 `errors.Is` 判断参数错误。

各子命令放在独立子包中：

- `internal/virt/qemuimg/create`：base 卷创建虚拟机磁盘。
- `internal/virt/qemuimg/info`：镜像元数据查询和 JSON 解析。
- `internal/virt/qemuimg/convert`：输出固定为 qcow2 的格式转换。
- `internal/virt/qemuimg/snapshot`：内部快照创建。
- `internal/virt/qemuimg/check`：qcow2 检查和 JSON 解析。
- `internal/virt/qemuimg/remove`：磁盘文件删除。

根包可以引用这些子包来组装 `QCOW2()` 资源入口，但子包不得反向引用根包，避免
Go import cycle。共享命令执行接口、命令结果、默认 `os/exec` runner 和参数错误构造
放在 `internal/virt/qemuimg/internal/exec`，供根包和子包共同依赖。根包只重新暴露
`ErrInvalidRequest`，不把 runner 类型作为公共 API。

## 命令映射

- `Create().Target(...).FromBase(...).SizeBytes(...)`：
  `qemu-img create -f qcow2 -F qcow2 -b <base> <target> <size>`。
- `Info().Path(...)`：`qemu-img info --output=json <path>`。
- `Convert().Source(...).Target(...)`：`qemu-img convert -O qcow2 <src> <dst>`。
- `Snapshot().Path(...).Name(...)`：`qemu-img snapshot -c <name> <path>`。
- `Check().Path(...)`：`qemu-img check --output=json <path>`。
- `Remove().Path(...)`：Go 侧 `os.Remove(path)`；`qemu-img` 没有删除子命令，删除仍归属
  qcow2 磁盘生命周期入口，便于上层统一调用。

## 数据结构

`info.ImageInfo` 表示 `qemu-img info --output=json` 的核心字段：文件名、格式、
虚拟大小、实际大小、backing 文件和 backing 格式。

`check.Result` 表示 `qemu-img check --output=json` 的可用字段，并保留原始输出。
不同 QEMU 版本输出字段可能不同，保留原始输出能让调用方在早期阶段排查宿主机
差异。

## 错误处理

- 参数错误统一包装为 `ErrInvalidRequest`，便于调用方用 `errors.Is` 分类。
- 命令执行错误直接返回底层错误，并保留 runner 捕获到的 stderr 供测试和后续扩展。
- JSON 解析失败直接返回解析错误，说明宿主机输出不符合预期。
- 删除磁盘文件时返回 `os.Remove` 的错误，不吞掉不存在或权限错误。

## 测试策略

- 每个子命令子包都有自己的单元测试，使用 fake runner 验证 builder 生成的 argv。
- 单元测试覆盖参数校验、`info.ImageInfo` JSON 解析、`check.Result` JSON 解析和删除文件行为。
- 基线验证命令为 `go test ./internal/virt/qemuimg`；最终可运行 `go test ./...`。

## 影响的调用关系

本次变更只影响 qemu-img 离线镜像管理边界：

```text
upper layer -> internal/virt/qemuimg.Client.QCOW2 -> subcommand builder -> qemu-img/os.Remove
```

当前仓库内尚无其它包调用 `internal/virt/qemuimg`，因此可以直接替换 API。
