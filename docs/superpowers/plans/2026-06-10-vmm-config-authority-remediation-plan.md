# vmm 配置权威整顿实现计划（json 漂移根治 + MAC 透传修复）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `vm.json` 的 argv 永远从 SpecSummary 确定性派生（消除 argv↔Spec 漂移），并修复 guest 网卡 MAC 未透传到 qemu argv 的缺陷。

**Architecture:** SpecSummary 升级为完整物理配置权威（含 CPUModel + 每盘 path + 每卡 {TapName, MAC}）。argv 构造逻辑从 VM 控制器 `buildVM` 下沉进 vmm，vmm 成为唯一「SpecSummary → qemu.Builder → argv」翻译者。CreateRequest 去掉 Builder，只收 SpecSummary。控制器解析逻辑 ref→物理配置，vmm 纯翻译。

**Tech Stack:** Go 1.26、`internal/vmm`、`pkg/virt/qemu`(Builder/blockdev/netdev/cpu/machine)、`internal/node/controllers`。

**Spec:** `docs/superpowers/specs/2026-06-10-vmm-config-authority-remediation-design.md`

---

### Task 1: SpecSummary 升级为完整物理配置描述

**Files:**
- Modify: `internal/vmm/vm.go:51-58` (SpecSummary struct)
- Test: `internal/vmm/store_test.go` (round-trip 已有，验证新字段序列化)

- [ ] **Step 1: 确认目标与验收**

Goal: SpecSummary 承载 argv 派生所需全部字段——`Arch`/`VCPUs`/`MemoryMiB`/`CPUModel`/`Disks []DiskSpec`/`NICs []NICSpec`；新增 `DiskSpec{Path}` 与 `NICSpec{TapName, MAC}`。
验收证据：`go build ./internal/vmm/...` 通过；`go test ./internal/vmm/ -run TestEncodeDecodeStateRoundTrip` 通过（含新字段）。

- [ ] **Step 2: 替换 SpecSummary 定义**

把 `internal/vmm/vm.go` 的 SpecSummary 替换为：

```go
// SpecSummary 是落盘的 VM 配置权威描述（spec §5）。argv 由它确定性派生，
// 它是 json→qemu flag 单向映射的唯一来源（无第二份 argv 配置）。
type SpecSummary struct {
	Name      string     `json:"name"`
	Arch      string     `json:"arch"`
	VCPUs     int        `json:"vcpus"`
	MemoryMiB int        `json:"memory_mib"`
	CPUModel  string     `json:"cpu_model"`
	Disks     []DiskSpec `json:"disks"`
	NICs      []NICSpec  `json:"nics"`
}

// DiskSpec 是一块已解析的物理盘配置。Path 由控制器从 Volume.status.VolumePath
// 解析；NodeName/Frontend 是 vmm 派生时的内部约定常量，不属于配置描述。
type DiskSpec struct {
	Path string `json:"path"`
}

// NICSpec 是一张已解析的物理网卡配置。TapName 来自 NIC.status.TapName，
// MAC 是控制面分配的 NIC.spec.MAC，原样贯穿到 qemu argv（memory 698）。
type NICSpec struct {
	TapName string `json:"tap_name"`
	MAC     string `json:"mac"`
}
```

- [ ] **Step 3: 更新 store round-trip 测试夹具**

`internal/vmm/store_test.go` 里构造 SpecSummary 的测试夹具改用新字段（`Disks: []DiskSpec{{Path: "/d.qcow2"}}`, `NICs: []NICSpec{{TapName: "gvtap0", MAC: "02:00:00:00:00:01"}}`, `CPUModel: "host"`），断言 encode→decode 后逐字段相等。

- [ ] **Step 4: 编译检查（预期失败处定位调用点）**

Run: `cd /Users/suknna/code/Govirta && go build ./... 2>&1 | head -40`
Expected: 报错指向 `service.go` Create、`controllers/vm.go` 构造 SpecSummary 处、`service_test.go` 夹具——这些是后续 Task 修复点。本 Task 只确保 SpecSummary 自身定义编译通过。

- [ ] **Step 5: 仅验证 vmm 包类型层（隔离）**

Run: `cd /Users/suknna/code/Govirta && go vet ./internal/vmm/ 2>&1 | head -20`
Expected: 仅 service.go Create 因仍引用旧字段/Builder 报错（Task 3 修），SpecSummary/DiskSpec/NICSpec 定义本身无误。

- [ ] **Step 6: Commit**

```bash
git add internal/vmm/vm.go internal/vmm/store_test.go
git commit -m "feat(vmm): upgrade SpecSummary to full physical config authority"
```

---

### Task 2: argv 派生下沉进 vmm

**Files:**
- Create: `internal/vmm/argv.go`
- Create: `internal/vmm/argv_test.go`

- [ ] **Step 1: 确认目标与验收**

Goal: vmm 内部 `deriveBuilder(spec SpecSummary) (*qemu.Builder, error)` 据 SpecSummary 确定性构造 base builder（arch/machine/cpu/smp/mem/disks/nics 含真实 MAC）；`mapArch` 随之下沉。
验收证据：`go test ./internal/vmm/ -run TestDeriveBuilder -v` 通过；黄金 argv 断言含 `mac=<真实值>`、disk path、smp、memory。

- [ ] **Step 2: 写 argv.go**

```go
package vmm

import (
	"fmt"

	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/blockdev"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
	"github.com/suknna/govirta/pkg/virt/qemu/device"
	"github.com/suknna/govirta/pkg/virt/qemu/machine"
	"github.com/suknna/govirta/pkg/virt/qemu/netdev"
	"github.com/suknna/govirta/pkg/virt/qemu/qflag"
)

// deriveBuilder 据 SpecSummary 确定性构造「配置好但未 Build」的 qemu.Builder。
// 这是 json→qemu flag 单向映射的唯一实现：相同 SpecSummary 永远产生相同 builder。
// 设施 flag（pidfile/QMP/serial/vnc/daemonize）由 injectFacilityFlags 在其后注入。
//
// 注意 API 形态（实际核对 pkg/virt/qemu）：流式构造用 qemu.NewVM(arch) +
// .Name()/.Machine()/.CPU()/.SMP()/.Memory()；磁盘/网卡用 AddBlockdev+AddDevice /
// AddNetdev+AddDevice 成对添加；MAC 落在 device.VirtioNetPCI.Mac（netdev.Tap 无 MAC 字段）。
func deriveBuilder(spec SpecSummary) (*qemu.Builder, error) {
	arch, profile, err := mapArch(spec.Arch)
	if err != nil {
		return nil, err
	}

	b := qemu.NewVM(arch).
		Name(spec.Name).
		Machine(profile).
		CPU(cpu.Model(spec.CPUModel)).
		SMP(qemu.SMP{CPUs: spec.VCPUs, Cores: spec.VCPUs, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(spec.MemoryMiB))

	for i, disk := range spec.Disks {
		node := fmt.Sprintf("disk%d", i)
		b = b.AddBlockdev(blockdev.Qcow2{
			NodeName: node,
			File:     blockdev.FileProtocol{Filename: disk.Path},
			Cache:    blockdev.Cache{Direct: qemu.Off},
			AIO:      blockdev.AIOThreads,
		}).AddDevice(device.VirtioBlkPCI{
			ID:    fmt.Sprintf("blk%d", i),
			Drive: blockdev.Ref(node),
		})
	}

	for i, nic := range spec.NICs {
		netID := fmt.Sprintf("net%d", i)
		b = b.AddNetdev(netdev.Tap{
			ID:         netID,
			IfName:     nic.TapName,
			Script:     netdev.ScriptNo,
			DownScript: netdev.ScriptNo,
			Vhost:      qemu.On,
		}).AddDevice(device.VirtioNetPCI{
			ID:      fmt.Sprintf("nic%d", i),
			Netdev:  netdev.Ref(netID),
			Mac:     device.MAC(nic.MAC), // 控制面分配的 MAC 原样贯穿（memory 698）
			RomFile: qflag.String(""),    // 显式禁用 PXE option ROM（本项目不支持 PXE 引导）
		})
	}

	return b, nil
}

// mapArch maps an arch string to typed qemu.Arch + KVM machine profile.
// 未知 arch 是永久配置错误（项目铁律：禁止裸 string 推断）。从控制器下沉而来。
func mapArch(arch string) (qemu.Arch, machine.Profile, error) {
	switch arch {
	case "x86_64":
		return qemu.ArchX86_64, machine.ProfileX86_64Q35KVM, nil
	case "aarch64":
		return qemu.ArchAArch64, machine.ProfileAArch64VirtKVM, nil
	default:
		return "", "", fmt.Errorf("%w: unsupported arch %q", ErrInvalidRequest, arch)
	}
}
```

- [ ] **Step 3: 写 argv_test.go（黄金派生 + MAC 填入）**

```go
package vmm

import (
	"strings"
	"testing"
)

func TestDeriveBuilderFillsMACAndDisks(t *testing.T) {
	spec := SpecSummary{
		Name:      "vm-derive",
		Arch:      "aarch64",
		VCPUs:     2,
		MemoryMiB: 512,
		CPUModel:  "host",
		Disks:     []DiskSpec{{Path: "/var/lib/govirta/d0.qcow2"}},
		NICs:      []NICSpec{{TapName: "gvtap0", MAC: "02:00:00:00:00:01"}},
	}
	b, err := deriveBuilder(spec)
	if err != nil {
		t.Fatalf("deriveBuilder: %v", err)
	}
	vm, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	argv := strings.Join(vm.Argv(), " ")
	if !strings.Contains(argv, "mac=02:00:00:00:00:01") {
		t.Fatalf("argv must carry real MAC, got: %s", argv)
	}
	if !strings.Contains(argv, "/var/lib/govirta/d0.qcow2") {
		t.Fatalf("argv must carry disk path, got: %s", argv)
	}
	if !strings.Contains(argv, "512") {
		t.Fatalf("argv must carry memory, got: %s", argv)
	}
}

func TestDeriveBuilderDeterministic(t *testing.T) {
	spec := SpecSummary{
		Name: "vm-det", Arch: "x86_64", VCPUs: 1, MemoryMiB: 256, CPUModel: "host",
		Disks: []DiskSpec{{Path: "/a.qcow2"}},
		NICs:  []NICSpec{{TapName: "gvtap0", MAC: "02:00:00:00:00:02"}},
	}
	b1, err := deriveBuilder(spec)
	if err != nil {
		t.Fatalf("deriveBuilder 1: %v", err)
	}
	b2, err := deriveBuilder(spec)
	if err != nil {
		t.Fatalf("deriveBuilder 2: %v", err)
	}
	vm1, _ := b1.Build()
	vm2, _ := b2.Build()
	a1, a2 := strings.Join(vm1.Argv(), " "), strings.Join(vm2.Argv(), " ")
	if a1 != a2 {
		t.Fatalf("derivation must be deterministic:\n%s\n%s", a1, a2)
	}
}

func TestMapArchRejectsUnknown(t *testing.T) {
	if _, _, err := mapArch("riscv64"); err == nil {
		t.Fatal("unknown arch must error")
	}
	for _, a := range []string{"x86_64", "aarch64"} {
		if _, _, err := mapArch(a); err != nil {
			t.Fatalf("arch %q must map: %v", a, err)
		}
	}
}
```

- [ ] **Step 4: 运行 vmm argv 测试**

Run: `cd /Users/suknna/code/Govirta && go test ./internal/vmm/ -run 'TestDeriveBuilder|TestMapArch' -v`
Expected: PASS（3 个测试）

- [ ] **Step 5: 若失败修实现**

Builder setter 名不符（如 `WithBlockDevice`/`WithNetDevice`/`WithCPU`/`WithSMP`/`WithMemoryMiB`/`WithMachine`）→ 核对 `pkg/virt/qemu` 真实方法名并修正。`netdev.Tap` 字段（`ID`/`IfName`/`Frontend`/`MAC`/`RomFile`/`VHost`）→ 核对 `pkg/virt/qemu/netdev` 真实字段。

- [ ] **Step 6: Commit**

```bash
git add internal/vmm/argv.go internal/vmm/argv_test.go
git commit -m "feat(vmm): sink argv derivation into vmm as deterministic SpecSummary->builder"
```

---

### Task 3: CreateRequest 去 Builder，Create 据 Spec 派生 argv

**Files:**
- Modify: `internal/vmm/vm.go:92-98` (CreateRequest struct)
- Modify: `internal/vmm/service.go:49-85` (Create)
- Modify: `internal/vmm/service_test.go` (夹具去 Builder)

- [ ] **Step 1: 确认目标与验收**

Goal: `CreateRequest` 只含 `UUID` + `Spec SpecSummary`（去掉 `Builder`）；`Create` 内部 `deriveBuilder(spec)` → `injectFacilityFlags` → 落盘，使 `persistedState.Argv` 必然 == `Spec` 的派生结果。
验收证据：`go test ./internal/vmm/ -run TestCreate -v` 通过；新增「Create 落盘 argv 与 Spec 派生一致」断言通过。

- [ ] **Step 2: 替换 CreateRequest**

`internal/vmm/vm.go`：

```go
// CreateRequest 是 Create 的输入。Spec 是唯一配置权威；vmm 据它确定性派生 argv，
// 不再接收外部 Builder（杜绝 argv↔Spec 漂移）。
type CreateRequest struct {
	UUID string      // 调用方显式提供，vmm 不生成
	Spec SpecSummary // 唯一配置权威
}
```

- [ ] **Step 3: 改 Create**

`internal/vmm/service.go` 的 Create：把 `req.Builder == nil` 校验改为派生路径。替换 builder 取得方式：

```go
	if req.UUID == "" {
		return VM{}, fmt.Errorf("%w: uuid is required", ErrInvalidRequest)
	}
	paths := runtimePathsFor(s.runtimeRoot, req.UUID)

	// 重复检测：vm.json 已存在即拒绝（不覆盖）。
	if _, err := s.proc.ReadState(ctx, paths.StateFile); err == nil {
		return VM{}, fmt.Errorf("%w: %s", ErrAlreadyExists, req.UUID)
	} else if !errors.Is(err, proc.ErrStateNotFound) {
		return VM{}, fmt.Errorf("vmm: probe existing state for %s: %w", req.UUID, err)
	}

	builder, err := deriveBuilder(req.Spec)
	if err != nil {
		return VM{}, err
	}
	argv, err := injectFacilityFlags(builder, paths)
	if err != nil {
		return VM{}, err
	}
```

（其余 persistedState 落盘逻辑不变，`Spec: req.Spec` 保持。）

- [ ] **Step 4: 迁移 service_test.go 夹具**

`internal/vmm/service_test.go` 的 `newCreateRequest` 去掉 Builder 构造，改为完整 SpecSummary：

```go
func newCreateRequest(uuid string) CreateRequest {
	return CreateRequest{
		UUID: uuid,
		Spec: SpecSummary{
			Name: "vm-test", Arch: "aarch64", VCPUs: 1, MemoryMiB: 256, CPUModel: "host",
			Disks: []DiskSpec{{Path: "/d.qcow2"}},
			NICs:  []NICSpec{{TapName: "gvtap0", MAC: "02:00:00:00:00:01"}},
		},
	}
}
```

- [ ] **Step 5: 新增「Create 落盘 argv 与 Spec 派生一致」测试**

加入 `internal/vmm/service_test.go`：

```go
func TestCreatePersistsArgvMatchingSpecDerivation(t *testing.T) {
	svc := newTestService(t)
	req := newCreateRequest("vm-derive")
	if _, err := svc.Create(context.Background(), req); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 独立派生一份 argv，证明落盘 argv == Spec 的确定性派生（无漂移）。
	b, err := deriveBuilder(req.Spec)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	paths := runtimePathsFor(svc.runtimeRoot, req.UUID)
	want, err := injectFacilityFlags(b, paths)
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	st, err := svc.loadState(context.Background(), req.UUID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !sameArgv(st.Argv, want) {
		t.Fatalf("persisted argv drifted from Spec derivation:\nstored=%v\nwant =%v", st.Argv, want)
	}
}
```

（复用 `service_test.go` 已有的 `sameArgv` helper；若不存在则用 `lifecycle_test.go` 的 `sameArgv`——核对实际位置，必要时在 service_test.go 内联一个切片相等比较。）

- [ ] **Step 6: 运行 vmm 全包测试**

Run: `cd /Users/suknna/code/Govirta && go test ./internal/vmm/... -count=1`
Expected: PASS

- [ ] **Step 7: 若失败修实现/陈旧测试**

`sameArgv` 不在 service_test.go → 内联切片比较或从 lifecycle_test.go 复用（同包可直接调）。其它 Create 测试因夹具变更失败 → 迁移到新 newCreateRequest 形态。

- [ ] **Step 8: Commit**

```bash
git add internal/vmm/vm.go internal/vmm/service.go internal/vmm/service_test.go
git commit -m "feat(vmm): derive argv from Spec in Create, drop Builder from CreateRequest"
```

---

### Task 4: 控制器解析 MAC + 构造完整 SpecSummary，移除 buildVM/mapArch

**Files:**
- Modify: `internal/node/controllers/vm.go` (gatherDependencies、reconcileMissingVM、移除 buildVM/mapArch)
- Modify: `internal/node/controllers/vm_test.go` (迁移测试)

- [ ] **Step 1: 确认目标与验收**

Goal: 控制器 `gatherDependencies` 返回每张卡的 `{TapName, MAC}`（从 NIC.spec.MAC 解析）和每块盘 path；`reconcileMissingVM` 构造完整 SpecSummary（含 CPUModel + Disks + NICs）传 `vmm.Create`，不再构造 `qemu.Builder`；删除 `buildVM` 和 `mapArch`。
验收证据：`go test ./internal/node/controllers/ -run TestVM -v` 通过；构造的 SpecSummary 含 CPUModel 与每卡 MAC。

- [ ] **Step 2: 改 gatherDependencies 返回结构化盘/卡**

把返回签名从 `(diskPaths, tapNames []string, ready bool, err error)` 改为返回 vmm 配置片段。新签名：

```go
func (c *VMController) gatherDependencies(ctx context.Context, obj vmv1.VM) (disks []vmm.DiskSpec, nics []vmm.NICSpec, ready bool, err error) {
	for _, ref := range obj.Spec.VolumeRefs {
		raw, gerr := c.client.Get(ctx, string(metav1.KindVolume), ref)
		if gerr != nil {
			if errors.Is(gerr, client.ErrNotFound) {
				return nil, nil, false, nil
			}
			return nil, nil, false, fmt.Errorf("read Volume %q: %w", ref, gerr)
		}
		var vol volumev1.Volume
		if uerr := json.Unmarshal(raw, &vol); uerr != nil {
			return nil, nil, false, fmt.Errorf("decode Volume %q: %w", ref, uerr)
		}
		if vol.Status.Phase != volumev1.VolumePhaseReady || vol.Status.VolumePath == "" {
			return nil, nil, false, nil
		}
		disks = append(disks, vmm.DiskSpec{Path: vol.Status.VolumePath})
	}

	for _, ref := range obj.Spec.NICRefs {
		raw, gerr := c.client.Get(ctx, string(metav1.KindNIC), ref)
		if gerr != nil {
			if errors.Is(gerr, client.ErrNotFound) {
				return nil, nil, false, nil
			}
			return nil, nil, false, fmt.Errorf("read NIC %q: %w", ref, gerr)
		}
		var nic nicv1.NIC
		if uerr := json.Unmarshal(raw, &nic); uerr != nil {
			return nil, nil, false, fmt.Errorf("decode NIC %q: %w", ref, uerr)
		}
		if nic.Status.Phase != nicv1.NICPhaseReady || nic.Status.TapName == "" {
			return nil, nil, false, nil
		}
		if nic.Spec.MAC == "" {
			return nil, nil, false, fmt.Errorf("NIC %q ready but spec.MAC empty", ref)
		}
		nics = append(nics, vmm.NICSpec{TapName: nic.Status.TapName, MAC: nic.Spec.MAC})
	}

	return disks, nics, true, nil
}
```

- [ ] **Step 3: 改 reconcileMissingVM 的 builder/Create 段**

把原来 `builder, err := c.buildVM(...)` + `create := vmm.CreateRequest{... Builder: builder, Spec: vmm.SpecSummary{...DiskPaths/TapNames...}}` 替换为直接构造完整 SpecSummary：

```go
	disks, nics, ready, err := c.gatherDependencies(ctx, obj)
	if err != nil {
		return controller.RequeueAfter(vmDependencyRequeueDelay), fmt.Errorf("vm controller: gate dependencies for %q: %w", obj.Name, err)
	}
	if !ready {
		zerolog.Ctx(ctx).Info().Str("key", key).Msg("vm dependencies not ready; delayed requeue")
		return controller.RequeueAfter(vmDependencyRequeueDelay), nil
	}

	spec := vmm.SpecSummary{
		Name:      obj.Name,
		Arch:      obj.Spec.Arch,
		VCPUs:     obj.Spec.VCPUs,
		MemoryMiB: obj.Spec.MemoryMiB,
		CPUModel:  string(c.cpu),
		Disks:     disks,
		NICs:      nics,
	}

	create := vmm.CreateRequest{UUID: obj.UID, Spec: spec}
	created, err := c.vmm.Create(ctx, create)
```

注意：原 `buildVM` 对未知 arch 返回永久失败。现在 arch 校验移入 vmm 的 `deriveBuilder`/`mapArch`，`vmm.Create` 会对未知 arch 返回 `ErrInvalidRequest`。`reconcileMissingVM` 需把 `Create` 的 `ErrInvalidRequest` 当作永久配置失败（不 requeue），其它 Create 错误仍当 transient。调整 Create 错误处理：

```go
	created, err := c.vmm.Create(ctx, create)
	if err != nil && !errors.Is(err, vmm.ErrAlreadyExists) {
		if errors.Is(err, vmm.ErrInvalidRequest) {
			// 永久配置错误（如未知 arch）：requeue 无法修复。
			if perr := c.reportFailure(ctx, obj.Name, obj.Spec.PowerState, err); perr != nil {
				return controller.Done(), fmt.Errorf("vm controller: invalid spec %q and status report failed: %w", obj.Name, errors.Join(err, perr))
			}
			zerolog.Ctx(ctx).Error().Err(err).Str("key", key).Msg("vm spec rejected permanently; not requeuing")
			return controller.Done(), nil
		}
		if perr := c.reportFailure(ctx, obj.Name, obj.Spec.PowerState, err); perr != nil {
			return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: create %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: create %q: %w", obj.Name, err)
	}
	if errors.Is(err, vmm.ErrAlreadyExists) {
		created = vmm.VM{Phase: vmm.PhaseDefined}
	}
```

- [ ] **Step 4: 删除 buildVM 和 mapArch**

删除 `internal/node/controllers/vm.go` 的 `buildVM` 方法（424-456）和 `mapArch` 函数（478-490）。`errUnsupportedArch` sentinel 若仅 buildVM/mapArch 使用也删除（核对：grep `errUnsupportedArch` 无其它引用则删）。移除因此不再使用的 import（`qemu`/`blockdev`/`netdev`/`qflag`/`machine`/`cpu` 中不再被引用的）。

- [ ] **Step 5: 编译 + 清理 import**

Run: `cd /Users/suknna/code/Govirta && go build ./internal/node/... 2>&1 | head -30`
Expected: 报未使用 import → 用 aft_import organize 或手动删 `internal/node/controllers/vm.go` 中 buildVM/mapArch 专用的 import；再次 build 通过。

- [ ] **Step 6: 迁移 vm_test.go 测试夹具**

`internal/node/controllers/vm_test.go` 中：
- 调用 `gatherDependencies` 或断言 diskPaths/tapNames 的测试，迁移到新返回 `[]vmm.DiskSpec`/`[]vmm.NICSpec`。
- fake VMRunner 的 `Create` 捕获的 `CreateRequest` 断言：从断言 `req.Builder != nil` 改为断言 `req.Spec.Disks`/`req.Spec.NICs`/`req.Spec.CPUModel`/`req.Spec.NICs[i].MAC`。
- seed 的 NIC fake 对象需带 `spec.MAC`（否则新增的「ready but MAC empty」校验会触发）。
- 删除任何直接调用 `buildVM`/`mapArch` 的测试，改为通过 `gatherDependencies` + SpecSummary 构造验证；未知 arch 的永久失败测试改为验证 `vmm.Create` 返回 `ErrInvalidRequest` 时控制器 patch failed 且不 requeue（用 fake VMRunner.Create 返回 `vmm.ErrInvalidRequest`）。

- [ ] **Step 7: 运行控制器测试**

Run: `cd /Users/suknna/code/Govirta && go test ./internal/node/controllers/ -run TestVM -count=1 -v 2>&1 | tail -40`
Expected: PASS

- [ ] **Step 8: 若失败修实现/陈旧测试**

测试断言旧 DiskPaths/TapNames 字段 → 迁移到 Disks/NICs。fake NIC 缺 MAC → 补 spec.MAC。未知 arch 测试路径变更 → 改为 fake Create 返回 ErrInvalidRequest 的永久失败断言。

- [ ] **Step 9: Commit**

```bash
git add internal/node/controllers/vm.go internal/node/controllers/vm_test.go
git commit -m "feat(node): resolve NIC MAC and build full SpecSummary, remove controller buildVM"
```

---

### Task 5: 全量验证 + e2e MAC 透传 live 断言

**Files:**
- Modify: `test/e2e/closure_test.go` (VM running 后新增 guest MAC live 断言)
- Possibly Modify: `test/e2e/guest.go` (若需新增 guest MAC 读取 helper)

- [ ] **Step 1: 确认目标与验收**

Goal: 整顿无回归（verify+race+linux build 全绿），且 e2e 证明 guest 网卡 MAC == 控制面分配的 NIC.spec.MAC（MAC 修复 live 铁证）。
验收证据：`scripts/verify.sh` exit 0；`go test -race ./internal/vmm/... ./internal/node/...` exit 0；`scripts/e2e.sh full` exit 0 且 guest MAC 断言命中。

- [ ] **Step 2: 加 guest MAC 读取 helper（若 guest.go 无）**

`test/e2e/guest.go` 加（//go:build e2e 文件）：

```go
// GuestNICMAC 在 guest 内读 eth0 的 MAC（/sys/class/net/eth0/address 是稳定的
// 小写无前缀字节契约）。读 live guest 实况验证控制面分配的 MAC 真正贯穿到 qemu。
func (g *Guest) GuestNICMAC(ctx context.Context, iface string) (string, error) {
	stdout, stderr, code, err := g.Exec(ctx, "cat /sys/class/net/"+shellQuote(iface)+"/address")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("read %s MAC exit %d: %s", iface, code, stderr)
	}
	return strings.TrimSpace(stdout), nil
}
```

（核对 guest.go 既有 import：`fmt`/`strings`/`shellQuote` 是否已在；`Exec` 签名是否为 `(stdout, stderr string, code int, err error)`——以实际为准调整。）

- [ ] **Step 3: 在 closure_test 的 VM running 段加 MAC 断言**

在 VM 达到 `running` 之后（NIC 已 ready、MAC 已知 `02:00:00:00:00:01`，对照 `06-nic.json`），插入：

```go
	// MAC 透传 live 铁证：guest 网卡 MAC 必须等于控制面分配的 NIC.spec.MAC，
	// 证明 MAC 真正贯穿到 qemu argv（整顿前 argv 是 mac= 空占位，guest 会拿到随机 MAC）。
	gotMAC, err := g.GuestNICMAC(ctx, "eth0")
	if err != nil {
		t.Fatalf("read guest eth0 MAC: %v", err)
	}
	if gotMAC != "02:00:00:00:00:01" {
		t.Fatalf("guest eth0 MAC = %q, want control-plane assigned 02:00:00:00:00:01 (MAC passthrough)", gotMAC)
	}
	t.Logf("guest eth0 MAC == control-plane assigned MAC: %s", gotMAC)
```

（核对：closure_test 中 guest handle 变量名、VM running 的确切行、NIC manifest 的 MAC 值——以实际 `06-nic.json` 和 closure_test 现状为准。）

- [ ] **Step 4: 本地快速验证**

Run: `cd /Users/suknna/code/Govirta && scripts/verify.sh 2>&1 | tail -5`
Expected: VERIFY exit 0

- [ ] **Step 5: race + 跨平台**

Run: `cd /Users/suknna/code/Govirta && go test -race ./internal/vmm/... ./internal/node/... -count=1 2>&1 | tail -15 && GOOS=linux GOARCH=arm64 go build ./... && echo LINUX_OK`
Expected: race PASS + LINUX_OK

- [ ] **Step 6: e2e 全闭环**

Run: `cd /Users/suknna/code/Govirta && scripts/e2e.sh full 2>&1 | tail -30`
Expected: E2E exit 0；日志含 `guest eth0 MAC == control-plane assigned MAC`。

- [ ] **Step 7: 若 e2e 失败诊断根因**

guest MAC 不等 → 核实 deriveBuilder 是否真把 MAC 填入 netdev.Tap（argv 应含 `mac=02:00:00:00:00:01`）。NIC ready 但 MAC 空被拒 → 核实 06-nic.json 是否带 spec.mac（控制面分配会填）。其它失败按 systematic-debugging 定位，不盲目重跑。

- [ ] **Step 8: 清理临时产物**

Run: `cd /Users/suknna/code/Govirta && rm -rf .tmp/* 2>/dev/null; true`

- [ ] **Step 9: Commit**

```bash
git add test/e2e/closure_test.go test/e2e/guest.go
git commit -m "test(e2e): assert guest NIC MAC matches control-plane assigned MAC"
```

---

## 自审清单（计划完成后）

- [ ] **Spec 覆盖**：spec §4.1（SpecSummary 升级）→ Task 1；§4.2（argv 下沉）→ Task 2；§4.3（CreateRequest 去 Builder）→ Task 3；§4.4（控制器调整）→ Task 4；§5（MAC 行为变更）→ Task 2/4 填 MAC + Task 5 e2e 验证；§6 测试策略 → 各 Task 测试步 + Task 5。全覆盖。
- [ ] **无 placeholder**：所有步含真实代码/命令。标注的「核对实际签名」项是真实存在的不确定性（Builder setter 名、Exec 签名、closure_test 变量名），实现时核对，非占位逃避。
- [ ] **类型一致**：`SpecSummary`/`DiskSpec`/`NICSpec`/`CreateRequest` 在 Task 1/2/3/4 一致；`deriveBuilder`/`mapArch` 在 Task 2 定义、Task 3 调用，名一致；`gatherDependencies` 新签名在 Task 4 内一致。

## 关键约束（实现时遵守）

- 单写者 + errors.Join + ctx 端到端 + 显式铁律（SpecSummary 字段全显式）保持。
- argv 派生是纯函数：相同 SpecSummary → 相同 argv（Task 2 黄金测试钉死）。
- MAC 修复改变 argv 内容（spec §5 已声明非纯结构整顿）——e2e live 断言是其铁证。
- 各 commit 单一逻辑变更（Task 1 类型、Task 2 派生、Task 3 Create、Task 4 控制器、Task 5 验证），不混合。
