# 周期 4:生产最小可用集

**状态**:未开始

## 北极星目标

让一个真实业务能在 Govirta 上跑起来:虚机创建可基于模板、可自动注入身份与网络配置、可远程访问显示、可按需扩盘,关键操作可审计、调用者可认证。

## 承诺

本周期完成后,使用方可以:

- 通过指定模板镜像创建虚机,无需每次拷贝整盘。
- 在虚机描述里声明 cloud-init 内容(主机名、SSH key、网络),首次启动 guest 自动落配。
- 通过控制面拿到 VNC 访问入口,远程访问虚机显示。
- 在虚机关机或运行中扩容磁盘。
- 用 token 或 mTLS 调用 API,关键操作落到审计日志。

## 范围内

- 模板与镜像目录抽象:共享只读模板 + per-VM overlay,基于 qcow2 backing chain。
- cloud-init 注入:自动生成 cidata(user-data / meta-data / network-config)并挂给 guest。
- 显示通道:VNC over UNIX socket,控制面返回连接信息。
- 磁盘扩容:关机走 qemu-img resize,运行时走 QMP block-resize。
- 设备能力增强:virtio-rng、watchdog、SMBIOS type=1、RTC 漂移修正、IOThreads。
- 基础认证:API token 或 mTLS,未认证请求一律拒绝。
- 审计:关键变更动作(创建、删除、扩容、状态切换)落到结构化审计流。

## 范围外

- 集中式镜像仓库与签名验证(继续推迟到 v2 规划)。
- 完整 RBAC 矩阵(资源 × 动作 × 角色),本周期只做最小角色集合。
- 在线热迁移 → 周期 5。
- 在线快照与备份导出 → 周期 5。
- Prometheus metrics → 周期 5。

## 完成判定

- [ ] 虚机描述支持模板引用,控制面下发后节点通过 backing chain 创建 overlay,启动正常。
- [ ] 虚机描述支持 cloud-init 字段,guest 第一次启动后能拿到声明的 hostname、SSH key、IP 配置。
- [ ] VNC 通道可用,govirtctl 输出连接信息,外部 viewer 可连入并看到 guest 显示。
- [ ] 磁盘扩容在关机与运行两种状态下均验证通过,guest 内文件系统可扩展使用。
- [ ] 基础认证生效,未认证请求被拒绝;关键变更操作能在审计日志中检索到。
- [ ] 端到端集成测试覆盖:模板 → 创建 → cloud-init 落配 → VNC 访问 → 扩盘。
- [ ] `go test ./...` 与 `go test -race ./...` 全部通过。
- [ ] 本文件状态切换到「已完成」并附验收证据。

## 进入下一周期的前置

- 模板 / overlay 路径稳定:周期 5 的在线快照、备份导出基于此路径。
- 审计与认证已落地:周期 5 的运维流程(节点排空、迁移)能被合规追踪。
- 生产最小集设备(rng、watchdog、SMBIOS、IOThread)已默认启用:周期 5 的 SLA 评估有可用基线。
