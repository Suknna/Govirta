# 周期 1:单机单虚机闭环

**状态**:进行中

## 北极星目标

在一台开发或验收机器上,以 Govirta 自身代码作为入口,启动并优雅停止一台 cirros 虚机,过程可观察、可复现,不依赖任何手写 shell 命令拼装 QEMU 参数。

## 承诺

本周期完成后,任何工程师可以:

- 用 Go 代码声明一台虚机并启动它。
- 通过 QMP 触发优雅关机并收到 SHUTDOWN 事件。
- 通过 Govirta 内置的 qemu-img 封装在代码中创建并查询 qcow2 镜像。
- 通过 Govirta 内置的网络模块在代码中创建/复用 Linux bridge 与 tap,并把 tap 接入 bridge。
- 在 amd64 与 arm64 两类宿主机上跑通同一套代码路径(架构默认值由 Govirta 内部处理)。

## 范围内

- QEMU 进程边界:Config → argv → runner → process 的端到端串通。
- QMP 客户端:连接、capabilities 协商、同步命令、事件订阅(至少覆盖 SHUTDOWN / RESET / STOP)。
- qemu-img 封装:create / info / resize / convert,info 输出按 JSON 解析。
- Linux 网络边界:bridge / tap / IP 地址的幂等创建与删除。
- 架构默认值:amd64 走 q35,arm64 走 virt(含必要的固件路径)。
- 端到端验收:在已有的 aarch64 验收主机上启动 cirros,guest 内可见 TAP 链路。

## 范围外(明确推迟)

- 多虚机并发与守护进程化 → 周期 2。
- 控制面、节点注册、调度 → 周期 3。
- VNC、cloud-init、模板镜像 → 周期 4。
- 热迁移、在线快照、备份、metrics → 周期 5。

## 完成判定

全部勾选视为周期完成。

- [ ] `internal/virt/qemu` 的 Config 覆盖最小启动集:machine、cpu、smp、memory、blockdev、netdev、chardev+mon、serial、display、pidfile、msg。
- [ ] `internal/virt/qmp` 是 Govirta 自有 thin client,支持 Connect、Run、Events、WaitFor 四个动作。
- [ ] `internal/virt/qemuimg` 支持 create / info(JSON) / resize / convert,字段命名对齐 QAPI ImageInfo。
- [ ] `internal/network/bridge` 基于 netlink 实现 EnsureBridge / EnsureTap / AttachTapToBridge / SetLinkUp / SetBridgeAddr,且 Ensure 类操作幂等。
- [ ] `internal/virt/qemu.Driver.Start` 把 Builder → Runner → QMP 串通,返回值携带 QMP 句柄。
- [ ] 验收主机能用 Govirta 二进制启动 cirros,guest 内 `ip link` 能看到对应的 TAP 链路。
- [ ] cirros 内执行 `poweroff`,Govirta 在阈值内收到 SHUTDOWN 事件并清理 pidfile / socket。
- [ ] `go test ./...` 与 `go test -race ./...` 全部通过。
- [ ] 本文件状态切换到「已完成」,并附验收日志或命令输出片段。

## 进入下一周期的前置

- QMP 客户端具备事件流能力:周期 2 的崩溃恢复依赖它。
- bridge / tap 幂等接口:周期 2 多虚机共享同一 bridge 的基础。
- VM 描述结构(Config)已稳定到能序列化:周期 2 的本地状态持久化依赖它。
