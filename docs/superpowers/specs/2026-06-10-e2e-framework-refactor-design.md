# e2e 测试框架重构设计：guest-side live 断言与顶层权威门禁

**日期**：2026-06-10
**状态**：设计已确认，待写实现计划
**关联**：memory 1061、note #34（刀 4 遗留前置项）；刀 4 冷快照 spec items 2/5（guest live 验证缺口）

## 背景与动机

刀 4 冷快照实现时暴露一个结构性缺口：spec items 2/5 要求「建快照后进 guest 用 `qemu-img snapshot -l` 看到内部快照、删快照后看不到」，但现有 e2e 测试架构无法在**资源存活期**进 Lima guest 做 live 实况验证：

- `test/e2e/closure_test.go` 是跑在 macOS host 上的 Go 测试，只走 govirtctl HTTP——只能看 API 层结果（status/404/409），摸不到 Lima guest 文件系统/内核里的 live 实况。
- `scripts/e2e.sh` 的 `verify_no_orphans` 虽然能进 guest（`limactl shell`），但它在 go test **整个跑完后**才调一次，无法在「快照存活期」这种生命周期中途点做验证。

因此 items 2/5 当时被接受为现状（选项 A），并记入 backlog（memory 1061 / note #34），作为下一个功能刀之前的前置项。

用户将本次重构明确定位为「**e2e 成为顶层权威门禁**，确保任何人修改代码功能不会出现问题」。

## 开源调研结论（第十七章：自研前先调研）

子代理联网调研了 Go 虚拟化/基础设施项目的 e2e 测试框架方案（kubevirt、k8s-sigs/e2e-framework、Ginkgo/Gomega、testcontainers-go、纯 testing+helper 模式），核心结论 `[已验证]`：

**没有任何现成框架天然解决「在 KVM guest 存活期 exec 进 guest 做 live 验证」**。三大对标项目全部自建 guest-access helper：
- **kubevirt**：Ginkgo v2 BDD + 自建 `console`/`libwait`/`matcher` helper（绑 K8s client-go，代码不可复用，模式可借鉴）。
- **firecracker-go-sdk**：纯 `testing` + testify + `golang.org/x/crypto/ssh`，VM 运行期 SSH 进 guest 执行命令再做快照——与本项目诉求几乎同构。
- **k8s e2e**：纯 `wait.PollUntilContextTimeout` + 自建 `ssh.SH` helper。

框架（Ginkgo/e2e-framework）只提供外层时序编排骨架；guest-side live 断言能力是必须自研的核心资产。

**决策**：采用自研轻量 helper 路径（不引入第三方测试框架），与项目「最小依赖、纯 Go testing + build tags」铁律一致。外层编排用标准 `testing` + `t.Run` 已足够。

## 架构定位

e2e 升级为**顶层权威门禁**：改任何代码功能后，e2e 是「确保功能正确」的最终权威关卡。三层门禁分层保留，本次重构只触及 e2e 层：

| 层 | 入口 | 范围 | 反馈速度 | 本次是否改动 |
| --- | --- | --- | --- | --- |
| 单元/race | `verify.sh` | 纯逻辑单元测试 | 秒级（macOS 本地） | 不动 |
| 原语 acceptance | `acceptance.sh full` | hostnet 原语真实 KVM 验证 | ~5min（Lima guest） | 不动 |
| 分布式 e2e | `e2e.sh full` | 分布式 spine 闭环 + guest live 实况 | ~25s（etcd+govirtad+Lima govirtlet） | **本次强化** |

「顶层权威门禁」的语义 = e2e 不仅验证 API 层闭环（status/404/409），还验证 guest 内 live 实况（qcow2 快照条目、零孤儿）。改任何代码若造成「API 说成功但 guest 实况不符」，e2e 现在能抓到（之前抓不到，正是 items 2/5 的缺口）。

非目标：本次不追求把三层物理合一成一个入口（用户已确认选 B：分层保留、强化 e2e，而非 A 三层合一）。单元测试的秒级反馈与 e2e 的分钟级真实验证是不同价值，强行合一会让「改一行纯逻辑也要起 Lima KVM」，反噬开发效率。

## 双层 guest 拓扑（重构要解决的根本约束）

```
macOS host
 ├── go test (closure_test.go)  ← 测试逻辑在这里，走 govirtctl HTTP
 ├── govirtad (master) + etcd 容器
 └── Lima guest (KVM VM)
      ├── govirtlet (node)      ← 拨回 host master
      ├── 真实 qcow2 / bridge / TAP / nftables  ← live 实况在这里
      └── nested QEMU guest (VM 实体)
```

根本矛盾：go test 在 host、要验证的 live 资源在 Lima guest 文件系统/内核里。当前 closure_test 只走 govirtctl HTTP（看 API 结果），摸不到 guest live 实况——这是 items 2/5 落不了地的根因。

## 解法骨架（已锁定的 5 个决策）

1. **guest-exec 下沉进 Go 层**（决策 A）：e2e.sh 把 Lima instance name + LIMA_HOME 经 env 传给 go test；Go 层封装 `limactl shell <instance> -- <cmd>` 获得进 Lima guest 执行命令的能力。
2. **单一 `Guest` handle 对象**（决策 A）：所有 guest live 验证挂在一个 handle 上，语义化方法逐步沉淀成框架资产。
3. **生命周期阶段 helper + 声明式 guest 断言钩子**（决策 A）：apply→waitReady→`afterReady` 钩子→delete→waitGone→`afterGone` 钩子，骨架复用、断言声明式插入。
4. **`verify_no_orphans` 迁进 Go**（决策 A）：teardown 后 orphan 检查统一为 Guest handle 方法，shell 退回纯环境编排器。
5. **不引入第三方测试框架**：自研轻量 helper（开源调研结论）。

## 组件 1：Guest handle（`test/e2e/guest.go`）

`//go:build e2e`，与 closure_test 同包。

### 构造
```go
type Guest struct {
    t        *testing.T
    instance string   // 从 GOVIRTA_E2E_LIMA_INSTANCE 读
    limaHome string   // 从 GOVIRTA_E2E_LIMA_HOME 读
}

func newGuest(t *testing.T) *Guest  // 读 env，缺失则 t.Fatalf（与现有 requireEnv 同款）
```

### 底层原语（所有 guest 验证的基石）
```go
// Exec 进 Lima guest 执行命令，返回结构化结果。封装 limactl shell。
// err 是 limactl/连接层失败；exitCode 是 guest 内命令退出码；两者分离，
// 调用方据此区分"无法进 guest"与"进了但命令失败"。
func (g *Guest) Exec(ctx context.Context, cmd string) (stdout, stderr string, exitCode int, err error)
```
对标 k8s `ssh.SH` / firecracker `session`：返回 stdout/stderr/exitCode 三元组 + error。

实现伪代码（spec review B3 据实核实 e2e.sh:263/295 的真实调用形态后明确）：
```go
func (g *Guest) Exec(ctx, cmd) (stdout, stderr string, exitCode int, err error) {
    // LIMA_HOME 经进程环境传给 limactl（与 e2e.sh `LIMA_HOME=... limactl shell` 同款）。
    // guest 命令用 `sh -c <cmd>` 包裹，使 exitCode 是 guest 内命令的退出码。
    c := exec.CommandContext(ctx, "limactl", "shell", g.instance, "--", "sh", "-c", cmd)
    c.Env = append(os.Environ(), "LIMA_HOME="+g.limaHome)
    var outBuf, errBuf bytes.Buffer
    c.Stdout, c.Stderr = &outBuf, &errBuf
    runErr := c.Run()
    // exitCode 从 *exec.ExitError 取（guest 命令非零退出）；其它错误是连接层失败。
    var exitErr *exec.ExitError
    switch {
    case runErr == nil:
        exitCode = 0
    case errors.As(runErr, &exitErr):
        exitCode = exitErr.ExitCode()   // guest 命令退出码，err 仍为 nil（命令层失败由调用方据 exitCode 判断）
    default:
        err = runErr                    // limactl 本身失败/ctx 取消（连接层）
    }
    return outBuf.String(), errBuf.String(), exitCode, err
}
```
注意：不带 `--workdir`（orphan/qcow 检查用绝对路径，不需要 repo 工作目录；与 e2e.sh setup 段的 `--workdir /govirta-src` 不同，那是为 `go build` 设的）。

### 语义化 live 查询 / 断言方法（逐步沉淀的框架资产）
建立在 `Exec` 之上，把「进 guest 问什么」收敛成可发现的 API：
```go
// 快照 live 实况（items 2/5 落地）
func (g *Guest) QcowSnapshots(ctx context.Context, qcowPath string) ([]string, error)  // 解析 qemu-img snapshot -l 的 tag 列表
func (g *Guest) AssertQcowHasSnapshot(ctx context.Context, qcowPath, tag string)       // 建后验证：tag 在
func (g *Guest) AssertQcowNoSnapshot(ctx context.Context, qcowPath, tag string)        // 删后验证：tag 不在

// orphan 检查（verify_no_orphans 迁进来，逐项对应）
func (g *Guest) AssertNoQEMUProcess(ctx context.Context, vmUID string)
func (g *Guest) AssertNoTAP(ctx context.Context, tapName string)
func (g *Guest) AssertNoBridge(ctx context.Context, bridge string)
func (g *Guest) AssertNoNftablesChain(ctx context.Context, chain string)
func (g *Guest) AssertNoQcow2(ctx context.Context, qcowPath string)
```

### 设计点
- **错误 vs 断言分离**：`Exec` 返回 error（连接层）让调用方决策；`Assert*` 方法内部 `t.Fatalf`（断言层），调用点干净。
- **派生复用 identity 包**（消除 shell 脆弱点 + 字节级一致）：TAP 名、nftables 链名直接 import `internal/node/identity` 的权威派生（详见组件 2），不在测试侧重抄 sha256 逻辑，也不依赖 e2e.sh 注释。
- **orphan 断言迁移必须保留现 shell 的三条防御语义（spec review M1/M2/M3）**：
  - **M1 读不到=硬失败**：`AssertNoNftablesChain` 若 `nft list ruleset` 本身执行失败（exitCode≠0 或连接层 error），必须 `t.Fatalf`，绝不当成「读不到=无链=通过」（现 shell line 117-122 的关键防御，迁移不能丢）。
  - **M2 sudo**：guest 内 orphan 检查多数需 `sudo`（`sudo nft list ruleset`、`sudo test -e <qcow2>`）。`Assert*` 方法内部按需在命令前加 `sudo`，调用方不感知。
  - **M3 pgrep 自匹配规避**：`AssertNoQEMUProcess` 用 `pgrep -f "<runtime_root>/<vmUID>"` 匹配 QEMU 进程；经 `limactl shell` 执行时进程树与原 shell 不同，需重新验证不会匹配到检查命令自身（现 shell line 95-103 有精巧规避），实现时实跑确认。

## 组件 2：guest 路径/身份派生（`test/e2e/guest_paths.go`）

**关键设计修正（spec review B1/B2 据实核实后）**：内核身份派生（TAP 名、nftables 链名）**不在测试侧重新实现**——`test/e2e` 与产品代码同属一个 module，可直接 `import "github.com/suknna/govirta/internal/node/identity"`，调用权威派生函数，字节级一致风险归零（不再依赖 e2e.sh 注释或重抄 sha256 逻辑）：

```go
import "github.com/suknna/govirta/internal/node/identity"

// TAP 名：复用权威派生，不重新实现。
//   identity.DeriveNICIdentity(vmUID, nicIndex).TapName
// 实测 identity.go:116 = "gv" + sha256(vmUID)[:8] + "." + nicIndex（tapHashHexLen=8），
// 与刀 4 verify_no_orphans 依赖的派生同源。
//
// nftables 链名：同样复用权威派生，不重抄前缀常量。
//   identity.DeriveNICIdentity(vmUID, nicIndex).AntiSpoofChain   // gv-as-<tap>
//   identity.DeriveNetworkIdentity(network).MasqueradeChain      // gv-masq-<network>
//   identity.DeriveNetworkIdentity(network).ForwardChain         // gv-fwd-<network>
```

**qcow2 路径**派生在测试侧重建（`local.Driver` 的 `pathForCreate` 是 unexported，无法直接复用），但以**真实权威输入**对齐而非拼凑：

```go
// guestQcowPath 对齐 local.Driver：poolRoot = filepath.Join(storageRoot, "pool", poolName)，
// 然后 <poolRoot>/<vmUID>/<vmName>-disk-<diskIndex>.qcow2。
//
// storageRoot 的权威来源是 StoragePool manifest 的 spec.storageRoot
// （01-storagepool-block.json = "/var/lib/govirta/block"），不是 e2e.sh 的 state_root
// 拼 "/block"——后者是隐式耦合。测试从 manifest 读 storageRoot 或用与 manifest 一致的常量。
func guestQcowPath(storageRoot, poolName, vmUID, vmName string, diskIndex int) string
```

**guest 固定路径**（runtime/image root）仍迁成 Go 常量，但权威性标注修正：

```go
const (
    // guestStateRoot 是 e2e 约定的 guest 状态根；e2e.sh 的 guest_state_root 必须等于此值
    // （跨语言契约，见组件 5）。注意：block pool 的 qcow2 根由 StoragePool manifest 的
    // spec.storageRoot 决定（= guestStateRoot + "/block"），那才是 guestQcowPath 的权威输入。
    guestStateRoot   = "/var/lib/govirta"
    guestRuntimeRoot = guestStateRoot + "/runtime"
    guestImageRoot   = guestStateRoot + "/images"
)
```

qcow2 路径派生（唯一测试侧重建的派生）有独立单测（不依赖 Lima）。TAP/链名因复用 identity 包，其正确性已由 identity 包自身单测保证，测试侧只验证「调用对了 identity 函数」。

## 组件 3：生命周期阶段 helper（`test/e2e/lifecycle.go`）

把「apply→waitReady→delete→waitGone」骨架抽成带 guest 断言钩子的可复用动作：
```go
type resourceLifecycle struct {
    manifest  string                       // 文件名，如 "08-snapshot.json"
    kind      string                       // "Snapshot"
    name      string                       // "snap-e2e"
    waitPhase string                       // "ready"
    waitFor   time.Duration

    afterReady func(ctx context.Context)   // ready 后的 guest live 断言钩子（可选）
    afterGone  func(ctx context.Context)   // 404 后的 guest live 断言钩子（可选）
}

// applyAndVerify: apply manifest → 轮询到 waitPhase → 跑 afterReady 钩子
func applyAndVerify(ctx context.Context, t *testing.T, ctl, server string, spec resourceLifecycle)

// deleteAndVerify: delete → 轮询到 404 → 跑 afterGone 钩子
func deleteAndVerify(ctx context.Context, t *testing.T, ctl, server string, spec resourceLifecycle)
```
骨架复用现有 `runCtl`/`waitObjectPhase`/`waitGone` 逻辑，钩子是声明式插入点。

### items 2/5 落地（第一个钩子实例）
```go
qcow := guestQcowPath(poolBlock, vmUID, vmName, diskIndex)
applyAndVerify(ctx, t, ctl, server, resourceLifecycle{
    manifest: snapshotManifestName, kind: "Snapshot", name: snapName, waitPhase: "ready",
    afterReady: func(ctx context.Context) { g.AssertQcowHasSnapshot(ctx, qcow, snapUID) },  // 建后 guest 看到
})
deleteAndVerify(ctx, t, ctl, server, resourceLifecycle{
    kind: "Snapshot", name: snapName,
    afterGone: func(ctx context.Context) { g.AssertQcowNoSnapshot(ctx, qcow, snapUID) },     // 删后 guest 看不到
})
```

### 边界：哪些进 helper、哪些保留手写
- **进 helper**：标准生命周期（apply→ready→delete→gone）的资源——7 类一等公民的常规路径。
- **保留手写**（不强塞进 helper，会扭曲）：VM 的 powerState 多变体序列（Off→On→Shutdown→Off）、admission 拒绝场景（create Shutdown→400、引用保护→409）。它们继续用现有 `applyVMVariant`/`expectShutdownCreateRejected`/`expectReferencedRejection`，但可调用 Guest handle 加 live 断言。

## 组件 4：closure_test 接入（`test/e2e/closure_test.go`）

- 构造 Guest handle（`g := newGuest(t)`）。
- 快照场景改用 lifecycle helper + 钩子，落地 items 2/5。
- teardown 后调用迁移来的 orphan 断言（`g.AssertNoQEMUProcess/TAP/Bridge/NftablesChain/Qcow2`），等价替换原 shell `verify_no_orphans`。

## 组件 5：e2e.sh 职责收缩（`scripts/e2e.sh`）

e2e.sh 退回**纯环境编排器**：
1. 起停 Lima guest + etcd 容器 + govirtad（不变）。
2. guest 内构建启动 govirtlet（不变）。
3. 把环境坐标经 env 传给 go test（强化），然后 `go test` 一次跑完整个 closure，不再内嵌验证逻辑。

**移除** `verify_no_orphans` 那 ~80 行 guest 检查 shell（迁进 Go 层 Guest handle）。e2e.sh 不再有任何 `limactl shell` 验证逻辑。

### env 契约（go test ← e2e.sh）
```
GOVIRTA_E2E=1                    # 启用门禁（不变）
GOVIRTA_E2E_SERVER=<master url>  # govirtctl --server（不变）
GOVIRTA_E2E_GOVIRTCTL=<binary>   # govirtctl 路径（不变）
GOVIRTA_E2E_MANIFESTS=<dir>      # manifest 目录（不变）
GOVIRTA_E2E_NODE=<node name>     # 已存在：节点名（e2e.sh line 317 已传，spec 原漏列）
GOVIRTA_E2E_LIMA_INSTANCE=<name> # 新增：Lima instance name（Guest.Exec 用）
GOVIRTA_E2E_LIMA_HOME=<path>     # 新增：LIMA_HOME（limactl 定位 instance）
```
`newGuest(t)` 读后两个，缺失 `t.Fatalf`（与现有 `requireEnv` 同款门禁）。

### 跨语言路径契约（诚实的权衡标注）
state_root 这类值天然跨 shell（建环境）和 Go（验证路径）两侧，无法做到纯单一来源（shell 起 guest 时 Go 还没跑）。取舍：**Go 常量是权威定义，e2e.sh 引用它的值并在注释里标注来源**（`guest_state_root` 必须等于 Go 的 `guestStateRoot`）。不追求消除 shell 侧那一份（那需要 go 生成 shell 片段，过度工程），用注释 + 本 spec 契约锁死「改一处必须改另一处」。

## 验证策略（重构本身怎么证明对）

1. **Go 单测覆盖纯逻辑**：路径派生（TAP 名哈希、qcow2 路径模板）、`qemu-img snapshot -l` 输出解析（`QcowSnapshots` 解析逻辑）抽成可单测纯函数，加单测，不依赖 Lima。
2. **真实 e2e full 跑通是最终铁证**：`scripts/e2e.sh full` 跑通且新的 guest live 断言全部命中——快照 `afterReady`（`qemu-img snapshot -l` 看到 `snap-e2e-001`）+ `afterGone`（看不到）+ 迁移后的 orphan 检查全过。这是「重构等价 + 新能力生效」的双重证明。

## 范围边界（明确不做什么）

- **不碰 verify.sh / acceptance.sh**：只重构 e2e 层。
- **不引入第三方测试框架**（Ginkgo/Gomega/e2e-framework）：自研轻量 helper。
- **不一次性重写整个 closure**：渐进式——建 Guest handle + lifecycle helper + 迁 orphan 检查 + 快照场景做第一个钩子样板；其余资源 live 断言作为后续演进。
- **不改 manifest / 不改任何被测产品代码**：纯测试基础设施重构，被测的 7 类资源行为不变。

## 交付物清单

- `test/e2e/guest.go`：Guest handle + Exec + 语义化 live 查询/断言方法。
- `test/e2e/guest_paths.go`：guest 路径/TAP 名/nftables 链名 Go 派生 + 常量。
- `test/e2e/guest_paths_test.go`：派生纯函数单测（不依赖 Lima）。
- `test/e2e/lifecycle.go`：resourceLifecycle spec + applyAndVerify/deleteAndVerify + 钩子。
- `test/e2e/closure_test.go`：接入 Guest handle，快照场景用钩子落地 items 2/5，teardown 后调迁移来的 orphan 断言。
- `scripts/e2e.sh`：移除 verify_no_orphans shell，新增 2 个 env，注释标注跨语言路径契约。

## 后续演进（不在本次范围）

- 其余 6 类资源的 guest-side live 断言增强（用 lifecycle 钩子逐步补，任何人加断言就是加钩子）。
- 未来若 e2e 矩阵爆炸（大量 VM×网络×存储组合、需并行/标签/报告），再评估是否引入 Ginkgo（kubevirt 同款）——当前 YAGNI。
