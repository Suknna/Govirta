package controllers

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/vmm"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
)

// buildSpecSummary 组装落盘的 VM 配置权威 vmm.SpecSummary：从 VM 对象的 spec
// 标量（Name/Arch/VCPUs/MemoryMiB）、节点 CPU 模型、依赖解析出的
// disks/nics/cdroms 拼出完整配置。create 路径（reconcileMissingVM）与冷态 drift 检测路径
// （reconcileConfigDrift）共用此组装逻辑——唯一权威，杜绝两处重复漂移。
func buildSpecSummary(obj vmv1.VM, disks []vmm.DiskSpec, nics []vmm.NICSpec, cdroms []vmm.CDROM, cpuModel cpu.Model) vmm.SpecSummary {
	return vmm.SpecSummary{
		Name:      obj.Name,
		Arch:      obj.Spec.Arch,
		VCPUs:     obj.Spec.VCPUs,
		MemoryMiB: obj.Spec.MemoryMiB,
		CPUModel:  string(cpuModel),
		Disks:     disks,
		NICs:      nics,
		CDROMs:    cdroms,
	}
}

// specDrifted 报告 live 落盘配置与 desired 配置是否漂移。SpecSummary 含切片字段
// （Disks/NICs，Go 切片不可比较，== 会编译错误），故用 reflect.DeepEqual 整体比对
// （与 admission 门禁 1 的 cold-mutable 口径一致：成员或顺序变都算 drift）。
func specDrifted(live, desired vmm.SpecSummary) bool {
	return !reflect.DeepEqual(live, desired)
}

// reconcileConfigDrift 是 powerState=Off 冷态收敛点的 drift 检测编排（spec §6）。
// 调用前提：进程已死（reconcileExistingVMOff 已判定 Observed=Off），此时配置可能
// 已变且重建 argv 安全。
//
// 流程：
//  1. gatherDependencies 解析当前 volumeRefs/nicRefs → disks/nics。依赖未就绪 →
//     RequeueAfter 等待（不 Redefine）；依赖读传输错误 → requeue（transient）。
//  2. buildSpecSummary 组装 desired，与 live.Spec（vmm.Status 已返回的落盘 Spec）比对。
//  3. 无 drift → 原 no-op 状态收敛（patchLivePowerStatus）。
//  4. 有 drift → vmm.Redefine 覆写 vm.json 配置 + 重派生 argv：
//     - 成功 → 结构化日志记 drift 已收敛，继续状态收敛（进程未动，phase 不变，复用 live）。
//     - ErrInvalidRequest（如不支持 arch，本应被刀 3 immutable 挡住）→ 永久失败 patch，不 requeue。
//     - 其它错误 → 失败 patch + requeue。
func (c *VMController) reconcileConfigDrift(ctx context.Context, obj vmv1.VM, live vmm.VM) (controller.ReconcileResult, error) {
	// 闸门依赖并收集其暴露的已解析 disk + NIC 配置。
	disks, nics, cdroms, ready, err := c.gatherDependencies(ctx, obj)
	if err != nil {
		if errors.Is(err, vmv1.ErrInvalidSpec) {
			if perr := c.reportFailure(ctx, obj.Name, obj.Spec.PowerState, err); perr != nil {
				return controller.Done(), fmt.Errorf("vm controller: invalid dependency spec %q and status report failed: %w", obj.Name, errors.Join(err, perr))
			}
			zerolog.Ctx(ctx).Error().Err(err).Str("kind", c.Kind()).Str("key", obj.Name).Msg("vm dependency spec rejected permanently; not requeuing")
			return controller.Done(), nil
		}
		// 依赖读传输错误：transient，等待重试，不 Redefine。
		return controller.RequeueAfter(vmDependencyRequeueDelay), fmt.Errorf("vm controller: gate dependencies for %q: %w", obj.Name, err)
	}
	if !ready {
		zerolog.Ctx(ctx).Info().
			Str("kind", c.Kind()).
			Str("key", obj.Name).
			Msg("vm dependencies not ready; delayed requeue without redefine")
		return controller.RequeueAfter(vmDependencyRequeueDelay), nil
	}

	desired := buildSpecSummary(obj, disks, nics, cdroms, c.cpu)
	if !specDrifted(live.Spec, desired) {
		// 无 drift：原 no-op 收敛。
		return c.patchLivePowerStatus(ctx, obj, live)
	}

	if _, err := c.vmm.Redefine(ctx, obj.UID, desired); err != nil {
		if errors.Is(err, vmm.ErrInvalidRequest) {
			// 永久配置错误（如不支持 arch，本应被刀 3 immutable admission 挡住）：requeue 修不了。
			if perr := c.reportFailure(ctx, obj.Name, obj.Spec.PowerState, err); perr != nil {
				return controller.Done(), fmt.Errorf("vm controller: redefine %q rejected and status report failed: %w", obj.Name, errors.Join(err, perr))
			}
			zerolog.Ctx(ctx).Error().
				Err(err).
				Str("kind", c.Kind()).
				Str("key", obj.Name).
				Str("uuid", obj.UID).
				Msg("vm config drift redefine rejected permanently; not requeuing")
			return controller.Done(), nil
		}
		if perr := c.reportFailure(ctx, obj.Name, obj.Spec.PowerState, err); perr != nil {
			return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: redefine %q failed and status report failed: %w", obj.Name, errors.Join(err, perr))
		}
		return controller.RequeueAfter(vmPowerRequeueDelay), fmt.Errorf("vm controller: redefine %q: %w", obj.Name, err)
	}

	c.logConfigDriftConverged(ctx, obj, live.Spec, desired)
	// Redefine 是纯磁盘操作，不碰进程：live phase 不变，复用 live 走原状态收敛。
	return c.patchLivePowerStatus(ctx, obj, live)
}

// logConfigDriftConverged 结构化记录一次冷态 drift 已通过 Redefine 收敛，附带漂移
// 字段清单，便于排障（哪些字段触发了重派生）。
func (c *VMController) logConfigDriftConverged(ctx context.Context, obj vmv1.VM, live, desired vmm.SpecSummary) {
	zerolog.Ctx(ctx).Info().
		Str("kind", c.Kind()).
		Str("key", obj.Name).
		Str("uuid", obj.UID).
		Strs("drift_fields", driftedFields(live, desired)).
		Msg("vm config drift converged via redefine")
}

// driftedFields 列出 live 与 desired 间发生变更的字段名（仅用于日志）。
func driftedFields(live, desired vmm.SpecSummary) []string {
	var fields []string
	if live.Name != desired.Name {
		fields = append(fields, "name")
	}
	if live.Arch != desired.Arch {
		fields = append(fields, "arch")
	}
	if live.VCPUs != desired.VCPUs {
		fields = append(fields, "vcpus")
	}
	if live.MemoryMiB != desired.MemoryMiB {
		fields = append(fields, "memoryMiB")
	}
	if live.CPUModel != desired.CPUModel {
		fields = append(fields, "cpuModel")
	}
	if !reflect.DeepEqual(live.Disks, desired.Disks) {
		fields = append(fields, "disks")
	}
	if !reflect.DeepEqual(live.NICs, desired.NICs) {
		fields = append(fields, "nics")
	}
	if !reflect.DeepEqual(live.CDROMs, desired.CDROMs) {
		fields = append(fields, "cdroms")
	}
	return fields
}
