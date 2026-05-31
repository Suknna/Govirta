# Lima 嵌套虚拟化验收测试框架设计

## 背景

Govirta 最终面向 Linux,但开发在 macOS（Apple M3 / macOS 26.5）上进行。当前仓库只有
`scripts/verify.sh`（gofmt + `go test ./...` + 三个主服务构建）作为本地验证入口,**没有任何
CI**（无 `.github/workflows`、无 Makefile）。已有的集成测试约定是 env-gate（如
`internal/virt/qmp/integration_test.go` 用 `GOVIRTA_QMP_INTEGRATION=1` 门控）,**零 build
tag**,零第三方测试框架依赖（仅 stdlib `testing` + 手写 fake runner）。

作为虚拟化平台,Govirta 有一批测试**在 macOS 上物理上跑不了**:真实 `qemu-system -accel kvm`
启 guest（需嵌套虚拟化）、真实 netlink 建网桥/TAP（需 Linux + `CAP_NET_ADMIN`）、真实
`qemu-img` 二进制全生命周期。这些必须在 Linux 环境里跑。

本设计为 Govirta 建立一个**仿 Kubernetes conformance 风格**的强制验收测试框架:把"在 macOS 上
跑不了的、且必须工作"的端到端路径,放进一个由 Lima 拉起的临时 Linux 虚拟机里跑;并在 git 合并进
`main` 时通过本地 `pre-push` hook 自动触发,**必过才允许 `main` 进入远程**。

### 可行性验证证据（make-or-break smoke test）

整个框架押在"M3 + Lima 能否暴露嵌套 KVM"这一**版本敏感**结论上。AGENTS.md 第十四章原记录
"KVM 硬件加速、嵌套虚拟化…本机不具备,必须在远程 Linux 节点验收"——该结论对 OrbStack 成立,
但对 M3 + Lima(vz) **已过时**。本设计落笔前已用一次性 Lima VM 实测验证 `[已验证 2026-05-31]`:

- 宿主实测:Apple M3（Mac15,12）/ macOS 26.5（25F71）/ `limactl` 2.1.1。
- 路径:`lima.yaml` 设 `vmType: "vz"` + `nestedVirtualization: true` + Ubuntu 24.04 arm64
  cloud image → guest 内 `qemu-system-aarch64 -machine virt -accel kvm -cpu host` 引导
  cirros aarch64。
- 结果:guest 内核日志出现 `smccc: KVM: hypervisor services detected`（**确定性的嵌套 KVM
  标记**,区别于 TCG 软件仿真）,cirros 启动到用户态（cloud-init 开始探测数据源）。`-accel kvm`
  初始化成功,无 "failed to initialize kvm" 错误。
- 关键教训:① 内层 QEMU 必须 `-cpu host`;② Lima 默认用户**不在 `kvm` 组**,直接开 `/dev/kvm`
  得 "Permission denied",provision 时必须加入 `kvm` 组或测试以 sudo 运行;③ 不能再往里套
  `-machine virt,virtualization=on`（Lima issue #4498 不支持深层嵌套）,但纯 cirros L2 boot
  不需要它。

权威来源:Apple `VZGenericPlatformConfiguration.isNestedVirtualizationSupported`（M3+/macOS 15+）
<https://developer.apple.com/documentation/virtualization/vzgenericplatformconfiguration/isnestedvirtualizationsupported>;
Lima 嵌套虚拟化 PR #2530 <https://github.com/lima-vm/lima/pull/2530>。

**结论:CONDITIONAL → FEASIBLE `[已验证]`**。框架据此设计。

## 目标

1. 建立两层测试模型:**mac 单元测试**（`go test ./...`,fake runner,每次 push 都跑,秒级）
   与 **Lima 验收测试**（真实二进制,Linux-only,按需触发,分钟级）。
2. 验收测试用 `//go:build acceptance` build tag 标记,集中在 `test/acceptance/`,模仿 k8s
   `[Conformance]` 必过子集的"打标签 + focus 过滤"范式。
3. 验收在 Lima 拉起的**临时即弃** Linux VM 内运行,VM 系统状态零漂移;`scripts/acceptance.sh`
   使用父目录 `.l/<repo_key>` 下的短路径 `LIMA_HOME` 避开 Lima socket 路径长度限制,同时把
   cirros guest 镜像、Go 工具链与生成配置缓存在项目根 `.lima/cache/` 下,摊薄冷启成本。
4. 通过本地 `.githooks/pre-push` hook 自动触发:**推 `main` → 无条件全量验收（必过门禁）**;
   推特性分支 → 仅当 diff 触及 Linux 相关包时跑验收（早反馈）。
5. 验收测试验证 **Govirta 自己的代码**对真实二进制的契约（用 `internal/virt/qemu` 渲染 argv、
   `internal/virt/qemuimg` 调 qemu-img、`internal/virt/qmp` 连 socket），而非裸 qemu 烟测。
6. 把远程主机（`192.168.139.206`）交叉编译验收工作流**完整迁移到 Lima**,移除 AGENTS.md 中的
   远程主机记录,并校正第十四章嵌套虚拟化表述。
7. 所有约束（门禁纪律、`.lima/` 缓存语义、sudo 约定、`--no-verify` 禁令）写入 AGENTS.md。

## 非目标

- **不引入** GitHub Actions self-hosted runner 或任何云端 CI。证据:GitHub 托管 macOS arm64
  runner 不支持嵌套虚拟化（官方文档 + 2026-01 issue 复确认）,且用户明确选择纯本地 hook 路线。
- **不引入** Ginkgo/Gomega 或任何 BDD 框架;沿用 stdlib `testing` + build tag,与既有约定一致,
  零新依赖。
- **不测占位实现**。`internal/network/bridge` 当前是 `NoopManager`（未调用 netlink）,故
  `bridge_test.go` **推迟**到真实 netlink `bridge.Manager` 落地后再加（呼应"重构阶段测试治理":
  不为占位代码写测试）。
- **不实现** PR 服务端 required status check 门禁;`pre-push` 是本地门禁,可被 `--no-verify`
  绕过——以 AGENTS.md 纪律约束之,不以技术强制之。
- **不在 VM 内做深层嵌套**（`-machine virt,virtualization=on`),不在 cirros 内再起 hypervisor。
- **不固定发行版为 Rocky**;采用 Lima 默认 Ubuntu 24.04 arm64（官方验证过嵌套虚拟化的路径、
  资源占用低）。
- **不改动** mac 单元测试现状;`go test ./...` 与 `scripts/verify.sh` 的单测部分保持不变。
- **不向验收测试硬编码**外部依赖路径;firmware、cirros 镜像、qemu-img 路径全部由 harness 经
  环境变量显式注入（呼应"拒绝隐式"约定）。

## 整体架构

### 两个执行环境 × 三层框架

```text
执行环境:
  [macOS 主机]  go test ./...              单元测试,fake runner,快,每次 push
  [Lima VM]     go test -tags acceptance   真 QEMU+KVM+qemu-img,慢,按需冷启即弃

框架三层:
  L1  验收测试  (Go, //go:build acceptance)   必过用例本体,在 test/acceptance/
  L2  Lima harness (shell + lima/*.yaml)      冷启VM→注入代码→跑测试→拆毁
  L3  门禁触发  (.githooks/pre-push)           判断推送目标→决定是否调 L2
```

### 仿 k8s conformance 映射（取精髓,砍过度工程）

| k8s 概念 | Govirta 对应 | 实现 |
| --- | --- | --- |
| e2e 全集 | 所有测试 | `go test ./...`（mac） |
| `[Conformance]` 必过子集 | Linux-only 验收集 | `//go:build acceptance` |
| focus 过滤跑子集 | 只跑验收集 | `go test -tags acceptance ./test/acceptance/...` |
| conformance 必过才合规 | 验收必过才合并 main | `pre-push` hook 阻断 |

**砍掉**:sonobuoy（分发/认证打包）、conformance 晋升 PR 流程、版本化 conformance doc、SIG 审批、
`framework.ConformanceIt()` 封装层——这些是治理大型多人项目的产物,小项目用一个 build tag 即可。

### 分层依据

- **留在 mac**:纯逻辑、argv 构建、fake runner 单测 → 现状 `go test ./...` 不变。
- **强制进 Lima**（mac 物理做不到的三类,首版落地前两类）:
  1. 真实 `qemu-system -accel kvm` 起 cirros（嵌套 KVM）——首版落地。
  2. 真实 `qemu-img` 二进制全生命周期 create/convert/resize/info/snapshot/check——首版落地。
  3. 真实 netlink 建网桥/TAP（需 Linux + `CAP_NET_ADMIN`）——**推迟**,待 `bridge.Manager`
     真实实现。

## L1:验收测试套件

### 目录与 build tag

所有验收测试位于 `test/acceptance/`,文件头统一 `//go:build acceptance`。该 tag 在普通
`go test ./...` 下被排除,只有 `go test -tags acceptance` 才编译,从而与 mac 单测彻底隔离。

```text
test/acceptance/
├── doc.go            # 包说明 + build tag 约定 + 环境变量契约文档
├── harness.go        # 环境变量读取（firmware/cirros/qemu-img 路径）、QMP/QEMU 辅助
├── boot_test.go      # 嵌套 KVM 起 cirros(-nic none) + QMP query-status 断言
└── qemuimg_test.go   # 真 qemu-img 全生命周期
                      # bridge_test.go 推迟:netlink bridge.Manager 实现后作为扩展点加入
```

### 环境变量契约（显式注入,拒绝隐式）

harness 不硬编码任何外部路径;所有外部依赖经环境变量注入,由 VM 内 provision 确定后写入。统一前缀
`GOVIRTA_ACCEPTANCE_`:

| 环境变量 | 含义 | 由谁设置 |
| --- | --- | --- |
| `GOVIRTA_ACCEPTANCE=1` | 验收总开关（无则 `t.Skip`,沿用既有 env-gate 风格） | harness |
| `GOVIRTA_ACCEPTANCE_QEMU` | `qemu-system-aarch64` 路径 | VM provision |
| `GOVIRTA_ACCEPTANCE_QEMU_IMG` | `qemu-img` 路径 | VM provision |
| `GOVIRTA_ACCEPTANCE_FIRMWARE` | edk2 aarch64 firmware 路径 | VM provision |
| `GOVIRTA_ACCEPTANCE_CIRROS` | cirros aarch64 镜像路径 | harness（指向缓存挂载） |

harness 在 `TestMain` 中校验开关与必需变量缺失即 `t.Skip`(开关未开)或 `t.Fatal`(开关开但变量
缺失),与 `qmp/integration_test.go` 现有模式一致。

### 首个必过用例:`boot_test.go`

验证刚实测证明可行的链路,但**全程走 Govirta 项目代码**:

1. 用 `internal/virt/qemuimg` 把 cirros 镜像备好（真 `qemu-img`,如需 convert/resize）。
2. 用 `internal/virt/qemu` typed builder 渲染 argv（`-accel kvm -cpu host`、`-nic none`、串口
   日志、QMP socket、pidfile 全部指向 **VM 本地可写 scratch**,见下文「读写边界」）→ spawn 真
   QEMU 子进程。
3. 用 `internal/virt/qmp` 连 QMP socket,断言 `query-status` 的 state 为 `running`。
4. `system_powerdown` / `quit` 收尾,校验进程退出与产物清理。

无网络模式:cirros 用 `-nic none` 启动（正是 smoke test 验证过的方式）,不依赖网桥,与 bridge
测试推迟一致。

### 必过用例:`qemuimg_test.go`

用真实 `qemu-img` 二进制走 `internal/virt/qemuimg` 全生命周期:create → info → convert
(raw→qcow2) → resize → snapshot → check → remove,断言每步对真实二进制的契约。

### 扩展模式

新增一条必过行为 = 在 `test/acceptance/` 加一个 `//go:build acceptance` 测试函数,自动纳入
`go test -tags acceptance` 范围,**无需改 hook 或脚本**。`bridge_test.go` 即为已规划的下一个
扩展点(`bridge.Manager` 真实实现落地后,以 sudo 跑 netlink 建网桥/TAP/校验/清理)。

## L2:Lima harness

### VM 配置 `lima/govirta.yaml`（纳入 git）

实测验证过的路径:

```yaml
vmType: "vz"
nestedVirtualization: true
images:
  - location: "<ubuntu-24.04-server-cloudimg-arm64>"
    arch: "aarch64"
cpus: 2
memory: "2GiB"
disk: "12GiB"
mounts:
  - location: "<项目根>"          # 源码,只读
    writable: false
  - location: "<项目根>/.lima/cache"  # 缓存,可写
    writable: true
provision:
  - mode: system
    script: |
      # 装 qemu-system-aarch64 + edk2 firmware
      # 把目标用户加入 kvm 组（实测教训:否则 /dev/kvm Permission denied）
  - mode: user
    script: |
      # 从 .lima/cache/toolchain/ 解压 Go tarball 到 VM 本地盘（guest 原生 ext4,最稳）
```

### 生命周期 = 临时即弃（B）

每次触发:`limactl start`（全新实例）→ provision → 注入代码跑测试 → `limactl delete`。**VM 系统
状态零漂移**:不复用任何被手动改过/包更新过的实例。

### 缓存布局与 Lima home（短路径 Lima home + 项目 `.lima/cache/`）

```text
<项目父目录>/.l/<repo_key>/       # scripts/acceptance.sh 生成的短路径 Lima home,实例即弃
<项目根>/.lima/cache/             # gitignored repo cache,跨实例持久
├── govirta.generated.yaml        # 展开后的 Lima 配置
├── images/
│   └── cirros-aarch64.qcow2      # cirros guest 镜像（下载一次）
├── toolchain/
│   └── go<version>.linux-arm64.tar.gz  # pinned Go 工具链 tarball 源
├── gocache/                      # GOCACHE
└── gomodcache/                   # GOMODCACHE
```

**零漂移 vs 缓存的关键澄清**:VM **系统状态**每次全新（满足 B 的零漂移）;缓存的只是"由
go.mod + 源码确定的可复现产物"（Go 工具链 tarball、GOCACHE、GOMODCACHE、cirros 镜像）——这些
不构成漂移,不给 B 开后门。Lima 基础镜像由 Lima 自身缓存,冷启只重建实例本身,不重复下载。

### 代码注入 = VM 内装 Go + 持久缓存（C）

- 源码**只读**挂载进 VM（如 `/govirta-src`）;`.lima/cache` **可写**挂载（如 `/govirta-cache`）。
- Go 工具链 = **VM 内解压缓存的 tarball**:provision 从 `.lima/cache/toolchain/` 解压
  linux-arm64 Go tarball 到 VM 本地盘,跑在 guest 原生 ext4 上（比 virtiofs 挂宿主 Go 二进制
  更稳;首次下载一次后续复用缓存,解压秒级可忽略）。
- VM 内 `GOCACHE`/`GOMODCACHE` 指向可写缓存挂载,脚本通过 `limactl shell --workdir /govirta-src
  "$instance_name" -- sh -eu -c 'sudo -E env ... go test -v -tags acceptance -count=1 ./test/acceptance/...'` 运行验收。

### 读写边界（VM 内只读源码 + 可写 scratch,含递归挂载处理）

VM 内有三类路径,读写语义必须分清——这是 AI 实现者最易踩坑处,故显式定义:

1. **只读源码挂载** `/govirta-src`:Govirta 源码,VM 内**绝不写入**。
2. **可写缓存挂载** `/govirta-cache`（宿主 `.lima/cache`）:跨实例持久,放 Go 工具链/GOCACHE/
   GOMODCACHE/cirros 镜像。VM 内 `GOCACHE`/`GOMODCACHE` 指向**此独立挂载点**(`/govirta-cache/...`),
   `go test` 的构建写入全落这里,**不经只读源码挂载**。注意:`.lima/cache` 在宿主物理上虽位于项目根
   内,但 VM 通过专门的可写挂载点访问,与只读源码挂载是两条不同的 VM 路径——不存在「往只读挂载写」。
3. **VM 本地可写 scratch**(`t.TempDir()`,落在 guest 本地 ext4):验收测试运行时产物——QMP
   socket、串口日志、pidfile、qemu-img 临时 qcow2——全部写这里。**不写只读源码挂载,也不写宿主**
   **`.tmp/`**。理由:① 源码挂载只读;② QMP unix socket 路径有长度限制(~108 字符),VM 本地短路径
   更稳;③ VM 即弃,scratch 随 `limactl delete` 一并消失,天然清理。

**与第十五章的关系**:第十五章「临时产物写项目 `.tmp/`」约束的是**宿主开发环境**;验收测试运行在
临时即弃 VM 内,产物用 VM 本地 scratch 更干净(无宿主污染、随 VM 销毁),不违背该规则的本意。

**Lima home 路径 footgun**:Lima 的 socket 路径长度受 Unix socket 限制影响,所以
`scripts/acceptance.sh` 不把 Lima home 放在项目 `.lima/` 下,而是生成父目录 `.l/<repo_key>`
短路径来避开 socket 路径限制。项目 `.lima/cache/` 是持久 gitignored repo cache,只保存可复现缓存和生成配置,不承载正在运行的 Lima 实例 diffdisk。

### 干活脚本 `scripts/acceptance.sh`

封装全流程,被 hook 调用,也可手动跑:生成短路径 Lima home 到父目录 `.l/<repo_key>` → 必要时下载 cirros/Go tarball
到项目 `.lima/cache/` → 生成 Lima config → `limactl start` → VM 内以 `limactl shell --workdir /govirta-src "$instance_name" -- sh -eu -c 'sudo -E env ... go test -v -tags acceptance -count=1 ./test/acceptance/...'` 跑验收 →
回收 `limactl delete`。脚本接收一个 mode 参数（全量 / 仅 Linux 相关包),hook 据分支决定。

### CAP 约定

Linux-only 验收测试在 VM 内**以 sudo 运行**（取得 `/dev/kvm` 与未来的 `CAP_NET_ADMIN`),与
smoke test 用 sudo 拿 `/dev/kvm` 一致。VM 即弃,sudo 无残留风险。

## L3:pre-push 门禁

### hook 安装

hook 进版本库 `.githooks/pre-push`,一次性 `git config core.hooksPath .githooks` 启用（写入
AGENTS.md setup）。`.git/hooks/` 不进版本库,故不放那。

### 决策树

`pre-push` 从 stdin 读取被推送的 ref（git 协议格式 `<local-ref> <local-sha> <remote-ref>
<remote-sha>`）,逐条决策:

```text
对每条推送的 ref:
├─ remote-ref == refs/heads/main?
│    └─ 是 →【必过门禁】mac 单测(verify.sh) + 全量 Lima 验收
│             失败 → exit 1,阻断 push,main 永不带病进远程
│
└─ 否（特性分支）:
     ├─ diff 触及 Linux 相关包?
     │   (network/bridge、virt/qemu、virt/qmp、virt/qemuimg、
     │    storage/local、storage/localfile、test/acceptance/)
     │    └─ 是 → mac 单测 + Lima 验收（早反馈）
     └─ 否 → 仅 mac 单测（秒级,不碰 Lima）
```

### 关键设计点

- **推 main 无条件全量**——不依赖任何 diff 检测（门禁路径必须确定、不靠猜,呼应"拒绝隐式魔法":
  main 门禁是显式的、按分支确定的）。
- **新分支边界**:`remote-sha` 为全 0 时,diff base 退回 `origin/main`,不让检测崩。
- hook 只判断 + 决定模式,干活委派给 `scripts/acceptance.sh <mode>`,保持 hook 薄。
- `--no-verify` 可绕过——这是 git 本地 hook 固有局限,以 AGENTS.md 明令"禁止用 `--no-verify`
  跳过 main 门禁"将其变为项目纪律。

## 目录结构（新增/改动汇总）

```text
lima/
└── govirta.yaml          # [新增,git] vz+nestedVirtualization+Ubuntu24.04 + provision
.githooks/
└── pre-push              # [新增,git] 门禁 hook
scripts/
├── verify.sh             # [已有] mac 单测,不变
└── acceptance.sh         # [新增,git] 干活层:冷启→注入→go test -tags acceptance→delete
test/
└── acceptance/           # [新增,git] 仿 k8s test/e2e,全部 //go:build acceptance
    ├── doc.go
    ├── harness.go
    ├── boot_test.go
    └── qemuimg_test.go
.lima/cache/              # [新增,gitignore] 持久 repo cache:cirros/Go/generated config
../.l/<repo_key>/          # [gitignore 外] 短路径 Lima home:VM 实例即弃
.gitignore                # [改] 追加 .lima/
AGENTS.md                 # [改] 见下节
```

## AGENTS.md 约束变更

1. **新增"验收测试"小节**（放 COMMANDS 后）:定义两层测试模型——mac 单测（`go test ./...`,
   fake runner,每次 push）vs Lima 验收（`-tags acceptance`,真二进制,Linux-only,
   `scripts/acceptance.sh`）。
2. **门禁纪律**:推 `main` 必过全量 Lima 验收;**禁止 `git push --no-verify` 跳过 main 门禁**
   （列入第十五章红线候选或本地化声明）。
3. **校正第十四章**:"本机不具备嵌套虚拟化"改为——OrbStack 不支持,但 M3 + Lima(vz) +
   `nestedVirtualization` 已实测可行 `[已验证 2026-05-31]`;KVM 嵌套验收从"远程 Linux 节点"
   迁移到本地 Lima。
4. **移除 NOTES 远程主机两行**:删除 `192.168.139.206` 远程主机记录 + 交叉编译远程跑的工作流
   描述,完整迁移到 Lima。
5. **新增 `.lima/` 缓存约定**:半持久缓存（区别于 `.tmp/` 即弃）,VM 系统状态零漂移、仅缓存可
   复现产物（Go 工具链/GOCACHE/cirros 镜像）。
6. **CAP 约定**:Linux-only 验收测试在 VM 内以 sudo 运行(`/dev/kvm` + 未来 `CAP_NET_ADMIN`)。
7. **更新 COMMANDS**:补充 `git config core.hooksPath .githooks` 的一次性 setup 与
   `scripts/acceptance.sh` 用法。

## 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| Lima 嵌套虚拟化在某次 macOS/Lima 升级后回归失效 | smoke test 步骤文档化;`scripts/acceptance.sh` 在 provision 后校验 guest 内 `/dev/kvm` 与 `dmesg KVM` 标记,失败早报 |
| 冷启 + provision 单次耗时偏高拖慢推 main | 镜像/Go 工具链/cirros 全缓存在 `.lima/cache`;特性分支按路径检测避免无谓冷启;只有推 main 才付全量成本 |
| `--no-verify` 绕过门禁 | AGENTS.md 明令禁止;依赖项目纪律（本地 hook 固有局限,已在非目标声明） |
| 源码递归挂载暴露 diffdisk | 源码只读挂载 + `go test` 限定路径 + 缓存目录在源码树外,三重缓解 |
| `.lima/` 误入 git（VM 磁盘体积大） | `.gitignore` 追加 `.lima/`,与 `.tmp/` 并列 |

## 验收标准

本框架自身的"验收":

1. `git config core.hooksPath .githooks` 后,在特性分支推送不触及 Linux 包的改动 → 仅跑 mac
   单测,不启动 Lima。
2. 推送触及 `internal/virt/qemu` 的改动到特性分支 → 自动冷启 Lima 跑验收。
3. 推 `main` → 无条件全量验收;`boot_test.go` 真起 cirros 且 QMP `query-status` == `running`,
   `qemuimg_test.go` 全生命周期通过 → 允许 push;任一失败 → exit 1 阻断。
4. 验收跑完 `limactl delete` 回收实例,`.lima/cache` 保留。
5. AGENTS.md 第十四章嵌套虚拟化表述已校正,NOTES 远程主机记录已移除。
