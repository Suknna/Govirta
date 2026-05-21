# 周期 2:节点守护进程与多虚机生命周期

**状态**:未开始

## 北极星目标

govirtlet 作为常驻守护进程,在一台机器上稳定管理多台虚机的完整生命周期;govirtlet 自身崩溃或重启后,能恢复对运行中虚机的控制,不丢失状态。

## 承诺

本周期完成后,任何工程师可以:

- 在节点上把 govirtlet 装成系统服务长期运行。
- 通过 govirtctl 直连 govirtlet,完成虚机的 create / start / stop / delete / list / inspect。
- 杀掉再拉起 govirtlet 后,所有运行中虚机的 QMP 流被自动重连,事件继续上报。
- 在同一节点上稳定并发跑多台虚机,互不干扰。

## 范围内

- govirtlet 长生命周期:信号驱动有序退出,根 context 从 main 注入。
- 本地状态持久化:每台虚机一份描述(配置 + pid / socket / log 路径),进程重启后可全部读回。
- 本地控制 API:create / start / stop / delete / list / inspect,通过本地 UNIX socket 暴露。
- 崩溃恢复:基于持久化状态重新打开 QMP socket,识别已退出虚机并清理资源。
- 进程隔离:每台虚机独立 pidfile、独立日志文件、独立 QMP socket、独立 serial socket。
- govirtctl 本地子命令:`govirtctl vm <verb>` 直连 govirtlet。

## 范围外

- 多节点编排、节点注册、控制面 → 周期 3。
- 调度与放置策略 → 周期 3。
- 模板、cloud-init、VNC → 周期 4。
- 热迁移、在线快照 → 周期 5。

## 完成判定

- [ ] govirtlet 以守护进程方式运行,SIGTERM 触发有序退出且不留孤儿 QEMU。
- [ ] 节点本地状态(虚机描述、运行时句柄)持久化到磁盘,进程重启后可完整读回。
- [ ] 杀掉 govirtlet 再启动,所有运行中虚机的 QMP 流被自动重连,事件继续可达。
- [ ] govirtctl 通过本地 UNIX socket 完成 create / start / stop / delete / list / inspect。
- [ ] 同一节点稳定并发运行至少 3 台 cirros,互不影响,资源各自释放。
- [ ] 任意虚机内 `poweroff`,govirtlet 在阈值内把状态推到 stopped 并清理资源。
- [ ] govirtlet 与 govirtctl 的本地 API 有契约级单元测试覆盖。
- [ ] `go test ./...` 与 `go test -race ./...` 全部通过。
- [ ] 本文件状态切换到「已完成」并附验收证据。

## 进入下一周期的前置

- 节点本地有稳定的虚机控制 API:控制面调用入口已就位。
- 虚机描述可序列化:跨节点传输的雏形。
- 崩溃恢复路径已验证:控制面失联后节点自治可行。
