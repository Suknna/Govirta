# 刀 6：冷配置更改实现计划

## Goal

让用户能修改已存在 VM 的 memoryMiB/vcpus/volumeRefs/nicRefs（改标量 + 增删硬件）：vmm 新增 `Redefine` 原语覆写 vm.json 配置权威并重派生 argv；apiserver admission 在 VM 非 Off 时拒绝 cold-mutable 变更；node 控制器在冷态检测 spec drift 并调 Redefine。下次开机以新配置拉起。

## Context

- 设计：`docs/superpowers/specs/2026-06-11-knife6-cold-config-change-design.md`
- 前置地基（均在 main）：vmm 配置权威整顿（`SpecSummary` 单一权威 + `deriveBuilder` 确定性派生）、电源模型修复（`powerState=Off` 真正停机判定）、刀 3 Validating Admission（`FieldPolicyValidator`）、刀 5 `vmIsCold` 门禁模式。

### 关键真实签名（已核实）

- `vmm.Create`（`internal/vmm/service.go:68-105`）：`deriveBuilder(req.Spec, s.env)` → `injectFacilityFlags(builder, paths)` → 落 `persistedState{Spec,Argv,Intended,Paths,CreatedAt,UpdatedAt}`。Redefine 镜像它但用 `loadState`。
- `loadState`/`writeState`（`internal/vmm/store.go`）：加载/原子落盘 vm.json，`writeState` 自动更新 `UpdatedAt`。
- `FieldPolicyValidator.Validate`（`internal/controlplane/apiserver/admission/fields.go:25`）：已 `req.Operation != OperationUpdate → return nil`；`validateVM`（:115）已有 arch immutable 检查。
- VM 控制器（`internal/node/controllers/vm.go`，580 行）：`VMRunner` 接口（:29-36）、`reconcileMissingVM` 的 SpecSummary 组装（:174-182）、`reconcileExistingVMOff` 冷态分支（:301-306 的 `obs.Observed != ObservedPowerStateOn → patchLivePowerStatus`）。
- `SpecSummary`（`internal/vmm/state.go:51-59`）：含切片 `Disks []DiskSpec`、`NICs []NICSpec` → 比对必须 `reflect.DeepEqual`，不能 `==`。

### 行数预判（AGENTS 第十八章）

- `internal/vmm/service.go`（~110 行 Create+Delete 区）：Redefine 放**新文件 `redefine.go`**，不增长 service.go。
- `internal/controlplane/apiserver/admission/fields.go`（~253 行）：加 cold-mutable 门禁约 +30 行，安全。
- `internal/node/controllers/vm.go`（580 行）：drift 检测 + SpecSummary 组装 helper 放**新文件 `vm_config.go`**，避免逼近 800 硬上限。

## Tasks

### Task 1 — vmm `Redefine` 原语 + VMRunner 接口扩展

**What**：新增 `Redefine(ctx, uuid, spec) (VM, error)`：覆写 vm.json 的 Spec、据新 spec 重派生 argv、覆写 Argv、原子落盘；不碰 Intended/Paths/CreatedAt/进程。

**Where**：
- 新文件 `internal/vmm/redefine.go`：实现 `Redefine`。
- `internal/node/controllers/vm.go`：`VMRunner` 接口（:29-36）加 `Redefine(ctx, uuid string, spec vmm.SpecSummary) (vmm.VM, error)`。
- fake VMRunner（`internal/node/controllers/` 测试 fake，搜 `vmm.VM` / `VMRunner` 实现）：加 Redefine。

**How**：
- 镜像 `Create`（service.go:68-105）但：(1) 用 `s.loadState(ctx, uuid)` 替代重复检测（不存在 → `ErrNotFound`）；(2) `deriveBuilder(spec, s.env)` + `injectFacilityFlags(builder, paths)` 重派生 argv（同 Create 路径，arch 校验复用，不支持 arch → `ErrInvalidRequest`）；(3) `st.Spec = spec`、`st.Argv = argv`，保 `st.Intended`/`st.UUID`/`st.Paths`/`st.CreatedAt` 不变；(4) `writeState(ctx, st)`；(5) 返回 `statusFrom(ctx, st)`。
- 纯磁盘操作：不探测进程、不校验运行态（冷态门禁归控制器）。

**Verify**：`go test ./internal/vmm/...`。单测：覆写 Spec+重派生 argv、不碰 Intended/进程、幂等（同 spec 重复 Redefine 产生同 json）、`ErrNotFound`（vm.json 不存在）、`ErrInvalidRequest`（不支持 arch）。

### Task 2 — apiserver admission 门禁 1（cold-mutable 检测）

**What**：`validateVM` 在 arch immutable 检查后追加 cold-mutable 门禁：若 new spec 存在 cold-mutable 字段变更（memoryMiB/vcpus/volumeRefs/nicRefs 任一 != old）且 `new.spec.powerState != Off` → `Reject(ReasonBadRequest, "cold config change requires powerState=Off")`。

**Where**：`internal/controlplane/apiserver/admission/fields.go` 的 `validateVM`（:115-120）。

**How**：
- 比对函数：标量 `memoryMiB`/`vcpus` 直接 `!=`；`volumeRefs`/`nicRefs` 用 `reflect.DeepEqual`（切片，顺序敏感——成员或顺序变都算 drift，与控制器 drift 检测口径一致）。
- 检测到任一 cold-mutable 变更后，**只看 `newVM.Spec.PowerState != vmv1.PowerStateOff`** 即拒绝。不读 status、不看 live。
- 错误用 `Reject(v.Name(), ReasonBadRequest, fmt.Errorf(...))`（确认 error.go 的 ReasonBadRequest 常量名）。

**Verify**：`go test ./internal/controlplane/apiserver/admission/...`。单测（加进 fields_test.go）：`On + memoryMiB 变更 → 拒绝`、`Off + memoryMiB 变更 → 接受`、`Off + volumeRefs 增加 → 接受`、`纯电源改 On→Off 无 cold-mutable 变更 → 接受`、`无变更 → 接受`、`arch 变更仍 → 拒绝`（既有行为不回归）。

### Task 3 — node 控制器 drift 检测

**What**：在 `reconcileExistingVMOff` 冷态分支（进程死，当前直接 `patchLivePowerStatus`）插入 drift 检测：组装 desired SpecSummary，对比 live.Spec，不等则 Redefine。

**Where**：
- 新文件 `internal/node/controllers/vm_config.go`：抽 `buildSpecSummary(obj, disks, nics, cpu) vmm.SpecSummary` helper（复用 reconcileMissingVM:174-182 的组装），`specDrifted(live, desired) bool`（`reflect.DeepEqual`），`reconcileConfigDrift` 编排。
- `internal/node/controllers/vm.go`：`reconcileMissingVM` 的组装改用 `buildSpecSummary`（消除重复）；`reconcileExistingVMOff` 冷态分支（:301-306）在 `patchLivePowerStatus` 前调 drift 检测。

**How**：
- 冷态分支新流程：(1) `gatherDependencies(ctx, obj)` → disks/nics；依赖未就绪 → `RequeueAfter(vmDependencyRequeueDelay)` 不 Redefine；传输错误 → requeue。(2) `desired := buildSpecSummary(...)`。(3) `if specDrifted(live.Spec, desired)`：调 `c.vmm.Redefine(ctx, obj.UID, desired)`——成功 → 结构化日志（kind/key/drift fields）记 drift 收敛，继续 `patchLivePowerStatus`；`ErrInvalidRequest` → 永久失败 patch 不 requeue；其它错误 → 失败 patch + requeue。(4) 无 drift → 直接 `patchLivePowerStatus`（原行为）。
- drift 检测**只在 powerState=Off 冷态分支**；On 分支不动。
- Defined/Stopped/Failed 走同一冷态分支（`observePower` 把三者都算 Observed=Off），无需特判。

**Verify**：`go test ./internal/node/controllers/...`。单测：`live.Spec == desired → 不调 Redefine`、`memoryMiB 不同 → 调 Redefine`、`volumeRefs 增加 → 调 Redefine`、`依赖未就绪 → requeue 且不 Redefine`、`Redefine 后状态正常收敛`、`Redefine 返回 ErrInvalidRequest → 永久失败不 requeue`。

### Task 4 — e2e 冷配置改场景

**What**：在 `TestDistributedSpineClosure` 电源序列之后、teardown 之前，插入冷配置改三场景（标量改 + 增硬件 + admission 拒绝）。

**Where**：`test/e2e/closure_test.go`（复用 `applyAndVerify`/`deleteAndVerify` lifecycle helper + guest `pgrep -a qemu-system` argv 探测）；`test/e2e/manifests/` 加所需 manifest 变体。

**How**：
- **场景 1 标量改**：VM 已 `Off`（前序电源场景停机态）→ apply VM manifest（memoryMiB 翻倍 + powerState=Off）→ 等 status 收敛 → apply powerState=On → guest-side 断言运行的 QEMU argv 含新 `-m <新值>`（复用 `AssertRunningQEMUArgvHasMAC` 同款 `pgrep -a qemu-system` + grep 模式，新增 `AssertRunningQEMUArgvHasMemory` 或等价断言）。
- **场景 2 增硬件**：apply 新 Volume（等 Ready）→ VM Off → apply VM（volumeRefs 追加该 volume + Off）→ On → guest-side 断言 QEMU argv 含两块盘 blockdev/device。
- **场景 3 admission 拒绝**：VM On 时 apply 改 memoryMiB → 期望 govirtctl 非零退出 + 错误信息含 `powerState=Off` 语义（4xx）。
- 可选：减删硬件（移除 ref）断言 argv 盘数减少。

**Verify**：`scripts/e2e.sh full`（真实三节点）。三场景全命中 + 既有序列不回归。

### Task 5 — 全量验证

**What**：跑完整门禁。

**How / Verify**：
- `scripts/verify.sh`（gofmt + build all + 全单测）= 0。
- `go test -race ./...` = 0。
- `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./... && go vet ./...` = 0。
- `scripts/e2e.sh full` PASS（含 Task 4 三场景）。
- 跨语言路径/常量漂移守卫照常。

## Verification（整体）

冷配置改端到端闭环：VM Off → apply 改 memoryMiB/加盘 → admission 接受（powerState=Off）→ node 冷态检测 drift → Redefine 重派生 argv 落盘 → apply On → 新 argv 拉起 → guest-side QEMU argv 实证新配置。On 时改配置被 admission 4xx 拒绝。全量门禁（verify + race + cross-build + e2e full）绿。

## 作用域边界

本刀只做 VM memoryMiB/vcpus/volumeRefs/nicRefs 冷配置改。不做热添加（前向钩子留待未来）、换池（Job spec）、Volume 冷扩容（刀 5 已做）、其它资源 update（无 cold-mutable 字段）。
