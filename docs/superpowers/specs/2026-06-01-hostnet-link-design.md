# Host Network Link 封装设计

日期：2026-06-01

状态：已由用户批准分段设计，等待用户审阅本文档。

## 背景

Govirta 当前处于单节点冷操作闭环阶段，需要补齐本地 bridge/TAP 网络准备能力。现有
`internal/network/bridge` 仍是 no-op skeleton，`internal/virt/qemu` 已能消费预创建的 TAP
名称，但 QEMU 层不负责创建宿主机网络资源。

本设计在 `internal/hostnet/link` 下封装 `github.com/vishvananda/netlink`，让上层按接近
`ip link` / `ip addr` 的显式语义使用宿主机网络功能，同时不暴露底层 netlink 类型。

## 目标

1. 在 `internal/hostnet/link` 提供 Govirta 自己的稳定 link 原语契约。
2. 支持 bridge 创建的完整链路：exists、add、addr、up。
3. 支持 TAP 创建并显式处理 VNET_HDR、owner/group、MTU、15 字符名限制、bridge MAC 漂移等坑。
4. 提供 Delete、幂等 Ensure、Exists、Get、List 和错误翻译。
5. 每个创建路径都有对应 Delete，避免宿主机接口泄漏。
6. 提供 `linkerr` 错误子包，常量留在根 `link` 包。
7. 通过 `test/acceptance` 在 Linux/Lima 实机环境完成 bridge+TAP+QEMU+guest 静态 IP 端到端网络连通验收。
8. acceptance 完整测试日志按日期时间写入 `test/log/`。

## 非目标

1. 本次不扩展 `internal/virt/qemu` typed builder 的 direct-kernel 启动能力。
2. 本次不实现 DHCP、dnsmasq、NAT、防火墙或路由转发策略。
3. 本次不把 `internal/network/bridge` 替换为完整 VM 网络编排层，只提供下层 host link 原语。
4. 本次不让普通 `go test ./...` 修改真实宿主机网络；真实网络操作只放 acceptance。
5. 本次不支持跨平台真实实现；macOS 仅通过接口/fake 覆盖，Linux 实现位于子包。

## 包结构

```text
internal/hostnet/link/
├── link.go
├── constants.go
├── noop.go
├── linkerr/
│   └── errors.go
└── linux/
    ├── manager.go
    └── manager_test.go
```

调用关系：

```text
internal/network/bridge 或未来 node 网络编排
    -> internal/hostnet/link.Manager
        -> internal/hostnet/link/linux.Manager
            -> github.com/vishvananda/netlink
```

`internal/virt/qemu` 不导入 `internal/hostnet/link`，只消费已经创建好的 TAP 名称。

## 根包 API 契约

根包保留强类型和显式请求，避免裸字符串或裸 bool 表示行为：

```go
type Name string
type Kind string
type AdminState string
type VNetHeaderMode string
type UID struct {
    Value uint32
    Set   bool
}
type GID struct {
    Value uint32
    Set   bool
}

const (
    KindAny    Kind = "any"
    KindBridge Kind = "bridge"
    KindTap    Kind = "tap"

    AdminStateUp   AdminState = "up"
    AdminStateDown AdminState = "down"

    VNetHeaderEnabled  VNetHeaderMode = "enabled"
    VNetHeaderDisabled VNetHeaderMode = "disabled"

    MaxInterfaceNameLength = 15
)

type Manager interface {
    EnsureBridge(ctx context.Context, spec BridgeSpec) (LinkInfo, error)
    EnsureTap(ctx context.Context, spec TapSpec) (LinkInfo, error)
    Delete(ctx context.Context, name Name) error
    Exists(ctx context.Context, name Name) (bool, error)
    Get(ctx context.Context, name Name) (LinkInfo, error)
    List(ctx context.Context, filter ListFilter) ([]LinkInfo, error)
}
```

请求类型全部显式：

```go
type BridgeSpec struct {
    Name        Name
    GatewayCIDR string
    MTU         int
    MAC         net.HardwareAddr
}

type TapSpec struct {
    Name       Name
    BridgeName Name
    OwnerUID   UID
    OwnerGID   GID
    MTU        int
    MAC        net.HardwareAddr
    VNetHeader VNetHeaderMode
}

type ListFilter struct {
    Kind Kind
}

type LinkInfo struct {
    Name       Name
    Kind       Kind
    Index      int
    MTU        int
    MAC        net.HardwareAddr
    AdminState AdminState
    MasterName Name
    Addresses  []string
}
```

`ListFilter.Kind` 必须显式传 `KindAny`、`KindBridge` 或 `KindTap`。空值无语义，返回参数错误。

`UID` 和 `GID` 使用 `Set` 字段区分“调用方显式传 root 0”和“调用方漏填零值”。
`TapSpec.OwnerUID.Set` 与 `TapSpec.OwnerGID.Set` 必须为 `true`；`Value == 0` 仅表示调用方显式选择 root。

## 错误模型

`internal/hostnet/link/linkerr` 提供稳定 sentinel：

```go
var (
    ErrInvalidRequest = errors.New("invalid host link request")
    ErrNotFound       = errors.New("host link not found")
    ErrAlreadyExists  = errors.New("host link already exists")
    ErrConflict       = errors.New("host link conflict")
    ErrPermission     = errors.New("host link permission denied")
    ErrIncompleteList = errors.New("host link list incomplete")
    ErrUnsupported    = errors.New("host link operation unsupported")
)
```

`linux.Manager` 将底层错误翻译为稳定错误：

| 底层错误 | Govirta 错误 |
| --- | --- |
| `netlink.LinkNotFoundError` | `linkerr.ErrNotFound` |
| `EPERM` / `EACCES` / `os.ErrPermission` | `linkerr.ErrPermission` |
| `EEXIST` | `linkerr.ErrAlreadyExists` 或 Ensure 中转为幂等 reconcile |
| `EINVAL` | `linkerr.ErrInvalidRequest` |
| `netlink.ErrDumpInterrupted` | `linkerr.ErrIncompleteList` |
| 同名不同类型 link | `linkerr.ErrConflict` |

错误必须向上传播。清理或回滚阶段如果同时存在主错误和清理错误，使用 Go 标准库
`errors.Join` 保留全部错误。

## Linux/netlink 操作序列

### EnsureBridge

`EnsureBridge(ctx, BridgeSpec)` 实现完整 `exists + add + addr + up`：

1. 校验 `ctx`、接口名长度、`GatewayCIDR`、`MTU`、`MAC`。
2. `LinkByName(name)`：
   - 不存在：`LinkAdd(&netlink.Bridge{LinkAttrs: attrs})`。
   - 已存在且是 bridge：继续 reconcile。
   - 已存在但不是 bridge：返回 `linkerr.ErrConflict`。
3. 创建或获取后显式执行：
   - `LinkSetHardwareAddr(bridge, spec.MAC)`，防止 bridge MAC 随端口漂移。
   - `LinkSetMTU(bridge, spec.MTU)`。
   - `AddrReplace(bridge, parsed GatewayCIDR)`，避免 `AddrAdd` 重复失败。
   - `LinkSetUp(bridge)`。
4. 返回实际 `LinkInfo`。

如果 bridge 是本次调用新建的，且 `LinkSetHardwareAddr`、`LinkSetMTU`、`AddrReplace` 或
`LinkSetUp` 失败，必须尝试 `LinkDel` 回滚本次新建的 bridge。主错误和回滚错误同时发生时，
使用 `errors.Join(primaryErr, rollbackErr)` 返回。若 bridge 在调用前已经存在，不自动删除，
只返回错误并保留现场。

### EnsureTap

`EnsureTap(ctx, TapSpec)` 显式处理 TUN/TAP 坑：

1. 校验 `Name`、`BridgeName`、`OwnerUID.Set`、`OwnerGID.Set`、`MTU`、`MAC`、`VNetHeader`。
2. 先查 `BridgeName`：
   - 不存在：返回 `linkerr.ErrNotFound`。
   - 存在但不是 bridge：返回 `linkerr.ErrConflict`。
3. `LinkByName(tapName)`：
   - 不存在：创建 `netlink.Tuntap`。
   - 已存在且是 TAP：继续 reconcile。
   - 已存在但不是 TAP：返回 `linkerr.ErrConflict`。
4. TAP 创建参数：
   - `Mode = TUNTAP_MODE_TAP`。
   - `Flags = TUNTAP_NO_PI`。
   - `VNetHeaderEnabled` 时增加 `TUNTAP_VNET_HDR`。
   - `Owner = spec.OwnerUID.Value`。
   - `Group = spec.OwnerGID.Value`。
5. TAP 创建后显式执行：
   - `LinkSetHardwareAddr(tap, spec.MAC)`。
   - `LinkSetMTU(tap, spec.MTU)`。
   - `LinkSetMaster(tap, bridge)`。
   - `LinkSetUp(tap)`。
6. 返回实际 `LinkInfo`。

如果 TAP 是本次调用新建的，且 `LinkSetHardwareAddr`、`LinkSetMTU`、`LinkSetMaster` 或
`LinkSetUp` 失败，必须尝试 `LinkDel` 回滚本次新建的 TAP。主错误和回滚错误同时发生时，
使用 `errors.Join(primaryErr, rollbackErr)` 返回。若 TAP 在调用前已经存在，不自动删除，
只返回错误并保留现场。

已存在 TAP 的 VNET_HDR 语义必须显式处理：实现应读取可观测的 `netlink.Tuntap.Flags`。
如果可观测 flags 与 `TapSpec.VNetHeader` 冲突，返回 `linkerr.ErrConflict`；不得把 VNET_HDR
不匹配的已有 TAP 当作成功。若当前内核或 netlink 版本无法可靠观测该 flags，`EnsureTap`
对已存在 TAP 返回 `linkerr.ErrUnsupported`，调用方必须先 Delete 再重新创建。

所有 `Manager` 方法的 `ctx` 语义一致：`ctx == nil` 返回 `linkerr.ErrInvalidRequest`；若
`ctx.Err()` 已非 nil，则返回该 context 错误本身，使 `errors.Is(err, context.Canceled)` 或
`errors.Is(err, context.DeadlineExceeded)` 可命中。多步骤 Ensure 在每次 netlink 调用前检查
`ctx.Err()`。

### Delete

`Delete(ctx, name)` 为幂等删除：

1. link 不存在：返回 `nil`。
2. link 存在：调用 `LinkDel(link)`。
3. 删除失败按错误模型翻译并返回。

### Exists / Get / List

`Exists(ctx, name)`：

- 不存在返回 `(false, nil)`。
- 存在返回 `(true, nil)`。
- 其他错误翻译返回。

`Get(ctx, name)`：

- 不存在返回 `linkerr.ErrNotFound`。
- 存在返回 `LinkInfo`。

`List(ctx, filter)`：

- `filter.Kind` 必须显式为 `KindAny`、`KindBridge` 或 `KindTap`。
- `LinkList()` 后过滤。
- 遇到 `netlink.ErrDumpInterrupted` 返回 `linkerr.ErrIncompleteList`，不把部分结果当完整成功。

## 单元测试策略

普通单元测试不修改真实网络。`linux.Manager` 内部通过可注入 handle 包装 netlink 调用：

```go
type handle interface {
    LinkByName(name string) (netlink.Link, error)
    LinkAdd(link netlink.Link) error
    LinkDel(link netlink.Link) error
    LinkSetUp(link netlink.Link) error
    LinkSetMTU(link netlink.Link, mtu int) error
    LinkSetHardwareAddr(link netlink.Link, hw net.HardwareAddr) error
    LinkSetMaster(link netlink.Link, master netlink.Link) error
    AddrReplace(link netlink.Link, addr *netlink.Addr) error
    LinkList() ([]netlink.Link, error)
    AddrList(link netlink.Link, family int) ([]netlink.Addr, error)
}
```

单元测试覆盖：

1. bridge 首次创建。
2. bridge 二次 Ensure 幂等。
3. TAP 首次创建并 attach。
4. TAP 二次 Ensure 幂等。
5. 15 字符名校验。
6. 同名非 bridge/TAP 冲突。
7. `ErrDumpInterrupted` 不被吞。
8. 权限错误翻译。
9. `Delete` 不存在幂等成功。
10. 所有 `Manager` 方法的 canceled ctx 行为。
11. 未设置 `OwnerUID.Set` / `OwnerGID.Set` 返回 `linkerr.ErrInvalidRequest`，显式 `Value == 0` 可通过。
12. bridge `LinkAdd` 成功后 `AddrReplace` 失败会调用 `LinkDel` 回滚。
13. TAP `LinkAdd` 成功后 `LinkSetMaster` 失败会调用 `LinkDel` 回滚。
14. 回滚失败时错误可通过 `errors.Is` 同时命中主错误与清理错误。
15. 已存在 TAP 与请求的 VNET_HDR 模式冲突时返回 `linkerr.ErrConflict` 或 `linkerr.ErrUnsupported`。
16. `ListFilter{}` 返回 `linkerr.ErrInvalidRequest`。

请求校验矩阵覆盖：空 `Name`、超过 15 字符 `Name`、非法 `GatewayCIDR`、`MTU <= 0`、nil 或非法
MAC、空 `VNetHeader`、空 `ListFilter.Kind`、已存在 link 类型冲突。

## Acceptance 端到端验收

新增：

```text
test/acceptance/hostnet_link_test.go
```

验收链路：

```text
link/linux.Manager
  -> EnsureBridge
  -> EnsureTap
  -> QEMU direct-kernel boot CirrOS
  -> guest static IP via kernel cmdline ip=
  -> host ping guest
  -> QMP quit
  -> Delete TAP
  -> Delete bridge
```

### CirrOS direct-kernel 资源

在现有 disk image 缓存基础上扩展：

```text
.lima/cache/images/cirros-0.6.2-aarch64-kernel
.lima/cache/images/cirros-0.6.2-aarch64-initramfs
```

资源来自 CirrOS 0.6.2 官方下载目录：

```text
https://download.cirros-cloud.net/0.6.2/cirros-0.6.2-aarch64-kernel
https://download.cirros-cloud.net/0.6.2/cirros-0.6.2-aarch64-initramfs
```

`scripts/acceptance.sh` 在 `prepare_cache` 中像现有 disk image 一样下载这两个文件：先写入
`<target>.download.$$`，下载失败删除临时文件，下载成功后 `mv` 到最终路径。脚本应从同目录
`https://download.cirros-cloud.net/0.6.2/MD5SUMS` 获取 checksum，并缓存为
`.lima/cache/images/cirros-0.6.2-MD5SUMS`。脚本必须校验 disk、kernel、initramfs 三个 CirrOS
资源；checksum 失败必须删除损坏文件并失败退出。若 checksum 文件不可用，脚本不得静默跳过，
应报告明确错误。

`test/acceptance/doc.go` 需要同步记录新增环境变量及其含义。

新增环境变量：

```text
GOVIRTA_ACCEPTANCE_CIRROS_KERNEL=/govirta-cache/images/cirros-0.6.2-aarch64-kernel
GOVIRTA_ACCEPTANCE_CIRROS_INITRAMFS=/govirta-cache/images/cirros-0.6.2-aarch64-initramfs
```

### 固定网络参数

acceptance 使用显式固定参数：

```text
bridge name: gvbr0
tap name:    gvtap0
bridge MAC:  02:00:00:00:01:01
tap MAC:     02:00:00:00:01:02
bridge IP:   192.168.100.1/24
guest IP:    192.168.100.10/24
gateway:     192.168.100.1
MTU:         1500
owner UID:   0 (Set=true)
owner GID:   0 (Set=true)
VNET_HDR:    enabled
```

测试开始前显式调用 `Delete(tap)` 和 `Delete(bridge)` 清理上次残留；清理失败则测试失败。
由于使用固定 link 名称，该测试只允许在 `scripts/acceptance.sh full/linux` 启动的隔离 Lima guest
内运行。测试除 `GOVIRTA_ACCEPTANCE=1` 外，还应检查 `GOVIRTA_ACCEPTANCE_LIMA_GUEST=1`，
避免用户在真实 Linux 宿主机网络命名空间中误删同名接口。

### QEMU 参数

acceptance 内部直接使用 `exec.CommandContext(ctx, binary, args...)` 启动 QEMU，参数必须是
`[]string`，禁止拼接 shell command string；不复用或扩展 QEMU typed builder：

启动 QEMU 前必须先通过 `Get(ctx, "gvtap0")` 确认 TAP 已由 `link.Manager` 创建并 attach 到
`gvbr0`。QEMU 只能打开已存在 TAP；测试不得依赖 QEMU 或脚本隐式创建 TAP。

```text
qemu-system-aarch64
  -machine virt,accel=kvm
  -cpu host
  -m 256M
  -smp 1
  -kernel <GOVIRTA_ACCEPTANCE_CIRROS_KERNEL>
  -initrd <GOVIRTA_ACCEPTANCE_CIRROS_INITRAMFS>
  -append "console=ttyAMA0 ip=192.168.100.10::192.168.100.1:255.255.255.0:govirta-net:eth0:off"
  -drive file=<copy-of-cirros-disk>,if=virtio,format=qcow2
  -netdev tap,id=net0,ifname=gvtap0,script=no,downscript=no,vhost=on
  -device virtio-net-pci,netdev=net0,mac=02:00:00:00:01:02
  -qmp unix:<qmp.sock>,server=on,wait=off
  -serial unix:<serial.sock>,server=on,wait=off
  -display none
  -no-reboot
  -no-shutdown
```

### 连通性验证

第一版硬门槛是 host 到 guest：

```text
ping -c 3 -W 1 192.168.100.10
```

测试先等待 CirrOS serial boot/login marker，再循环 ping，整体超时 60 秒。失败时输出：

1. bridge/TAP `LinkInfo`。
2. QEMU argv。
3. QMP/serial 状态。
4. ping stdout/stderr。
5. `ip addr show` / `ip route show`。
6. serial captured output。
7. QEMU stderr。

acceptance harness 或 `scripts/acceptance.sh` 必须显式检查 `ip` 与 `ping` 命令可用；缺失时快速失败，
避免网络失败时诊断命令本身缺失。

guest 到 host 的串口登录后 `sudo ping 192.168.100.1` 不作为第一版硬门槛；后续可在串口命令交互器成熟后补充。

### 清理

`t.Cleanup` 顺序：

1. 通过 QMP `quit` 停 QEMU。
2. `Delete(tap)`。
3. `Delete(bridge)`。
4. 删除临时磁盘副本。

主体失败和清理失败同时发生时，用 `errors.Join(primaryErr, cleanupErr)` 保留全部错误。

## Acceptance 日志归档

完整测试日志写入：

```text
test/log/
```

如果目录不存在，由 `scripts/acceptance.sh` 显式创建。日志文件名使用日期、时间和模式，避免同一天多次运行互相覆盖：

```text
test/log/YYYY-MM-DD-HHMMSS-acceptance-<mode>.log
```

示例：

```text
test/log/2026-06-01-153045-acceptance-full.log
```

`full` / `linux` 模式记录完整输出，等价于：

```text
scripts/acceptance.sh full 2>&1 | tee test/log/<timestamp>-acceptance-full.log
```

日志由宿主机侧的 `scripts/acceptance.sh` 创建并写入 `test/log/`，不在 Lima guest 内直接写该路径。

日志至少包含：

1. Lima 准备、启动、删除过程。
2. cache 准备与 CirrOS kernel/initramfs/disk 检查。
3. `go test -v -tags acceptance -count=1 ./test/acceptance/...` 完整输出。
4. hostnet acceptance 的诊断输出。

`test/log/` 作为运行产物目录不提交日志文件。提交 `test/log/.gitkeep` 保留目录，并在 `.gitignore` 中忽略：

```text
test/log/*.log
```

## 验证命令

本地快速验证：

```bash
scripts/verify.sh
```

Linux acceptance：

```bash
scripts/acceptance.sh full
```

该命令在 Lima Linux guest 内以 `sudo -E` 执行：

```bash
go test -v -tags acceptance -count=1 ./test/acceptance/...
```

## 官方文档和来源

本设计阶段使用过的资料：

1. Context7 查询：
   - Library ID：`/vishvananda/netlink`
   - 命令：`ctx7 docs /vishvananda/netlink "How to create Linux bridge links, assign addresses, set link up, create TAP/TUN devices with VNET_HDR owner MTU, list links, delete links, and handle existing links in Go"`
2. `github.com/vishvananda/netlink` README 和源码。
3. Linux TUN/TAP 文档：`https://www.kernel.org/doc/Documentation/networking/tuntap.txt`。
4. Linux bridge 文档：`https://docs.kernel.org/networking/bridge.html`。
5. Linux bridge MAC 重算源码：`https://raw.githubusercontent.com/torvalds/linux/master/net/bridge/br_stp_if.c`。
6. CirrOS README：`https://github.com/cirros-dev/cirros/blob/main/README.md`。
7. CirrOS 0.6.2 release note：`https://github.com/cirros-dev/cirros/releases/tag/0.6.2`。
8. Linux kernel command line `ip=` 语义资料。

## 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| acceptance 需要 `CAP_NET_ADMIN` | 仅在 Lima/Linux acceptance 中运行，现有脚本使用 `sudo -E` |
| TAP/bridge 泄漏 | 测试开始和结束均显式 Delete；Delete 幂等 |
| bridge MAC 随 TAP 变化漂移 | bridge 创建和 reconcile 都显式设置 MAC |
| TAP MTU 创建时不生效 | 创建后显式 `LinkSetMTU` |
| 同一天多次验收日志覆盖 | 日志名包含日期和时间 |
| CirrOS cloud-init network-config 不稳定 | 不依赖 cloud-init network-config；使用 direct-kernel `ip=` |
| QEMU builder 范围膨胀 | acceptance 内部直接拼 direct-kernel QEMU 参数，本次不改 builder |
