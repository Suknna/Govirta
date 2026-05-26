# PROJECT AGENTS KNOWLEDGE BASE

**Generated:** 2026-05-23
**Commit:** 1f893ee
**Branch:** main

<!--
Verified-against:
  base_commit: 1f893ee
  files:
    - cmd/govirtad/main.go
    - cmd/govirtlet/main.go
    - cmd/govirtctl/main.go
    - cmd/qemucli/main.go
    - internal/controlplane/service.go
    - internal/apiserver/server.go
    - internal/node/agent.go
    - internal/virt/qmp/client.go
    - internal/network/bridge/bridge.go
    - internal/scheduler/scheduler.go
    - internal/store/store.go
    - internal/types/types.go
    - internal/version/version.go
    - internal/virt/qemu/vm.go
    - internal/virt/qemuimg/client.go
    - scripts/verify.sh
    - go.mod
    - README.md
    - docs/architecture.md
    - docs/roadmap/README.md
  flows:
    - anchor: flow-govirtad-boot
      sources:
        - cmd/govirtad/main.go
        - internal/controlplane/service.go
        - internal/apiserver/server.go
    - anchor: flow-govirtlet-boot
      sources:
        - cmd/govirtlet/main.go
        - internal/node/agent.go
        - internal/virt/qmp/client.go
        - internal/network/bridge/bridge.go
    - anchor: flow-govirtctl-version
      sources:
        - cmd/govirtctl/main.go
        - internal/version/version.go
    - anchor: flow-qemucli-argv
      sources:
        - cmd/qemucli/main.go
        - internal/virt/qemu/vm.go
-->

## OVERVIEW

Govirta is a Go virtualization infrastructure platform that starts at the QEMU layer and builds toward lightweight VM orchestration. The current stack is Go + QEMU + QMP + qemu-img + Linux bridge/netlink with zerolog for structured logging.

## CURRENT PHASE

Govirta is currently in the single-node cold-operation closure phase. Prioritize completing the local QEMU/qemu-img/QMP/network path before adding distributed scheduling, API orchestration, Kubernetes integration, migration, hot-plug, or multi-node behavior.

Core project goal for this phase: complete the full single-node VM cold-operation loop. A compute node must be able to create and inspect local qcow2 images, render and start a QEMU process for a stopped VM, attach a pre-existing TAP device to the guest, observe/control VM state through QMP, shut the VM down or quit it safely, and perform snapshot/resize/config changes only while the VM is offline.

Current implementation priority, in order:

1. qemu-system CLI builder
2. qemu-img qcow2 management
3. VM create/start/stop/delete
4. QMP `query-status` / `system_powerdown` / `quit`
5. Local TAP/bridge networking
6. Cold snapshots
7. Cold disk expansion
8. Cold CPU/memory/disk/NIC modification

Acceptance target for this phase: on a single compute node, prepare local bridge/TAP, manage qcow2 images, start a CirrOS VM through generated QEMU argv, observe guest TAP attachment, control shutdown/quit through QMP, and perform offline snapshot/resize/config edits while the VM is stopped.

## AGENTS TREE

```text
./AGENTS.md                          # 全仓库规则、入口、跨模块边界、调用链全景
├── internal/virt/AGENTS.md          # QEMU / QMP / qemu-img 本地虚拟化导航中枢
│   ├── internal/virt/qemu/AGENTS.md     # typed QEMU argv builder 内部展开
│   └── internal/virt/qemuimg/AGENTS.md  # qemu-img 子命令 + runner 边界
└── docs/roadmap/AGENTS.md           # 路线图维护规则
```

## STRUCTURE

```text
Govirta/
├── cmd/                 # govirtad/govirtlet/govirtctl/qemucli 入口
├── configs/             # govirtad/govirtlet 示例配置
├── docs/roadmap/        # 路线图维护说明；不存放里程碑明细
├── docs/superpowers/    # specs/plans 设计与执行计划归档
├── image/               # govirta_icon.png 项目视觉标识
├── internal/            # 所有 Go 内部模块边界；无 pkg/
│   ├── apiserver/       # API server boundary，目前 no-op skeleton
│   ├── controlplane/    # control plane composition
│   ├── network/bridge/  # Linux bridge boundary
│   ├── node/            # compute node agent composition
│   ├── scheduler/       # placement boundary
│   ├── store/           # state storage boundary
│   ├── types/           # shared domain types
│   ├── version/         # version string
│   └── virt/            # QEMU/QMP/qemu-img boundary
└── scripts/verify.sh    # 本地 CI 等价验证入口
```

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| 控制面入口 | `cmd/govirtad/main.go` → `internal/controlplane/service.go` → `internal/apiserver/server.go` | 当前 API server 为 no-op skeleton |
| 节点入口 | `cmd/govirtlet/main.go` → `internal/node/agent.go` → `internal/virt/qmp` + `internal/network/bridge` | 当前 QMP/bridge 为 no-op skeleton |
| CLI 版本输出 | `cmd/govirtctl/main.go` → `internal/version/version.go` | 当前只打印版本 |
| QEMU argv 示例 | `cmd/qemucli/main.go` → `internal/virt/qemu` | `qemucli` 只打印 argv，不启动 QEMU |
| QEMU 配置/参数 | `internal/virt/qemu/` (详见 `internal/virt/qemu/AGENTS.md`) | typed argv builder；黄金测试在 `vm_test.go` |
| qemu-img | `internal/virt/qemuimg/` (详见 `internal/virt/qemuimg/AGENTS.md`) | runner 边界在 `internal/virt/qemuimg/internal/exec` |
| 规划文档 | `docs/superpowers/specs`, `docs/superpowers/plans`, `docs/roadmap/README.md` | 设计和执行计划放 superpowers；roadmap 只保留维护说明 |
| 当前阶段优先级 | `## CURRENT PHASE` in this file + module AGENTS under `internal/virt/` | 单机冷操作闭环优先；不要先扩展分布式/热操作能力 |
| 本地验证 | `scripts/verify.sh` | gofmt check + tests + main service builds |

## CODE MAP

| Symbol | Type | Location | Role |
| --- | --- | --- | --- |
| `main` | func | `cmd/govirtad/main.go:11` | 初始化 logger/root context，运行 control plane |
| `controlplane.NewService` | func | `internal/controlplane/service.go:16` | 注入 `apiserver.NewNoopServer()` |
| `controlplane.Service.Run` | method | `internal/controlplane/service.go:23` | 调用 `apiserver.Server.Run(ctx)` |
| `apiserver.NoopServer.Run` | method | `internal/apiserver/server.go:19` | 当前唯一 Server 实现，等待 ctx done |
| `main` | func | `cmd/govirtlet/main.go:11` | 初始化 logger/root context，运行 node agent |
| `node.Agent.Run` | method | `internal/node/agent.go:26` | 组合 QMP client 与 bridge manager；当前注入 noop |
| `qmp.NoopClient.Connect` | method | `internal/virt/qmp/client.go:25` | QMP socket 边界，未连接（`Client` 接口在 `:6`） |
| `bridge.NoopManager.Ensure` | method | `internal/network/bridge/bridge.go:25` | bridge 创建边界，未调用 netlink |
| `qemu.NewVM` / `Builder.Build` / `VM.Argv` | funcs/methods | `internal/virt/qemu/vm.go:118-261` | typed VM composition → deterministic QEMU argv |
| `qemuimg.NewClient` | func | `internal/virt/qemuimg/client.go:34` | qemu-img client 聚合入口 |
| `imgexec.Runner.Run` | interface | `internal/virt/qemuimg/internal/exec/exec.go:18` | binary + `[]string` 外部命令执行边界 |
| `version.String` | func | `internal/version/version.go:12` | 拼接 `"govirta 0.1.0-dev"` |

## CALL GRAPHS & DATA FLOW

四种主要入口形态：control-plane daemon、compute-node daemon、CLI 一次性输出、QEMU argv 渲染器。每条跨模块跳转都给出 `file:line (Symbol)` 与下钻锚点。

### Flow: govirtad control plane boot {#flow-govirtad-boot}

- Trigger: `cmd/govirtad/main.go:11 (main)` (process entry; reads no flags currently)
- Cross-module chain:
  1. `cmd/govirtad/main.go:12 (main)` — 构造 zerolog logger（`process=govirtad`）
  2. `cmd/govirtad/main.go:13 (main)` — `logger.WithContext(context.Background())` 得到 root `ctx`
  3. `cmd/govirtad/main.go:15 (main → controlplane.NewService)` — 进入 `internal/controlplane` 装配层
  4. `internal/controlplane/service.go:16 (NewService)` — 注入 `apiserver.NewNoopServer()`，返回 `*Service`
  5. `internal/controlplane/service.go:23 (Service.Run)` — 写 `Info("starting control plane")`，立即调用 `s.apiServer.Run(ctx)`
  6. `internal/apiserver/server.go:19 (NoopServer.Run)` — `select { <-ctx.Done() / default: return nil }`，无监听端口
- Data: 无业务数据；`context.Context` 透传，logger 字段 `process=govirtad`
- Boundaries: 单进程同步；无 RPC/MQ；无事务作用域
- Sinks: stdout 一行启动日志后立即返回 `nil` → 进程 `os.Exit(0)`；当前**未**绑定任何 socket / 端口

### Flow: govirtlet node agent boot {#flow-govirtlet-boot}

- Trigger: `cmd/govirtlet/main.go:11 (main)` (process entry on compute host)
- Cross-module chain:
  1. `cmd/govirtlet/main.go:12 (main)` — 构造 zerolog logger（`process=govirtlet`）
  2. `cmd/govirtlet/main.go:13 (main)` — `logger.WithContext(context.Background())` 得到 root `ctx`
  3. `cmd/govirtlet/main.go:15 (main → node.NewAgent)` — 进入 `internal/node` 组合层
  4. `internal/node/agent.go:18 (NewAgent)` — 注入 `qmp.NewNoopClient()` + `bridge.NewNoopManager()`，返回 `*Agent`
  5. `internal/node/agent.go:26 (Agent.Run)` — 在 logger 上挂 `component=node` / `qmp_client=qmp-noop` / `bridge_manager=bridge-noop`，写 `Info("starting node agent")`
  6. `internal/node/agent.go:36 (Agent.Run)` — `select { <-ctx.Done() / default: return nil }`，**未**调用 `qmpClient.Connect`，**未**调用 `bridgeManager.Ensure`
  7. (future) `internal/virt/qmp/client.go:25 (NoopClient.Connect)` — 未来由真实实现替换；连接 QMP unix socket [详见 `internal/virt/AGENTS.md` 边界说明]
  8. (future) `internal/network/bridge/bridge.go:25 (NoopManager.Ensure)` — 未来由真实实现替换；通过 netlink 创建/复用 bridge
  9. (future) `internal/virt/qemu/vm.go:224 (VM.Argv)` — 未来构建 QEMU argv 并 spawn 子进程 [详见 `internal/virt/qemu/AGENTS.md#flow-argv-build`]
- Data: `context.Context` + 注入的 `qmp.Client` / `bridge.Manager` 接口；无 VM 规约数据流（待 store/scheduler 接入）
- Boundaries: 单进程；未来将跨进程对接 QEMU（QMP unix socket）、跨内核子系统对接 netlink；当前全部 in-proc no-op
- Sinks: stdout 一行启动日志后立即 `os.Exit(0)`；未来 sinks 包括 QMP 命令、netlink 操作、QEMU 子进程生命周期

### Flow: govirtctl version output {#flow-govirtctl-version}

- Trigger: `cmd/govirtctl/main.go:9 (main)` (CLI 一次性执行)
- Cross-module chain:
  1. `cmd/govirtctl/main.go:10 (main)` — `fmt.Println(version.String())`
  2. `internal/version/version.go:12 (String)` — `return Name + " " + Version`，硬编码 `"govirta 0.1.0-dev"`
- Data: 无；纯字符串拼接
- Boundaries: 同步、单进程
- Sinks: stdout 一行 `"govirta 0.1.0-dev"`，进程退出 0

### Flow: qemucli argv rendering {#flow-qemucli-argv}

- Trigger: `cmd/qemucli/main.go:23 (main)` (CLI 一次性执行)
- Cross-module chain:
  1. `cmd/qemucli/main.go:24 (main → buildDefaultArgv)` — 进入本地辅助函数
  2. `cmd/qemucli/main.go:35 (buildDefaultArgv)` — 构造 typed VM 链式调用 [详见 `internal/virt/qemu/AGENTS.md#flow-argv-build`]
  3. `internal/virt/qemu/vm.go:118 (NewVM)` → `Builder.<setters>` → `Build()` → `VM.Argv()` 返回 `[]string`
  4. `cmd/qemucli/main.go:29 (main)` — `fmt.Println(strings.Join(argv, " "))`
- Data: `qemu.Arch` → `*Builder` (字段化配置) → `VM` (不可变快照) → `[]string` argv → 单行字符串
- Boundaries: 同步、单进程；**不**调用 `os/exec`，**不**启动 QEMU
- Sinks: stdout 一行 QEMU 命令字符串；进程退出 0（构建错误走 stderr + exit 1）

```text
govirtad:  main → controlplane.Service.Run → apiserver.NoopServer.Run → ctx.Done() / nil
govirtlet: main → node.Agent.Run → (future: qmp.Connect + bridge.Ensure + qemu.Argv → exec)
govirtctl: main → version.String → stdout
qemucli:   main → qemu.Builder → VM.Argv → stdout (no exec)
```

证据来源：直接读取 4 个 `cmd/*/main.go` 与对应内部包源码 + 已有单元测试 (`server_test.go` / `agent_test.go` / `vm_test.go`)。`[已验证]`

## CONVENTIONS

- Module: `github.com/suknna/govirta`; `go.mod` declares Go `1.26` and direct dependency `github.com/rs/zerolog v1.34.0`.
- Root `context.Context` is created in `cmd/*/main.go`; internal packages must accept caller-provided `ctx` for I/O, long-running work, cross-package calls, and goroutines.
- Unit tests live next to packages and favor behavior names such as `Test<Subject><ExpectedBehavior>` plus table-driven `t.Run` cases.
- Any I/O / runner / long-running boundary that accepts `ctx` should cover `context.Canceled` behavior in tests.
- Unit tests must not require real QEMU binaries, TAP devices, or the remote acceptance host. Use fake runners for qemu-img unit tests.
- Command execution boundaries must pass `binary` + `[]string`; do not build shell command strings in production code.
- Runtime logs use zerolog structured fields. `fmt.Println` is acceptable for CLI user output, not library runtime logs.

## ANTI-PATTERNS (THIS PROJECT)

- **No libvirt, ever.** Do not introduce `libvirt.org/go/libvirt`, `digitalocean/go-libvirt`, libvirtd, or libvirt-derived abstractions or design notes.
- Do not preserve backward compatibility for internal APIs during this fast-iteration phase; replace wrong abstractions directly.
- Do not reintroduce standalone milestone documents under `docs/roadmap/cycle-*.md`; keep planning details in specs/plans.
- Do not create orphan `context.Background()` / `context.TODO()` inside internal production packages.
- Do not start fire-and-forget goroutines; every goroutine needs owner, shutdown path, and `ctx.Done()` for long-running work.
- Do not use `panic` for expected business errors, string-match errors, swallow errors silently, or use `goto` as normal control flow.
- Do not let QEMU packages create host bridge/TAP resources; host networking belongs under `internal/network/bridge`.
- Do not spend implementation effort on distributed scheduling, Kubernetes integration, live migration, hot-plug, or multi-node control before the single-node cold-operation closure is complete.
- Do not implement cold snapshot, cold resize, or cold config modification against a running VM; these operations must require a stopped/offline VM until a later hot-operation phase is explicitly designed.

## UNIQUE STYLES

- Project icon: `image/govirta_icon.png`; brand colors from non-white icon regions are primary violet-blue `#2000C0` and secondary teal `#00B0B0`.
- Architecture is Kubernetes-inspired control plane / node separation, but short-term scope explicitly excludes Kubernetes and CRD integration.
- Current product shape is a single-node cold-operation loop: qemu-system argv, qemu-img qcow2 lifecycle, VM process lifecycle, minimal QMP control, local TAP/bridge, and offline mutation.
- `docs/superpowers/specs` and `docs/superpowers/plans` hold implementation design and execution plans; root docs stay high-level.
- Current skeleton/no-op packages are intentional boundary placeholders, not proof that the feature is complete.
- Every implementation handoff must report affected call relationships, for example `cmd/govirtlet/main.go -> internal/node.Agent.Run -> internal/virt/qemu.Driver`.

## COMMANDS

```bash
# Local CI equivalent from scripts/verify.sh
gofmt -l .
go test ./...
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl

# Required for concurrency-sensitive changes
go test -race ./...
```

Notes: no `.github/workflows` CI exists currently. `scripts/verify.sh` does not build `cmd/qemucli`; update the script if qemucli becomes a release binary.

## NOTES

- Remote acceptance environment from project memory: `root@192.168.139.206`, Rocky Linux 8.10 aarch64, QEMU `/usr/libexec/qemu-kvm`, qemu-img `/usr/bin/qemu-img`, bridge `govirta0`, tap `gv-tap0`, firmware `/usr/share/edk2/aarch64/QEMU_EFI.fd`.
- Cross-compile remote QEMU tests locally because Go is not installed on the acceptance host: `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c ./internal/virt/qemu -o .tmp/govirta-qemu.test`, then copy and run remotely with `GOVIRTA_QEMU_INTEGRATION=1` and the known QEMU/qemu-img/firmware/TAP env vars.
- Development temporary artifacts belong under project `.tmp/`; do not use global `/tmp` for debugging artifacts.
- Call-graph evidence: 全量人工读取 `cmd/*/main.go` + 对应 internal 包源码与测试，未走 LSP `prepareCallHierarchy`（Go LSP 在当前 toolchain 未配置）。`[降级]` LSP；`[已验证]` 源码与测试断言。
