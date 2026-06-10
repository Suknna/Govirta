# vmm 配置权威整顿设计（json 漂移根治 + MAC 透传修复）

**日期**: 2026-06-10
**状态**: 设计已确认，待实现
**定位**: 刀 6（冷配置改）的前置架构整顿。本修复独立成 spec→plan→实现闭环；刀 6 冷配置改设计在本修复合并后重新开始。

## 1. 背景与动机

刀 6（冷配置改）要让用户改 `VM.spec`（memoryMiB/vcpus/volumeRefs/nicRefs）后，node 重建 vm.json + 下次重启由 json 派生 flag 生效。其 drift 检测比对的是 `SpecSummary`（配置描述），而 VM 真正 boot 的是 `vm.json` 里的 `Argv`。

**审查发现的真实漂移风险** [已验证]：`internal/vmm` 的 `vm.json`（`persistedState`）同时持久化两份独立的配置表示：

```go
type persistedState struct {
    Spec  SpecSummary  // 配置描述摘要（上报 + 未来 drift 比对用）
    Argv  []string     // 真正被 exec 的 qemu flag（Start 直接跑它）
    ...
}
```

而 `VMMService.Create` 接收**两个独立输入**——`req.Builder`（生成 `Argv`）+ `req.Spec`（`SpecSummary`）——且 **vmm 不校验两者一致**（`service.go` Create：argv 从 Builder 烤出，SpecSummary 原样照存）。调用方完全可以传一个 memoryMiB=512 的 Builder + memoryMiB=256 的 SpecSummary，vmm 会落盘「argv 跑 512、SpecSummary 报 256」的矛盾 json。

当前实践未暴露此问题，只因 VM 控制器的 `buildVM`（构造 Builder）和 `reconcileMissingVM`（构造 SpecSummary）都从同一个 `obj.Spec` 派生——**但 vmm 契约本身不保证**。一旦刀 6 的 drift 检测依赖「SpecSummary == desired → 无需重建」，这个不保证会让判断失真：SpecSummary 说没变，argv 实际是旧的或矛盾的，VM 跑着错配置而控制器以为已收敛。

**核心心智模型（用户确立）**：vm.json 等同 libvirt 的 xml——它是虚拟机配置的**权威描述**，`json → qemu flag` 是确定性单向映射，永远一致。json 是 flag 的上级，flag 永远从 json 派生，不存在「另存一份 argv」的第二来源。

本修复在 vmm 层结构性根治这个漂移，使刀 6 能站得住。

## 2. 设计目标

1. **SpecSummary 成为唯一配置权威**：vm.json 里的 argv 必然从 SpecSummary 确定性派生，杜绝两者漂移的结构性可能。
2. **vmm 成为唯一「配置 → argv」翻译层**：argv 构造逻辑从 VM 控制器 `buildVM` 下沉进 vmm，控制器不再持有 `qemu.Builder` 知识。
3. **修复 MAC 透传缺陷**：作为「SpecSummary 是完整配置权威」的必然结果，guest 网卡 MAC 显式纳入配置并填入 argv。

## 3. 当前架构（整顿前）[已验证]

argv 派生分两段：

1. **VM 控制器 `buildVM`**（`internal/node/controllers/vm.go`）：构造 base builder——`mapArch(arch)` → `qemu.NewBuilder(arch).WithMachine(profile).WithCPU(cpuModel).WithSMP(...).WithMemoryMiB(...)` + 逐个 `WithBlockDevice(qcow2 disk)` + 逐个 `WithNetDevice(tap)`。
2. **vmm `injectFacilityFlags`**（`internal/vmm/facility.go`）：向 base builder 注入运行时设施 flag（pidfile / QMP chardev+monitor / serial / vnc / daemonize）后 `Build()` → `Argv()`。

`CreateRequest` 双输入：`Builder *qemu.Builder`（控制器构造完段 1）+ `Spec SpecSummary`（控制器另行构造）。

**当前 SpecSummary** [已验证]：
```go
type SpecSummary struct {
    Arch      string   `json:"arch"`
    VCPUs     int      `json:"vcpus"`
    MemoryMiB int      `json:"memory_mib"`
    DiskPaths []string `json:"disk_paths"`
    TapNames  []string `json:"tap_names"`
}
```
无 `CPUModel`（在控制器 `c.cpuModel` 持有）、无 MAC。

**MAC 缺陷** [已验证]：`buildVM` 给 `netdev.Tap` 设 `MAC: ""` 占位，注释写「下方按 NIC ref 顺序填真实 MAC」，但函数直接 return，MAC 永远没被填（全文件唯一 MAC 引用就是这个占位）。控制面分配的 `NIC.Spec.MAC` 未透传到 qemu argv，违反 memory 698（control-plane 分配的 MAC 应原样贯穿）。当前 e2e 能跑通仅因 DHCP 静态绑定按 TAP 而非 guest 网卡 MAC 工作。

## 4. 整顿方案

### 4.1 SpecSummary 升级为完整物理配置描述

承载 argv 派生所需的全部字段，硬件用显式 typed 列表（禁止裸 string 推断）：

```go
type SpecSummary struct {
    Arch      string      `json:"arch"`
    VCPUs     int         `json:"vcpus"`
    MemoryMiB int         `json:"memory_mib"`
    CPUModel  string      `json:"cpu_model"`  // 从控制器移入：它是配置的一部分，该进 json 权威
    Disks     []DiskSpec  `json:"disks"`
    NICs      []NICSpec   `json:"nics"`
}

// DiskSpec 是一块已解析的物理盘配置（path 由控制器从 Volume.status.VolumePath 解析）。
type DiskSpec struct {
    Path string `json:"path"`
}

// NICSpec 是一张已解析的物理网卡配置（tapName + MAC 由控制器从 NIC 对象解析）。
type NICSpec struct {
    TapName string `json:"tap_name"`
    MAC     string `json:"mac"`  // 控制面分配的 NIC.Spec.MAC，原样贯穿（memory 698）
}
```

- `CPUModel` 用 `string`（`cpu.Model` 本身是 `type Model string`，json 可序列化；vmm 派生时转回 `cpu.Model`）。
- `DiskPaths []string` → `Disks []DiskSpec`，`TapNames []string` → `NICs []NICSpec`（携带 MAC）。这是 SpecSummary 的不兼容结构变更——快速迭代期直接替换，不保留旧字段（项目反模式：不为内部 API 留兼容层）。

### 4.2 argv 派生下沉进 vmm

- VM 控制器的 `buildVM`（含 `mapArch`）逻辑**移入 vmm**，成为 vmm 内部的 `SpecSummary → *qemu.Builder` 确定性构造（新文件，如 `internal/vmm/argv.go`）。vmm 据 SpecSummary 构造 base builder（arch/machine/cpu/smp/mem/disks/nics，nics 填入真实 MAC），再接现有 `injectFacilityFlags` 注入设施 flag + Build + Argv。
- `mapArch`（arch string → `qemu.Arch` + `machine.Profile`）随之移入 vmm。未知 arch 是永久配置错误，返回 `ErrInvalidRequest`（或 vmm 既有 sentinel），由控制器映射为永久失败。
- 派生是**纯函数**：相同 SpecSummary 永远产生相同 argv。黄金测试钉死（给定 SpecSummary，断言 argv 逐项相等），杜绝第二来源。

### 4.3 CreateRequest 去掉 Builder

```go
type CreateRequest struct {
    UUID string      // 调用方显式提供，vmm 不生成
    Spec SpecSummary // 唯一配置权威；vmm 据此确定性派生 argv
}
```

`Create` 内部：校验 Spec → `deriveBuilder(Spec)` → `injectFacilityFlags` → 落盘 `persistedState{Spec, Argv, ...}`。此时 `Argv` 必然 == `Spec` 的派生结果（同一函数链），结构性消除漂移。

### 4.4 控制器侧调整

- `gatherDependencies` 扩展：读 NIC 对象时除 `status.TapName` 外，**还解析 `spec.MAC`**，返回每张卡的 `{TapName, MAC}`；读 Volume 同样返回每块盘的 path。
- 控制器构造完整 `SpecSummary`（Arch/VCPUs/MemoryMiB/CPUModel + Disks + NICs(含 MAC)）传给 `vmm.Create`，不再构造 `qemu.Builder`。
- 控制器移除 `buildVM` / `mapArch`（已下沉 vmm）。`c.cpuModel` 仍由控制器持有并写入 SpecSummary.CPUModel（控制器是 CPUModel 的注入点，但它现在通过 SpecSummary 传递而非直接构造 builder）。
- 边界清晰：**控制器解析「逻辑 ref → 物理资源」**（读 etcd Volume/NIC status），**vmm 翻译「物理配置 → qemu flag」**（不读 etcd、不解析 ref）。

## 5. 行为变更声明（非纯结构整顿）

本修复**不是纯行为保持重构**——MAC 透传修复会改变 argv 内容：`netdev.Tap` 的 MAC 从空占位（`mac=`）变为填入控制面分配的真实 MAC（`mac=02:00:00:00:00:01`）。这是 SpecSummary 成为**完整**配置权威的必然结果（残缺的 NIC 配置不能算权威），与结构整顿不可分割，故一并修复。

argv 的其余内容（arch/machine/cpu/smp/mem/disks/设施 flag）保持不变——只是构造位置从控制器移到 vmm。

## 6. 测试策略

### 6.1 单元测试（vmm 层）
- **黄金 argv 派生测试**：给定完整 SpecSummary（含多盘、多卡带 MAC），断言派生的 argv 逐项精确相等（含 `mac=<真实值>`、disk path、smp、memory、设施 flag）。这是 json→flag 确定性的钉子。
- **MAC 填入测试**：SpecSummary.NICs 带 MAC → argv 的 `-device virtio-net-pci` 含 `mac=<该值>`（回归防护 MAC 缺陷）。
- **Create 落盘一致性测试**：Create 后读回 vm.json，断言 `persistedState.Argv` 与 `persistedState.Spec` 的独立派生结果一致（漂移防护）。
- **mapArch 下沉测试**：x86_64/aarch64 正确映射，未知 arch 返回错误。
- **CreateRequest 校验**：缺 UUID / 缺必填 Spec 字段被拒。

### 6.2 控制器层单元测试
- `gatherDependencies` 返回每张卡的 MAC（从 NIC.spec.MAC 解析）。
- 控制器构造的 SpecSummary 含 CPUModel + 每盘 path + 每卡 {TapName, MAC}。
- 现有 VM 控制器测试迁移到新 CreateRequest 形态（不再传 Builder）。

### 6.3 e2e（复用现有单节点闭环）
- 现有 `TestDistributedSpineClosure` 的 VM 启动场景验证整顿后 VM 仍正常 boot（argv 派生下沉无回归）。
- **新增 MAC 透传 live 断言**：VM running 后，guest 内读网卡 MAC（如 `ip link show eth0` / `cat /sys/class/net/eth0/address`），断言等于控制面分配的 NIC.spec.MAC。这是 MAC 修复的 live 铁证（上下一致：从 guest 实况验证，非信 status）。

## 7. Out of Scope（推迟到刀 6）

本修复**只做** vmm 配置权威整顿 + MAC 透传修复。以下全部属于刀 6 冷配置改，在本修复合并后重新开始设计：

- `Redefine` 原语（覆写 json.Spec + 重派生 argv）
- json 双轴：`StartedSpec` 快照（last-started 配置）
- `VMStatus.ConfigState`（Synced/PendingRestart）字段
- 控制器侧 drift 检测（desired Spec != StartedSpec → Redefine）
- 运行中改配置「立即更新 json、待重启生效」收敛逻辑
- 冷配置改 e2e（标量改 + 增删硬件全集）

## 8. 影响面汇总

| 层 | 改动 |
| --- | --- |
| `internal/vmm/vm.go` | SpecSummary 扩展（+CPUModel/Disks/NICs）、CreateRequest 去 Builder、新增 DiskSpec/NICSpec |
| `internal/vmm/argv.go`（新） | argv 派生下沉：SpecSummary → builder（含 mapArch、MAC 填入） |
| `internal/vmm/service.go` | Create 改为据 Spec 派生 argv（不收 Builder） |
| `internal/vmm/facility.go` | 不变（injectFacilityFlags 接派生后的 builder） |
| `internal/node/controllers/vm.go` | 移除 buildVM/mapArch，gatherDependencies 返回 MAC，构造完整 SpecSummary |
| 各层测试 | 迁移到新 CreateRequest/SpecSummary 形态 + 新增黄金/MAC/一致性测试 |

## 9. 关键不变量（整顿后）

1. **json.Argv 永远从 json.Spec 确定性派生**——vmm 是唯一翻译者，无第二来源。
2. **SpecSummary 是完整物理配置权威**——含 CPUModel + 每盘 path + 每卡 {TapName, MAC}。
3. **控制器解析逻辑 ref，vmm 翻译物理配置**——分层边界清晰，vmm 不读 etcd。
4. **control-plane 分配的 MAC 原样贯穿**到 qemu argv（memory 698）。
