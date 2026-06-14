# 推送门禁化 Linux 测试设计

**日期**：2026-06-14  
**状态**：设计已确认，待写实现计划  
**决策**：B. 推送门禁 + 显式手动入口

## 背景

Govirta 当前测试成本的主要浪费来自真实 Linux 主机测试在开发流中过早触发。项目已经用 `//go:build acceptance` 和 `//go:build e2e` 把重测试从普通 `go test ./...` 中隔离出来，但 `.githooks/pre-push` 仍保留“特性分支触及 Linux 路径时跑 `scripts/acceptance.sh linux`”的历史策略。该策略会让日常开发阶段的大量推送进入 Lima/QEMU/hostnet 验收路径，反馈太慢。

本次目标是把真实 Linux/Lima/QEMU 测试重新收束到两个显式入口：

1. 推送 `main` 时作为权威门禁自动运行。
2. 开发者明确手动运行 `scripts/acceptance.sh full` 或 `scripts/e2e.sh full` 时运行。

普通 `go test ./...`、`scripts/verify.sh`、特性分支普通 push 都不应隐式触发真实 Linux 主机测试。

## 目标

1. 保持 `go test ./...` 和 `scripts/verify.sh` 为快速本地反馈，只跑普通 Go 单测和构建。
2. 保留 `scripts/acceptance.sh full` 与 `scripts/e2e.sh full` 作为显式手动入口。
3. 保留推送 `main` 的强门禁：先跑 `scripts/verify.sh`，再跑 `scripts/acceptance.sh full` 和 `scripts/e2e.sh full`。
4. 取消特性分支按路径自动触发 `scripts/acceptance.sh linux` 的逻辑。
5. 同步文档口径，删除“特性分支触及 Linux 路径也自动验收”的旧规则，避免未来误恢复。

## 非目标

- 不删除 acceptance/e2e 测试本体。
- 不删除 `scripts/acceptance.sh` 或 `scripts/e2e.sh` 的显式模式。
- 不引入 GitHub Actions、CI 服务或新测试框架。
- 不改变 `//go:build acceptance` / `//go:build e2e` 的隔离机制。
- 不解决当前工作区里与 `internal/node/controllers/volume_*` 相关的既有未提交变更。

## 现状触发矩阵

| 入口 | 当前行为 | 问题 |
| --- | --- | --- |
| `go test ./...` | 不带额外 build tag，排除 acceptance/e2e | 符合目标 |
| `scripts/verify.sh` | `gofmt -l .` + `go test ./...` + 构建三个主服务 | 符合目标 |
| `scripts/acceptance.sh full` | 显式拉起 Lima 并运行 acceptance | 符合目标 |
| `scripts/e2e.sh full` | 显式运行分布式 spine e2e | 符合目标 |
| `.githooks/pre-push` 推 `main` | `verify.sh` + `acceptance.sh full` + `e2e.sh full` | 符合目标 |
| `.githooks/pre-push` 推特性分支且命中 Linux 路径 | 自动运行 `acceptance.sh linux` | 需要移除 |

## 设计

### 1. 默认快测路径

`scripts/verify.sh` 保持只做快速本地验证：

- `gofmt -l .`
- `go test ./...`
- `go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl`

该脚本不得添加 `-tags acceptance`、`-tags e2e`，也不得调用 `scripts/acceptance.sh` 或 `scripts/e2e.sh`。

### 2. 手动重测试入口

真实 Linux 主机测试仍通过显式脚本运行：

- `scripts/acceptance.sh full`：hostnet、qemu-img、VMM、guest egress 等 Linux-only 验收。
- `scripts/e2e.sh full`：etcd + govirtad + Lima govirtlet 的分布式 spine e2e。

这些入口保留，因为它们是排查 Linux/guest/QEMU 行为时的权威手段；但只有人或 `main` 推送门禁显式调用它们。

### 3. `pre-push` 门禁策略

`.githooks/pre-push` 简化为两层：

1. 任意非删除 push：运行 `scripts/verify.sh`。
2. 目标包含 `refs/heads/main`：追加运行 `scripts/acceptance.sh full` 和 `scripts/e2e.sh full`。

特性分支不再按 diff 路径推断 Linux 相关性，不再自动运行 `scripts/acceptance.sh linux`。这符合项目“显式优于隐式”的规则：是否需要 Linux 验收由开发者显式选择，而不是由 hook 根据路径隐式推断。

### 4. Build tag 边界

继续使用 Go 官方 build constraints：

- acceptance 文件保留 `//go:build acceptance` 或 `//go:build acceptance && linux`。
- e2e 文件保留 `//go:build e2e`。
- 普通 `go test ./...` 不传 `-tags`，因此不会编译这些测试。

该边界是最小、稳定、官方支持的隔离方式，不需要额外测试框架。

## 文档同步

需要更新以下口径：

- `AGENTS.md`：确认 `scripts/verify.sh` 是快速本地验证；推 `main` 必须通过 full Lima acceptance/e2e；特性分支不再自动触发 Linux 验收。
- `docs/superpowers/specs/2026-05-31-lima-acceptance-framework-design.md`：标记“特性分支触及 Linux 路径时跑验收”为历史策略，当前已被本设计取代。

## 验证方法

1. 静态检查 `scripts/verify.sh`：不包含 `-tags acceptance`、`-tags e2e`、`acceptance.sh`、`e2e.sh`。
2. 静态检查 `.githooks/pre-push`：推 `main` 分支运行 `scripts/acceptance.sh full` 和 `scripts/e2e.sh full`；特性分支不再调用 `scripts/acceptance.sh linux`。
3. 运行 `go test ./...`：验证默认开发路径可直接完成，不进入 acceptance/e2e。
4. 如需确认门禁脚本语法，运行一次轻量 shell 语法检查；不需要启动 Lima。

## 官方文档引用

- Go 官方文档 `cmd/go` / Build constraints：`https://pkg.go.dev/cmd/go#hdr-Build_constraints`。依据：只有目标 GOOS/GOARCH、工具链内置 tag 与显式 `-tags` 会使 build constraint 成立。
- Go 官方文档 `cmd/go` / Test packages 与 Testing flags：`https://pkg.go.dev/cmd/go#hdr-Test_packages`、`https://pkg.go.dev/cmd/go#hdr-Testing_flags`。依据：`go test ./...` 在 package list mode 编译运行匹配包的普通测试；额外 build tags 通过 `-tags` 传入。
