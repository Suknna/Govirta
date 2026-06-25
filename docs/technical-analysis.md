# Govirta 技术分析

## 状态说明

[已验证] 本文档记录初始化阶段可确认的技术方向、外部资料来源和后续开放问题。本文档不锁定 Web 框架、数据库、libvirt Go binding 或集群控制面实现。

[已验证] `ctx7` 当前环境可用，并已用于查询 libvirt 与 Go libvirt 相关资料。实际执行过的命令如下：

```bash
ctx7 library libvirt "How to manage virtual machines through libvirt APIs and understand connection, domain, storage, and network concepts"
ctx7 library "go libvirt" "How Go applications should call libvirt APIs and handle connections, domains, storage pools, and networks"
ctx7 docs /libvirt/libvirt "How to manage virtual machines through libvirt APIs and understand connection, domain, storage, network, daemon, and permission concepts"
ctx7 docs /gitlab_libvirt/libvirt-go-module "How Go applications should call libvirt APIs and handle connections, domains, storage pools, and networks"
ctx7 docs /digitalocean/go-libvirt "How pure Go applications should call libvirt RPC APIs and handle connections, domains, storage pools, and networks"
```

选定的 Context7 Library ID：

- `/libvirt/libvirt`：libvirt 官方项目镜像，Source Reputation 为 High。
- `/gitlab_libvirt/libvirt-go-module`：libvirt 官方 Go module 绑定，Source Reputation 为 High。
- `/digitalocean/go-libvirt`：纯 Go libvirt RPC 客户端，Source Reputation 为 High；仅作为后续候选参考，不在初始化阶段采用。

## MVP：单机 libvirt Web 管理端

[已验证] MVP 聚焦单机 libvirt 管理。核心问题是如何把 libvirt 的连接、domain、storage pool、volume、network、interface、权限和错误状态显式呈现给浏览器用户。

[已验证] libvirt 的入口对象是 connection。应用通常先通过连接 URI 建立连接，再基于连接管理 domain、network、storage pool、storage volume、host interface 等资源。

[已验证] libvirt 的连接 URI 是行为影响参数。`qemu:///system` 与 `qemu:///session` 在权限、daemon、socket、资源作用域上语义不同；远程 URI、只读连接和认证机制也会改变运行时行为。

[分析] Govirta MVP 不应隐藏连接 URI，也不应在连接失败时静默 fallback 到其他 URI。即便第一版只支持一个本地连接 URI，也应在配置、日志或 UI 中显式展示，并在失败时报告所使用的 URI、连接模式和认证/权限路径。

[已验证] libvirt 的 domain lifecycle 包含 define/undefine、create/start、shutdown、destroy、reboot、reset、suspend/resume、managed save、snapshot 等操作；其中 shutdown 与 destroy、reboot 与 reset、define 与 start 不是同一语义。

[分析] Web 管理端不能把这些操作折叠成模糊按钮。至少需要在 API 设计和 UI 文案中显式区分优雅关机与强制断电、ACPI reboot 与硬 reset、定义但未运行与正在运行、暂停与保存状态。

[已验证] libvirt storage 以 storage pool 和 storage volume 为基本边界。pool 类型可覆盖目录、文件系统、网络文件系统、逻辑卷、iSCSI、RBD、ZFS 等；volume 包含容量、分配、格式和路径等属性。

[分析] Govirta 不应把磁盘简单建模为 VM 附属文件路径。MVP 可以简化 UI，但技术模型至少应保留 pool、volume、format、capacity、allocation、source path 等显式信息。

[已验证] libvirt virtual network 与 domain interface 是不同层级的对象。virtual network 可描述 NAT、route、open、bridge、isolated 等模式；domain interface XML 描述 guest NIC 如何连接到 host bridge、virtual network 或其他 source。

[分析] 网络不应被压缩为“启用/禁用”。MVP 至少应能显式展示 network 名称、forward mode、bridge、interface source、MAC、model 等影响运行时行为的字段。

[已验证] libvirt 现代部署可能使用传统 monolithic `libvirtd`，也可能使用 modular daemon，例如 `virtqemud`、`virtnetworkd`、`virtstoraged`、`virtinterfaced`、`virtlogd`、`virtlockd`。

[分析] 连接失败不能只报告“libvirt unavailable”。错误分析应区分 daemon 未运行、socket 不存在、权限拒绝、driver daemon 缺失、认证失败、对象不存在、资源忙、超时和操作不支持等情况。

## 长期演进：类 Kubernetes 编排集群

[已验证] 长期方向是从“管理本机 libvirt”演进到“声明式编排多节点虚拟化资源”。架构分析应关注 API 对象、期望状态、控制循环、节点 agent、调度、状态存储、网络与存储抽象。

[分析] 不应在 MVP 阶段直接实现集群抽象。MVP 只需要避免把未来必然变化的边界写死，例如把 libvirt 调用散落在 Web handler 中、把连接 URI 作为隐藏默认、或把 XML 生成过程变成不可审计的隐式翻译。

[分析] 长期如果要接近 Kubernetes 式控制面，Govirta 需要明确区分以下边界：

- 用户提交的声明式期望状态。
- 控制面保存和校验后的资源对象。
- 调度器或 admission 决策。
- 节点 agent 观察到的实际状态。
- reconciliation loop 对差异的修正动作。
- 底层 libvirt 或替代运行时承接的本地执行语义。

[分析] domain XML 是 libvirt 当前最重要的声明边界之一。未来的 Govirta 资源模型可以生成 XML，但生成结果必须可解释、可审计，并且在影响运行时前允许用户理解或编辑关键字段。

## 参考模型

### libvirt

官方来源：

- https://libvirt.org/
- https://libvirt.org/api.html
- https://libvirt.org/drivers.html
- https://libvirt.org/uri.html
- https://libvirt.org/auth.html
- https://libvirt.org/acl.html
- https://libvirt.org/daemons.html
- https://libvirt.org/html/index.html
- https://libvirt.org/html/libvirt-libvirt-host.html
- https://libvirt.org/html/libvirt-libvirt-domain.html
- https://libvirt.org/html/libvirt-libvirt-storage.html
- https://libvirt.org/html/libvirt-libvirt-network.html
- https://libvirt.org/html/libvirt-libvirt-interface.html
- https://libvirt.org/html/libvirt-virterror.html
- https://libvirt.org/storage.html
- https://libvirt.org/formatdomain.html
- https://libvirt.org/formatnetwork.html

[已验证] libvirt 是 C API、daemon、driver 和管理工具组成的虚拟化管理体系，不只是 `virsh` 命令行。公共 API 会根据连接 URI 委派到内部 driver；可用 daemon 通常还涉及 hypervisor、network、storage、interface 等驱动边界。

[已验证] libvirt API 模块覆盖 host、domain、storage、network、interface、event、error handling 等领域。核心对象包括 connection、domain、network、storage pool、storage volume、interface、node device、snapshot、secret。

[已验证] libvirt 错误结构包含错误 domain、错误 code、错误 level 和 message。Govirta 应尽量保留这些结构化信息，避免压平成不可追踪字符串。

[影响] Govirta MVP 的技术分析应围绕 connection、domain、storage、network、interface、permission、daemon、error 这些边界展开，而不是从页面按钮或 CLI 命令开始建模。

### Go libvirt 候选

Context7 来源：

- `/gitlab_libvirt/libvirt-go-module`
- `/digitalocean/go-libvirt`

公开来源：

- https://gitlab.com/libvirt/libvirt-go-module
- https://github.com/digitalocean/go-libvirt

[已验证] `libvirt-go-module` 提供 `libvirt.org/go/libvirt` 包，围绕 `NewConnect(uri string)` 建立 libvirt 连接，并暴露 domain、network、storage pool、storage volume 等 API。

[已验证] `digitalocean/go-libvirt` 是纯 Go RPC 客户端，可通过 URI 或 dialer 连接 libvirt daemon，并提供 domain、network、storage pool 等枚举与管理 API。

[分析] 初始化阶段不选择具体 Go binding。后续应单独比较 cgo 依赖、API 覆盖、错误类型表达、维护活跃度、许可证、跨平台构建、Linux 发行版打包和远程/本地连接支持。

### Kubernetes

官方来源：

- https://kubernetes.io/docs/concepts/overview/
- https://kubernetes.io/docs/concepts/architecture/
- https://kubernetes.io/docs/concepts/architecture/controller/
- https://kubernetes.io/docs/concepts/architecture/nodes/
- https://kubernetes.io/docs/reference/command-line-tools-reference/kubelet/
- https://kubernetes.io/docs/reference/node/kubelet-sync-loop/
- https://kubernetes.io/docs/concepts/overview/working-with-objects/kubernetes-objects/
- https://kubernetes.io/docs/tasks/manage-kubernetes-objects/declarative-config/

[已验证] Kubernetes 官方架构文档把集群分为 control plane 与 node。control plane 做全局决策，例如调度；node 运行 workload，并包含 kubelet、container runtime 和可选 kube-proxy 等组件。

[已验证] Kubernetes controller 是控制循环：观察集群当前状态，基于对象 `spec` 中的期望状态请求或执行变更，使当前状态接近期望状态。

[已验证] kubelet 是运行在每个 node 上的 agent。它接收 PodSpecs，并确保这些规格描述的容器正在运行且健康；kubelet sync loop 会持续把 Pod 期望状态与容器运行时实际状态进行协调。

[分析] Kubernetes 对 Govirta 的核心启发不是“复制全部组件”，而是清晰区分 API 对象、期望状态、控制器、节点 agent、调度和实际状态回报。Govirta 长期可以学习这个分层，但 MVP 不应实现集群控制面。

[分析] kubelet 模型对未来 Govirta node agent 有参考价值：节点 agent 负责本机实际状态观察和执行，但不应在初始化阶段创建 agent 目录或接口。

### 现有虚拟化管理项目

参考来源：

- Cockpit：https://cockpit-project.org/、https://github.com/cockpit-project/cockpit、https://github.com/cockpit-project/cockpit-machines
- Kimchi：https://github.com/kimchi-project/kimchi
- oVirt：https://www.ovirt.org/、https://github.com/oVirt/ovirt-engine、https://ovirt.github.io/ovirt-engine-api-model/master/
- Proxmox VE：https://www.proxmox.com/en/products/proxmox-virtual-environment/overview、https://pve.proxmox.com/pve-docs/、https://pve.proxmox.com/pve-docs/pve-admin-guide.html
- Harvester：https://harvesterhci.io/、https://docs.harvesterhci.io/v1.8、https://github.com/harvester/harvester
- OpenNebula：https://opennebula.io/、https://docs.opennebula.io/、https://github.com/OpenNebula/one

[已验证] Cockpit 是面向服务器的 Web 管理界面，强调使用系统已有 API 和命令，不重造 Linux 子系统；`cockpit-machines` 是其虚拟机管理 UI，并依赖 libvirt-dbus、virt-install、virt-xml 等组件。

[分析] Cockpit 对 Govirta 的启发是单机运维入口、浏览器化操作体验和“尊重系统已有工具”的思路；不应复制其面向通用 Linux 系统管理的广泛插件范围。

[已验证] Kimchi 是 HTML5 KVM guest 管理界面，作为 Wok 插件运行，并通过 libvirt 管理 KVM guests。其 README 展示了模板、guest 列表、截图、启动/关闭、浏览器访问等轻量 Web 管理模式。

[分析] Kimchi 对 Govirta 的启发是轻量级 KVM/libvirt Web 管理思路。其最新 GitHub release 显示为 2020 年，维护活跃度需要后续复核，因此不能直接作为现代架构模板。

[已验证] oVirt Engine 是虚拟化管理器，提供 Web 管理和 REST API；其 API 文档明确描述服务、集合、对象、认证、安全和 VM/storage/network 等数据中心级资源模型。

[分析] oVirt 对 Govirta 的启发是虚拟化管理控制面的资源抽象、API 对象和运维模型；不应把其企业级数据中心复杂度搬进单机 MVP。

[已验证] Proxmox VE 是基于 Debian 的虚拟化平台，集成 KVM、LXC、Web 管理界面、存储、网络、备份、HA、集群和 pmxcfs/Corosync 等能力。官方文档说明它可以从单节点扩展为多节点集群。

[分析] Proxmox VE 对 Govirta 的启发是 Web 运维体验、节点/存储/网络统一呈现和日常操作闭环；不应在 MVP 阶段复制完整集群、备份、HA、权限、发行版集成和 Corosync/pmxcfs 复杂度。

[已验证] Harvester 是基于 Kubernetes、KubeVirt、Longhorn 和 KVM 的 HCI 平台，使用 Kubernetes API 作为统一自动化语言，同时管理 VM 和容器化环境。

[分析] Harvester 对 Govirta 的启发是 Kubernetes 风格虚拟化平台和 HCI 思路；不应在 libvirt MVP 阶段引入 Kubernetes 作为运行前提。

[已验证] OpenNebula 是面向企业、私有、混合或边缘云基础设施的开源平台，文档覆盖 control plane、cluster provisioning、host/cluster、network、storage、scheduler、VM operation、multitenancy、Sunstone GUI 和 API 集成。

[分析] OpenNebula 对 Govirta 的启发是虚拟化资源编排、调度、多租户、网络/存储抽象和云平台化演进；不应把其云平台级抽象提前引入单机管理端。

## 开放问题

- MVP 是 Web 进程直接连接 libvirt，还是通过本地 agent 连接 libvirt？
- VM、存储、网络是否需要从第一版就建立内部资源模型？
- 长期控制面是否从单机 API server 平滑演进，还是另起集群控制面？
- 哪些 libvirt 能力应长期保留，哪些能力适合逐步替换？
- domain XML 应作为用户可见的底层配置、只读审计结果，还是高级资源模型生成物？
- 连接 URI、认证方式、进程身份和只读/读写模式如何在 UI/API 中显式表达？
- libvirt structured error 如何映射到 Govirta 的 API 错误模型？

## 后续验证

- 在真实 Linux/libvirt 环境验证连接权限、错误路径和 domain 生命周期。
- 评估 Go binding 的维护状态、API 覆盖、许可证和版本兼容性。
- 对比直接 libvirt API 与本地 agent 封装的安全边界。
- 实测 `qemu:///system` 与 `qemu:///session` 的权限、资源范围和错误差异。
- 验证 modular daemon 部署下 daemon/socket 缺失时的错误表现。
- 对 VM lifecycle 操作建立可复现的状态转换测试清单。
- 对 storage pool/volume 与 network/interface 建立最小资源建模样例。

## 初始化阶段结论

[已验证] 初始化阶段可以固定项目方向、Go module、许可证、协作规则和技术分析框架。

[分析] 初始化阶段不应选择 Web 框架、数据库、libvirt Go binding 或集群控制面实现。下一阶段应先围绕真实 Linux/libvirt 环境完成连接、权限、错误和 lifecycle 验证，再进入 MVP 架构计划。
