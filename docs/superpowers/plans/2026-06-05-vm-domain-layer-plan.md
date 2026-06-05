# 虚拟机领域层（`internal/vmm`）实现计划

**日期:** 2026-06-05
**Spec:** `docs/superpowers/specs/2026-06-05-vm-domain-layer-design.md`
**范围:** 节点本地 QEMU 进程生命周期管理领域层 + `pkg/virt/qemu` 设施 flag 扩展 + acceptance 覆盖

## 执行须知

- 每个 Task 是一个独立提交单元（第九章：单一逻辑变更）。
- 每个 Task 完成后运行该 Task 的「验证」命令，全绿再进入下一个。
- 全程不调用 storage/network（Q1-A：只管进程生命周期）。
- VM uuid 由调用方显式提供，本层不生成（显式铁律 + 一等公民判据）。
- 真实 QEMU 仅在 Task 7 的 Lima acceptance 验证；Task 1–6 单测用 fake，不碰真实 QEMU。

## 关键设计决断（贯穿所有 Task）

**持久化 argv 模型（解决重启重建）：** `Create` 时 vmm 向上层传入的 builder 注入设施 flag、`Build()`、`Argv()`，把**完整 argv 字符串数组**连同身份/路径/意图一起落盘 `vm.json`。于是：
- `Start` 直接 exec `vm.json` 里存好的 argv（已含 `-daemonize`），无需重新持有 builder。
- `Discover`/`Reattach` 重启后只读 `vm.json` 即可拿到 argv 与所有 socket 路径，无需重建 builder。
- builder 是「一次性配置输入」，不跨操作存活；跨操作存活的是它渲染出的 argv 快照（落盘）。

**QMP 客户端按需构造：** vmm 不长期持有 qmp.Client。`Start`/`Status`/`Reattach` 时用注入的 `QMPFactory` + 从 uuid 算出的 `qmp.sock` 路径按需 new 一个 client，用完 `Disconnect`。这贴合「QEMU 进程生命周期与编排器解耦」——qmp 连接是瞬时的、可重连的，不是 guest 存活的依赖。

## 文件行数预判与拆分（第十八章）

| 文件 | 预估行数 | 处理 |
| --- | --- | --- |
| `internal/vmm/service.go` | ~180 | struct + 构造 + Create + Delete |
| `internal/vmm/lifecycle.go` | ~160 | Start + Stop + Kill |
| `internal/vmm/discover.go` | ~170 | Status + List + Discover + Reattach |
| `internal/vmm/state.go` | ~90 | 状态派生纯逻辑 |
| `internal/vmm/store.go` | ~140 | vm.json 编解码 + 扫描编排 |
| `internal/vmm/paths.go` | ~70 | 运行时路径布局 |
| `internal/vmm/facility.go` | ~110 | 设施 flag 注入 |
| `internal/vmm/vm.go` | ~120 | 领域类型 |
| `internal/vmm/errors.go` | ~30 | sentinel |
| `internal/vmm/proc/controller_linux.go` | ~200 | 真实实现 |

`service.go`/`lifecycle.go`/`discover.go` 三分法把编排按职责切开，每个都远低于 800 行硬上限。**执行阶段严格按此结构落地，不因行数中途再拆（第十八章 plan 执行阶段纪律）。**

---

## Task 1 — `pkg/virt/qemu` 设施 flag 扩展

**目标：** 新增 `-daemonize`、`-vnc unix:<path>`、`-serial file:<path>` 三个 typed builder 能力，使 vmm 能通过 builder 渲染运行时设施 flag（Q56）。`-pidfile`（`b.PidFile`）、QMP chardev（`chardev.Socket{Server:On,Wait:Off}`）、`-mon`（`monitor.Monitor{Mode:ModeControl}`）均已存在，无需新增。

> **已核实的现有公开包（执行前已确认，§202/§203/§227/§228/§232）：**
> - `pkg/virt/qemu/{chardev,monitor,serial,display,qflag,qopt}` 都是**公开包**（非 internal），`internal/vmm` 可直接 import，正如 `cmd/qemucli`/acceptance 已直接使用。
> - `chardev.Socket{ID, Path, Server qflag.OnOff, Wait qflag.OnOff}`：struct 字面量构造，已支持 server/wait 模式。`chardev.Ref` 是 string 类型别名。
> - `monitor.Monitor{Chardev chardev.Ref, Mode monitor.Mode}` + `monitor.ModeControl`：struct 字面量构造，已存在。
> - `serial.Chardev(id) serial.Serial` 已存在，但**只有 `chardev:<id>` 形态**；`-serial file:<path>` 形态需新增 `serial.File`。
> - `display.Display` 仅支持 `""`/`none`；VNC 是独立 `-vnc` flag，需新增 `display.VNCUnix` 类型 + builder `VNC` setter。
> - `qflag.OnOff`（`qflag.On`/`qflag.Off`）已存在。

### 1.1 新增 `pkg/virt/qemu/display/vnc.go`（公开包）

VNC unix socket 是 `-vnc` 的值（与 `-display` 正交，QEMU 是两个独立 flag）。新建独立类型而非塞进 `display.Display`：

```go
package display

import (
	"fmt"

	"github.com/suknna/govirta/pkg/virt/qemu/qopt"
)

// VNCUnix 描述 -vnc unix:<path> 的值。仅支持 unix socket，不支持 TCP
// 监听端口——避免无认证 VNC 网络端点（spec Q54-A 安全约束）。
type VNCUnix struct{ socketPath string }

// VNCUnixSocket 构造一个 unix-socket VNC 显示目标。
func VNCUnixSocket(socketPath string) VNCUnix { return VNCUnix{socketPath: socketPath} }

func (v VNCUnix) Validate() error {
	return qopt.ValidateValue("vnc unix socket", v.socketPath)
}

// Arg 渲染 -vnc 的值，例如 unix:/var/lib/govirtlet/<uuid>/vnc.sock。
func (v VNCUnix) Arg() (string, error) {
	if err := v.Validate(); err != nil {
		return "", fmt.Errorf("vnc: %w", err)
	}
	return "unix:" + v.socketPath, nil
}
```

### 1.2 新增 `serial.File` 构造器（`pkg/virt/qemu/serial/serial.go`）

现有 `serial.Chardev(id)` 渲染 `chardev:<id>`；新增 `serial.File(path)` 渲染 `file:<path>`，供 console.log 串口落盘。`serial.Serial` 现为 `struct{ chardevID string }`——改为可承载两种形态（保持 `Arg()` 契约不变）：

```go
// File 构造一个把串口输出写入文件的 -serial 目标（file:<path>）。
func File(path string) Serial { return Serial{mode: serialFile, value: path} }
```

> 执行时读 `serial.go` 现有结构，最小改动支持 `file:` 形态：给 `Serial` 加一个内部 mode 区分 `chardev:` / `file:`，两者都走 `qopt.ValidateValue` 校验 value。保持 `Chardev` 与 `Arg()` 现有行为不变（现有 golden test 不回归）。

### 1.3 `pkg/virt/qemu/vm.go` 新增 Builder 字段与 setter

在 `Builder` struct（vm.go:156-177）加两个字段：

```go
	daemonize bool
	vnc       *display.VNCUnix
```

在 setter 区（Display setter 之后，vm.go:290 附近）加：

```go
// Daemonize 渲染 -daemonize，让 QEMU 自行 fork 到后台并脱离父进程。
// vmm 依赖此 flag 实现「编排器死后 guest 存活」（spec 硬约束 1）。
func (b *Builder) Daemonize() *Builder { b.daemonize = true; return b }

// VNC 渲染 -vnc unix:<path>。仅 unix socket，不开 TCP 端口。
func (b *Builder) VNC(v display.VNCUnix) *Builder {
	b.vnc = &v
	return b
}
```

（vm.go 已 import `pkg/virt/qemu/display`，无需新增 import。）

### 1.4 `Build()` 校验 + `Argv()` 渲染

`Build()`（vm.go:299-352）在 display 校验后加 vnc 校验：

```go
	if b.vnc != nil {
		if err := b.vnc.Validate(); err != nil {
			return VM{}, fmt.Errorf("%w: invalid vnc: %v", ErrInvalidVM, err)
		}
	}
```

`Argv()`（vm.go:354-394）渲染顺序：`-vnc` 与 `-daemonize` 紧跟在 `-pidfile` 之后（进程设施聚集在尾部，保持确定性）。在 `b.pidFile` 块（vm.go:390-392）之后追加：

```go
	if b.vnc != nil {
		if arg, err := b.vnc.Arg(); err == nil {
			argv = append(argv, "-vnc", arg)
		}
	}
	if b.daemonize {
		argv = append(argv, "-daemonize")
	}
```

> 注：`Argv()` 当前对已 `Build()` 校验过的内容直接渲染（不返回 error），故 `vnc.Arg()` 的 err 在此被吞——安全，因为 `Build()` 已校验过 vnc。沿用现有 `Argv()` 的 no-error 契约，不改签名。

### 1.5 golden test 同步（vm_test.go）

新增子用例：
- `daemonize_flag`：`NewVM(ArchX86_64).Daemonize()` → argv 末尾含 `-daemonize`。
- `vnc_unix_socket`：`.VNC(display.VNCUnixSocket("/run/vnc.sock"))` → 含 `-vnc unix:/run/vnc.sock`。
- `vnc_rejects_comma`：`display.VNCUnixSocket("/run/a,b.sock")` → `Build()` 返回 `ErrInvalidVM`。
- `serial_file`：`.Serial(serial.File("/run/console.log"))` → 含 `-serial file:/run/console.log`。
- 一个组合用例：daemonize + vnc + pidfile + qmp chardev（`Server:On,Wait:Off`）+ mon（`ModeControl`）+ serial file 同时存在，断言完整 argv 顺序。

### 1.6 验证

```bash
gofmt -l pkg/virt/qemu/
go test -count=1 ./pkg/virt/qemu/...
```

**提交：** `feat(qemu): add -daemonize, -vnc unix socket, and -serial file builder setters`

---

## Task 2 — `internal/vmm/proc` 进程控制原语（接口 + 类型）

**目标：** 定义 `ProcessController` 可替换接口（Q9）+ 平台无关类型。带真实 OS 副作用的动作全部经此边界，单测注入 fake。

### 2.1 `internal/vmm/proc/controller.go`

```go
// Package proc 定义 vmm 的进程控制原语边界：spawn daemonized QEMU、
// 进程存活探测、SIGKILL 兜底、vm.json 原子读写、运行时目录扫描。
// 生产实现带真实 OS 副作用（controller_linux.go）；单测注入 fake，
// 使 vmm 状态机与编排逻辑无需真实 QEMU 即可验证。
package proc

import "context"

// ProcessController 抽象所有带真实 OS 副作用的进程/文件操作。
// QMP 交互不在此边界（pkg/virt/qmp 已是独立可替换边界，由 QMPFactory 注入）。
type ProcessController interface {
	// SpawnDaemonized exec QEMU（argv 已含 -daemonize），QEMU fork 到后台后
	// 立即返回。QEMU 自己写 -pidfile；本方法不持有子进程、不 Wait、不依赖
	// 父子关系（spec 硬约束 1）。runtimeDir 作为子进程工作目录。
	SpawnDaemonized(ctx context.Context, argv []string, runtimeDir string) error

	// ProcessAlive 读 pidfile 解析 pid，再 signal 0 探测进程是否存活。
	// pidfile 不存在或进程不存在返回 (false, nil)；解析/权限错误返回 error。
	ProcessAlive(ctx context.Context, pidfilePath string) (bool, error)

	// ForceKill 读 pidfile 后向进程发 SIGKILL（QMP quit 不可达时的兜底）。
	// 进程已不存在视为幂等成功。
	ForceKill(ctx context.Context, pidfilePath string) error

	// WriteState 原子写 vm.json（写临时文件 + rename），不存在目录则创建。
	WriteState(ctx context.Context, path string, data []byte) error
	// ReadState 读 vm.json 原始字节；不存在返回 ErrStateNotFound。
	ReadState(ctx context.Context, path string) ([]byte, error)
	// RemoveState 删除整个运行时目录（Delete 用）。
	RemoveState(ctx context.Context, runtimeDir string) error

	// ListStateDirs 扫 runtimeRoot 列出直接子目录名（每个对应一个 uuid）。
	// runtimeRoot 不存在返回空切片 + nil（节点首次启动无任何 VM）。
	ListStateDirs(ctx context.Context, runtimeRoot string) ([]string, error)
}
```

### 2.2 `internal/vmm/proc/errors.go`

```go
package proc

import "errors"

// ErrStateNotFound 表示 vm.json 不存在（ReadState）。vmm 据此判定 ErrNotFound。
var ErrStateNotFound = errors.New("proc: state file not found")
```

### 2.3 验证

```bash
gofmt -l internal/vmm/proc/
go vet ./internal/vmm/proc/
```

（此 Task 只有接口+类型，无逻辑可测；编译通过即可。）

**提交：** `feat(vmm): define ProcessController primitive boundary`

---

## Task 3 — `internal/vmm` 领域类型 + 状态派生 + 路径 + 错误

**目标：** 纯逻辑层——领域类型、状态派生表（spec §4）、运行时路径布局（spec §5）、sentinel 错误（spec §10）。无 OS 副作用，全部可纯单测。

### 3.1 `internal/vmm/errors.go`

```go
package vmm

import "errors"

// vmm sentinel 错误类。全部 %w 传播，errors.Is/As 可分类。
var (
	ErrInvalidRequest = errors.New("vmm: invalid request")
	ErrNotFound       = errors.New("vmm: vm not found")
	ErrAlreadyExists  = errors.New("vmm: vm already exists")
	ErrConflict       = errors.New("vmm: vm state conflict")
	ErrNotReady       = errors.New("vmm: vm not ready")
)
```

### 3.2 `internal/vmm/vm.go`

```go
package vmm

import "time"

// Phase 是对外观测的 VM 运行态（由 IntendedPhase + live 探测派生）。
// 强类型常量（项目铁律：state-machine 值不能用裸 string）。
type Phase string

const (
	PhaseDefined  Phase = "defined"  // 已建未启
	PhaseStarting Phase = "starting" // 进程活、QMP 未就绪
	PhaseRunning  Phase = "running"  // 进程活、QMP running
	PhaseStopping Phase = "stopping" // intent=stopped、进程仍活
	PhaseStopped  Phase = "stopped"  // 进程死、intent=stopped
	PhaseFailed   Phase = "failed"   // 进程死、intent=running（异常退出）
)

// IntendedPhase 是持久化到 vm.json 的意图态（desired 维度）。
// 仅用于在 live 信号相同的态之间消歧（spec §4 冲突仲裁）。
type IntendedPhase string

const (
	IntendedDefined IntendedPhase = "defined"
	IntendedRunning IntendedPhase = "running"
	IntendedStopped IntendedPhase = "stopped"
)

// Valid 报告 IntendedPhase 是否为已知值。
func (p IntendedPhase) Valid() bool {
	switch p {
	case IntendedDefined, IntendedRunning, IntendedStopped:
		return true
	default:
		return false
	}
}

// SpecSummary 是落盘的不可变 VM spec 摘要（上报用，非运行态权威）。
type SpecSummary struct {
	Arch       string   `json:"arch"`
	VCPUs      int      `json:"vcpus"`
	MemoryMiB  int      `json:"memory_mib"`
	DiskPaths  []string `json:"disk_paths"`
	TapNames   []string `json:"tap_names"`
}

// RuntimePaths 是运行时目录内各文件的绝对路径集（vmm 私有布局产物）。
type RuntimePaths struct {
	Dir         string `json:"dir"`
	StateFile   string `json:"state_file"`
	PidFile     string `json:"pid_file"`
	QEMULog     string `json:"qemu_log"`
	QMPSocket   string `json:"qmp_socket"`
	VNCSocket   string `json:"vnc_socket"`
	ConsoleLog  string `json:"console_log"`
}

// persistedState 是 vm.json 的磁盘表示。argv 落盘是「持久化 argv 模型」
// 的核心：Start 直接 exec 它、Discover 重启后据它重建，无需重持 builder。
type persistedState struct {
	UUID        string        `json:"uuid"`
	Spec        SpecSummary   `json:"spec"`
	Paths       RuntimePaths  `json:"paths"`
	Argv        []string      `json:"argv"`
	Intended    IntendedPhase `json:"intended_phase"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// VM 是对外返回的 VM 视图：身份 + spec + 路径 + 持久意图 + live 观测 Phase。
type VM struct {
	UUID     string
	Spec     SpecSummary
	Paths    RuntimePaths
	Intended IntendedPhase
	Phase    Phase // live 派生，不落盘
}

// CreateRequest 是 Create 的输入。Builder 为「配置好但未 Build」的 builder
// （Q6-A + Q70）：上层设 cpu/内存/machine/磁盘/tap，vmm 注入设施 flag 后 Build。
type CreateRequest struct {
	UUID    string         // 调用方显式提供，vmm 不生成
	Builder *qemu.Builder  // 配置好但未 Build；vmm 收尾注入设施 flag + Build
	Spec    SpecSummary    // 上报用只读摘要
}
```

（import 需补 `github.com/suknna/govirta/pkg/virt/qemu`。）

### 3.3 `internal/vmm/state.go`

状态派生是纯函数，便于穷举单测 spec §4 全部 6 行：

```go
package vmm

// liveProbe 是一次 live 探测的物理事实输入。
type liveProbe struct {
	processAlive bool
	qmpRunning   bool // 仅当 processAlive 且 QMP 可连且 query-status=running 时 true
	qmpReachable bool // QMP 连得上（区分 Starting vs Running）
}

// derivePhase 把持久意图 + live 探测派生为对外观测 Phase。
// 核心铁律（spec §4）：live 永远是物理事实权威；意图只在 live 信号相同的
// 态之间消歧；冲突时 live 赢。
func derivePhase(intended IntendedPhase, probe liveProbe) Phase {
	if !probe.processAlive {
		// 进程死：物理事实压倒一切意图。
		switch intended {
		case IntendedRunning:
			return PhaseFailed // 意图运行但进程没了 = 异常退出
		default:
			return PhaseStopped // defined 从未启动过也归 stopped 语义由调用方区分
		}
	}
	// 进程活：
	switch intended {
	case IntendedStopped:
		return PhaseStopping // 已发 powerdown 但进程未退
	case IntendedRunning:
		if probe.qmpRunning {
			return PhaseRunning
		}
		return PhaseStarting // 进程活但 QMP 未就绪/未 running
	default: // IntendedDefined：理论上不该有活进程，但 live 赢 → 报 Starting
		return PhaseStarting
	}
}
```

> `PhaseDefined` 不在 `derivePhase` 产生——它专属「Create 后从未 Start」的场景：此时无进程、intent=Defined，`derivePhase` 会返回 `PhaseStopped`。为区分二者，`Status`/`Discover` 在 intent==IntendedDefined 且进程从未存在时显式置 `PhaseDefined`（见 Task 6）。这条特例在 state.go 加一个 helper：

```go
// observedPhase 在 derivePhase 之上叠加 Defined 特例：intent=defined 且进程不存在
// 时报 Defined（而非 Stopped），区分「从未启动」与「启动后已停」。
func observedPhase(intended IntendedPhase, probe liveProbe) Phase {
	if intended == IntendedDefined && !probe.processAlive {
		return PhaseDefined
	}
	return derivePhase(intended, probe)
}
```

### 3.4 `internal/vmm/paths.go`

```go
package vmm

import "path/filepath"

// 运行时目录内的固定文件名（vmm 私有布局，spec §5）。
const (
	stateFileName  = "vm.json"
	pidFileName    = "qemu.pid"
	qemuLogName    = "qemu.log"
	qmpSocketName  = "qmp.sock"
	vncSocketName  = "vnc.sock"
	consoleLogName = "console.log"
)

// runtimePathsFor 从 runtimeRoot + uuid 计算一台 VM 的全部运行时路径。
// 布局归 vmm 私有，绝不泄漏给上层（spec Q7/Q8，与 storage local.Driver 同构）。
func runtimePathsFor(runtimeRoot, uuid string) RuntimePaths {
	dir := filepath.Join(runtimeRoot, uuid)
	return RuntimePaths{
		Dir:        dir,
		StateFile:  filepath.Join(dir, stateFileName),
		PidFile:    filepath.Join(dir, pidFileName),
		QEMULog:    filepath.Join(dir, qemuLogName),
		QMPSocket:  filepath.Join(dir, qmpSocketName),
		VNCSocket:  filepath.Join(dir, vncSocketName),
		ConsoleLog: filepath.Join(dir, consoleLogName),
	}
}
```

### 3.5 单测

- `state_test.go`：table-driven 穷举 `observedPhase` 的全部组合（spec §4 六行 + Defined 特例 + 冲突仲裁两条）。
- `paths_test.go`：断言路径拼接正确、uuid 注入位置正确。
- `vm_test.go`：`IntendedPhase.Valid()` 边界。

### 3.6 验证

```bash
gofmt -l internal/vmm/
go test -count=1 ./internal/vmm/
```

**提交：** `feat(vmm): add domain types, phase derivation, runtime path layout`

---

## Task 4 — `internal/vmm/facility.go` 设施 flag 注入

**目标：** 把 vmm 拥有的运行时路径注入上层传来的 builder（Q70 收尾 Build）。这是「持久化 argv 模型」的渲染点。

> **依赖前提：** Task 1 已加 `display.VNCUnix`/`b.VNC`/`b.Daemonize`/`serial.File`。`chardev.Socket`/`monitor.Monitor`/`monitor.ModeControl`/`qflag.On`/`qflag.Off` 均已是现有公开 API（§227/§228/§232）。`pkg/virt/qemu/{chardev,monitor,serial,display,qflag}` 是公开包，`internal/vmm` 直接 import，无 internal 可见性限制。

### 4.1 `internal/vmm/facility.go`

```go
package vmm

import (
	"fmt"

	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/chardev"
	"github.com/suknna/govirta/pkg/virt/qemu/display"
	"github.com/suknna/govirta/pkg/virt/qemu/monitor"
	"github.com/suknna/govirta/pkg/virt/qemu/qflag"
	"github.com/suknna/govirta/pkg/virt/qemu/serial"
)

// qmpChardevID 是 QMP 控制 socket 的 chardev id（facility 内部约定）。
const qmpChardevID = "vmm-qmp"

// injectFacilityFlags 向「配置好但未 Build」的 builder 注入 vmm 的运行时设施
// flag，然后 Build + Argv，返回落盘用的 argv 快照（spec §6/§7）。
//
// 注入项：
//   - -pidfile <qemu.pid>：QEMU 自写 pid，供 ProcessAlive 探测
//   - QMP unix socket（-chardev socket,server=on,wait=off + -mon mode=control）：
//     server 模式 + wait=off 让 QEMU 不阻塞等待连接，支持重启后 reattach
//   - -serial file:<console.log>：串口控制台日志（spec §5，Q52「vnc日志」=console.log）
//   - -vnc unix:<vnc.sock>：被动持有的 VNC unix socket（Q54-A，不开 TCP）
//   - -daemonize：QEMU fork 后台脱离父进程（spec 硬约束 1）
func injectFacilityFlags(b *qemu.Builder, paths RuntimePaths) ([]string, error) {
	if b == nil {
		return nil, fmt.Errorf("%w: builder is required", ErrInvalidRequest)
	}

	b.PidFile(paths.PidFile).
		AddChardev(chardev.Socket{
			ID:     qmpChardevID,
			Path:   paths.QMPSocket,
			Server: qflag.On,
			Wait:   qflag.Off,
		}).
		Monitor(monitor.Monitor{
			Chardev: chardev.Ref(qmpChardevID),
			Mode:    monitor.ModeControl,
		}).
		Serial(serial.File(paths.ConsoleLog)).
		VNC(display.VNCUnixSocket(paths.VNCSocket)).
		Daemonize()

	vm, err := b.Build()
	if err != nil {
		return nil, fmt.Errorf("vmm: build qemu argv: %w", err)
	}
	return vm.Argv(), nil
}
```

> chardev/monitor/serial/vnc 的非法值（如路径含逗号）由各自 `Validate()` 在 `b.Build()` 内捕获，统一以 `ErrInvalidVM` 经 `injectFacilityFlags` 的 `b.Build()` 错误返回，不在 facility 层重复校验。

### 4.2 单测 `facility_test.go`

- 用真实 builder（`qemu.NewVM(qemu.ArchX86_64).Binary("qemu-system-x86_64")`，不碰 QEMU）注入 facility，断言返回 argv 含 `-pidfile <pid>`、`-chardev socket,id=vmm-qmp,path=<qmp.sock>,server=on,wait=off`、`-mon chardev=vmm-qmp,mode=control`、`-serial file:<console.log>`、`-vnc unix:<vnc.sock>`、`-daemonize`。
- nil builder → `ErrInvalidRequest`。
- 路径含逗号（构造一个 QMPSocket 含 `,` 的 RuntimePaths）→ `b.Build()` 失败 → 返回 `ErrInvalidVM`（断言 `errors.Is`）。

### 4.3 验证

```bash
gofmt -l internal/vmm/ pkg/virt/qemu/
go test -count=1 ./internal/vmm/ ./pkg/virt/qemu/...
```

**提交：** `feat(vmm): inject runtime facility flags into qemu builder`

---

## Task 5 — `internal/vmm/store.go` vm.json 编解码 + 扫描编排

**目标：** vm.json 的 JSON 编解码 + Discover 的扫描编排（调 ProcessController 原语，自己不碰文件系统）。

### 5.1 `internal/vmm/store.go`

```go
package vmm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/suknna/govirta/internal/vmm/proc"
)

// encodeState 把 persistedState 编码为缩进 JSON（人类可读，便于运维排障）。
func encodeState(s persistedState) ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("vmm: encode state: %w", err)
	}
	return data, nil
}

// decodeState 解码 vm.json 并校验关键不变量（uuid 非空、intent 合法）。
func decodeState(data []byte) (persistedState, error) {
	var s persistedState
	if err := json.Unmarshal(data, &s); err != nil {
		return persistedState{}, fmt.Errorf("vmm: decode state: %w", err)
	}
	if s.UUID == "" {
		return persistedState{}, fmt.Errorf("%w: persisted state missing uuid", ErrInvalidRequest)
	}
	if !s.Intended.Valid() {
		return persistedState{}, fmt.Errorf("%w: persisted state invalid intent %q", ErrInvalidRequest, s.Intended)
	}
	return s, nil
}

// writeState 编码并经 ProcessController 原子落盘 vm.json，更新 UpdatedAt。
func (s *VMMService) writeState(ctx context.Context, st persistedState) error {
	st.UpdatedAt = time.Now().UTC()
	data, err := encodeState(st)
	if err != nil {
		return err
	}
	if err := s.proc.WriteState(ctx, st.Paths.StateFile, data); err != nil {
		return fmt.Errorf("vmm: persist state for %s: %w", st.UUID, err)
	}
	return nil
}

// loadState 读 vm.json 并解码；不存在映射为 ErrNotFound。
func (s *VMMService) loadState(ctx context.Context, uuid string) (persistedState, error) {
	paths := runtimePathsFor(s.runtimeRoot, uuid)
	data, err := s.proc.ReadState(ctx, paths.StateFile)
	if err != nil {
		if errors.Is(err, proc.ErrStateNotFound) {
			return persistedState{}, fmt.Errorf("%w: %s", ErrNotFound, uuid)
		}
		return persistedState{}, fmt.Errorf("vmm: read state for %s: %w", uuid, err)
	}
	return decodeState(data)
}
```

### 5.2 单测 `store_test.go`

- encode/decode 往返一致。
- decode 缺 uuid / 非法 intent → `ErrInvalidRequest`。
- `loadState` 用 fake proc 返回 `ErrStateNotFound` → `ErrNotFound`。

### 5.3 验证

```bash
go test -count=1 ./internal/vmm/
```

**提交：** `feat(vmm): add vm.json encode/decode and state persistence`

---

## Task 6 — `internal/vmm` service（生命周期 + 查询编排）

**目标：** 三个文件实现全部公开 API。这是核心编排层。

### 6.1 `internal/vmm/service.go`（struct + 构造 + Create + Delete）

```go
package vmm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/vmm/proc"
	"github.com/suknna/govirta/pkg/virt/qmp"
)

// QMPFactory 按 socket 路径构造一个 qmp.Client（按需、瞬时；不长期持有）。
// 注入此工厂使单测可用 qmp.NopClient/fake，生产用真实 SocketClient。
type QMPFactory func(socketPath string) (qmp.Client, error)

// VMMService 是节点本地 QEMU 进程生命周期领域服务（spec §3）。
type VMMService struct {
	runtimeRoot string
	proc        proc.ProcessController
	qmpFactory  QMPFactory
}

// NewVMMService 构造服务。runtimeRoot 通常是 /var/lib/govirtlet。
func NewVMMService(runtimeRoot string, pc proc.ProcessController, qf QMPFactory) (*VMMService, error) {
	if runtimeRoot == "" {
		return nil, fmt.Errorf("%w: runtime root is required", ErrInvalidRequest)
	}
	if pc == nil {
		return nil, fmt.Errorf("%w: process controller is required", ErrInvalidRequest)
	}
	if qf == nil {
		return nil, fmt.Errorf("%w: qmp factory is required", ErrInvalidRequest)
	}
	return &VMMService{runtimeRoot: runtimeRoot, proc: pc, qmpFactory: qf}, nil
}

// Create 渲染 facility-injected argv 并落盘 vm.json，不 spawn。intent=Defined。
// 重复 uuid（vm.json 已存在）返回 ErrAlreadyExists。
func (s *VMMService) Create(ctx context.Context, req CreateRequest) (VM, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "create").Str("vm_id", req.UUID).Logger()
	if req.UUID == "" {
		return VM{}, fmt.Errorf("%w: uuid is required", ErrInvalidRequest)
	}
	if req.Builder == nil {
		return VM{}, fmt.Errorf("%w: builder is required", ErrInvalidRequest)
	}
	paths := runtimePathsFor(s.runtimeRoot, req.UUID)

	// 重复检测：vm.json 已存在即拒绝（不覆盖，与 storage image 同构）。
	if _, err := s.proc.ReadState(ctx, paths.StateFile); err == nil {
		return VM{}, fmt.Errorf("%w: %s", ErrAlreadyExists, req.UUID)
	} else if !errors.Is(err, proc.ErrStateNotFound) {
		return VM{}, fmt.Errorf("vmm: probe existing state: %w", err)
	}

	argv, err := injectFacilityFlags(req.Builder, paths)
	if err != nil {
		return VM{}, err
	}
	now := time.Now().UTC()
	st := persistedState{
		UUID: req.UUID, Spec: req.Spec, Paths: paths, Argv: argv,
		Intended: IntendedDefined, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.writeState(ctx, st); err != nil {
		return VM{}, err
	}
	log.Info().Str("outcome", "success").Msg("vm created")
	return VM{UUID: req.UUID, Spec: req.Spec, Paths: paths, Intended: IntendedDefined, Phase: PhaseDefined}, nil
}

// Delete 删除逻辑定义 + 整个运行时目录。要求无 live 进程，否则 ErrConflict。
func (s *VMMService) Delete(ctx context.Context, uuid string) error {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "delete").Str("vm_id", uuid).Logger()
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return err
	}
	alive, err := s.proc.ProcessAlive(ctx, st.Paths.PidFile)
	if err != nil {
		return fmt.Errorf("vmm: probe process for delete %s: %w", uuid, err)
	}
	if alive {
		return fmt.Errorf("%w: cannot delete running vm %s", ErrConflict, uuid)
	}
	if err := s.proc.RemoveState(ctx, st.Paths.Dir); err != nil {
		return fmt.Errorf("vmm: remove runtime dir %s: %w", uuid, err)
	}
	log.Info().Str("outcome", "success").Msg("vm deleted")
	return nil
}
```

### 6.2 `internal/vmm/lifecycle.go`（Start + Stop + Kill）

```go
package vmm

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog"
)

// Start exec vm.json 里存好的 daemonized argv，等待 QMP 就绪，落 intent=Running。
// 幂等：已 Running（进程活 + QMP running）直接返回当前态。
func (s *VMMService) Start(ctx context.Context, uuid string) (VM, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "start").Str("vm_id", uuid).Logger()
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return VM{}, err
	}
	alive, err := s.proc.ProcessAlive(ctx, st.Paths.PidFile)
	if err != nil {
		return VM{}, fmt.Errorf("vmm: probe process for start %s: %w", uuid, err)
	}
	if alive {
		// 已有活进程：幂等返回 live 态，不重复 spawn（防双进程）。
		return s.statusFrom(ctx, st)
	}

	if err := s.proc.SpawnDaemonized(ctx, st.Argv, st.Paths.Dir); err != nil {
		// spawn 失败：意图仍记 Running，下次探测会派生 Failed（live 赢）。
		st.Intended = IntendedRunning
		if werr := s.writeState(ctx, st); werr != nil {
			return VM{}, errors.Join(fmt.Errorf("vmm: spawn %s: %w", uuid, err), werr)
		}
		return VM{}, fmt.Errorf("vmm: spawn %s: %w", uuid, err)
	}
	st.Intended = IntendedRunning
	if err := s.writeState(ctx, st); err != nil {
		return VM{}, err
	}

	// 等待 QMP 就绪（瞬时连接，用完即断）。
	if err := s.waitQMPReady(ctx, st.Paths.QMPSocket); err != nil {
		log.Warn().Err(err).Msg("qmp not ready after spawn; phase will derive from live probe")
	}
	log.Info().Str("outcome", "success").Msg("vm started")
	return s.statusFrom(ctx, st)
}

// Stop 优雅停止：QMP system_powerdown，落 intent=Stopped。
func (s *VMMService) Stop(ctx context.Context, uuid string) error {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "stop").Str("vm_id", uuid).Logger()
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return err
	}
	client, err := s.qmpFactory(st.Paths.QMPSocket)
	if err != nil {
		return fmt.Errorf("vmm: qmp client for stop %s: %w", uuid, err)
	}
	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("vmm: qmp connect for stop %s: %w", uuid, err)
	}
	defer func() { _ = client.Disconnect(ctx) }()
	if err := client.SystemPowerdown(ctx); err != nil {
		return fmt.Errorf("vmm: powerdown %s: %w", uuid, err)
	}
	st.Intended = IntendedStopped
	if err := s.writeState(ctx, st); err != nil {
		return err
	}
	log.Info().Str("outcome", "success").Msg("vm powerdown requested")
	return nil
}

// Kill 强制停止：先试 QMP quit，不可达则 ProcessController SIGKILL 兜底。
func (s *VMMService) Kill(ctx context.Context, uuid string) error {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "kill").Str("vm_id", uuid).Logger()
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return err
	}
	var qmpErr error
	client, err := s.qmpFactory(st.Paths.QMPSocket)
	if err == nil {
		if cerr := client.Connect(ctx); cerr == nil {
			qmpErr = client.Quit(ctx)
			_ = client.Disconnect(ctx)
		} else {
			qmpErr = cerr
		}
	} else {
		qmpErr = err
	}
	if qmpErr != nil {
		// QMP 不可达：SIGKILL 兜底。
		if kerr := s.proc.ForceKill(ctx, st.Paths.PidFile); kerr != nil {
			return errors.Join(fmt.Errorf("vmm: qmp quit %s: %w", uuid, qmpErr), fmt.Errorf("vmm: sigkill %s: %w", uuid, kerr))
		}
		log.Warn().Err(qmpErr).Msg("qmp quit failed; used SIGKILL fallback")
	}
	st.Intended = IntendedStopped
	if err := s.writeState(ctx, st); err != nil {
		return err
	}
	log.Info().Str("outcome", "success").Msg("vm killed")
	return nil
}
```

### 6.3 `internal/vmm/discover.go`（Status + List + Discover + Reattach + 探测 helper）

```go
package vmm

import (
	"context"
	"fmt"
	"sort"

	"github.com/rs/zerolog"
)

// probe 对一台 VM 做一次 live 探测（进程存活 + QMP 可达/running）。
func (s *VMMService) probe(ctx context.Context, st persistedState) (liveProbe, error) {
	alive, err := s.proc.ProcessAlive(ctx, st.Paths.PidFile)
	if err != nil {
		return liveProbe{}, fmt.Errorf("vmm: probe process %s: %w", st.UUID, err)
	}
	p := liveProbe{processAlive: alive}
	if !alive {
		return p, nil
	}
	// 进程活：尝试瞬时 QMP query-status。连不上不是错误（→ Starting）。
	client, err := s.qmpFactory(st.Paths.QMPSocket)
	if err != nil {
		return p, nil
	}
	if cerr := client.Connect(ctx); cerr != nil {
		return p, nil
	}
	defer func() { _ = client.Disconnect(ctx) }()
	p.qmpReachable = true
	status, qerr := client.QueryStatus(ctx)
	if qerr != nil {
		return p, nil
	}
	p.qmpRunning = status.Running
	return p, nil
}

// statusFrom 从 persistedState live 派生 VM 视图。
func (s *VMMService) statusFrom(ctx context.Context, st persistedState) (VM, error) {
	p, err := s.probe(ctx, st)
	if err != nil {
		return VM{}, err
	}
	return VM{
		UUID: st.UUID, Spec: st.Spec, Paths: st.Paths, Intended: st.Intended,
		Phase: observedPhase(st.Intended, p),
	}, nil
}

// Status 单台 live 探测。
func (s *VMMService) Status(ctx context.Context, uuid string) (VM, error) {
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return VM{}, err
	}
	return s.statusFrom(ctx, st)
}

// Discover 扫 runtimeRoot 读所有 vm.json + 逐个 live 验证（kubelet CRI-list 角色）。
// 结构上无「自动 start」路径：死进程派生 Failed/Stopped，绝不在此拉起（spec §8 防脑裂）。
func (s *VMMService) Discover(ctx context.Context) ([]VM, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "discover").Logger()
	dirs, err := s.proc.ListStateDirs(ctx, s.runtimeRoot)
	if err != nil {
		return nil, fmt.Errorf("vmm: list runtime dirs: %w", err)
	}
	vms := make([]VM, 0, len(dirs))
	for _, uuid := range dirs {
		st, lerr := s.loadState(ctx, uuid)
		if lerr != nil {
			// 损坏/无 vm.json 的目录：记录并跳过，不让单个坏目录污染全量发现。
			log.Warn().Err(lerr).Str("vm_id", uuid).Msg("skip undiscoverable runtime dir")
			continue
		}
		vm, serr := s.statusFrom(ctx, st)
		if serr != nil {
			log.Warn().Err(serr).Str("vm_id", uuid).Msg("skip vm with probe error")
			continue
		}
		vms = append(vms, vm)
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].UUID < vms[j].UUID })
	return vms, nil
}

// List 等价 Discover（当前无独立内存索引；单一事实源是磁盘 vm.json + live 探测）。
func (s *VMMService) List(ctx context.Context) ([]VM, error) {
	return s.Discover(ctx)
}

// Reattach 对给定 uuid 验证 live 进程并重建 QMP 连接（只接管活进程，spec §8）。
// 进程不存在返回 ErrNotReady——绝不在此 spawn（防止重启拉起已迁走的 VM）。
func (s *VMMService) Reattach(ctx context.Context, uuid string) (VM, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "vmm").Str("operation", "reattach").Str("vm_id", uuid).Logger()
	st, err := s.loadState(ctx, uuid)
	if err != nil {
		return VM{}, err
	}
	alive, err := s.proc.ProcessAlive(ctx, st.Paths.PidFile)
	if err != nil {
		return VM{}, fmt.Errorf("vmm: probe process for reattach %s: %w", uuid, err)
	}
	if !alive {
		// 进程已死：不拉起，上报 ErrNotReady，由上层/控制面裁决（spec §8 问题2）。
		return VM{}, fmt.Errorf("%w: vm %s process not alive, will not auto-start", ErrNotReady, uuid)
	}
	vm, err := s.statusFrom(ctx, st)
	if err != nil {
		return VM{}, err
	}
	log.Info().Str("outcome", "success").Str("phase", string(vm.Phase)).Msg("vm reattached")
	return vm, nil
}

// waitQMPReady 用瞬时 client 等待 QMP 就绪（Start 内部用）。
func (s *VMMService) waitQMPReady(ctx context.Context, socketPath string) error {
	client, err := s.qmpFactory(socketPath)
	if err != nil {
		return err
	}
	if err := client.Connect(ctx); err != nil {
		return err
	}
	defer func() { _ = client.Disconnect(ctx) }()
	return client.WaitReady(ctx)
}
```

### 6.4 测试 fake

`internal/vmm/fake_test.go`：
- `fakeController` 实现 `proc.ProcessController`：内存 map 存 path→bytes、可编程 `processAlive`、记录 spawn 调用的 argv，断言用。
- `fakeQMPClient` 实现 `qmp.Client`：可编程 `QueryStatus` 返回、`Connect` 可注入失败、记录 powerdown/quit 调用。

### 6.5 单测覆盖

`service_test.go` / `lifecycle_test.go` / `discover_test.go`：
- Create：成功落盘 argv+intent=Defined；重复 uuid → ErrAlreadyExists；nil builder/空 uuid → ErrInvalidRequest。
- Start：spawn 调用收到的 argv == vm.json.Argv；intent→Running 落盘；已活进程幂等不重复 spawn；spawn 失败仍落 intent=Running 且返回 error。
- Stop：QMP powerdown 被调用；intent→Stopped；QMP 连接失败 → error 传播。
- Kill：QMP quit 优先；quit 不可达 → ForceKill 被调；两者都失败 → errors.Join；intent→Stopped。
- Delete：活进程 → ErrConflict；死进程 → RemoveState 被调。
- Status/Discover：穷举派生表（活+running→Running、活+QMP不可达→Starting、死+intent=running→Failed、死+intent=stopped→Stopped、死+intent=defined→Defined、活+intent=stopped→Stopping）。
- Discover：坏目录跳过不报错全量失败；结果按 uuid 排序。
- Reattach：活进程→返回 live 态；死进程→ErrNotReady（**断言绝不调 SpawnDaemonized**——这是防脑裂结构护栏的关键测试）。
- ctx 取消：各操作在 ctx canceled 时传播 `context.Canceled`。

### 6.6 验证

```bash
gofmt -l internal/vmm/
go test -race -count=1 ./internal/vmm/...
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl
```

**提交：** `feat(vmm): implement VM process lifecycle service`

---

## Task 7 — `internal/vmm/proc` 真实 Linux 实现 + acceptance

**目标：** `controller_linux.go` 真实实现 + acceptance 覆盖 VM 全部能力（Q66/Q72）。

### 7.1 `internal/vmm/proc/controller_linux.go`

`//go:build linux`。实现要点：
- `SpawnDaemonized`：`exec.CommandContext(ctx, argv[0], argv[1:]...)`，`cmd.Dir = runtimeDir`，stdout/stderr 丢弃（QEMU `-daemonize` 后日志走 `-D`/`-serial file:`，且 daemonize 后父进程读不到）。**关键：不设 `SysProcAttr.Pdeathsig`、不设共享进程组**（spec 硬约束 1）。`-daemonize` 让 QEMU 自己 fork，`cmd.Run()` 会等到父 QEMU 进程 daemonize 退出即返回（QEMU 的 daemonize 语义：父进程 fork 出真正的 VM 进程后退出，故 `Run()` 不会挂住）。
- `ProcessAlive`：读 pidfile → `strconv.Atoi` → `os.FindProcess` → `proc.Signal(syscall.Signal(0))`；`ESRCH` → false；pidfile 不存在 → false。
- `ForceKill`：读 pid → `syscall.Kill(pid, SIGKILL)`；`ESRCH` 幂等成功。
- `WriteState`：`os.MkdirAll(dir, 0o700)` → 写 `<path>.tmp`（`0o600`）→ `os.Rename`（原子）。
- `ReadState`：`os.ReadFile`；`os.IsNotExist` → `ErrStateNotFound`。
- `RemoveState`：`os.RemoveAll(runtimeDir)`。
- `ListStateDirs`：`os.ReadDir(runtimeRoot)`；不存在 → `(nil, nil)`；只收 `IsDir()` 项。

非 linux 占位 `controller_other.go`（`//go:build !linux`）返回 unsupported，保 darwin 编译通过（memory #700：linux-tagged 包 darwin 不可见，但 proc 包会被 service 引用，需有非 linux stub 或让整个 vmm linux-only）。

> **决策：** vmm service 本身是平台无关纯逻辑（无 syscall），只有 `proc/controller_linux.go` 是 linux-only。darwin 下 `go build ./internal/vmm/` 能编译 service（用接口），但 `proc` 包需要一个 `controller_other.go` stub 让 darwin 也能编译 `proc` 包。Lima acceptance 用真实 linux 实现。

### 7.2 生产 QMPFactory 接线

在 `internal/vmm` 提供一个生产 helper（或在 node agent 接线）：
```go
func ProductionQMPFactory(socketPath string) (qmp.Client, error) {
	return qmp.NewSocketClient(qmp.Config{SocketPath: socketPath})
}
```

### 7.3 acceptance `test/acceptance/vmm_lifecycle_test.go`

`//go:build acceptance`。复用 harness（`requireHostnetAcceptanceEnv`/`waitForSerialMarkerGroups`/`shortSocketPath`）。流程：
1. 准备 cirros 磁盘 + runtimeRoot 临时目录（`.tmp` 下）。
2. 用真实 `proc.NewLinuxController()` + `ProductionQMPFactory` 构造 `VMMService`。
3. 用 `qemu.NewVM` 配置 cirros boot builder（machine virt + accel kvm + cpu host + bios + 磁盘 + `-serial file:`），**不**自己设 daemonize/qmp/vnc/pidfile（交给 vmm facility 注入）。
4. `Create` → 断言 vm.json 落盘、Phase=Defined。
5. `Start` → `waitForSerialMarkerGroups` 等 cirros login marker → `Status` 断言 Phase=Running、QMP query-status=running。
6. `Discover` → 断言能发现这台 VM 且 Phase=Running。
7. 模拟「编排器重启」：丢弃 service 实例，新建一个 `VMMService`（同 runtimeRoot）→ `Discover` → 断言仍发现 Running（验证 reattach 不依赖进程内存）→ `Reattach` 成功。
8. `Stop`（powerdown）→ 轮询 `Status` 直到 Phase=Stopped（进程退出）。
9. `Start` 再次 → Running → `Kill`（quit/SIGKILL）→ Phase=Stopped。
10. `Delete` → 断言 runtime 目录消失；对 Running VM 调 Delete 断言 ErrConflict。
11. **防脑裂护栏测试：** 手动制造「vm.json intent=Running 但无进程」（Start 后 SIGKILL 进程、不调 vmm）→ 新建 service `Discover` → 断言该 VM Phase=Failed 且**未被重新拉起**（进程数不增）。

### 7.4 `scripts/acceptance.sh` / harness 更新

确认新 acceptance 文件被 `go test -tags acceptance ./test/acceptance/...` 自动纳入（按现有 glob，无需改脚本）。若 cirros 磁盘准备逻辑可复用现有 boot_test helper 则复用。

### 7.5 验证

```bash
# darwin 快速验证
gofmt -l internal/vmm/... pkg/virt/qemu/...
go vet ./internal/vmm/...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./internal/vmm/...
go test -race -count=1 ./internal/vmm/...
scripts/verify.sh

# Linux 真实验证（Lima）
scripts/acceptance.sh full
```

**提交：** `feat(vmm): add linux ProcessController and VM lifecycle acceptance`

---

## 全局完成标准

- `scripts/verify.sh` 全绿（gofmt + go test + 三个 main build）。
- `go test -race ./...` 全绿。
- `scripts/acceptance.sh full` 全绿（含新 VM 生命周期 acceptance + 现有 hostnet/network 用例无回归）。
- 无 storage/network import（`grep` 确认 `internal/vmm` 不依赖 `internal/storage`/`internal/network`）。
- 防脑裂结构护栏：`grep` 确认 `Discover`/`Reattach` 代码路径无 `SpawnDaemonized` 调用。
- AGENTS 知识库更新：新增 `internal/vmm/AGENTS.md` + 根 AGENTS.md 挂载（独立提交或并入 Task 7）。

## 与 spec 的对应

| spec 小节 | Task |
| --- | --- |
| §7 builder 扩展 | Task 1 |
| §6 ProcessController 接口 | Task 2 |
| §4 状态模型 + §5 路径 + §10 错误 | Task 3 |
| §6/§7 facility 注入 | Task 4 |
| §5 vm.json | Task 5 |
| §3 公开 API + §8 恢复语义 | Task 6 |
| §6 linux 实现 + §11 acceptance | Task 7 |
