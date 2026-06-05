# 虚拟机领域层（`internal/vmm`）设计

**日期:** 2026-06-05
**状态:** 已批准设计，待编写实现计划
**范围:** 节点本地 QEMU 进程生命周期管理领域层

## 背景与目标

Govirta 当前已具备 QEMU argv 构建（`pkg/virt/qemu`）、QMP 控制边界（`pkg/virt/qmp`）、qemu-img 镜像管理（`pkg/virt/qemuimg`）、存储层（`internal/storage`）与网络编排层（`internal/network`）。`pkg/virt` 树明确是纯边界层——其 AGENTS 注明「进程 spawn / QMP 生命周期……属于上层 `internal/node`」。

本任务实现**虚拟机领域层 `internal/vmm`**：负责 fork QEMU 进程、并对已存在的 QEMU 进程进行生命周期管理。它把 `pkg/virt/qemu`（argv）+ `pkg/virt/qmp`（状态/控制）组合成可被上层编排的 VM 进程生命周期能力。

### 核心硬约束（项目铁律）

1. **编排器进程生命周期与 QEMU 进程生命周期解耦**：编排器崩溃/重启绝不能终止运行中的 QEMU；不用 `Pdeathsig`、不放入会被编排器信号波及的进程组、不让 QEMU 依赖编排器持有的 stdio/pipe/QMP 连接存活；重启后通过 pidfile + QMP socket 重连而非父子关系。
2. **上下一致（单一事实源）**：VM 运行时态的权威来自实际运行的 QEMU 进程 + QMP 状态，绝不能用本层缓存/持久化状态投影顶替运行态。
3. **积木式可替换分层**：带真实 OS 副作用的动作抽象成可替换接口，单测注入 fake、不依赖真实 QEMU。
4. **显式优于隐式**：所有行为相关参数显式传入；VM uuid 由调用方提供，本层不生成。
5. **etcd-only**：本层的 JSON 是节点本地运行时检查点，非控制面元数据库；控制面权威 desired 仍走 etcd。
6. **no-libvirt**：自实现 JSON store，复刻 libvirt/docker 的*能力*，不引入任何 libvirt 派生代码/抽象。

## 决策摘要（Q1–Q72）

| 决策 | 选择 |
| --- | --- |
| Q1 职责边界 | **A. 只管进程生命周期**。接收已解析输入（配置好的 builder + 已发布卷路径 + 已就绪 TAP 名 + 调用方提供 uuid），不调 storage/network。 |
| Q2 状态模型 | **B. 细化态**（含 Starting/Stopping/Failed 过渡态）。 |
| Q3 过渡态与上下一致调和 | 意图记忆仅用于在 live 信号相同的态之间消歧，绝不与 live 现实冲突；冲突时 **live 赢**。 |
| Q4 spawn 机制 | **A. QEMU 原生 `-daemonize`**。退出码交给 live 探测。 |
| Q5 VNC 接口持有 | **A. 被动持有 unix socket + 上报路径**，字节流代理留给上层。 |
| Q6 spec 输入形态 | **A. 接收配置好的 builder**（Q70 收紧：vmm 收尾 `Build()`）。 |
| Q7/Q60 恢复模型 | kubelet 式：节点本地 JSON 持久化（身份+spec+路径+意图态），恢复时只 reattach 活进程。 |
| Q8 JSON 布局 | **A. 每台 VM 一个 `vm.json`**，原子写（temp + rename）。 |
| Q9 包名 + 原语边界 | `internal/vmm/`；spawn/signal/pidfile/json 抽象成一个可替换 `ProcessController` 接口。 |
| Q56 设施 flag 位置 | 扩进 `pkg/virt/qemu` builder（走 typed 校验）。 |
| Q70 spec 输入收尾 | vmm 参与 `Build()`：上层传配置好但未 Build 的 builder，vmm 注入设施 flag 后 Build。 |
| Q66/Q72 验证 | acceptance 新增覆盖 VM 全部能力的用例（Lima）。`vnc日志` = `console.log`。 |

## 1. 架构与分层

vmm 与 `internal/storage`、`internal/network` 平级，沿用同一形态：**VM-facing service → 进程控制原语（可替换边界）**，组合 `pkg/virt/qemu`（argv）+ `pkg/virt/qmp`（状态/控制）。

```
internal/vmm (VM-facing service：进程生命周期)
  ├── 注入 qmp.Client          (pkg/virt/qmp，已有可替换边界)
  ├── 注入 ProcessController   (本层新增的可替换原语：spawn/signal/pidfile/json)
  └── 收尾 qemu.Builder.Build  (上层传配置好的 builder，vmm 注入设施 flag 后 Build)

上层组合根 internal/node：拉 desired + 调 vmm.Discover 拿 actual + reconcile（本任务之外）
```

**只管进程生命周期（Q1-A）：** vmm 收到已解析输入（配置好的 `qemu.Builder` + 已发布卷路径 + 已就绪 TAP 名 + 调用方提供的 VM uuid），不调 storage/network。装配由上层组合根负责。

## 2. 包结构

```
internal/vmm/
  service.go        # VMMService：Create/Start/Stop/Kill/Delete/Status/List/Discover/Reattach
  vm.go             # 领域类型：VM, CreateRequest, Phase, IntendedPhase
  state.go          # 状态派生：intent + live probe → Phase（纯逻辑，易测）
  paths.go          # 运行时目录布局：/var/lib/govirtlet/<uuid>/... 路径生成（vmm 私有）
  store.go          # vm.json 原子读写 + Discover 扫描
  facility.go       # 向 qemu.Builder 注入设施 flag（pidfile/qmp/vnc/log/daemonize）
  errors.go         # sentinel 错误类：vmmerr
  proc/             # 进程控制原语（可替换接口）
    controller.go        # ProcessController 接口 + 类型
    controller_linux.go  # 真实实现：os/exec daemonize spawn、signal 0、SIGKILL、文件原子写
```

按第十八章预判：`service.go` 编排逻辑可能接近软上限，计划阶段会把 Create/Start vs Stop/Kill/Delete vs Discover/Status 的编排按职责切分文件，避免单文件越 800 行硬上限。

## 3. 公开 API

```go
type VMMService struct { /* runtimeRoot string, proc ProcessController, qmpFactory QMPFactory */ }

func NewVMMService(runtimeRoot string, proc ProcessController, qmpFactory QMPFactory) *VMMService

// Create 渲染并落盘 vm.json，不 spawn。IntendedPhase=Defined。
func (s *VMMService) Create(ctx context.Context, req CreateRequest) (VM, error)
// Start detached spawn QEMU（-daemonize），等待 QMP 就绪。IntendedPhase=Running 落盘。
func (s *VMMService) Start(ctx context.Context, uuid string) (VM, error)
// Stop 优雅停止：QMP system_powerdown。IntendedPhase=Stopped 落盘。
func (s *VMMService) Stop(ctx context.Context, uuid string) error
// Kill 强制停止：QMP quit；不可达时 ProcessController SIGKILL 兜底。IntendedPhase=Stopped。
func (s *VMMService) Kill(ctx context.Context, uuid string) error
// Delete 删除逻辑定义 + 整个运行时目录。要求已停止（无 live 进程），否则 ErrConflict。
func (s *VMMService) Delete(ctx context.Context, uuid string) error
// Status 单台 live 探测：intent + 进程存活 + QMP query-status → Phase。
func (s *VMMService) Status(ctx context.Context, uuid string) (VM, error)
// List 返回所有已知 VM（内存索引 + 各自 live 探测）。
func (s *VMMService) List(ctx context.Context) ([]VM, error)
// Discover 扫 runtimeRoot/*/ 读 vm.json + 逐个 live 验证，返回 actual（kubelet CRI-list 角色）。
func (s *VMMService) Discover(ctx context.Context) ([]VM, error)
// Reattach 对给定 uuid 验证 live 进程并重建 QMP 连接（只接管活进程）。
func (s *VMMService) Reattach(ctx context.Context, uuid string) (VM, error)
```

`CreateRequest` 携带：`UUID`（调用方显式提供，vmm 不生成）、配置好但未 Build 的 `*qemu.Builder`、用于上报的卷/网络只读元信息。

### 输入边界（Q6-A + Q70）

vmm 参与 `Build()`：上层用 typed Builder 设 cpu/内存/machine profile/磁盘/tap 等 **VM 配置**但不 `Build()`，把 `*qemu.Builder` 交给 vmm；vmm 从它拥有的运行时目录布局算出 pid/qmp/vnc/log 路径，设进 builder（`-daemonize`/`-vnc`/`-pidfile`/`-mon`），再 `Build()` + spawn。

这保证：设施 flag 仍由 builder 渲染（走 typed 校验，Q56）；运行时目录布局留在 vmm 内、不泄漏给上层（Q7/Q8）；与 storage `local.Driver`「调用方给 base + 身份，driver 内部生成完整路径」同构（memory #329）。

## 4. 状态模型（Q2-B + Q3 + Q60）

**两条轴：** 持久化的 `IntendedPhase`（意图，落 vm.json）+ live 探测（进程存活 + QMP）。观测 `Phase` 由两者派生。**live 永远是物理事实权威；意图只在 live 信号相同的态之间消歧；冲突时 live 赢。**

| IntendedPhase（持久） | 进程 | QMP | → 观测 Phase |
| --- | --- | --- | --- |
| Defined | 死 | — | `Defined`（已建未启） |
| Running | 活 | running | `Running` |
| Running | 活 | 未就绪/连不上 | `Starting` |
| Running | 死 | — | `Failed`（异常退出/spawn 失败） |
| Stopped | 活 | running/任意 | `Stopping`（已发 powerdown 未退） |
| Stopped | 死 | — | `Stopped` |

意图落盘后，`Failed`/`Stopping` 跨重启可重建（Q3 当时担心的「reattach 丢意图」已被 Q60 持久化解决）。所有 Phase 用强类型常量（项目铁律：state-machine 值不能用裸 string）。

**冲突仲裁规则（上下一致在 VM 层的落地）：**
- 本层记 `IntendedPhase=Running`，但 live 探测进程已不在 → 派生 `Failed`/`Stopped`，绝不上报 `Running`。
- 本层记 `IntendedPhase=Stopped`（刚发过 powerdown），但下次探测进程已消失 → 直接 `Stopped`，意图记忆作废。

## 5. 运行时目录 + vm.json

```
/var/lib/govirtlet/<vm-uuid>/
  vm.json       # 身份 + spec + 运行时路径 + IntendedPhase（原子写：temp + rename）
  qemu.pid      # QEMU -pidfile 自写
  qemu.log      # QEMU -D 自身运行日志
  qmp.sock      # QMP 控制 unix socket（server=on,wait=off，支持重启 reattach）
  vnc.sock      # VNC unix socket（被动持有 + 上报，字节流代理留给上层）
  console.log   # 串口控制台输出（-serial file:），guest 启动/控制台日志（即 Q52 的「vnc日志」，Q72 确认）
```

`vm.json` 字段：`uuid`、不可变 spec 摘要（arch/vcpu/mem/磁盘路径/tap 名）、运行时路径集、`IntendedPhase`、创建/最后转移时间戳。它对**身份/spec/意图**权威（desired 维度），对**运行时态**永不权威（始终 live）。

VNC-over-unix-socket 本身没有 QEMU 原生独立日志文件，VNC 服务消息并入 `qemu.log`；`console.log` 持有串口控制台日志。

## 6. 进程控制原语（可替换边界，Q9 + Q66）

```go
type ProcessController interface {
    // SpawnDaemonized exec QEMU（argv 已含 -daemonize），QEMU fork 后台立即返回。
    SpawnDaemonized(ctx context.Context, argv []string, runtimeDir string) error
    // ProcessAlive 读 pidfile → signal 0 验进程存活。
    ProcessAlive(ctx context.Context, pidfilePath string) (bool, error)
    // ForceKill SIGKILL 兜底（QMP quit 不可达时）。
    ForceKill(ctx context.Context, pidfilePath string) error
    // WriteState/ReadState/RemoveState vm.json 原子读写 + 删除。
    WriteState(ctx context.Context, path string, data []byte) error
    ReadState(ctx context.Context, path string) ([]byte, error)
    RemoveState(ctx context.Context, runtimeDir string) error
    // ListStateDirs 扫 runtimeRoot 列出 <uuid>/ 目录（Discover 用）。
    ListStateDirs(ctx context.Context, runtimeRoot string) ([]string, error)
}
```

生产 `controller_linux.go` 真实 `os/exec` + 文件 IO；单测注入 fake，断言 argv 正确/状态机转移/错误传播，不碰真实 QEMU。QMP 交互不进此原语（`qmp.Client` 已是独立可替换边界，由 `QMPFactory` 注入）。

**硬约束落地：** QEMU 走原生 `-daemonize`（Q4-A），不用 `Pdeathsig`、不放共享进程组、不依赖 vmm 持有的 stdio/QMP 存活。vmm 崩溃/重启后 guest 不受影响，靠 pidfile + QMP 重连。`-daemonize` 后拿不到子进程退出码，异常退出由 live 探测发现（派生 `Failed`）。

## 7. `pkg/virt/qemu` builder 扩展（Q56）

新增 typed setter（走现有 qopt 校验 + 黄金测试）：
- `Daemonize()` → `-daemonize`
- `VNCUnixSocket(path)` → `-vnc unix:<path>`（仅 unix socket，不开 TCP 端口 — 安全，Q54-A）

QMP/串口/pidfile/`-D` 日志：复用现有 `-chardev socket,server=on` + `-mon`/`-serial`/`-pidfile`，vmm 在 facility.go 用 typed setter 注入（路径值由 vmm 算）。`-D <qemu.log>` 若 builder 尚无对应 setter 则一并新增。

vm_test.go 黄金测试 + qemucli 输出契约同步更新。

## 8. 恢复语义（kubelet 式，Q58/Q60/Q62）

vmm 提供 reconcile 所需原语，**不实现 reconcile 循环**（那是上层 node agent）：

- **进程崩、VM 没迁（问题 1）：** `Discover` 读 vm.json → live 探测 → 进程活 → `Reattach` 重连 QMP 接管。接管活进程不需控制面授权（进程活着即归属证明）。
- **整机宕、VM 已迁走（问题 2）：** vm.json 说 intent=Running，但 live 探测进程已死 → 派生 `Failed` → **vmm 结构上无「恢复时自动 start」代码路径**，只上报，由控制面裁决（它知道 VM 已在别处）。这条结构护栏从机制上杜绝重启自拉起迁走的 VM 导致脑裂。

**与 storage「不扫盘」（memory #251/#367）的刻意差异（用户授权偏离）：** storage 的 qcow2 是惰性文件、无 live 进程，内存元数据是逻辑账本无法从文件还原。VM 有跨重启存活的 daemonize 进程，必须被重新发现 + 接管才能 Stop/Kill/Status。两者解决的不是同一个问题，故 VM 层落盘 + 扫描发现是正当的。

## 9. 可观测性（强制规约）

- 日志：zerolog，统一字段 `vm_id`/`component=vmm`/`operation`/`outcome`，每个生命周期操作记起始 + 结果。
- 指标/追踪：依赖待引入的 `Meter`/`Tracer` 抽象（项目尚未落地，本层留好 ctx 透传与字段词汇，不直连后端）。
- 节点资源汇报：`Discover`/`Status` 产出的 live actual 即上报数据源，绝不报 vm.json 投影顶替运行态。

## 10. 错误处理

`vmmerr` sentinel：`ErrInvalidRequest`/`ErrNotFound`/`ErrAlreadyExists`/`ErrConflict`（如 Delete 一个仍在跑的 VM）/`ErrNotReady`。全部 `%w` 传播，清理/回滚多错用 `errors.Join`。

## 11. 测试

- 单测：fake `ProcessController` + `qmp.NoopClient`/fake，覆盖状态派生表全部 6 态、Create/Start/Stop/Kill/Delete/Discover/Reattach、ctx 取消、错误传播。不依赖真实 QEMU。
- 验收（Q66）：`test/acceptance/` 新增覆盖 VM 全部能力的用例（真实 daemonize spawn cirros、QMP query-status、powerdown、kill、reattach、discover），走 Lima。

## 12. 不在本任务范围

- 热迁移/热插拔
- fencing token / STONITH 强一致防双写（未来 + 上层职责）
- reconcile 循环本身（上层 node agent）
- VNC 字节流代理/websocket（上层 web 前端）
- 控制面 etcd 集成
- 冷快照/冷扩容/冷配置修改（针对停止态 VM 的操作，后续阶段）

## 已知局限（诚实标注）

「本层不自动拉起 + 控制面是唯一启动权」是当前阶段防脑裂的正确护栏，但不是强一致 fencing。真正杜绝双写需 fencing token / STONITH / 存储侧租约，属控制面 + 存储 fencing 范畴，超出本任务与当前阶段。本任务先把「节点永不自拉起死 VM」这条结构性护栏做实。
