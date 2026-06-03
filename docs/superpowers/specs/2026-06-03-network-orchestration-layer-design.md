# 网络编排层设计:仿存储结构的 VM 网络编排(guest 出外网闭环)

**日期**: 2026-06-03
**主题**: 在现有 `internal/hostnet/*` 主机网络原语之上,新增仿 `internal/storage` 结构的 VM-facing 网络编排层,打通 guest 出外网完整链路
**状态**: 设计已确认,待写实现计划

## 1. 背景与目标

### 1.1 现状

当前网络能力只有最底层原语,等价于 storage 的 driver 层:

- `internal/hostnet/link/` — bridge / TAP 原语(`link.Manager` 契约 + linux 实现 + noop + linkerr)
- `internal/hostnet/route/` — host IPv4 route 原语 + 转发就绪检查
- `internal/hostnet/firewall/` — nftables masquerade + endpoint 反欺骗原语
- `internal/hostnet/dhcp/` — CoreDHCP-backed 静态 MAC/IP 绑定原语
- `internal/network/bridge/` — 旧 skeleton(`NoopManager`,已被 `hostnet/link` 取代,死代码)

缺少 storage 那样的「VM-facing service → 共享注册核心 → driver」编排主链。

### 1.2 存储层参考结构(被仿照对象)

```
VolumeService / ImageService   (VM-facing 编排,VM 视角 API)
        ↓
pool.Service                   (命名池注册/记账/内存索引)
        ↓
block.Driver / image.Driver → local / localfile  (driver 契约 + 实现)
```

### 1.3 本次目标

仿存储结构,在 `internal/network/` 新增 VM 网络编排层,把 `link`/`route`/`firewall`/`dhcp` 四原语当 driver 编排起来,做到 **guest 真正出外网**的完整闭环,并通过 Lima 真实验收(CirrOS 经我们的编排自动获得 IP/默认路由/DNS,并 ping 通外网)。

### 1.4 已锁定的设计方向(七项澄清决策)

1. **新增网络编排服务层**(不是只重组目录,也不是只补中间层)。
2. **混合状态模型**:编排 service 无独立可漂移真实状态;只持有「逻辑网络定义」,真实资源状态永远现读现聚合。
3. **两个 VM-facing service 共享一个注册核心**(完全仿存储)。
4. **住进 `internal/network/`**,与 `internal/storage/` 完全对称。
5. **删除死包 `internal/network/bridge/`**,本次编排层完整住入,`node.Agent` 改组合新编排层。
6. **一路打到 guest 出外网**(B 范围,不是只做主机原语闭环,也不是只做骨架)。
7. **本次新增 firewall FORWARD-accept 原语**,出网正确性由 Govirta 显式拥有,不依赖宿主默认策略。

## 2. 整体架构与分层

```
                VM-facing 编排层 (internal/network/)
   NetworkService                NICService
   (共享网段:建网/删网/查网)              (单 VM 网卡:挂卡/卸卡/查卡)
        │                                       │
        └────────────────┬──────────────────────┘
                         ↓
            netpool 注册核心 (internal/network/netpool/)
   持有逻辑网络定义(网络名→网段/bridge名/地址段/egress/DHCP server ID)
   不缓存任何可漂移的真实资源状态;真实状态永远现读现聚合
                         ↓
        ┌────────────┬───────────┬────────────┬──────────┐
        ↓            ↓           ↓            ↓
   hostnet/link  hostnet/route hostnet/firewall hostnet/dhcp  (四原语 = driver 层,已存在)
   bridge/TAP    route+        masquerade+      静态绑定
                 forwarding检查 antispoof+
                               NEW forward-accept
```

### 2.1 与 storage 的精确对应

| storage | network | 角色 |
| --- | --- | --- |
| `VolumeService` | `NetworkService` | VM-facing,管理共享资源(池/网段) |
| `ImageService` | `NICService` | VM-facing,管理单元资源(镜像/网卡) |
| `pool.Service` | `netpool.Service` | 注册核心,持有逻辑定义 + 编排记账 |
| `block.Driver` / `image.Driver` | `link`/`route`/`firewall`/`dhcp` 的 `Manager` | driver 契约 |
| `local` / `localfile` | 各 `linux` 子包 | driver 真实实现 |

### 2.2 与 storage 的关键差异(显式承认,不机械照搬)

1. **storage 是一池一 driver;network 是一网多原语。** `netpool.Service` 的一个「网络」要编排 4-5 个原语(bridge + route 检查 + masquerade + forward-accept + dhcp),不是单一 driver 调用。这是领域差异,不强行掰成单 driver。
2. **storage 内存索引是容量记账投影;netpool 内存只存逻辑意图定义。** 不缓存真实资源状态(bridge 是否存在、lease 是否 bound、规则 handle 都现读),只存「这个网络/网卡该长什么样」的声明,重启由控制面重放。严格守住「实际资源即唯一事实标准」。
3. **依赖方向单向向下。** `internal/network/` 依赖 `internal/hostnet/*`,反向禁止。原语层完全不知道编排层存在,可独立替换(积木式拼接)。

### 2.3 包结构落点

```
internal/network/
├── service.go          # NetworkService (网段编排)
├── nic_service.go      # NICService (网卡编排)
├── netpool/            # netpool.Service 注册核心 + 逻辑网络/网卡定义
├── networker/          # 稳定错误哨兵 (仿 storage errors + 各 hostnet *err)
└── (原语在 internal/hostnet/*,作为 driver 被依赖)
```

### 2.4 死包清理

删除 `internal/network/bridge/`。证据:真实 Go 代码中仅 `internal/node/agent.go` 一处引用(skeleton nop 占位),其余引用全在文档/plan/spec(历史记录,不动)与 pre-push 钩子路径串(非 Go 引用)。`node.Agent` 改为组合新的 `NetworkService`/`NICService`,本次编排层完整住入,不留新老并存。

## 3. 数据模型与 netpool 注册核心

### 3.1 核心原则

`netpool.Service` 持有的是**逻辑意图定义**(声明式),**绝不缓存可漂移的真实资源状态**。真实状态永远通过四原语的 `Get`/`List` 现读现聚合。

### 3.2 逻辑网络定义

```go
// NetworkDefinition 是一个网段的声明式逻辑意图,由控制面显式注册,重启后重放。
// 只描述"该长什么样",不持有任何观测到的真实资源状态。
type NetworkDefinition struct {
    Name          NetworkName            // 显式网络标识,调用方提供,不自动生成
    BridgeName    link.Name              // 该网络的 host bridge 名
    BridgeMAC     net.HardwareAddr       // bridge 自身 MAC(显式,不自动生成)
    BridgeMTU     int                    // bridge MTU(显式,EnsureBridge 必需)
    Subnet        netip.Prefix           // 网段,如 192.168.100.0/24
    GatewayCIDR   netip.Prefix           // bridge 自身地址,如 192.168.100.1/24
    Pool          dhcp.AddressRange      // DHCP 可分配范围
    EgressIface   firewall.InterfaceName // 出网物理网卡(masquerade egress)
    DHCPServerID  dhcp.ServerID          // 关联的 DHCP 实例 ID
    Router        dhcp.DHCPOptionAddrs   // 下发给 guest 的默认路由(出网必须 enabled)
    DNS           dhcp.DHCPOptionAddrs   // 下发给 guest 的 DNS
    LeaseDuration time.Duration
    // firewall 表/链/owner/priority 等显式命名也在这里声明(masquerade + forward-accept 共用)
}
```

注:`BridgeMAC`/`BridgeMTU` 与 NIC 的 guest MAC 是不同对象(见 3.5);bridge MAC 同样由控制面显式提供,编排层不生成。

### 3.3 逻辑网卡定义

```go
// NICDefinition 是一块 VM 网卡的声明式逻辑意图。
type NICDefinition struct {
    NetworkName NetworkName      // 所属网络(必须已注册)
    VMID        VMID             // 归属 VM
    TapName     link.Name        // 该网卡的 host TAP 名
    MAC         net.HardwareAddr // guest 网卡 MAC(见 3.5 MAC 决策)
    IP          netip.Addr       // 分配给该 MAC 的静态 IP(在网络 Pool 内)
    TapMTU      int              // TAP MTU(显式,EnsureTap 必需)
    VNetHeader  link.VNetHeaderMode // VNetHeader 模式(显式,EnsureTap 必需)
    OwnerUID    int              // TAP owner(QEMU 运行用户)
    OwnerGID    int
}
```

### 3.4 netpool.Service 职责(仿 pool.Service)

```go
type Service struct {
    mu       sync.RWMutex
    networks map[NetworkName]*networkRecord  // 逻辑定义 + 关联 NIC 定义
    // 注入的四原语 Manager(driver):
    link     link.Manager
    route    route.Manager
    firewall firewall.Manager
    dhcp     dhcp.Manager
}
```

- `RegisterNetwork(def)`:校验逻辑定义合法性(网段/网关/pool 自洽、名字非空、原语命名显式),存入内存索引。**只注册逻辑意图,不碰内核**。仿 `pool.Service.RegisterPool`。
- 存逻辑定义时返回 service-owned 副本(仿 storage「返回克隆,调用方不能改内部索引」)。
- `EnsureNetwork(ctx, name)`:读逻辑定义,编排四原语 reconcile 内核(见第 4 节)。
- `EnsureNIC(ctx, networkName, vmID)`:编排 TAP + 绑定 + 反欺骗。
- 真实状态查询(`GetNetworkStatus`/`GetNICStatus`)现读原语 `Get`/`List` 聚合,不返回内存定义。

### 3.5 MAC 决策(已确认:方案 A 单 MAC 字段)

概念上存在两个 MAC:

| MAC | 是什么 | 谁消费 |
| --- | --- | --- |
| **guest 网卡 MAC** | guest OS 在 virtio-net 上看到的 MAC | QEMU `-device mac=` + DHCP 绑定键 + 反欺骗目标 |
| **TAP host 侧 MAC** | TAP 接口在宿主侧自己的 MAC | 仅 host 侧链路,对 guest 出网基本无语义影响 |

证据(已验收 `hostnet_dhcp_test.go`):guest MAC `02:...:01:02` 同值用于 `TapSpec.MAC` + `dhcp.ApplyBinding` + QEMU `-device mac=`;bridge MAC `02:...:01:01` 独立。

**决策**:`NICDefinition.MAC` 单字段(guest MAC),由控制面显式提供;`EnsureNIC` 原样透传给 TAP/DHCP/反欺骗三处。TAP host 侧 MAC 复用同一 guest MAC 值,是编排层的确定性规则,值 100% 来自调用方,**不构成隐式生成**。

**MAC 由谁生成**:控制面,显式传入。编排层**绝不**自动生成、推断或填默认。理由:(1) guest MAC 决定 DHCP 绑定/反欺骗/guest 网络身份,跨重启必须稳定可重放;若编排层随机生成,重启后会变,违反「控制面重放逻辑意图」模型。(2) 自动生成 MAC 等于隐式决定行为相关参数,违反「显式优于隐式」原则。MAC 唯一性、稳定性、分配策略是**控制面**职责,不在本编排层。

### 3.6 错误处理

新增 `internal/network/networker`(稳定错误哨兵):`ErrInvalidRequest` / `ErrNotFound` / `ErrAlreadyExists` / `ErrConflict` / `ErrNotReady` 等。所有原语返回的 `linkerr`/`routeerr`/`firewallerr`/`dhcperr` 用 `%w` 向上包装并归类,调用方可 `errors.Is`。多原语失败用 `errors.Join` 保留全部错误。

## 4. 编排数据流

### 4.1 EnsureNetwork:建网(共享网段)

```text
EnsureNetwork(ctx, name)
  1. 读逻辑定义(内存),校验已注册
  2. link.EnsureBridge(bridge, gatewayCIDR, MTU, MAC)
        → host bridge 起来,带网关地址
  3. route.CheckIPv4Forwarding(expected=enabled)
        → 只检查,未开启则 ErrNotReady(forwarding 是安装期职责)
  4. firewall.EnsureMasquerade(guestCIDR=subnet, egress=egressIface)
        → 源 NAT,guest 网段 → 出网网卡
  5. firewall.EnsureForwardAccept(guestCIDR, egress)   ← 本次新增原语
        → 显式放行 filter FORWARD,不靠宿主默认策略
  6. dhcp.Start(ServerSpec{bridge, subnet, pool, router=enabled, dns, ...})
        → DHCP 实例在 bridge 上监听;Router option 下发默认路由
  7. 现读各原语 Get 聚合 NetworkStatus 返回(不返回内存定义)
```

顺序理由:bridge 是载体必须最先;forwarding 是内核前提先验;NAT 和 forward 放行要在 DHCP 发地址前就位;DHCP 最后起。

### 4.2 EnsureNIC:挂卡(单 VM 网卡)

```text
EnsureNIC(ctx, networkName, vmID)
  1. 读 NIC 逻辑定义 + 所属 NetworkDefinition,校验网络已注册
  2. link.EnsureTap(tap, bridge, ownerUID/GID, MTU, MAC, vnethdr)
        → TAP 起来并 enslave 到 bridge
  3. dhcp.ApplyBinding(serverID, MAC, IP, hostname)
        → 静态 MAC→IP 绑定(幂等)
  4. firewall.EnsureEndpointAntiSpoofing(bridge, tap, MAC, IPv4)
        → 该 endpoint 反欺骗
  5. 现读聚合 NICStatus 返回
```

QEMU 之后用这个 TAP 名(`-netdev tap`)。编排层**不创建 QEMU 进程**,只准备好 TAP——和 storage「只准备 qcow2,不 spawn QEMU」对称。

### 4.3 完整出外网链路(B 目标端到端)

```text
控制面 RegisterNetwork + RegisterNIC(逻辑意图)
  → EnsureNetwork:bridge + forwarding 检查 + masquerade + forward-accept + DHCP(router=网关)
  → EnsureNIC:TAP + MAC/IP 绑定 + 反欺骗
  → QEMU 启动消费 TAP
  → guest DHCP DISCOVER/REQUEST
      → 拿到 IP + 默认路由(router option)+ DNS
  → guest ping 外网
      → 出 TAP → bridge → host FORWARD(放行)→ masquerade(源 NAT)→ egress 网卡 → 外网
      → 回包反向经 NAT/forward 回到 guest
```

### 4.4 失败回滚:Ensure 永不主动拆资源

编排是多原语副作用,中途失败用 `errors.Join` 保留主错误 + 回滚错误(仿 storage cleanup 约定)。但回滚策略是:

- **`Ensure*` 永不主动拆已建资源。** Ensure 是幂等 reconcile,重试会补齐;拆 bridge 可能影响已挂的其它 NIC。失败只返回错误 + 已完成步骤的观测状态,让控制面决定重试或显式 Delete。
- **只有显式 `Delete*` 才真正拆资源。** 避免重试误伤。

### 4.5 Delete 语义

- `DeleteNIC`:删反欺骗 → 删绑定 → 删 TAP(逆序),`errors.Join` 保留全部错误。
- `DeleteNetwork`:停 DHCP → 删 forward-accept → 删 masquerade → 删 bridge(逆序);若仍有 NIC 挂在该网络则 `ErrConflict` 拒绝(仿 storage 池非空不能删)。

### 4.6 幂等与并发

- 所有 `Ensure*` 幂等:重复调用收敛到同一状态(底层原语已幂等)。
- `netpool.Service` 用 `mu` 保护逻辑定义索引;编排真实原语时不长期持锁(避免阻塞),仿 DHCP manager 锁策略。

## 5. 新增 firewall FORWARD-accept 原语

### 5.1 为什么需要

masquerade(nat 表 postrouting)只做源地址转换,不保证包被转发。guest 出网还要穿过 filter 表 FORWARD 链;若宿主 FORWARD 默认策略是 DROP(装了 Docker/firewalld 很常见),masquerade 后的包仍被丢。当前 firewall 包没有 forward-accept 原语,出网正确性靠环境碰运气。本原语让 Govirta **显式拥有**这段放行。

### 5.2 契约(严格仿现有两个 Ensure 原语)

在 `firewall.Manager` 接口新增:

```go
// EnsureForwardAccept creates or reconciles the explicit filter-forward accept
// rule that allows guest CIDR traffic to and from the egress interface.
EnsureForwardAccept(ctx context.Context, spec ForwardAcceptSpec) (RuleInfo, error)

// DeleteForwardAccept removes the forward-accept rule selected by ref.
DeleteForwardAccept(ctx context.Context, ref RuleRef) error
```

### 5.3 ForwardAcceptSpec(全字段显式,仿 MasqueradeSpec)

```go
// ForwardAcceptSpec describes the complete desired filter-forward accept state.
// TableName, ChainName, RuleOwner, GuestCIDR, EgressInterfaceName, and Priority
// are all behavior-affecting fields and must be explicitly supplied by callers.
type ForwardAcceptSpec struct {
    TableName           TableName     // filter 表(显式)
    ChainName           ChainName     // forward 链(显式)
    RuleOwner           RuleOwner
    GuestCIDR           netip.Prefix  // 放行的 guest 网段
    EgressInterfaceName InterfaceName // 出网卡
    Priority            Priority
}
```

### 5.4 行为(对齐既定 firewall 边界)

- 创建 Govirta-owned filter forward accept 规则,放行 `GuestCIDR ↔ EgressInterface` 双向转发。匹配维度与 masquerade **精确对称**(masquerade 只按 `ip saddr {GuestCIDR}` + `oifname {egress}` 匹配,不限 iif/bridge),forward-accept 作为其转发侧搭档采用同一匹配哲学,**不引入 bridge 维度**(因此 `ForwardAcceptSpec` 无 `BridgeName` 字段,与 `MasqueradeSpec` 字段集对称)。
- **forward-accept 是双向规则组(两条规则),不是单条规则**,这点像 anti-spoofing 的 group:
  - 出向规则:`ip saddr {GuestCIDR} oifname {egress} accept`
  - 回向规则:`ip daddr {GuestCIDR} iifname {egress} ct state established,related accept`
  - 回向必须由 Govirta **自己显式拥有** `ct state established,related accept`,不能赌宿主已有 established 放行规则——否则 DROP 策略宿主下出网回包仍被丢。
- 只管 Govirta-owned 规则,用 owner/purpose/guard 识别;**绝不 flush/删非 Govirta 规则**(现有铁律)。
- `Ensure` 返回观测状态(`RuleInfo` + 新增 `ForwardAcceptSummary`,把双向组压成一个逻辑 summary),不 echo spec。
- **不改 FORWARD 链默认策略**(改默认策略影响全局,是宿主/安装期职责,和 ip_forward 同类)——只**加 Govirta-owned 显式 accept 规则组**。
- 不创建 bridge/TAP,不改 forwarding,不碰 route。

### 5.5 新增类型与常量

- `RulePurpose` 增加 `RulePurposeForwardAccept` 常量。
- `RuleSummary` 增加 `ForwardAccept *ForwardAcceptSummary` 指针(三选一互斥变四选一)。
- `ForwardAcceptSummary{GuestCIDR, EgressInterfaceName, Priority}`(仿 `MasqueradeSummary`)。
- 双向规则组用 guard 区分两条规则(仿 anti-spoofing 的 guard 机制):新增 `guardForwardEgress` / `guardForwardReturn` 两个 `endpointGuardKind` 常量,写入各自规则的 user data。
- linux 实现走 anti-spoofing 同款的**规则组路径**(`ensureDesiredRuleGroup` / 组式校验匹配),不是 masquerade 的单规则路径。
- nftables expr 构造(`expr_linux.go`)增加 `forwardEgressAcceptExprs`(`ip saddr` + `oifname` + accept)和 `forwardReturnAcceptExprs`(`ip daddr` + `iifname` + `ct state established,related` + accept);解析增加 `parseForwardAccept`,把两条观测规则压回一个 `ForwardAcceptSummary`。
- filter 表 forward 链:`chainForDesired` / `validateExistingChain` 增加 `RulePurposeForwardAccept` 分支(IPv4 family + `ChainTypeFilter` + `ChainHookForward`)。

### 5.6 边界张力(显式承认)

「加一条 forward accept 规则」与「宿主 FORWARD 默认 DROP」并存时,能否放行取决于规则优先级和宿主其它规则。Govirta 加的是一条高优先级 accept,但若宿主有更高优先级 DROP,仍可能被挡。本原语保证「Govirta 显式放行存在」,**不保证**「覆盖宿主一切敌对规则」——后者不是可能的契约。Lima 干净环境下这条 accept 足以让 guest 出网;复杂宿主策略冲突是节点准备职责。此边界写进 spec 与 AGENTS。

### 5.7 计划影响

独立任务:契约(`firewall.go` + `constants.go`)→ linux 实现(`rules_linux.go` + `expr_linux.go` + `validate_linux.go`)→ 单元测试(fake handle)→ noop 更新。沿用 firewall 包既有结构,不新建子包。

## 6. 测试与 Lima 验收

### 6.1 单元测试范围

不依赖真实内核/QEMU/网络,全部用 fake 原语 Manager。

**编排层(`internal/network/`)**:
- 注入 fake `link`/`route`/`firewall`/`dhcp` Manager,验证 `EnsureNetwork`/`EnsureNIC` 的编排顺序、参数透传、幂等、失败时 `errors.Join`、Ensure 不主动拆资源、Delete 逆序。
- `netpool.Service` 注册校验:网段/网关/pool 自洽、名字非空、IP 在 pool 内、IP 不重复、返回克隆不可外部 mutate、未注册网络挂卡报 `ErrNotFound`、网络仍有 NIC 时 `DeleteNetwork` 报 `ErrConflict`。
- MAC 透传:fake 验证 `EnsureNIC` 把 `NICDefinition.MAC` 原样传给 TAP/DHCP/反欺骗三处,编排层不生成。
- 状态查询现读:验证 `GetNetworkStatus` 调用原语 `Get`/`List` 而非返回内存定义。

**新增 firewall 原语(`internal/hostnet/firewall`)**:
- 沿用现有 `fake_handle_test.go` 模式,验证 `EnsureForwardAccept` 幂等 reconcile、owner/purpose 识别、不碰非 Govirta 规则、`parseForwardAccept` 还原 summary、校验拒绝缺字段。

### 6.2 本地验证

```bash
scripts/verify.sh
go test -race -count=1 ./internal/network/... ./internal/hostnet/firewall/...
```

### 6.3 Lima 验收:guest 真出外网(B 核心证据)

新增 `test/acceptance/network_egress_test.go`(`//go:build acceptance`):

```text
1. 控制面式调用:RegisterNetwork + RegisterNIC(逻辑意图)
2. EnsureNetwork:bridge + forwarding检查 + masquerade + forward-accept + DHCP(Router=网关 enabled, DNS=enabled)
3. EnsureNIC:TAP + MAC/IP绑定 + 反欺骗
4. 启动 CirrOS,网卡接 TAP
5. 等 QMP running + serial 可用
6. guest 经 DHCP 自动拿到 IP + 默认路由 + DNS
7. 【核心断言】guest serial 执行 ping 外部地址成功
```

### 6.4 出外网验收目标地址(已确认:分两步断言)

- **第一步**:ping `8.8.8.8`(纯 IP,验证 NAT + forward + 默认路由整条链,不依赖 DNS)——B 的硬核证据。
- **第二步**:ping 域名(如 `one.one.one.one`,验证 DNS 下发生效)。

两步分开断言,失败时能精确定位是转发链问题还是 DNS 问题。

### 6.5 CirrOS metadata 延迟风险(显式正视)

上次 DHCP 验收特意关掉 Router option 以避开 CirrOS 探测 `169.254.169.254` metadata 的长延迟。B 要求出网必须开 Router option(下发默认路由),延迟会重现。处理方案(实现期需 Lima 实测确认):

- 用 `ds=nocloud`/`ds=none` 等 kernel cmdline 显式抑制 CirrOS metadata 探测(上次 `ds=nocloud` 在 0.6.2 上效果不彻底,本次需实测确认有效参数)。
- 或调长 serial 等待超时,容忍 metadata 探测延迟后再验证出网。
- 失败诊断必须输出:`NetworkStatus`/`NICStatus`、nft ruleset、`ip route`/`ip addr`(host + guest)、DHCP lease、QEMU argv、serial tail。

标注为「acceptance 实现期需 Lima 实测调参」,不假装零成本。

### 6.6 验收不扩大范围

本次验收**只证明**:经新编排 API 一键建网 + 挂卡,CirrOS 自动获得 IP/路由/DNS 并能 ping 通外网。**不含**:多 VM 并发、IPv6、控制面持久化、govirtlet 重启 reattach、复杂宿主防火墙策略共存。

## 7. 约束与边界小结

- 编排层依赖 `internal/hostnet/*` 原语,反向禁止;原语层不知编排层存在(积木式拼接)。
- `netpool` 只存逻辑意图,绝不缓存可漂移真实状态;真实状态现读现聚合(上下一致,实际资源即唯一事实标准)。
- 所有行为相关参数(网络名、bridge 名、网段、MAC、IP、egress、DHCP server ID、firewall 表/链/owner/priority)由控制面显式提供;编排层不自动生成、推断、填默认。
- MAC 由控制面生成与分配;编排层只透传。
- forwarding(ip_forward)是安装期职责,编排层只 `Check`,不开启。
- FORWARD-accept 原语只加 Govirta-owned 显式 accept 规则,不改宿主 FORWARD 默认策略,不动非 Govirta 规则。
- 删除死包 `internal/network/bridge/`,`node.Agent` 改组合新编排层,不留新老并存。
- 多原语失败用 `errors.Join` 保留全部错误;`Ensure` 永不主动拆资源,只有显式 `Delete` 才拆。
- 验收必须通过 Lima 实际测试:CirrOS 经编排自动获得 IP/路由/DNS 并 ping 通外网(分两步:8.8.8.8 + 域名)。

## 8. 官方文档引用

- 不涉及新的第三方 SDK/库(CoreDHCP / netlink / nftables 已在前序工作引入并固定版本)。本次仅在既有原语之上新增编排层 + 一个 firewall 原语。
- nftables filter forward accept 规则语义参考既有 `internal/hostnet/firewall/linux` 实现模式(masquerade / anti-spoofing)。
