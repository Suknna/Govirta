# 电源模型修复 — 实现计划

**设计**：`docs/superpowers/specs/2026-06-11-power-model-fix-design.md`
**定位**：刀 6 冷配置改的前置独立修复（与 vmm 配置权威整顿同性质）

执行前先读设计文档获取完整背景与决策依据；本计划是执行序列，不重述设计。

## 背景

把 VM 电源模型从三值 `PowerState = On|Shutdown|Off`（动作冒充状态）修正为二维：
`spec.powerState: On|Off`（电源状态）+ `spec.powerOffMode: Acpi|Force`（到达 Off 的方式）。
硬切删除 `Shutdown`，无兼容别名。

## 行数预判（AGENTS 第十八章）

- `pkg/apis/vm/v1alpha1/types.go` 174 → ~200（加 PowerOffMode 枚举 + 字段 + 条件必填）
- `internal/node/controllers/vm.go` 577（删 reconcileExistingVMShutdown 后净变化小）
- `internal/controlplane/apiserver/admission/apply.go` 217（删 Shutdown 分支，净减）
- `test/e2e/closure_test.go` 671（重写 power 序列 helper，净变化小）

无文件逼近 800 硬上限，无需拆分设计。

## 任务序列

### Task 1：API 契约 — PowerState 二值 + PowerOffMode 枚举 + 条件必填

**Goal:** `pkg/apis/vm/v1alpha1` 落地二维电源模型 + 单对象内部一致性校验。

**Files:**
- `pkg/apis/vm/v1alpha1/types.go` — 删 `PowerStateShutdown`；`PowerState.Valid()` 改只认 On/Off；
  新增 `PowerOffMode` 类型 + `PowerOffModeAcpi`/`PowerOffModeForce` 常量 + `Valid()`；
  `VMSpec` 加 `PowerOffMode` 字段（`json:"powerOffMode,omitempty"`）；
  `VMSpec.Validate()` 加条件必填（设计 §4：On+非空拒绝、Off+空拒绝、Off+非法值拒绝），
  并修正 powerState 错误消息（去掉 "Shutdown"）。
- `pkg/apis/vm/v1alpha1/types_test.go` — 迁移：删 `PowerStateShutdown` 用例（types_test.go:70）；
  新增 `PowerOffMode.Valid()` 测试；新增条件必填测试（On+非空、Off+空、Off+非法、On+空 OK、Off+Acpi OK、Off+Force OK）。

**Verification:**
- [ ] `go test ./pkg/apis/vm/...` 通过
- [ ] `PowerState.Valid()` 对 `"Shutdown"` 返回 false
- [ ] 条件必填六组用例全部符合设计 §4

### Task 2：admission — 清理 VMPowerStateValidator 的 Shutdown 分支，保留 nodeName

**Goal:** admission 层删除已死的 create-拒绝-Shutdown 逻辑，保留 nodeName immutable 检查。

**Files:**
- `internal/controlplane/apiserver/admission/apply.go` — `VMPowerStateValidator.Validate`
  删除 `OperationCreate && PowerState == Shutdown` 分支（:139-141）；保留 nodeName immutable
  逻辑（:142-155）。条件必填已由 `VMSpec.Validate()`（经 SpecValidator）覆盖，此 validator
  不再做 powerState 值校验。
- `internal/controlplane/apiserver/admission/apply_test.go` — 删 `TestVMPowerStateValidatorRejectsCreateShutdown`
  和 `TestVMPowerStateValidatorAllowsUpdateShutdown`（:57-78，Shutdown 值已不存在）；
  保留 nodeName mismatch 测试；新增「create powerState=Off 缺 powerOffMode 经 SpecValidator 拒绝」
  「create On+powerOffMode 非空经 SpecValidator 拒绝」的 admission 链路测试（验证条件必填在 apply 入口生效）。
- `internal/controlplane/apiserver/admission/fields_test.go` — :46 旧 Shutdown fixture 迁移到 Off+Acpi。

**Verification:**
- [ ] `go test ./internal/controlplane/apiserver/admission/...` 通过
- [ ] `VMPowerStateValidator` 不再引用 `PowerStateShutdown`
- [ ] nodeName immutable 测试仍通过

### Task 3：VM 控制器收敛矩阵重构

**Goal:** 控制器分发从三值改两值，`reconcileExistingVMOff` 按 `powerOffMode` 分流 Stop/Kill。

**Files:**
- `internal/node/controllers/vm.go` —
  `reconcileExistingVM`（:266-281）分发改两值（On/Off + default）；
  删除 `reconcileExistingVMShutdown`（:303-320）；
  重写 `reconcileExistingVMOff`（:322）：进程活时按 `obj.Spec.PowerOffMode` 选
  `vmm.Stop`（Acpi→ShutdownRequested）/`vmm.Kill`（Force→PoweringOff）+ RequeueAfter，
  进程死时 patch Off/None；
  `reconcileMissingVM`（:158-171）删除「create 时 Shutdown 拒绝」分支（条件必填已上移到 admission/Validate，
  且 Shutdown 值已不存在）。
- `internal/node/controllers/vm_power.go` — `vmPowerStatus`（:74 case Shutdown）改为按
  `(powerState, powerOffMode)` 产出 transition：Off+Acpi 未收敛→ShutdownRequested，
  Off+Force 未收敛→PoweringOff（与设计 §5 收敛矩阵一致）。
- `internal/node/controllers/vm_test.go` — :493/516/563/678 Shutdown fixture 迁移；
  新增 Off+Acpi（活→Stop+ShutdownRequested）、Off+Force（活→Kill+PoweringOff）、
  Off+任意 mode（死→no-op Off/None）的收敛测试。
- `internal/node/controllers/vm_power_test.go` — 整组 Shutdown 用例（:25-30,83）重写为
  Off+Acpi / Off+Force 的 transition 断言。

**Verification:**
- [ ] `go test ./internal/node/controllers/...` 通过
- [ ] `reconcileExistingVMShutdown` 已删除，无引用
- [ ] Off+Acpi 活→Stop、Off+Force 活→Kill、Off 死→no-op 三路径有测试覆盖

### Task 4：apiserver handler 测试 + e2e manifest fixture 迁移

**Goal:** 穷尽迁移所有 `Shutdown` 引用点到 Off+Acpi/Force，补全 powerOffMode。

**Files:**
- `internal/controlplane/apiserver/handler_apply_test.go` — 11 处 `PowerStateShutdown`
  （:569/615/622-623/706/716-717/740/763/840/864-865/877）迁移到 Off+Acpi（update 场景）
  或相应语义；create+Shutdown 场景改为「create+Off 缺 powerOffMode 拒绝」等价校验。
- e2e VM manifest（`test/e2e` 下 govirtctl apply 的 VM manifest 文件）— 补 `powerOffMode`，
  凡声明 Off 的补 Acpi/Force。
- 全局兜底扫描：`grep -rn 'PowerStateShutdown\|powerState.*Shutdown' --include='*.go'`
  确认零残留（QMP system_powerdown / PowerTransitionShutdownRequested / qemu 层不在迁移范围）。

**Verification:**
- [ ] `grep -rn PowerStateShutdown` 零结果
- [ ] `go build ./...` + `go vet ./...` 通过
- [ ] 所有 VM manifest 含显式 powerState + 条件正确的 powerOffMode

### Task 5：e2e 电源场景重写

**Goal:** closure_test 电源序列覆盖设计 §7 的六场景（含 Acpi 真收敛 + Force 强制断电 + 三 admission 拒绝）。

**Files:**
- `test/e2e/closure_test.go` —
  `applyVMVariant`（:173）签名加 powerOffMode 参数；
  删 `waitVMShutdownRequestedOrOff`（:452），新增 `waitVMPowerConverged`（轮询 ObservedPowerState=Off，60s）；
  `expectShutdownCreateRejected`（:183）改为三个 admission 拒绝断言（create Shutdown→400、
  create On+powerOffMode→400、create Off 缺 powerOffMode→400）；
  主序列改为设计 §7：Off+Acpi create → On → Off+Acpi（验真收敛）→ On → Off+Force（验无 QEMU 进程）。
  Force 无进程检查复用 e2e Guest handle 的 `AssertNoQEMUProcess`（若存在）或新增。

**Verification:**
- [ ] `go vet -tags e2e ./test/e2e/...` 通过
- [ ] 场景结构匹配设计 §7（六场景齐全）

### Task 6：全量验证 + 真实三节点 e2e 闭环

**Goal:** 证明电源二维模型端到端工作，Acpi 真收敛 + Force 强制断电都到达 ObservedPowerState=Off。

**Steps:**
1. `scripts/verify.sh`（gofmt + 单测 + 主服务 build）
2. `go test -race ./...`
3. `GOOS=linux GOARCH=arm64 go build ./...` + cross vet
4. `scripts/e2e.sh full`（真实三节点：etcd 容器 + macOS govirtad + Lima KVM govirtlet）

**Verification:**
- [ ] verify.sh exit 0
- [ ] race exit 0
- [ ] linux cross build/vet exit 0
- [ ] e2e exit 0，VM 经 Off→On→Off(Acpi)→On→Off(Force) 全序列收敛
- [ ] 场景 3（Acpi）：ObservedPowerState 真收敛到 Off（CirrOS 0.6.2 ACPI 实测权威证据；
      若不收敛按设计 §7 `[待实测]` 判断 arch 细节 vs guest 配置）
- [ ] 场景 5（Force）：无 QEMU 进程，ObservedPowerState=Off
- [ ] 三 admission 拒绝场景返回 400

## 整体验证

全部 6 任务完成后，电源模型从「动作冒充状态」修正为干净的二维模型，`Shutdown` 硬切删除零残留，
Acpi/Force 两种到达 Off 的方式都经真实 e2e 验证收敛。这为刀 6 冷配置改提供「VM 真正 Off」的
正确判定地基。
