# PROJECT AGENTS KNOWLEDGE BASE

**Generated:** 2026-05-28
**Commit:** 6c06c5f
**Branch:** main

<!--
Verified-against:
  base_commit: 6c06c5f
  files:
    - cmd/govirtad/main.go
    - cmd/govirtlet/main.go
    - cmd/govirtctl/main.go
    - cmd/qemucli/main.go
    - internal/controlplane/service.go
    - internal/apiserver/server.go
    - internal/node/agent.go
    - internal/network/bridge/bridge.go
    - internal/scheduler/scheduler.go
    - internal/types/types.go
    - internal/version/version.go
    - internal/storage/service.go
    - internal/storage/image_service.go
    - internal/storage/pool/service.go
    - internal/storage/pool/pool.go
    - internal/storage/block/driver.go
    - internal/storage/image/driver.go
    - internal/storage/local/driver.go
    - internal/storage/localfile/driver.go
    - internal/storage/diskformat/format.go
    - internal/virt/qmp/client.go
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
    - anchor: flow-storage-volume
      sources:
        - internal/storage/service.go
        - internal/storage/pool/service.go
        - internal/storage/local/driver.go
        - internal/virt/qemuimg/client.go
    - anchor: flow-storage-image
      sources:
        - internal/storage/image_service.go
        - internal/storage/pool/service.go
        - internal/storage/localfile/driver.go
    - anchor: flow-storage-image-root-volume
      sources:
        - internal/storage/image_service.go
        - internal/storage/service.go
        - internal/storage/pool/service.go
        - internal/storage/local/driver.go
        - internal/virt/qemuimg/client.go
-->

## OVERVIEW

Govirta is a Go virtualization infrastructure platform that starts at the QEMU layer and builds toward lightweight VM orchestration. Current stack: Go 1.26 + QEMU + QMP + qemu-img + Linux bridge/netlink + zerolog, with OpenStack-style internal storage abstractions now present under `internal/storage`.

## CURRENT PHASE

Govirta is in the single-node cold-operation closure phase. Prioritize the local QEMU/qemu-img/QMP/network/storage path before distributed scheduling, API orchestration, Kubernetes integration, live migration, hot-plug, or multi-node behavior.

Acceptance target: on one compute node, explicitly register storage pools, store raw/qcow2 images, create independent qcow2 root volumes, prepare bridge/TAP, render/start QEMU argv, observe/control QMP state, and perform snapshot/resize/config edits only while the VM is stopped.

Current implementation priority:

1. qemu-system CLI builder
2. qemu-img qcow2 management
3. storage pool / image / root-volume lifecycle
4. VM create/start/stop/delete
5. QMP `query-status` / `system_powerdown` / `quit`
6. Local TAP/bridge networking
7. Cold snapshots
8. Cold disk expansion
9. Cold CPU/memory/disk/NIC modification

## AGENTS TREE

```text
./AGENTS.md                          # 全仓库规则、入口、跨模块边界、调用链全景
├── internal/storage/AGENTS.md       # VM-facing storage, pool, block/image drivers
├── internal/virt/AGENTS.md          # QEMU / QMP / qemu-img 本地虚拟化导航中枢
│   ├── internal/virt/qemu/AGENTS.md     # typed QEMU argv builder 内部展开
│   ├── internal/virt/qemuimg/AGENTS.md  # qemu-img 子命令 + runner 边界
│   └── internal/virt/qmp/AGENTS.md      # project-owned QMP socket facade
└── docs/roadmap/AGENTS.md           # 路线图维护规则
```

## STRUCTURE

```text
Govirta/
├── cmd/                 # govirtad/govirtlet/govirtctl/qemucli 入口
├── configs/             # govirtad/govirtlet 示例配置
├── docs/roadmap/        # 路线图维护说明；不存放 milestone 明细
├── docs/superpowers/    # specs/plans 设计与执行计划归档
├── image/               # govirta_icon.png 项目视觉标识
├── internal/            # 所有 Go 内部模块边界；无 pkg/
│   ├── apiserver/       # API server boundary，目前 no-op skeleton
│   ├── controlplane/    # control plane composition
│   ├── network/bridge/  # Linux bridge boundary
│   ├── node/            # compute node agent composition
│   ├── scheduler/       # placement boundary
│   ├── storage/         # pool + volume + image storage boundary
│   ├── types/           # shared domain types
│   ├── version/         # version string
│   └── virt/            # QEMU/QMP/qemu-img boundary
└── scripts/verify.sh    # 本地 CI 等价验证入口
```

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| 控制面入口 | `cmd/govirtad/main.go` → `internal/controlplane/service.go` → `internal/apiserver/server.go` | 当前 API server 为 no-op skeleton |
| 节点入口 | `cmd/govirtlet/main.go` → `internal/node/agent.go` → `internal/virt/qmp` + `internal/network/bridge` | 当前 QMP/bridge 仍为 skeleton 注入 |
| CLI 版本输出 | `cmd/govirtctl/main.go` → `internal/version/version.go` | 当前只打印版本 |
| QEMU argv 示例 | `cmd/qemucli/main.go` → `internal/virt/qemu` | `qemucli` 只打印 argv，不启动 QEMU |
| VM-facing storage | `internal/storage/` (详见 `internal/storage/AGENTS.md`) | `VolumeService` / `ImageService` / `pool.Service` |
| QEMU 配置/参数 | `internal/virt/qemu/` (详见 `internal/virt/qemu/AGENTS.md`) | typed argv builder；黄金测试在 `vm_test.go` |
| qemu-img | `internal/virt/qemuimg/` (详见 `internal/virt/qemuimg/AGENTS.md`) | Create/Info/Convert/Resize/Snapshot/Check/Remove + runner |
| QMP | `internal/virt/qmp/` (详见 `internal/virt/qmp/AGENTS.md`) | socket client, command facade, events |
| 规划文档 | `docs/superpowers/specs`, `docs/superpowers/plans`, `docs/roadmap/README.md` | 设计和执行计划放 superpowers；roadmap 只保留维护说明 |
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
| `qmp.SocketClient.Connect` | method | `internal/virt/qmp/client.go:76` | 连接 QMP unix socket 并完成 capabilities handshake |
| `qmp.NoopClient.Connect` | method | `internal/virt/qmp/client.go:279` | skeleton composition test 用 no-op QMP 边界 |
| `bridge.NoopManager.Ensure` | method | `internal/network/bridge/bridge.go:25` | bridge 创建边界，未调用 netlink |
| `storage.VolumeService` | struct | `internal/storage/service.go:14` | VM-facing block volume API；所有操作显式 PoolName |
| `storage.ImageService` | struct | `internal/storage/image_service.go:12` | file image byte-stream API；Put/Get/Delete |
| `pool.Service` | struct | `internal/storage/pool/service.go:16` | pool registry, capacity accounting, in-memory indexes |
| `local.Driver` | struct | `internal/storage/local/driver.go:38` | host-local qcow2 block driver using qemu-img |
| `localfile.Driver` | struct | `internal/storage/localfile/driver.go:39` | host-local raw/qcow2 image byte store |
| `qemu.NewVM` / `Builder.Build` / `VM.Argv` | funcs/methods | `internal/virt/qemu/vm.go:178-377` | typed VM composition → deterministic QEMU argv |
| `qemuimg.NewClient` | func | `internal/virt/qemuimg/client.go:81` | qemu-img client 聚合入口 |
| `imgexec.Runner.Run` | interface | `internal/virt/qemuimg/internal/exec/exec.go:18` | binary + `[]string` 外部命令执行边界 |
| `version.String` | func | `internal/version/version.go:12` | 拼接 `"govirta 0.1.0-dev"` |

## CALL GRAPHS & DATA FLOW

主要入口：control-plane daemon、compute-node daemon、CLI 输出、QEMU argv 渲染器，以及 storage service API（当前尚未接入 cmd 入口，但已是 VM 编排层内部边界）。

### Flow: govirtad control plane boot {#flow-govirtad-boot}

- Trigger: `cmd/govirtad/main.go:11 (main)` (process entry; reads no flags currently)
- Cross-module chain:
  1. `cmd/govirtad/main.go:12 (main)` — 构造 zerolog logger（`process=govirtad`）
  2. `cmd/govirtad/main.go:13 (main)` — `logger.WithContext(context.Background())` 得到 root `ctx`
  3. `cmd/govirtad/main.go:15 (main → controlplane.NewService)` — 进入 `internal/controlplane` 装配层
  4. `internal/controlplane/service.go:16 (NewService)` — 注入 `apiserver.NewNoopServer()`，返回 `*Service`
  5. `internal/controlplane/service.go:23 (Service.Run)` — 写 `Info("starting control plane")`，调用 `s.apiServer.Run(ctx)`
  6. `internal/apiserver/server.go:19 (NoopServer.Run)` — `select { <-ctx.Done() / default: return nil }`，无监听端口
- Data: 无业务数据；`context.Context` 透传，logger 字段 `process=govirtad`
- Boundaries: 单进程同步；无 RPC/MQ；无事务作用域
- Sinks: stdout 启动日志后立即返回 `nil`；当前未绑定 socket / 端口

### Flow: govirtlet node agent boot {#flow-govirtlet-boot}

- Trigger: `cmd/govirtlet/main.go:11 (main)` (process entry on compute host)
- Cross-module chain:
  1. `cmd/govirtlet/main.go:12 (main)` — 构造 zerolog logger（`process=govirtlet`）
  2. `cmd/govirtlet/main.go:13 (main)` — `logger.WithContext(context.Background())` 得到 root `ctx`
  3. `cmd/govirtlet/main.go:15 (main → node.NewAgent)` — 进入 `internal/node` 组合层
  4. `internal/node/agent.go:18 (NewAgent)` — 注入 `qmp.NewNoopClient()` + `bridge.NewNoopManager()`，返回 `*Agent`
  5. `internal/node/agent.go:26 (Agent.Run)` — 在 logger 上挂 `component=node` / `qmp_client` / `bridge_manager`
  6. `internal/node/agent.go:36 (Agent.Run)` — `select { <-ctx.Done() / default: return nil }`，未调用 QMP/bridge
  7. (future) `internal/virt/qmp/client.go:76 (SocketClient.Connect)` — 连接 QMP unix socket [详见 `internal/virt/qmp/AGENTS.md#flow-qmp-ready`]
  8. (future) `internal/network/bridge/bridge.go:25 (NoopManager.Ensure)` — 未来由真实 netlink manager 替换
  9. (future) `internal/storage/service.go:171 (VolumeService.PublishVolume)` — 获取 root disk file attachment [详见 `internal/storage/AGENTS.md#flow-storage-volume`]
 10. (future) `internal/virt/qemu/vm.go:340 (VM.Argv)` — 构建 QEMU argv 并 spawn 子进程 [详见 `internal/virt/qemu/AGENTS.md#flow-argv-build`]
- Data: `context.Context` + 注入的 `qmp.Client` / `bridge.Manager`；未来会接收 VM spec + storage attachment
- Boundaries: 当前 in-proc no-op；未来跨进程 QMP unix socket、QEMU 子进程、内核 bridge/TAP
- Sinks: 当前仅启动日志；未来 sinks 包括 QMP 命令、netlink 操作、QEMU 子进程生命周期

### Flow: govirtctl version output {#flow-govirtctl-version}

- Trigger: `cmd/govirtctl/main.go:9 (main)` (CLI 一次性执行)
- Cross-module chain:
  1. `cmd/govirtctl/main.go:10 (main)` — `fmt.Println(version.String())`
  2. `internal/version/version.go:12 (String)` — `return Name + " " + Version`
- Data: 无；纯字符串拼接
- Boundaries: 同步、单进程
- Sinks: stdout 一行 `"govirta 0.1.0-dev"`

### Flow: qemucli argv rendering {#flow-qemucli-argv}

- Trigger: `cmd/qemucli/main.go:23 (main)` (CLI 一次性执行)
- Cross-module chain:
  1. `cmd/qemucli/main.go:24 (main → buildDefaultArgv)` — 进入本地辅助函数
  2. `cmd/qemucli/main.go:35 (buildDefaultArgv)` — 构造 typed VM 链式调用 [详见 `internal/virt/qemu/AGENTS.md#flow-argv-build`]
  3. `internal/virt/qemu/vm.go:178 (NewVM)` → `Builder.<setters>` → `Build()` → `VM.Argv()` 返回 `[]string`
  4. `cmd/qemucli/main.go:29 (main)` — `fmt.Println(strings.Join(argv, " "))`
- Data: `qemu.Arch` → `*Builder` → `VM` → `[]string` argv → 单行字符串
- Boundaries: 同步、单进程；不调用 `os/exec`，不启动 QEMU
- Sinks: stdout 一行 QEMU 命令字符串；错误走 stderr + exit 1

### Flow: storage block volume lifecycle {#flow-storage-volume}

- Trigger: `internal/storage/service.go:80 (VolumeService.CreateVolume)` / `:171 (PublishVolume)` / `:206 (DeleteVolume)` (future VM orchestration caller)
- Cross-module chain:
  1. `internal/storage/service.go:80 (VolumeService.CreateVolume)` — 校验 explicit `PoolName` + VM/disk identity [详见 `internal/storage/AGENTS.md#flow-storage-volume`]
  2. `internal/storage/pool/service.go:158 (pool.Service.CreateVolume)` — block pool lookup + capacity admission + in-memory index update
  3. `internal/storage/local/driver.go:87 (local.Driver.Create)` — driver-owned qcow2 path + `qemu-img create`
  4. `internal/virt/qemuimg/client.go:105 (QCOW2Client.Create)` — qemu-img builder [详见 `internal/virt/qemuimg/AGENTS.md#flow-qcow2-do`]
- Data: `CreateVolumeRequest` → `block.CreateRequest` → `volume.Volume` → optional `volume.PublishedVolume`
- Boundaries: in-proc service/driver calls; qemu-img subprocess via runner; filesystem under trusted storage root
- Sinks: qcow2 file create/delete, in-memory `Pool.volumes`, runtime file attachment path

### Flow: storage image lifecycle {#flow-storage-image}

- Trigger: `internal/storage/image_service.go:42 (ImageService.PutImage)` / `:57 (GetImage)` / `:68 (DeleteImage)` (future control-plane image catalog caller)
- Cross-module chain:
  1. `internal/storage/image_service.go:42 (ImageService.PutImage)` — 校验 explicit `PoolName` + image request [详见 `internal/storage/AGENTS.md#flow-storage-image`]
  2. `internal/storage/pool/service.go:397 (pool.Service.PutImage)` — reserve capacity + create pending image record
  3. `internal/storage/localfile/driver.go:71 (localfile.Driver.Put)` — open `target.tmp` writer under file pool
  4. `internal/storage/pool/service.go:606 (pendingImageWriter.Close)` — driver commit success 后将 pending → ready
- Data: `PutImageRequest` → `image.PutRequest` → `image.ImageWriter` → `pool.ImageRecord{pending|ready|deleting}`
- Boundaries: in-proc writer; filesystem hard-link commit; metadata only in memory
- Sinks: raw/qcow2 image bytes under `StorageRoot/pool/<pool>/images`, in-memory `Pool.images`

### Flow: image-derived root volume {#flow-storage-image-root-volume}

- Trigger: future orchestration path uses `ImageService.GetImage` then `VolumeService.CreateRootVolumeFromReader`
- Cross-module chain:
  1. `internal/storage/image_service.go:57 (ImageService.GetImage)` — open ready image reader [详见 `internal/storage/AGENTS.md#flow-storage-image-root-volume`]
  2. `internal/storage/service.go:123 (VolumeService.CreateRootVolumeFromReader)` — require explicit `PoolName` and `diskformat.Format`
  3. `internal/storage/pool/service.go:206 (pool.Service.CreateVolumeFromReader)` — block pool capacity/index lifecycle
  4. `internal/storage/local/driver.go:126 (local.Driver.CreateFromReader)` — qcow2 full copy or raw→qcow2 convert
  5. `internal/virt/qemuimg/client.go:115 (QCOW2Client.Convert)` / `:120 (Resize)` — qemu-img subprocess [详见 `internal/virt/qemuimg/AGENTS.md#flow-qcow2-do`]
- Data: image `io.ReadCloser` + explicit `Format` → `block.CreateFromReaderRequest` → independent qcow2 `volume.Volume`
- Boundaries: byte-stream read/write; qemu-img convert/resize subprocess for raw or capacity expansion
- Sinks: full independent qcow2 root disk; no backing-file links to source image

证据来源：子代理只读扫描 + AFT outline/zoom + 直接读取入口、storage、virt 源码；调用图以 AFT/源码读取为主，LSP call hierarchy 未全量可用。`[已验证]` / `[降级: LSP call hierarchy]`

## CONVENTIONS

- Module: `github.com/suknna/govirta`; `go.mod` declares Go `1.26` and direct dependency `github.com/rs/zerolog v1.34.0`.
- Root `context.Context` is created in `cmd/*/main.go`; internal packages must accept caller-provided `ctx` for I/O, long-running work, cross-package calls, and goroutines.
- Unit tests live next to packages and favor behavior names such as `Test<Subject><ExpectedBehavior>` plus table-driven `t.Run` cases.
- Any I/O / runner / long-running boundary that accepts `ctx` should cover `context.Canceled` behavior in tests.
- Unit tests must not require real QEMU binaries, TAP devices, or the remote acceptance host. Use fake runners for qemu-img and storage-local unit tests.
- Command execution boundaries must pass `binary` + `[]string`; do not build shell command strings in production code.
- Runtime logs use zerolog structured fields. `fmt.Println` is acceptable for CLI user output, not library runtime logs.
- Storage APIs require explicit pool, format, and source choices when behavior affects storage outcomes; no implicit default storage pool or format inference.

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
- Do not add qemu-nbd, qemu-storage-daemon, qemu-io, CSI sidecars, gRPC storage services, or libvirt-derived storage abstractions in the current phase.

## UNIQUE STYLES

- Project icon: `image/govirta_icon.png`; brand colors from non-white icon regions are primary violet-blue `#2000C0` and secondary teal `#00B0B0`.
- Architecture is Kubernetes/OpenStack-inspired control plane / node / storage separation, but short-term scope excludes Kubernetes and CRD integration.
- Current product shape is a single-node cold-operation loop: storage pools/images/root volumes, qemu-system argv, qemu-img qcow2 lifecycle, VM process lifecycle, minimal QMP control, local TAP/bridge, and offline mutation.
- `docs/superpowers/specs` and `docs/superpowers/plans` hold implementation design and execution plans; root docs stay high-level.
- Current skeleton/no-op packages are intentional boundary placeholders, not proof that the feature is complete.
- Every implementation handoff must report affected call relationships, for example `cmd/govirtlet/main.go -> internal/node.Agent.Run -> internal/storage.VolumeService -> internal/virt/qemu.Driver`.

## COMMANDS

```bash
# Local CI equivalent from scripts/verify.sh
gofmt -l .
go test ./...
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl

# Required for concurrency-sensitive changes
go test -race ./...

# Focused storage / qemu-img verification
go test -count=1 ./internal/storage/... ./internal/virt/qemuimg/...
go test -race -count=1 ./internal/storage/...
```

Notes: no `.github/workflows` CI exists currently. `scripts/verify.sh` does not build `cmd/qemucli`; update the script if qemucli becomes a release binary.

## NOTES

- Remote acceptance environment from project memory: `root@192.168.139.206`, Rocky Linux 8.10 aarch64, QEMU `/usr/libexec/qemu-kvm`, qemu-img `/usr/bin/qemu-img`, bridge `govirta0`, tap `gv-tap0`, firmware `/usr/share/edk2/aarch64/QEMU_EFI.fd`.
- Cross-compile remote QEMU tests locally because Go is not installed on the acceptance host: `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c ./internal/virt/qemu -o .tmp/govirta-qemu.test`, then copy and run remotely with `GOVIRTA_QEMU_INTEGRATION=1` and the known QEMU/qemu-img/firmware/TAP env vars.
- Development temporary artifacts belong under project `.tmp/`; do not use global `/tmp` for debugging artifacts.
- Storage metadata is in memory only: after restart, callers must explicitly re-register pools and image catalog state; drivers do not scan storage roots or write metadata files.
- File/image pool overcommit ratio is `1.0`; block pool overcommit ratio is `1.5`.
- Call-graph evidence: AFT outline/zoom and direct source/test reads; LSP call hierarchy was not used end-to-end. `[降级]` LSP；`[已验证]` 源码与测试断言。
