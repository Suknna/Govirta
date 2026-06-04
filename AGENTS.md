# PROJECT AGENTS KNOWLEDGE BASE

**Generated:** 2026-06-04
**Commit:** 3edfafd
**Branch:** main

<!--
Verified-against:
  base_commit: 3edfafd
  files:
    - cmd/govirtad/main.go
    - cmd/govirtlet/main.go
    - cmd/govirtctl/main.go
    - cmd/qemucli/main.go
    - internal/controlplane/service.go
    - internal/apiserver/server.go
    - internal/node/agent.go
    - internal/hostnet/link/link.go
    - internal/hostnet/link/constants.go
    - internal/hostnet/link/linkerr/errors.go
    - internal/hostnet/link/linux/manager_linux.go
    - internal/hostnet/link/linux/handle_linux.go
    - internal/hostnet/link/linux/info_linux.go
    - internal/hostnet/link/linux/validate_linux.go
    - internal/hostnet/link/linux/errors_linux.go
    - internal/hostnet/route/route.go
    - internal/hostnet/route/constants.go
    - internal/hostnet/route/forwarding.go
    - internal/hostnet/route/noop.go
    - internal/hostnet/route/noop_test.go
    - internal/hostnet/route/routeerr/errors.go
    - internal/hostnet/route/linux/manager_linux.go
    - internal/hostnet/route/linux/handle_linux.go
    - internal/hostnet/route/linux/info_linux.go
    - internal/hostnet/route/linux/validate_linux.go
    - internal/hostnet/route/linux/errors_linux.go
    - internal/hostnet/route/linux/sysctl_linux.go
    - internal/hostnet/route/linux/fake_handle_test.go
    - internal/hostnet/route/linux/forwarding_test.go
    - internal/hostnet/route/linux/validation_test.go
    - internal/hostnet/route/linux/route_test.go
    - internal/hostnet/route/linux/list_get_test.go
    - internal/hostnet/route/linux/errors_test.go
    - internal/hostnet/firewall/firewall.go
    - internal/hostnet/firewall/constants.go
    - internal/hostnet/firewall/noop.go
    - internal/hostnet/firewall/firewallerr/errors.go
    - internal/hostnet/firewall/linux/manager_linux.go
    - internal/hostnet/firewall/linux/handle_linux.go
    - internal/hostnet/firewall/linux/rules_linux.go
    - internal/hostnet/firewall/linux/info_linux.go
    - internal/hostnet/firewall/linux/expr_linux.go
    - internal/hostnet/firewall/linux/validate_linux.go
    - internal/hostnet/firewall/linux/errors_linux.go
    - internal/hostnet/dhcp/dhcp.go
    - internal/hostnet/dhcp/constants.go
    - internal/hostnet/dhcp/noop.go
    - internal/hostnet/dhcp/noop_test.go
    - internal/hostnet/dhcp/dhcperr/errors.go
    - internal/hostnet/dhcp/coredhcp/manager.go
    - internal/hostnet/dhcp/coredhcp/runtime.go
    - internal/hostnet/dhcp/coredhcp/handler.go
    - internal/hostnet/dhcp/coredhcp/validate.go
    - internal/hostnet/dhcp/coredhcp/errors.go
    - internal/hostnet/dhcp/coredhcp/info.go
    - internal/hostnet/dhcp/coredhcp/starter_linux.go
    - internal/hostnet/dhcp/coredhcp/starter_unsupported.go
    - internal/hostnet/dhcp/coredhcp/binding_test.go
    - internal/hostnet/dhcp/coredhcp/handler_test.go
    - internal/hostnet/dhcp/coredhcp/manager_test.go
    - internal/network/service.go
    - internal/network/nic_service.go
    - internal/network/netpool/network.go
    - internal/network/netpool/service.go
    - internal/network/netpool/orchestrate.go
    - internal/network/networker/errors.go
    - internal/hostnet/firewall/linux/forward_linux.go
    - internal/hostnet/firewall/linux/forward_expr_linux.go
    - test/acceptance/network_egress_test.go
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
    - scripts/acceptance.sh
    - test/acceptance/doc.go
    - test/acceptance/harness.go
    - test/acceptance/hostnet_route_test.go
    - test/acceptance/hostnet_dhcp_test.go
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
        - internal/network/service.go
        - internal/network/nic_service.go
    - anchor: flow-hostnet-bridge
      sources:
        - internal/hostnet/link/link.go
        - internal/hostnet/link/linux/manager_linux.go
        - internal/hostnet/link/linux/handle_linux.go
        - internal/hostnet/link/linux/info_linux.go
        - internal/hostnet/link/linux/validate_linux.go
        - internal/hostnet/link/linux/errors_linux.go
    - anchor: flow-hostnet-tap
      sources:
        - internal/hostnet/link/link.go
        - internal/hostnet/link/constants.go
        - internal/hostnet/link/linux/manager_linux.go
        - internal/hostnet/link/linux/handle_linux.go
        - internal/hostnet/link/linux/info_linux.go
        - internal/hostnet/link/linux/validate_linux.go
        - internal/hostnet/link/linux/errors_linux.go
    - anchor: flow-hostnet-route
      sources:
        - internal/hostnet/route/route.go
        - internal/hostnet/route/constants.go
        - internal/hostnet/route/forwarding.go
        - internal/hostnet/route/linux/manager_linux.go
        - internal/hostnet/route/linux/handle_linux.go
        - internal/hostnet/route/linux/info_linux.go
        - internal/hostnet/route/linux/validate_linux.go
        - internal/hostnet/route/linux/errors_linux.go
        - internal/hostnet/route/linux/sysctl_linux.go
    - anchor: flow-hostnet-firewall
      sources:
        - internal/hostnet/firewall/firewall.go
        - internal/hostnet/firewall/constants.go
        - internal/hostnet/firewall/linux/manager_linux.go
        - internal/hostnet/firewall/linux/handle_linux.go
        - internal/hostnet/firewall/linux/rules_linux.go
        - internal/hostnet/firewall/linux/info_linux.go
        - internal/hostnet/firewall/linux/expr_linux.go
        - internal/hostnet/firewall/linux/validate_linux.go
        - internal/hostnet/firewall/linux/errors_linux.go
    - anchor: flow-hostnet-dhcp
      sources:
        - internal/hostnet/dhcp/dhcp.go
        - internal/hostnet/dhcp/constants.go
        - internal/hostnet/dhcp/coredhcp/manager.go
        - internal/hostnet/dhcp/coredhcp/runtime.go
        - internal/hostnet/dhcp/coredhcp/handler.go
        - internal/hostnet/dhcp/coredhcp/validate.go
        - internal/hostnet/dhcp/coredhcp/errors.go
        - test/acceptance/hostnet_dhcp_test.go
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
    - anchor: flow-network-orchestrate
      sources:
        - internal/network/service.go
        - internal/network/nic_service.go
        - internal/network/netpool/orchestrate.go
    - anchor: flow-guest-egress
      sources:
        - internal/network/service.go
        - internal/network/nic_service.go
        - internal/network/netpool/orchestrate.go
        - test/acceptance/network_egress_test.go
-->

## OVERVIEW

Govirta is a Go distributed virtualization cluster platform — a Kubernetes-inspired master/node architecture where each compute node opens a long-lived, node-initiated connection to the control plane, registers itself, and executes VM tasks dispatched over that channel. It starts at the QEMU layer and builds upward into cluster-wide VM orchestration. Current stack: Go 1.26 + QEMU + QMP + qemu-img + Linux bridge/TAP/route/firewall primitives + CoreDHCP-backed static DHCP + zerolog, with OpenStack-style internal storage abstractions under `internal/storage` and a VM-facing network orchestration layer under `internal/network` that composes the hostnet primitives into a guest egress closure.

## CURRENT PHASE

Govirta is a distributed cluster from the ground up. The architectural spine is the Kubernetes-inspired master/node model: each `govirtlet` compute node dials the `govirtad` control plane over a long-lived, node-initiated connection, registers, and receives dispatched VM tasks on that channel. The node-local capabilities listed below (storage / virt / hostnet / network) are the execution building blocks the master orchestrates onto nodes — not a standalone single-host product.

Two scope deferrals are independent of this positioning and still hold: operations are cold-only for now (no hot-plug, no live migration yet), and the cluster is Kubernetes-inspired but not Kubernetes-integrated (no CRDs, does not run on or depend on Kubernetes).

Node-local capability acceptance: on a compute node, explicitly register storage pools, store raw/qcow2 images, create independent qcow2 root volumes, prepare bridge/TAP, host route, firewall, and static DHCP primitives, render/start QEMU argv, observe/control QMP state, and perform snapshot/resize/config edits only while the VM is stopped. This proves the per-node execution surfaces the master dispatches onto; it is not the project's end goal.

Current node-local capability priority:

1. qemu-system CLI builder
2. qemu-img qcow2 management
3. storage pool / image / root-volume lifecycle
4. VM create/start/stop/delete
5. QMP `query-status` / `system_powerdown` / `quit`
6. Local TAP/bridge/route/firewall/static DHCP networking primitives
7. Cold snapshots
8. Cold disk expansion
9. Cold CPU/memory/disk/NIC modification

## AGENTS TREE

```text
./AGENTS.md                          # 全仓库规则、入口、跨模块边界、调用链全景
├── internal/network/AGENTS.md       # VM-facing network orchestration layer (netpool + services)
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
│   ├── hostnet/dhcp/    # host DHCP primitive boundary and CoreDHCP-backed static binding implementation
│   ├── hostnet/firewall/ # host firewall primitive boundary and Linux nftables implementation
│   ├── hostnet/link/    # host bridge/TAP primitive boundary and Linux netlink implementation
│   ├── hostnet/route/   # host IPv4 route primitive boundary, forwarding checks, and Linux netlink implementation
│   ├── network/         # VM-facing network orchestration layer (详见 internal/network/AGENTS.md)
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
| 节点入口 | `cmd/govirtlet/main.go` → `internal/node/agent.go` → `internal/virt/qmp` + `internal/network`(编排层) | 当前 QMP 仍为 no-op；node agent 已组合 `NetworkService`/`NICService`（注入 no-op host 原语） |
| CLI 版本输出 | `cmd/govirtctl/main.go` → `internal/version/version.go` | 当前只打印版本 |
| QEMU argv 示例 | `cmd/qemucli/main.go` → `internal/virt/qemu` | `qemucli` 只打印 argv，不启动 QEMU |
| host bridge/TAP primitives | `internal/hostnet/link` → `internal/hostnet/link/linux` | `link.Manager` contract；Linux 通过 netlink ensure/get/list/delete bridge 和 TAP |
| host route primitives | `internal/hostnet/route` → `internal/hostnet/route/linux` | `route.Manager` contract；Linux 通过 netlink add/replace/delete/list/get IPv4 routes，并只读检查 `/proc/sys/net/ipv4/ip_forward` |
| host firewall primitives | `internal/hostnet/firewall` → `internal/hostnet/firewall/linux` | `firewall.Manager` contract；Linux 通过 nftables ensure/delete/list/get masquerade 和 endpoint anti-spoofing rules |
| hostnet DHCP static binding | `internal/hostnet/dhcp` → `internal/hostnet/dhcp/coredhcp` | `dhcp.Manager` contract；CoreDHCP-backed in-process static MAC/IP binding responder |
| VM-facing storage | `internal/storage/` (详见 `internal/storage/AGENTS.md`) | `VolumeService` / `ImageService` / `pool.Service` |
| VM-facing 网络编排层 | `internal/network/` (详见 `internal/network/AGENTS.md`) | `NetworkService` / `NICService` over 共享 `netpool.Service`；编排 link/route/firewall/dhcp 原语实现 guest 出外网闭环 |
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
| `node.Agent.Run` | method | `internal/node/agent.go:43` | 组合 QMP client 与 `NetworkService`/`NICService`；当前注入 no-op host 原语 |
| `qmp.SocketClient.Connect` | method | `internal/virt/qmp/client.go:76` | 连接 QMP unix socket 并完成 capabilities handshake |
| `qmp.NoopClient.Connect` | method | `internal/virt/qmp/client.go:279` | skeleton composition test 用 no-op QMP 边界 |
| `network.NetworkService` | struct | `internal/network/service.go:16` | VM-facing network API over `*netpool.Service`；`NewNetworkService` at `:22` |
| `network.NICService` | struct | `internal/network/nic_service.go:12` | VM-facing NIC API sharing the same `*netpool.Service`；`NewNICService` at `:18` |
| `netpool.Service` | struct | `internal/network/netpool/service.go:19` | registration + orchestration core；`NewService(link, route, firewall, dhcp)` at `:32` |
| `netpool.Service.EnsureNetwork` | method | `internal/network/netpool/orchestrate.go:42` | bridge → forwarding check → masquerade → forward-accept → DHCP，then live `GetNetworkStatus` |
| `netpool.Service.EnsureNIC` | method | `internal/network/netpool/orchestrate.go:111` | TAP → DHCP binding → endpoint anti-spoofing，then live `GetNICStatus` |
| `networker` sentinels | vars | `internal/network/networker/errors.go:9` | `ErrInvalidRequest` / `ErrNotFound` / `ErrAlreadyExists` / `ErrConflict` / `ErrNotReady` |
| `link.Manager` | interface | `internal/hostnet/link/link.go:14` | host link primitive API：`EnsureBridge` / `EnsureTap` / `Delete` / `Exists` / `Get` / `List` |
| `link.BridgeSpec` / `link.TapSpec` | structs | `internal/hostnet/link/link.go:52` / `:66` | 显式描述 bridge gateway/MTU/MAC 与 TAP owner/bridge/MTU/MAC/VNetHeader |
| `linklinux.Manager` | struct | `internal/hostnet/link/linux/manager_linux.go:15` | Linux netlink-backed implementation of `link.Manager` |
| `linklinux.NewManager` | func | `internal/hostnet/link/linux/manager_linux.go:21` | 构造真实 `netlink` handle-backed manager |
| `linklinux.Manager.EnsureBridge` | method | `internal/hostnet/link/linux/manager_linux.go:33` | validate spec → parse CIDR → create/reconcile bridge → set MAC/MTU/address/up → return observed info |
| `linklinux.Manager.EnsureTap` | method | `internal/hostnet/link/linux/manager_linux.go:80` | validate spec → require bridge → create/reconcile TAP → set MAC/MTU/master/up → return observed info |
| `linkerr` sentinels | vars | `internal/hostnet/link/linkerr/errors.go:6` | stable host link error classes for invalid/not-found/conflict/permission/incomplete/unsupported |
| `linklinux.translateError` | func | `internal/hostnet/link/linux/errors_linux.go:15` | maps netlink/syscall failures to `linkerr` sentinel classes while preserving cause |
| `route.Manager` | interface | `internal/hostnet/route/route.go:19` | host route primitive API：`GetIPv4Forwarding` / `CheckIPv4Forwarding` / `AddRoute` / `ReplaceRoute` / `DeleteRoute` / `ListRoutes` / `GetRoute` |
| `routelinux.Manager` | struct | `internal/hostnet/route/linux/manager_linux.go:18` | Linux netlink + `/proc/sys/net/ipv4/ip_forward` implementation of `route.Manager` |
| `routelinux.NewManager` | func | `internal/hostnet/route/linux/manager_linux.go:27` | 构造真实 handle-backed route manager |
| `routelinux.Manager.GetIPv4Forwarding` | method | `internal/hostnet/route/linux/manager_linux.go:59` | read `/proc/sys/net/ipv4/ip_forward` and return observed forwarding state without mutation |
| `routelinux.Manager.CheckIPv4Forwarding` | method | `internal/hostnet/route/linux/manager_linux.go:87` | validate expected state, read observed forwarding state, return `routeerr.ErrNotReady` on mismatch |
| `routelinux.Manager.AddRoute` | method | `internal/hostnet/route/linux/manager_linux.go:107` | validate explicit `RouteSpec` → netlink `RouteAdd` → re-read matching observed `RouteInfo` |
| `routelinux.Manager.ReplaceRoute` | method | `internal/hostnet/route/linux/manager_linux.go:114` | validate explicit `RouteSpec` → netlink `RouteReplace` → cleanup stale managed route metrics → re-read observed `RouteInfo` |
| `routelinux.Manager.DeleteRoute` | method | `internal/hostnet/route/linux/manager_linux.go:125` | validate explicit `RouteSpec` → netlink `RouteDel`; missing route is idempotent success |
| `routelinux.Manager.ListRoutes` | method | `internal/hostnet/route/linux/manager_linux.go:149` | validate explicit `RouteFilter` → netlink `RouteListFiltered` → Go-side exact filtering + stable sorting |
| `routelinux.Manager.GetRoute` | method | `internal/hostnet/route/linux/manager_linux.go:182` | validate `RouteQuery` → netlink `RouteGet` path selection → observed primary `RouteInfo` |
| `firewall.Manager` | interface | `internal/hostnet/firewall/firewall.go:17` | host firewall primitive API：`EnsureMasquerade` / `DeleteMasquerade` / `EnsureEndpointAntiSpoofing` / `DeleteEndpointAntiSpoofing` / `EnsureForwardAccept` / `DeleteForwardAccept` / `GetRule` / `ListRules` |
| `firewalllinux.Manager` | struct | `internal/hostnet/firewall/linux/manager_linux.go:15` | Linux nftables-backed implementation of `firewall.Manager` |
| `firewalllinux.Manager.EnsureMasquerade` | method | `internal/hostnet/firewall/linux/manager_linux.go:37` | validate explicit NAT spec → ensure table/chain/rule → return observed masquerade rule info |
| `firewalllinux.Manager.EnsureEndpointAntiSpoofing` | method | `internal/hostnet/firewall/linux/manager_linux.go:51` | validate explicit endpoint spec → ensure bridge-chain guard rule group → return observed logical endpoint rule info |
| `firewalllinux.Manager.EnsureForwardAccept` | method | `internal/hostnet/firewall/linux/manager_linux.go:65` | validate explicit forward spec → ensure two-rule filter-forward accept group (egress + conntrack return) → return observed logical rule info |
| `dhcp.Manager` | interface | `internal/hostnet/dhcp/dhcp.go:12-34` | host DHCP primitive API：`Start` / `Stop` / `ApplyBinding` / `RemoveBinding` / `GetServer` / `GetLease` / `ListLeases` |
| `coredhcp.Manager` | struct | `internal/hostnet/dhcp/coredhcp/manager.go:30-34` | CoreDHCP-backed in-process implementation of `dhcp.Manager` |
| `coredhcp.NewManager` | func | `internal/hostnet/dhcp/coredhcp/manager.go:38-41` | constructs the real CoreDHCP-backed manager while hiding CoreDHCP from the root contract |
| `coredhcp.Manager.Start` | method | `internal/hostnet/dhcp/coredhcp/manager.go:47-124` | validate explicit `ServerSpec` → register runtime/plugin → start CoreDHCP listener → return observed server info |
| `coredhcp.Manager.ApplyBinding` | method | `internal/hostnet/dhcp/coredhcp/manager.go:197-222` | validate explicit MAC/IP/hostname → update process-memory binding indexes → return reserved lease info |
| `coredhcp.newHandler4` | internal helper | `internal/hostnet/dhcp/coredhcp/handler.go:26-55` | CoreDHCP DHCPv4 handler bridge；known MACs get OFFER/ACK, unknown or conflicting requests are silently dropped |
| `storage.VolumeService` | struct | `internal/storage/service.go:16` | VM-facing block volume API；所有操作显式 PoolName |
| `storage.ImageService` | struct | `internal/storage/image_service.go:13` | file image byte-stream API；Put/Get/Delete |
| `pool.Service` | struct | `internal/storage/pool/service.go:17` | pool registry, capacity accounting, in-memory indexes |
| `local.Driver` | struct | `internal/storage/local/driver.go:41` | host-local qcow2 block driver using qemu-img |
| `localfile.Driver` | struct | `internal/storage/localfile/driver.go:42` | host-local raw/qcow2 image byte store |
| `qemu.NewVM` / `Builder.Build` / `VM.Argv` | funcs/methods | `internal/virt/qemu/vm.go:185-394` | typed VM composition → deterministic QEMU argv |
| `qemuimg.NewClient` | func | `internal/virt/qemuimg/client.go:81` | qemu-img client 聚合入口 |
| `imgexec.Runner.Run` | interface | `internal/virt/qemuimg/internal/exec/exec.go:18` | binary + `[]string` 外部命令执行边界 |
| `version.String` | func | `internal/version/version.go:12` | 拼接 `"govirta 0.1.0-dev"` |

## CALL GRAPHS & DATA FLOW

主要入口：control-plane daemon、compute-node daemon、CLI 输出、QEMU argv 渲染器、hostnet bridge/TAP/route/firewall/DHCP primitive API、VM-facing 网络编排层（`internal/network`，详见 `internal/network/AGENTS.md`），以及 storage service API（当前尚未接入 cmd 入口，但已是 VM 编排层内部边界）。

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
  4. `internal/node/agent.go:28 (NewAgent)` — 构造 `netpool.NewService(...)`（注入 4 个 no-op host 原语），再注入 `qmp.NewNoopClient()` + 共享该 core 的 `NetworkService`/`NICService`，返回 `*Agent`
  5. `internal/node/agent.go:43 (Agent.Run)` — 在 logger 上挂 `component=node` / `qmp_client`
  6. `internal/node/agent.go:52 (Agent.Run)` — `select { <-ctx.Done() / default: return nil }`，未调用 QMP/network
  7. (future) `internal/virt/qmp/client.go:76 (SocketClient.Connect)` — 连接 QMP unix socket [详见 `internal/virt/qmp/AGENTS.md#flow-qmp-ready`]
  8. (future) `internal/network/service.go:33 (NetworkService.EnsureNetwork)` — 未来用真实 netlink/nftables/CoreDHCP 原语替换 no-op，编排 guest egress 闭环 [详见 `internal/network/AGENTS.md#flow-network-ensure`]
  9. (future) `internal/storage/service.go:179 (VolumeService.PublishVolume)` — 获取 root disk file attachment [详见 `internal/storage/AGENTS.md#flow-storage-volume`]
 10. (future) `internal/virt/qemu/vm.go:354 (VM.Argv)` — 构建 QEMU argv 并 spawn 子进程 [详见 `internal/virt/qemu/AGENTS.md#flow-argv-build`]
- Data: `context.Context` + 注入的 `qmp.Client` / `NetworkService` / `NICService`；未来会接收 VM spec + storage attachment
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

### Flow: hostnet bridge ensure {#flow-hostnet-bridge}

- Trigger: `internal/hostnet/link/linux/manager_linux.go:33 (Manager.EnsureBridge)` (caller wants a named host bridge ready for guest TAP attachment)
- Cross-module chain:
  1. `internal/hostnet/link/link.go:19 (Manager.EnsureBridge)` — root contract requires explicit `BridgeSpec` and caller context
  2. `internal/hostnet/link/linux/validate_linux.go:25 (validateBridgeSpec)` — require non-nil/non-canceled context, valid name, explicit CIDR, positive MTU, locally administered unicast MAC
  3. `internal/hostnet/link/linux/manager_linux.go:37 (EnsureBridge)` — parse `GatewayCIDR` with netlink before host mutation
  4. `internal/hostnet/link/linux/manager_linux.go:42 (EnsureBridge → ensureBridgeLink)` — lookup existing link; existing non-bridge is `linkerr.ErrConflict`; absent link becomes `netlink.Bridge` via `LinkAdd`
  5. `internal/hostnet/link/linux/manager_linux.go:46 (configureCreatedLink)` — set bridge MAC, MTU, address via `AddrReplace`, and admin state up; if a newly created link cannot be configured, rollback uses `LinkDel` and joins rollback errors
  6. `internal/hostnet/link/linux/manager_linux.go:77 (EnsureBridge → currentLinkInfo)` — re-read observed kernel state instead of trusting requested spec
  7. `internal/hostnet/link/linux/info_linux.go:49 (linkInfo)` — return `LinkInfo` with kind, index, MTU, MAC, admin state, master name, and sorted CIDR addresses
- Data: `link.BridgeSpec{Name,GatewayCIDR,MTU,MAC}` → netlink `Bridge` + `Addr` → observed `link.LinkInfo`
- Boundaries: Linux-only netlink kernel boundary through `realHandle` (`internal/hostnet/link/linux/handle_linux.go:24`); no shell commands
- Sinks: host bridge link, gateway address, MAC/MTU/admin state; errors classify through `linkerr` via `translateError`

### Flow: hostnet TAP ensure {#flow-hostnet-tap}

- Trigger: `internal/hostnet/link/linux/manager_linux.go:80 (Manager.EnsureTap)` (caller wants a TAP attached to an existing bridge for QEMU `-netdev tap`)
- Cross-module chain:
  1. `internal/hostnet/link/link.go:25 (Manager.EnsureTap)` — root contract requires explicit `TapSpec`, including owner UID/GID and VNetHeader mode
  2. `internal/hostnet/link/linux/validate_linux.go:45 (validateTapSpec)` — require explicit TAP name, bridge name, owner UID/GID, MTU, MAC, and `VNetHeaderEnabled` or `VNetHeaderDisabled`
  3. `internal/hostnet/link/linux/manager_linux.go:87 (EnsureTap)` — lookup bridge by name; a non-bridge master is `linkerr.ErrConflict`
  4. `internal/hostnet/link/linux/manager_linux.go:96 (EnsureTap → ensureTapLink)` — lookup existing TAP; reject non-TAP, wrong tuntap mode, owner UID/GID mismatch, unsupported VNetHeader observation, or VNetHeader mismatch
  5. `internal/hostnet/link/linux/manager_linux.go:292 (ensureTapLink)` — for absent TAP, create `netlink.Tuntap` with `TUNTAP_NO_PI`, optional `TUNTAP_VNET_HDR`, explicit owner/group, MTU, and MAC
  6. `internal/hostnet/link/linux/manager_linux.go:100 (configureCreatedLink)` — set TAP MAC, MTU, bridge master, and admin state up; rollback newly created TAP on configuration failure
  7. `internal/hostnet/link/linux/manager_linux.go:131 (EnsureTap → currentLinkInfo)` — return observed kernel state through `linkInfo`, including `MasterName`
- Data: `link.TapSpec{Name,BridgeName,OwnerUID,OwnerGID,MTU,MAC,VNetHeader}` → netlink `Tuntap` → observed `link.LinkInfo`
- Boundaries: Linux-only netlink kernel boundary plus `/dev/net/tun` semantics through netlink tuntap creation; QEMU only consumes the resulting TAP name later
- Sinks: host TAP link enslaved to bridge; errors classify through `linkerr` via `translateError`

### Flow: hostnet route primitives {#flow-hostnet-route}

- Trigger: `internal/hostnet/route.Manager` methods (caller wants to inspect IPv4 forwarding or manage a host IPv4 route)
- Cross-module chain:
  1. `internal/hostnet/route/route.go:19 (Manager)` — root contract requires caller context and explicit forwarding expectation, `RouteSpec`, `RouteFilter`, or `RouteQuery`
  2. `internal/hostnet/route/linux/manager_linux.go:59 (Manager.GetIPv4Forwarding)` / `:87 (CheckIPv4Forwarding)` — validate context/state and read `/proc/sys/net/ipv4/ip_forward`; never write sysctl state
  3. `internal/hostnet/route/linux/manager_linux.go:107 (AddRoute)` / `:114 (ReplaceRoute)` / `:125 (DeleteRoute)` — validate explicit `RouteSpec`, resolve link name, build netlink route identity, then call `RouteAdd` / `RouteReplace` / `RouteDel`
  4. `internal/hostnet/route/linux/manager_linux.go:149 (ListRoutes)` — validate explicit `RouteFilter`, build netlink filter, call `RouteListFiltered`, and apply exact Go-side filtering where netlink cannot express the full filter
  5. `internal/hostnet/route/linux/manager_linux.go:182 (GetRoute)` — validate `RouteQuery`, call `RouteGet`, and treat the first result as Linux path-selection output
  6. `internal/hostnet/route/linux/info_linux.go:210 (netlinkRouteInfo)` — resolve link index back to `link.Name` and translate observed netlink fields into `RouteInfo`; protocol `0` maps to `RouteProtocolUnspecified` for observed path-selection results
  7. `internal/hostnet/route/linux/errors_linux.go:13 (translateError)` — map netlink/syscall/route sentinel failures to stable `routeerr` classes while preserving causes
- Data: `route.RouteSpec` / `RouteFilter` / `RouteQuery` / `IPv4ForwardingState` → netlink `Route*` or `/proc` read → observed `route.RouteInfo` / `IPv4ForwardingInfo`
- Boundaries: Linux-only netlink kernel route table and read-only `/proc/sys/net/ipv4/ip_forward`; no shell commands and no sysctl writes inside the route package
- Sinks: host IPv4 route table mutations for add/replace/delete; read-only forwarding readiness and route observations; errors classify through `routeerr`

### Flow: hostnet firewall primitives {#flow-hostnet-firewall}

- Trigger: `internal/hostnet/firewall.Manager` methods (caller wants to manage Govirta-owned host firewall rules)
- Cross-module chain:
  1. `internal/hostnet/firewall/firewall.go:9 (Manager)` — root contract requires caller context and explicit `MasqueradeSpec`, `EndpointAntiSpoofingSpec`, `ForwardAcceptSpec`, `RuleRef`, `RuleQuery`, or `RuleFilter`
  2. `internal/hostnet/firewall/linux/manager_linux.go:37 (EnsureMasquerade)` — validate explicit NAT spec, then build desired Govirta-owned nftables table/chain/rule state
  3. `internal/hostnet/firewall/linux/manager_linux.go:51 (EnsureEndpointAntiSpoofing)` — validate explicit bridge/TAP/MAC/IPv4 endpoint spec, then build the bridge-chain anti-spoofing guard rule group
  4. `internal/hostnet/firewall/linux/manager_linux.go:65 (EnsureForwardAccept)` — validate explicit guest-CIDR/egress spec, then build the filter-forward accept rule group (egress accept + conntrack established/related return) via `forward_linux.go:50 (ensureDesiredForwardGroup)`
  5. `internal/hostnet/firewall/linux/rules_linux.go:69 (ensureDesiredRule)` / `:126 (ensureDesiredRuleGroup)` — ensure table/chain, reject conflicting managed rules, reconcile missing Govirta-owned rules, flush nftables batch, then re-read observed state
  6. `internal/hostnet/firewall/linux/info_linux.go:14 (listObservedRules)` / `:148 (logicalEndpointInfo)` — list observed nftables rules, ignore non-Govirta rules, compact endpoint guard groups into logical `RuleInfo`
  7. `internal/hostnet/firewall/linux/expr_linux.go:237 (parseMasquerade)` / `:277 (parseEndpointAntiSpoofing)` and `forward_expr_linux.go:61 (parseForwardAccept)` — parse nftables expressions and Govirta user data back into stable firewall summaries
  8. `internal/hostnet/firewall/linux/errors_linux.go:14 (translateError)` — map nftables/syscall/firewall sentinel failures to stable `firewallerr` classes while preserving causes
- Data: `firewall.MasqueradeSpec` / `EndpointAntiSpoofingSpec` / `ForwardAcceptSpec` / `RuleRef` / `RuleFilter` → nftables table/chain/rule operations → observed `firewall.RuleInfo`
- Boundaries: Linux-only nftables kernel boundary through `realHandle` (`internal/hostnet/firewall/linux/handle_linux.go:20`); no shell commands, no sysctl writes, no bridge/TAP creation, and no change to the host `FORWARD` default policy
- Sinks: Govirta-owned nftables masquerade, endpoint anti-spoofing, and forward-accept rules only; non-Govirta rules are observed but not flushed or deleted

### Flow: hostnet DHCP static binding {#flow-hostnet-dhcp}

- Trigger: `internal/hostnet/dhcp/coredhcp/manager.go:47 (Manager.Start)` and `:197 (Manager.ApplyBinding)` (caller wants an in-process DHCP listener to answer explicit static MAC/IP bindings on an existing host interface)
- Cross-module chain:
  1. `internal/hostnet/dhcp/dhcp.go:12 (Manager)` — root contract requires caller context and explicit `ServerSpec`, `BindingRequest`, or `BindingQuery`
  2. `internal/hostnet/dhcp/coredhcp/manager.go:47 (Manager.Start)` — validate context/spec, register the Govirta CoreDHCP plugin runtime, start the CoreDHCP listener, and return observed `ServerInfo`
  3. `internal/hostnet/dhcp/coredhcp/manager.go:197 (Manager.ApplyBinding)` — validate explicit server ID, MAC, IP-in-pool, and hostname, then update process-memory binding indexes as a reserved lease
  4. `internal/hostnet/dhcp/coredhcp/handler.go:26 (newHandler4)` — CoreDHCP dispatches guest DHCPv4 packets to the Govirta handler; known MAC `DISCOVER` returns `OFFER`, matching `REQUEST` returns `ACK` and marks the lease bound
  5. `internal/hostnet/dhcp/coredhcp/handler.go:32 (newHandler4)` — unknown MACs, stopped servers, unsupported message types, or conflicting requested IPs return no response instead of DHCPNAK
  6. `test/acceptance/hostnet_dhcp_test.go:24 (TestHostnetDHCPBindingEndToEnd)` — Lima boots CirrOS on a real bridge/TAP and verifies the guest reaches the bound static lease
- Data: `dhcp.ServerSpec` + `dhcp.BindingRequest{MAC,IP}` → CoreDHCP listener/plugin runtime → guest `DISCOVER`/`REQUEST` → `OFFER`/`ACK` → observed `dhcp.LeaseInfo{State:LeaseStateBound}`
- Boundaries: in-process CoreDHCP server and UDP listener bound to an existing interface/address; no QEMU process, bridge/TAP, route, firewall, guest, or persistent metadata mutation inside DHCP
- Sinks: process-memory DHCP server/runtime and binding table only; callers must replay bindings after restart and own all surrounding VM/network lifecycle

### Flow: storage block volume lifecycle {#flow-storage-volume}

- Trigger: `internal/storage/service.go:82 (VolumeService.CreateVolume)` / `:179 (PublishVolume)` / `:214 (DeleteVolume)` (future VM orchestration caller)
- Cross-module chain:
  1. `internal/storage/service.go:82 (VolumeService.CreateVolume)` — 校验 explicit `PoolName` + VM/disk identity [详见 `internal/storage/AGENTS.md#flow-storage-volume`]
  2. `internal/storage/pool/service.go:158 (pool.Service.CreateVolume)` — block pool lookup + capacity admission + in-memory index update
  3. `internal/storage/local/driver.go:92 (local.Driver.Create)` — driver-owned qcow2 path + `qemu-img create`
  4. `internal/virt/qemuimg/client.go:105 (QCOW2Client.Create)` — qemu-img builder [详见 `internal/virt/qemuimg/AGENTS.md#flow-qcow2-do`]
- Data: `CreateVolumeRequest` → `block.CreateRequest` → `volume.Volume` → optional `volume.PublishedVolume`
- Boundaries: in-proc service/driver calls; qemu-img subprocess via runner; filesystem under trusted storage root
- Sinks: qcow2 file create/delete, in-memory `Pool.volumes`, runtime file attachment path

### Flow: storage image lifecycle {#flow-storage-image}

- Trigger: `internal/storage/image_service.go:44 (ImageService.PutImage)` / `:59 (GetImage)` / `:70 (DeleteImage)` (future control-plane image catalog caller)
- Cross-module chain:
  1. `internal/storage/image_service.go:44 (ImageService.PutImage)` — 校验 explicit `PoolName` + image request [详见 `internal/storage/AGENTS.md#flow-storage-image`]
  2. `internal/storage/pool/service.go:455 (pool.Service.PutImage)` — reserve capacity + create pending image record
  3. `internal/storage/localfile/driver.go:74 (localfile.Driver.Put)` — open `target.tmp` writer under file pool
  4. `internal/storage/pool/service.go:685 (pendingImageWriter.Close)` — driver commit success 后将 pending → ready
- Data: `PutImageRequest` → `image.PutRequest` → `image.ImageWriter` → `pool.ImageRecord{pending|ready|deleting}`
- Boundaries: in-proc writer; filesystem hard-link commit; metadata only in memory
- Sinks: raw/qcow2 image bytes under `StorageRoot/pool/<pool>/images`, in-memory `Pool.images`

### Flow: image-derived root volume {#flow-storage-image-root-volume}

- Trigger: future orchestration path uses `ImageService.GetImage` then `VolumeService.CreateRootVolumeFromReader`
- Cross-module chain:
  1. `internal/storage/image_service.go:59 (ImageService.GetImage)` — open ready image reader [详见 `internal/storage/AGENTS.md#flow-storage-image-root-volume`]
  2. `internal/storage/service.go:128 (VolumeService.CreateRootVolumeFromReader)` — require explicit `PoolName` and `diskformat.Format`
  3. `internal/storage/pool/service.go:213 (pool.Service.CreateVolumeFromReader)` — block pool capacity/index lifecycle
  4. `internal/storage/local/driver.go:152 (local.Driver.CreateFromReader)` — qcow2 full copy or raw→qcow2 convert
  5. `internal/virt/qemuimg/client.go:115 (QCOW2Client.Convert)` / `:120 (Resize)` — qemu-img subprocess [详见 `internal/virt/qemuimg/AGENTS.md#flow-qcow2-do`]
- Data: image `io.ReadCloser` + explicit `Format` → `block.CreateFromReaderRequest` → independent qcow2 `volume.Volume`
- Boundaries: byte-stream read/write; qemu-img convert/resize subprocess for raw or capacity expansion
- Sinks: full independent qcow2 root disk; no backing-file links to source image

### Flow: network orchestration ensure {#flow-network-orchestrate}

- Trigger: `internal/network/service.go:33 (NetworkService.EnsureNetwork)` and `internal/network/nic_service.go:28 (NICService.EnsureNIC)` (VM-facing caller reconciles a registered network/NIC onto the host)
- Cross-module chain:
  1. `internal/network/service.go:33 (NetworkService.EnsureNetwork)` → `internal/network/netpool/orchestrate.go:42 (Service.EnsureNetwork)` — bridge → IPv4 forwarding check → masquerade → forward-accept → DHCP, then live `GetNetworkStatus` [详见 `internal/network/AGENTS.md#flow-network-ensure`]
  2. `internal/network/nic_service.go:28 (NICService.EnsureNIC)` → `internal/network/netpool/orchestrate.go:111 (Service.EnsureNIC)` — TAP → DHCP binding → endpoint anti-spoofing, then live `GetNICStatus` [详见 `internal/network/AGENTS.md#flow-nic-ensure`]
  3. exits into the hostnet primitive flows: `#flow-hostnet-bridge`, `#flow-hostnet-tap`, `#flow-hostnet-route`, `#flow-hostnet-firewall`, `#flow-hostnet-dhcp`
- Data: declarative `netpool.NetworkDefinition` / `NICDefinition` → primitive specs → observed `netpool.NetworkStatus` / `NICStatus` read live; one control-plane-supplied `MAC` threaded unchanged to TAP + DHCP binding + anti-spoofing
- Boundaries: in-proc orchestration over injected `link`/`route`/`firewall`/`dhcp` managers; the core caches no observed state and never mutates IPv4 forwarding
- Sinks: host bridge/TAP, Govirta-owned masquerade + forward-accept + anti-spoofing nftables rules, static DHCP lease; `Delete*` reverses order with `errors.Join`

### Flow: guest external egress closure {#flow-guest-egress}

- Trigger: `test/acceptance/network_egress_test.go:43 (TestNetworkEgressEndToEnd)` (Lima acceptance proves real guest internet access through the orchestration API)
- Cross-module chain:
  1. `test/acceptance/network_egress_test.go:129 (TestNetworkEgressEndToEnd → NetworkService.EnsureNetwork)` — bridge + forwarding readiness + masquerade + forward-accept + DHCP [详见 `internal/network/AGENTS.md#flow-network-ensure`]
  2. `test/acceptance/network_egress_test.go:206 (TestNetworkEgressEndToEnd → NICService.EnsureNIC)` — TAP + static binding + anti-spoofing for the guest MAC [详见 `internal/network/AGENTS.md#flow-nic-ensure`]
  3. CirrOS boots on the TAP and obtains IP + default route + DNS from the static DHCP binding; no in-guest static IP commands
  4. `test/acceptance/network_egress_test.go:295 (TestNetworkEgressEndToEnd)` — `ping 8.8.8.8` proves NAT + forward-accept + default route
  5. `test/acceptance/network_egress_test.go:308 (TestNetworkEgressEndToEnd)` — `ping one.one.one.one` proves DNS option delivery
- Data: declarative `NetworkDefinition` + `NICDefinition` → ensured host primitives → guest DHCP lease (IP/route/DNS) → ICMP egress + DNS resolution
- Boundaries: full single-network single-NIC egress path; teardown resolves firewall rule refs via `firewall.ListRules` then `DeleteNIC` / `DeleteNetwork` [详见 `internal/network/AGENTS.md#flow-guest-egress`]
- Sinks: real guest packets traverse host bridge → TAP → masquerade/forward-accept → egress interface → internet

证据来源：子代理只读扫描 + AFT outline/zoom + 直接读取入口、storage、virt 源码；调用图以 AFT/源码读取为主，LSP call hierarchy 未全量可用。`[已验证]` / `[降级: LSP call hierarchy]`

## CONVENTIONS

- Module: `github.com/suknna/govirta`; `go.mod` declares Go `1.26` and direct dependency `github.com/rs/zerolog v1.34.0`.
- Root `context.Context` is created in `cmd/*/main.go`; internal packages must accept caller-provided `ctx` for I/O, long-running work, cross-package calls, and goroutines.
- Unit tests live next to packages and favor behavior names such as `Test<Subject><ExpectedBehavior>` plus table-driven `t.Run` cases.
- Any I/O / runner / long-running boundary that accepts `ctx` should cover `context.Canceled` behavior in tests.
- Unit tests must not require real QEMU binaries, TAP devices, or the remote acceptance host. Use fake runners for qemu-img and storage-local unit tests.
- Command execution boundaries must pass `binary` + `[]string`; do not build shell command strings in production code.
- Runtime logs use zerolog structured fields. `fmt.Println` is acceptable for CLI user output, not library runtime logs.
- All errors must propagate to the caller. Do not ignore errors with `_ = err`, blank assignments, best-effort cleanup that discards failures, or silent fallback paths. When an operation has both a primary error and cleanup/rollback errors, compose them with Go stdlib `errors.Join` so callers can inspect every failure with `errors.Is` / `errors.As`.
- Storage APIs require explicit pool, format, and source choices when behavior affects storage outcomes; no implicit default storage pool or format inference.
- All externally provided APIs, including Go package APIs, HTTP APIs, and gRPC APIs, must require callers to pass every behavior-affecting parameter explicitly. Do not infer, auto-fill, default, or decide missing API parameters on behalf of callers.
- `internal/hostnet/route` may read/check IPv4 forwarding but must not enable, disable, or persist it. Node installation, operations tooling, and acceptance setup own `net.ipv4.ip_forward` configuration.
- `internal/hostnet/firewall` manages Govirta-owned nftables rules only. Callers must explicitly pass endpoint MAC/IP/TAP/bridge and NAT egress/source choices; the package must not infer endpoint identity, create links, or change IPv4 forwarding state.
- `internal/hostnet/dhcp` bindings must use explicit MAC/IP pairs. The CoreDHCP implementation is in-process and process-memory only; after restart, upper layers must replay `Start` and `ApplyBinding` inputs. Router and DNS options must use explicit `DHCPOptionAddrs` modes, including disabled mode, and the package must not auto-allocate IP addresses.
- `internal/hostnet/firewall` forward-accept adds only Govirta-owned accept rules for the guest CIDR across the egress interface. It must not change the host `FORWARD` default policy, must not touch IPv4 forwarding, and must not flush non-Govirta rules; it is symmetric with masquerade (guest CIDR + egress interface, no bridge/TAP identity).
- The VM-facing network orchestration layer (`internal/network`) stores declarative logical intent only. Observed network/NIC state is always read live from the injected `link`/`route`/`firewall`/`dhcp` primitives (single source of truth); the core never caches drift-prone observed state, never generates/infers names/addresses/firewall identities, and never mutates IPv4 forwarding (it only checks readiness). `Ensure*` is idempotent and forward-only; `Delete*` reverses order and composes failures with `errors.Join`.
- The guest NIC MAC is supplied by the control plane in `netpool.NICDefinition.MAC` and threaded unchanged to the TAP, the DHCP binding, and the endpoint anti-spoofing guard. The orchestration layer never generates a MAC.
- Image-derived root volumes must always be full independent copies of source image bytes. Do not use qcow2 backing-file links, reflink-style logical sharing, or any image-to-root-disk link semantics in the current project scope.
- Context/knowledge-base references must not be dangling: every `AGENTS.md` cross-reference, `#flow-*` anchor, docs path, and symbol reference added to this knowledge base must resolve to an existing section, file, or source symbol at the time it is written.
- Control-plane persistent data storage follows the Kubernetes-inspired architecture and permanently considers only etcd. This is a fixed long-term decision on par with the no-libvirt rule: never introduce SQLite, PostgreSQL, MySQL, embedded KV stores, or any alternative metadata database. etcd is the sole persistence backend the project will ever target.
- Variables that model a type discriminator, enum-like choice, lifecycle state, phase, role, backend kind, operation mode, or other state-machine value must use a dedicated custom Go type plus named constants. Do not represent such values as ad-hoc raw `string`, `int`, or `bool` values.
- Layered, swappable-by-design architecture (积木式拼接): every layer must hide its internal implementation behind a stable interface/contract so replacing one layer's internals has zero impact on the layers above. Upper layers depend only on abstractions (interfaces, request/result types), never on concrete lower-layer implementations. The codebase must stay composable like building blocks — any driver/runner/client implementation can be swapped (for example `block.Driver`, `image.Driver`, `qmp.Client`, `bridge.Manager`, `imgexec.Runner`) without rippling changes upward. Minimize cross-layer coupling and never leak a lower layer's implementation details across its boundary. (This is orthogonal to 上下一致: vertical consistency governs which data is authoritative, this rule governs how implementations are decoupled and replaceable.)
- The VM orchestration layer (`govirtad`/`govirtlet`) process lifecycle must stay decoupled from the QEMU process lifecycle. An orchestrator crash, panic, fatal error, or restart must never terminate, kill, or destabilize running QEMU processes. Spawn QEMU so guests survive orchestrator death and reattach to existing processes (QMP socket + pidfile) on restart instead of relying on the parent-child relationship. The orchestrator manages QEMU; it must not be a single point of failure for already-running guests.
- Vertical consistency and single source of truth (上下一致): the authoritative state of every resource (storage volume/image, network bridge/TAP, VM process + QMP state) is the actually existing/running resource itself, not any upper-layer cache, record, or projection. Lower-layer reality defines the truth and the upper layer must always match the lower layer, never the reverse. Every upper surface (control-plane records, scheduler view, future `govirtctl` CLI, future web frontend) must derive resource information from this single authoritative source so a VM, volume, image, or network created or mutated through any path is reported identically everywhere. When an upper-layer record conflicts with the actual resource, the actual resource is the fact standard and the upper layer reconciles toward it.

## ANTI-PATTERNS (THIS PROJECT)

- **No libvirt, ever.** Do not introduce `libvirt.org/go/libvirt`, `digitalocean/go-libvirt`, libvirtd, or libvirt-derived abstractions or design notes.
- Do not preserve backward compatibility for internal APIs during this fast-iteration phase; replace wrong abstractions directly.
- Do not reintroduce standalone milestone documents under `docs/roadmap/cycle-*.md`; keep planning details in specs/plans.
- Do not create orphan `context.Background()` / `context.TODO()` inside internal production packages.
- Do not start fire-and-forget goroutines; every goroutine needs owner, shutdown path, and `ctx.Done()` for long-running work.
- Do not use `panic` for expected business errors, string-match errors, swallow errors silently, or use `goto` as normal control flow.
- Do not discard, suppress, overwrite, or log-and-continue errors that affect correctness, cleanup, rollback, persistence, storage, networking, process execution, or API responses; return them upward and use `errors.Join` when multiple errors must be preserved.
- Do not let QEMU packages create host bridge/TAP resources; host link primitives belong under `internal/hostnet/link`.
- Do not let `internal/hostnet/firewall` enable, disable, or persist IPv4 forwarding; acceptance setup and operations tooling own sysctl state.
- Do not let `internal/hostnet/firewall` create bridge/TAP devices; host link primitives belong under `internal/hostnet/link`.
- Do not let `internal/hostnet/firewall` infer endpoint MAC, IP, TAP, bridge, egress interface, or guest CIDR; callers must pass every behavior-affecting firewall field explicitly.
- Do not flush, delete, or rewrite non-Govirta firewall rules from `internal/hostnet/firewall`; only Govirta-owned rules selected by explicit owner/purpose identity may be reconciled.
- Do not let `internal/hostnet/dhcp` create or modify QEMU processes, TAP devices, bridges, routes, firewall rules, or guest state; callers must prepare those resources explicitly through their owning packages.
- Do not send DHCPNAK for unknown MACs or conflicting requested IPs in the current DHCP wrapper; silently do not respond so callers do not imply ownership of non-Govirta guests.
- Do not add DHCP persistence or automatic IP allocation in `internal/hostnet/dhcp`; upper layers own replay, identity, and address assignment.
- Do not cache drift-prone observed network/NIC state in the `internal/network` orchestration layer (`netpool.Service`); always re-read live through the injected `link`/`route`/`firewall`/`dhcp` managers (single source of truth).
- Do not generate, infer, or default network names, addresses, MACs, or firewall identities in `internal/network`; the control plane supplies every behavior-affecting field and the guest `MAC` is threaded unchanged to TAP + DHCP binding + anti-spoofing.
- Do not let `internal/hostnet/firewall` forward-accept change the host `FORWARD` default policy or touch `ip_forward`; it only adds Govirta-owned accept rules for the guest CIDR across the egress interface.
- Do not tear down already-created resources inside `network`/`netpool` `Ensure*` on partial failure (forward-only idempotent reconcile), and do not let `Delete*` short-circuit on the first error; reverse order and join failures.
- Distributed scheduling, multi-node control, and the master/node long-lived task channel are in scope — they are the project goal, not deferred work. Do not frame or gate them as something to attempt only after a single-node closure.
- Do not spend implementation effort on live migration, hot-plug, or Kubernetes/CRD integration; these remain explicitly deferred (cold-only operations, k8s-inspired but not k8s-integrated).
- Do not implement cold snapshot, cold resize, or cold config modification against a running VM; these operations must require a stopped/offline VM until a later hot-operation phase is explicitly designed.
- Do not add qemu-nbd, qemu-storage-daemon, qemu-io, CSI sidecars, gRPC storage services, or libvirt-derived storage abstractions in the current phase.
- Do not design public Go package APIs, HTTP APIs, or gRPC APIs that silently infer defaults, complete missing fields, choose storage/network/runtime behavior, or otherwise make caller decisions implicitly.
- Do not introduce backing-file chains or linked root disks for image-derived VM roots; root disks copied from images must remain independent even if the source image is later deleted.
- Do not introduce non-etcd persistent metadata stores for control-plane data — etcd is the only persistence backend, permanently, not just for the current phase.
- Do not tightly couple layers or leak a lower layer's implementation details (concrete types, file path layout, backend specifics) into upper layers, and do not make upper layers depend on concrete lower-layer implementations instead of interfaces. Swapping a layer's internal implementation must never force changes in the layers above; if it does, the boundary abstraction is wrong and must be fixed rather than the upper layer patched.
- Do not add raw primitive state/type variables when a custom typed constant set is appropriate for API contracts, state machines, or persisted/serialized domain values.
- Do not couple the QEMU process lifecycle to the orchestrator process. Do not spawn QEMU with a parent-death signal (`SysProcAttr.Pdeathsig`), do not keep QEMU in a process group where orchestrator termination signals propagate to it, and do not make QEMU depend on orchestrator-held stdio/pipes/QMP connections for survival. An orchestrator crash or restart must leave running guests untouched.
- Do not let any upper layer become an independent source of truth that can drift from actual resources, and do not give each frontend (CLI vs web) its own private, unreconciled view of VM/storage/network state. Do not report, cache, or persist a resource state that contradicts the real qcow2 file, bridge/TAP, or QEMU process + QMP state; the running resource is the fact standard and every surface must reflect it consistently.
- Do not use `git push --no-verify` to bypass the main-branch full Lima acceptance gate; pushing `main` must pass the configured hook and `scripts/acceptance.sh full`.

## UNIQUE STYLES

- Project icon: `image/govirta_icon.png`; brand colors from non-white icon regions are primary violet-blue `#2000C0` and secondary teal `#00B0B0`.
- Architecture is a Kubernetes/OpenStack-inspired distributed cluster: control plane / node / storage separation with a master/node long-lived task channel. It is k8s-inspired, not k8s-integrated — CRD and Kubernetes integration stay excluded.
- Product shape is a distributed VM cluster: a master (`govirtad`) dispatching cold VM operations onto nodes (`govirtlet`), where each node provides storage pools/images/root volumes, qemu-system argv, qemu-img qcow2 lifecycle, VM process lifecycle, minimal QMP control, and local TAP/bridge as execution surfaces.
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

## ACCEPTANCE TESTS

- Fast macOS verification: run `scripts/verify.sh` for the local CI-equivalent loop before broader acceptance.
- Linux-only acceptance: run `scripts/acceptance.sh full`; it executes acceptance tests with `go test -v -tags acceptance -count=1 ./test/acceptance/...` inside the Lima guest and archives stdout/stderr under `test/log/<timestamp>-acceptance-full.log`.
- Lima acceptance uses a short generated `LIMA_HOME` under the parent `.l/<repo_key>` to avoid Lima socket path limits, boots an ephemeral Ubuntu arm64 VM with nested KVM, runs the acceptance suite, deletes the VM, and preserves the gitignored persistent repo cache under project `.lima/cache/`.
- `lima/govirta.yaml` must keep `vmType: "vz"` and `nestedVirtualization: true`; this path is verified on Apple M3 + macOS 26.5 + Lima 2.1.1.
- Full acceptance includes the hostnet bridge/TAP path: `TestHostnetLinkBridgeTapEndToEnd` creates a real bridge + TAP with `internal/hostnet/link/linux`, direct-kernel boots CirrOS with QEMU, waits for QMP running state and serial login marker, then verifies host-to-guest ping over the bridge/TAP path.
- Full acceptance includes the hostnet route path: `TestHostnetRoutePrimitives` creates a real dummy link, checks IPv4 forwarding readiness, exercises add/list/get/replace/delete route primitives through `internal/hostnet/route/linux`, and relies on `scripts/acceptance.sh` to enable `net.ipv4.ip_forward=1` for the guest test run.
- Full acceptance includes the hostnet firewall path: `TestHostnetFirewallMasqueradePrimitives` and `TestHostnetFirewallAntiSpoofingPrimitives` exercise real nftables masquerade and endpoint anti-spoofing lifecycle behavior through `internal/hostnet/firewall/linux` without validating full guest internet egress.
- Full acceptance includes the hostnet DHCP path: `TestHostnetDHCPBindingEndToEnd` starts the CoreDHCP-backed manager on a real bridge/TAP, applies an explicit CirrOS MAC/IP binding, boots the guest without static IP commands, and verifies the lease reaches `LeaseStateBound`. The test disables the Router option to avoid CirrOS metadata-delay behavior; Router option rendering is covered by unit tests.
- Full acceptance includes the network orchestration egress closure: `TestNetworkEgressEndToEnd` (`test/acceptance/network_egress_test.go`) registers and ensures a network + NIC through `internal/network` (`NetworkService`/`NICService` over `netpool.Service` with real `internal/hostnet/{link,route,firewall,dhcp}/linux` managers), boots CirrOS, lets the guest obtain IP + default route + DNS from the static DHCP binding, then verifies `ping 8.8.8.8` (NAT + forward-accept + route) and `ping one.one.one.one` (DNS delivery). This is the end-to-end guest internet-access proof the hostnet primitive tests alone do not provide.
- `test/log/*.log` is gitignored; keep `test/log/.gitkeep` tracked and do not commit generated acceptance logs.
- Setup required before pushing: `git config core.hooksPath .githooks`.
- Pushing `main` must pass full Lima acceptance; do not use `git push --no-verify` to bypass the main gate.

## NOTES

- Lima local acceptance is the authoritative hardware-backed path: `scripts/acceptance.sh full` uses a short generated `LIMA_HOME` under parent `.l/<repo_key>` to avoid Lima socket path limits, boots an ephemeral Ubuntu arm64 VM with nested KVM, runs acceptance tests, deletes the VM, and preserves project `.lima/cache/`.
- Verified nested KVM evidence: cirros booted with `qemu-system-aarch64 -machine virt -accel kvm -cpu host` and the kernel logged `smccc: KVM: hypervisor services detected`.
- Development temporary artifacts belong under project `.tmp/`; do not use global `/tmp` for debugging artifacts.
- Storage metadata is in memory only: after restart, callers must explicitly re-register pools and image catalog state; drivers do not scan storage roots or write metadata files.
- File/image pool overcommit ratio is `1.0`; block pool overcommit ratio is `1.5`.
- The hostnet packages prove host primitive lifecycle behavior only — bridge/TAP, IPv4 route management and forwarding readiness, nftables masquerade/anti-spoofing, and static DHCP lease behavior. The `internal/network` orchestration layer (`NetworkService`/`NICService` over `netpool.Service`) composes those primitives into the guest external-egress closure (bridge + forwarding readiness + masquerade + forward-accept + static DHCP + endpoint anti-spoofing), proven end-to-end by `TestNetworkEgressEndToEnd`. See `internal/network/AGENTS.md`.
- This file keeps the original generated header metadata, but the current branch has appended DHCP knowledge-base entries for `internal/hostnet/dhcp` and the `internal/network` orchestration layer (forward-accept primitive, `NetworkService`/`NICService`/`netpool.Service`, the network-orchestrate/guest-egress flows, and `TestNetworkEgressEndToEnd` acceptance coverage); the dead `internal/network/bridge` skeleton was removed.
- Call-graph evidence: AFT outline/zoom and direct source/test reads; LSP call hierarchy was not used end-to-end. `[降级]` LSP；`[已验证]` 源码与测试断言。
