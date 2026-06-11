# 刀 6：冷配置更改设计

## 1. 定位与目标

冷配置更改是生命周期"另一半"的最后一刀（overview roadmap 第 9 项）：让用户能修改已存在
VM 的 **memoryMiB / vcpus / volumeRefs / nicRefs**（改标量 + 增删硬件），node 在 VM 处于
冷态（进程已停）时重建 argv 并落盘，下次开机以新配置拉起。

这一刀**不建新引擎**——执行面 argv builder（`deriveBuilder`）、配置权威（`vmm.SpecSummary`）、
冷态门禁模式（刀 5 `vmIsCold`）、电源二维模型（`powerState=Off` 的真正停机判定）都已就位。
工作集中在三处：

1. **vmm 层**：新增 `Redefine` 原语——覆写 vm.json 的 `Spec`、据新 spec 确定性重派生 argv、
   覆写 `Argv`、原子落盘；不碰 `Intended`、不碰进程（纯磁盘操作）。
2. **apiserver admission 层**：新增 cold-mutable 门禁——检测 cold-mutable 字段变更时要求
   `new.spec.powerState == Off`，否则 4xx 拒绝。
3. **node 控制器层**：在 VM 冷态收敛点插入 drift 检测——比对 desired `SpecSummary` 与 live
   `Spec`，不等则调 `Redefine`。

### 前置地基（均已合并到 main）

| 前置 | 提供的能力 | 提交 |
| --- | --- | --- |
| vmm 配置权威整顿 | `SpecSummary` 单一配置权威，argv 由它确定性派生 | `2b73bdf` |
| 电源模型修复 | `powerState=Off` 是"VM 真正停机"的正确判定 | `d0e2eda` |
| 刀 3 Validating Admission | FieldPolicyValidator（immutable 拒绝）、admission Chain | merged |
| 刀 5 冷扩容 | `vmIsCold` 冷态门禁模式（PhaseStopped/PhaseDefined） | `16b4368` |

## 2. 五条架构原则（继承 overview，本刀适用部分）

1. **配置权威单一**：`vmm.SpecSummary`（落盘在 vm.json 的 `persistedState.Spec`）是 VM 配置的
   唯一权威。argv 由它确定性派生，没有第二份配置。Redefine 覆写 Spec 即覆写配置。
2. **上下一致**：live（运行的 QEMU 进程 + QMP）永远是物理事实权威。drift 检测比对的"live 配置"
   读自 `vmm.Status()` 返回的 `VM.Spec`（即 vm.json 持久化的 Spec），不引入第三方缓存。
3. **status 是投影，绝不驱动 admission**（刀 3 铁律）：admission 门禁只能用 apiserver 权威拥有的
   `spec`，绝不读 `status`。判断"VM 是否该接受配置改"用 `spec.powerState`，不用 `status.observedPowerState`。
4. **显式优于隐式**：配置改要求用户显式声明 `powerState=Off`。不存在"接受运行中改配置 + 隐式延迟生效"
   的 PendingRestart 机制——运行中改配置直接 admission 拒绝。
5. **积木式拼接**：`Redefine` 复用 `Create` 已有的 argv 派生路径（`deriveBuilder` + `injectFacilityFlags`），
   不重写 argv 构造逻辑。

## 3. 双重门禁（defense in depth）

运行中的 VM 收到冷配置改时，两道独立门禁拦截，各用各的权威信号：

### 门禁 1 — apiserver admission（纯 spec 比对，符合刀 3 铁律）

apply update 时对比 old spec vs new spec。判定规则极简：

```
若 new spec 存在 cold-mutable 字段变更（memoryMiB/vcpus/volumeRefs/nicRefs 任一 != old）
且 new.spec.powerState != Off
  → 4xx 拒绝，错误信息："cold config change requires powerState=Off"
```

关键性质：
- **只看 new spec 自己的 powerState**，不看 VM 当前 live 态（apiserver 够不到 live phase，也不该用 status 投影）。
- 因此**允许一次 apply 同时声明关机 + 新配置**：`{powerState: Off, powerOffMode: Acpi, memoryMiB: 512}`
  被接受（new.powerState=Off），node 门禁 2 会等真正冷态才 Redefine。比强制"先关机再改配置"两次 apply 更顺。
- 纯电源改（`powerState: On→Off`，无 cold-mutable 变更）照常接受。

### 门禁 2 — node controller（live phase，复用刀 5 `vmIsCold`）

即使 `powerState=Off` 被门禁 1 接受写入，控制器**仍只在 live phase 真正冷态**（Stopped/Defined/Failed，
即 `ObservedPowerState=Off`）才执行 drift 检测 + Redefine。若 VM 还在 ACPI 关机途中（Stopping），
requeue 等待——不在进程仍活时重建 argv（重建无害但语义上应等进程真正停）。

两道门禁的信号分工：门禁 1 用 `spec.powerState`（apiserver 权威期望），门禁 2 用 live phase
（node 物理事实）。无任何一方读 status 投影。

## 4. cold-mutable 字段分级（本刀作用域）

继承 overview 第 7 节字段分级表的 VM 行，本刀实现其 cold-mutable 列：

| 字段 | 分级 | 门禁 1 拦截 | 说明 |
| --- | --- | --- | --- |
| `memoryMiB` | cold-mutable | 是 | 标量改，Redefine 重派生 `-m` |
| `vcpus` | cold-mutable | 是 | 标量改，Redefine 重派生 `-smp` |
| `volumeRefs` | cold-mutable | 是 | 增删盘，成员变更 → Disks 切片变 → argv blockdev/device 增减 |
| `nicRefs` | cold-mutable | 是 | 增删卡，成员变更 → NICs 切片变 → argv netdev/device 增减 |
| `arch` | immutable | 刀 3 FieldPolicyValidator | 改架构 = 删旧建新 |
| `name`/`uid` | immutable | 刀 3 envelope | 身份不可变 |
| `powerState`/`powerOffMode` | live-mutable | 否 | 电源意图，刀 2 已实现 |

immutable 由刀 3 已建的 FieldPolicyValidator 在 admission 拒绝，与 cold-mutable 门禁正交，本刀不动。

### 增删硬件无需新机制

`SpecSummary` 已含 `Disks []DiskSpec` + `NICs []NICSpec`。增删 volumeRef/nicRef → `gatherDependencies`
解析出的切片成员变化 → desired SpecSummary 与 live Spec 直接比对**自然检测到 drift** → Redefine
重派生 argv。增删硬件和改标量走**完全相同的 drift→Redefine 路径**，无特判。

三个落地性质：
1. **依赖门禁自然适用**：新增 volumeRef 指向的 Volume 必须已存在且 Ready，否则 `gatherDependencies`
   返回 not-ready → requeue 等待（与 create 路径一致）。
2. **删硬件 = 先从 VM.refs 移除，再删独立资源**：从 `volumeRefs` 移除让 Redefine 后 argv 不含该盘，
   Volume 对象仍在 etcd；之后单独删 Volume，刀 3 反向引用守卫此时放行（VM 已不引用）。顺序天然正确，
   无级联删除。
3. **`VMSpec.Validate` 守底线**：`len(VolumeRefs)==0` / `len(NICRefs)==0` 已被 Validate 拒绝——
   删到空会被 admission 挡住，不可能 Redefine 出无盘无卡的 VM。

## 5. vmm 层：`Redefine` 原语

```go
// Redefine 覆写 vm.json 的配置权威：用新 Spec 替换 persistedState.Spec，据新 Spec
// 确定性重派生 argv 替换 persistedState.Argv，原子落盘。不碰 Intended、不碰进程
// （纯磁盘操作）。下次 Start 自然 exec 新 argv。
//
// 冷态前提由调用方（控制器）负责：vmm 不探测进程、不校验运行态（任何状态可调，
// 单一职责 = 磁盘配置权威）。幂等：同样的 spec 重复 Redefine 产生同样的 json。
func (s *VMMService) Redefine(ctx context.Context, uuid string, spec SpecSummary) (VM, error)
```

实现路径（复用 Create 已有逻辑）：
1. `loadState(ctx, uuid)` 加载 vm.json（不存在 → `ErrNotFound`）。
2. 据新 `spec` 走 `deriveBuilder` + `injectFacilityFlags` 重派生 argv（与 `Create` 同一路径，
   含 arch 校验：不支持的 arch → `ErrInvalidRequest`，由控制器作永久失败处理）。
3. 覆写 `st.Spec = spec`、`st.Argv = newArgv`，保持 `st.Intended`、`st.UUID`、`st.Paths`、
   `st.CreatedAt` 不变。
4. `writeState(ctx, st)` 原子落盘（更新 `UpdatedAt`）。
5. 返回 `statusFrom(ctx, st)`（含 live phase）。

`VMRunner` 接口（node 控制器侧窄接口）新增 `Redefine` 方法。

## 6. node 控制器：drift 检测

drift 检测插入点 = `reconcileExistingVMOff` 的**进程死分支**（冷态收敛点，即当前
`obs.Observed != ObservedPowerStateOn` 走 `patchLivePowerStatus` no-op 收敛的位置）。这是唯一
同时满足"配置可能已变"且"进程已停可安全重建"的点。

冷态分支新流程：
1. `gatherDependencies(ctx, obj)` 解析当前 volumeRefs/nicRefs → disks/nics。依赖未就绪 →
   `RequeueAfter` 等待（不 Redefine）。依赖读传输错误 → requeue（transient）。
2. 组装 desired `SpecSummary`（与 `reconcileMissingVM` 同一组装逻辑：Name/Arch/VCPUs/MemoryMiB/
   CPUModel/Disks/NICs）。
3. 比对 `live.Spec`（`vmm.Status()` 已返回）vs desired。`SpecSummary` 含切片
   （`Disks`/`NICs`），**不能用 `==`**（Go 切片不可比较，会编译错误）——用 `reflect.DeepEqual`
   或显式逐字段 + 切片逐元素比较。比对结果：
   - **相等** → 无 drift，继续原 no-op 状态收敛（`patchLivePowerStatus`）。
   - **不等** → `vmm.Redefine(ctx, obj.UID, desired)`：
     - 成功 → 结构化日志记录 drift 已收敛，继续状态收敛。
     - `ErrInvalidRequest`（如不支持 arch，但 arch 本应被刀 3 immutable 挡住）→ 永久失败 patch，不 requeue。
     - 其它错误 → 失败 patch + requeue。

drift 检测**只在 powerState=Off 冷态分支**——powerState=On 分支不检测（VM 在跑，门禁 1 已保证
不会有 cold config drift；即便有 stale，也等用户声明 Off 后才处理）。

Defined（从未启动）与 Stopped（启动后已停）走同一冷态分支，drift→Redefine 对两者都正确：
create 落的初始 json 也能被 Redefine 更新。无需特判 Defined。

### 已丢弃的早期设计（YAGNI）

早期 brainstorming（在电源模型修复之前）曾锁定 json 双轴 `Spec`+`StartedSpec` 与 `ConfigState`
enum（`Synced`/`PendingRestart`），为"接受运行中改配置 → 标 PendingRestart → 重启生效"的 ESXi
模型设计。选定**门禁 1 拦截拒绝**后，配置改只可能发生在 `powerState=Off` 时——永不存在"进程正运行
但 desired 配置已变"的状态（被门禁 1 拒掉）。故 `PendingRestart` 是死状态、`StartedSpec` 双轴失去
意义。drift 检测简化为直接比对，**丢弃双轴 + ConfigState**。

## 7. govirtctl 与 e2e 验证

### govirtctl

无新子命令。用户通过 manifest 修改 `spec.memoryMiB`/`vcpus`/`volumeRefs`/`nicRefs` 表达配置改意图，
`govirtctl apply -f` 提交，`govirtctl get vm` 观测。

### e2e（插入 `TestDistributedSpineClosure` 电源序列之后、teardown 之前）

1. **改标量（内存）**：VM 已 `Off`（前序电源场景留下的停机态）→ apply `memoryMiB` 翻倍 +
   `powerState=Off` → 等 status 收敛 → apply `powerState=On` → guest-side 断言 **运行的 QEMU 进程
   argv 含新 `-m <新值>`**（复用 vmm 配置权威整顿建的 `pgrep -a qemu-system` argv 探测模式）。
2. **增硬件（加第二块盘）**：先 apply 一个新 Volume(等 Ready) → VM `Off` → apply `volumeRefs`
   追加该 volume + `Off` → `On` → guest-side 断言 QEMU argv 含**两块盘**的 blockdev/device。
3. **admission 拒绝**：VM `On` 时 apply 改 memoryMiB → 期望 **4xx 拒绝**，错误信息含 `powerState=Off` 语义。

可选补充：减删硬件（移除 ref）作为增硬件逆操作，断言 argv 盘数减少。

### 单元测试最小覆盖

- **admission 门禁 1**：`On + cold-mutable 变更 → 拒绝`、`Off + cold-mutable 变更 → 接受`、
  `纯电源改（无 cold-mutable 变更）→ 接受`、`无变更 → 接受`。
- **vmm `Redefine`**：覆写 Spec + 重派生 argv、不碰 Intended/进程、幂等（重复同 spec 产生同 json）、
  `ErrNotFound`（vm.json 不存在）、`ErrInvalidRequest`（不支持 arch）。
- **控制器 drift 检测**：`live.Spec == desired → 无 Redefine`、`!= → Redefine`、
  `依赖未就绪 → requeue 不 Redefine`、`Redefine 后状态正常收敛`。

## 8. 作用域边界

**本刀做**：VM 的 memoryMiB/vcpus/volumeRefs/nicRefs 冷配置改（标量 + 增删硬件），vmm `Redefine`
原语，admission cold-mutable 门禁，控制器 drift 检测。

**本刀不做**：
- 热添加（hot-plug）：不支持。热添加能力标识作为前向钩子留待未来——未来支持热添加的字段可绕过门禁 1。
- 换池（poolRef 变更）：immutable（刀 3 已挡），归未来 Job spec（冷热迁移 / 存储迁移）。
- Volume.sizeBytes 冷扩容：刀 5 已实现，不重复。
- 其它资源（Network/StoragePool/Image）的 update：本刀只动 VM 配置；这些资源的 immutable
  分级刀 3 已落地，无 cold-mutable 字段（overview 标"本期无"）。
