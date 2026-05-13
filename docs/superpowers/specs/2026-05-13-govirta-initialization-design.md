# Govirta 项目初始化设计

## 目标

Govirta 是一个 Go 语言虚拟化基础设施项目，底层能力对标 ESXi / VMware，长期目标是在轻量化场景中替代 OpenStack 的虚拟机编排能力。项目从 QEMU 层向上构建，短期不接入 Kubernetes，但架构参考 Kubernetes 的控制面 / 节点模型、调度思想和控制循环思想。

本次初始化交付一个可编译的最小基础骨架：建立 Git 仓库、Go module、三入口命令、平台分层目录、项目文档、AI 协作规范、Apache-2.0 许可证和基础验证脚本。

## 已确认约束

- Module 路径：`github.com/suknna/govirta`
- Go 版本：使用本机工具链对应的 `go 1.26`
- 许可证：`Apache-2.0`
- 日志库：`github.com/rs/zerolog`
- 架构定位：控制面 + 计算节点
- 短期不接入 Kubernetes / CRD
- 计算节点封装 QEMU、QMP、Linux bridge
- 当前项目处于快速迭代阶段，不承诺向后兼容，不为了兼容保留技术债务

## 目录结构

```text
.
├── AGENTS.md
├── LICENSE
├── README.md
├── go.mod
├── .gitignore
├── cmd/
│   ├── govirtad/
│   │   └── main.go
│   ├── govirtlet/
│   │   └── main.go
│   └── govirtctl/
│       └── main.go
├── configs/
│   ├── govirtad.example.yaml
│   └── govirtlet.example.yaml
├── docs/
│   ├── architecture.md
│   └── superpowers/
│       └── specs/
├── scripts/
│   └── verify.sh
└── internal/
    ├── apiserver/
    ├── controlplane/
    ├── node/
    ├── scheduler/
    ├── store/
    ├── virt/
    │   ├── qemu/
    │   └── qmp/
    ├── network/
    │   └── bridge/
    ├── types/
    └── version/
```

## 包职责

- `internal/apiserver`：控制面 API 服务边界，初始化阶段只提供接口与空服务，不监听真实端口。
- `internal/controlplane`：控制面编排入口，未来协调 API、调度、存储和控制循环。
- `internal/scheduler`：虚拟机调度边界，未来负责把虚拟机分配到计算节点。
- `internal/store`：状态存储抽象，未来可替换为本地数据库或分布式存储。
- `internal/node`：计算节点代理边界，未来管理本机虚拟化资源。
- `internal/virt/qemu`：QEMU 进程管理抽象，初始化阶段不执行系统命令。
- `internal/virt/qmp`：QMP 协议连接与命令抽象，初始化阶段不打开 socket。
- `internal/network/bridge`：Linux bridge 抽象，初始化阶段不修改系统网络。
- `internal/types`：共享领域类型，例如 `Node`、`VirtualMachine`、`ResourceList`。
- `internal/version`：项目版本信息，供三个入口和测试使用。

所有平台内部代码默认放在 `internal/` 下，暂不创建 `pkg/`，避免过早承诺公共 Go API。

## 命令入口

- `cmd/govirtad`：控制面服务入口，创建根上下文、初始化 zerolog、启动 `controlplane.Service.Run(ctx)`。
- `cmd/govirtlet`：计算节点代理入口，创建根上下文、初始化 zerolog、启动 `node.Agent.Run(ctx)`。
- `cmd/govirtctl`：命令行客户端入口，初始化 zerolog，当前只输出版本或基础帮助信息，不连接控制面。

入口层负责根 `context.Context` 和日志初始化。所有子上下文必须从入口根上下文派生，禁止在内部模块中创建孤儿上下文。

## AGENTS.md 内容设计

`AGENTS.md` 是给 AI / 自动化代理阅读的项目级长期上下文和工程规范，不写一次性任务说明。内容包括：

1. 项目定位：Govirta 是 Go 虚拟化基础设施平台，对标 ESXi / VMware，长期目标是在轻量化场景中替代 OpenStack 的虚拟机编排能力。
2. 技术栈：Go、QEMU、QMP、Linux bridge、zerolog。
3. 架构：控制面 + 计算节点，参考 Kubernetes 的控制面 / 节点分工、调度和控制循环思想，但短期不接入 Kubernetes / CRD。
4. 快速迭代声明：当前不考虑向后兼容，架构、接口、配置和目录允许大幅调整，不为了兼容保留技术债务。
5. Go 易踩雷规范：
   - `context.Context` 必须从 `main` 根上下文派生；禁止内部模块使用 `context.Background()` / `context.TODO()` 创建孤儿上下文。
   - goroutine 必须有退出路径、错误回传或可观测处理，并监听 `ctx.Done()`。
   - panic 不能用于业务错误；goroutine 和进程边界要有 recover 机制，recover 后必须记录结构化日志并转化为错误或退出信号。
   - 禁止不可退出的无限流程；循环必须有 `ctx.Done()` 或明确退出条件。
   - 错误使用返回值传递，包装使用 `%w`，禁止吞错和字符串匹配错误。
   - 日志使用 zerolog 结构化日志，禁止库包直接 `fmt.Println`。
6. 协作规范：每次变更交付必须输出关键函数调用关系；修改核心逻辑前必须说明受影响调用链。
7. Git 提交规范：使用 Conventional Commits，例如 `feat(node): add qemu runtime boundary`。

## README 内容设计

`README.md` 面向项目读者，说明：

- Govirta 是什么。
- 对标 ESXi / VMware 的虚拟化基础设施能力。
- 未来期望在轻量化场景中替代 OpenStack 的虚拟机编排能力。
- 架构参考 Kubernetes，但短期不接入 Kubernetes / CRD。
- 控制面与计算节点的职责分工。
- QEMU、QMP、Linux bridge 在系统中的位置。
- 当前处于快速迭代早期阶段。
- 许可证为 Apache-2.0。

## 日志设计

使用 `github.com/rs/zerolog` 作为结构化日志库。依据 zerolog 文档，logger 可通过 `Logger.WithContext(ctx)` 写入上下文，并通过 `zerolog.Ctx(ctx)` 读取。Govirta 初始骨架采用入口层初始化 logger，并把带 logger 的根上下文传入下游组件的方式。

## 测试与验证

初始化后必须满足：

- `go test ./...` 通过。
- `scripts/verify.sh` 可执行，并至少运行格式检查和 `go test ./...`。
- 三个入口可编译。
- 内部包提供最小接口或空实现，必要时添加轻量单元测试覆盖构造或版本信息。

## 范围外

- 不实现真实 QEMU 进程启动。
- 不实现真实 QMP socket 通信。
- 不修改 Linux bridge 或宿主机网络。
- 不实现真实 HTTP API、认证、调度算法或持久化存储。
- 不接入 Kubernetes、CRD 或容器编排能力。

## 参考依据

- Go 官方模块组织建议：Go module 根目录使用 `go.mod`，命令入口可放在 `cmd/`，内部实现可放在 `internal/`。
- Go 官方测试约定：使用 `*_test.go` 和 `go test ./...`。
- zerolog 官方 README：支持结构化日志和通过 `context.Context` 传播 logger。
- Kubernetes 架构思想：控制面 / 节点分工、调度和控制循环作为 Govirta 的架构参考，但不直接依赖 Kubernetes。
