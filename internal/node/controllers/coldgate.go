package controllers

import (
	"context"
	"errors"
	"fmt"

	"github.com/suknna/govirta/internal/vmm"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

// VMStatusReader 是冷门禁所需的最窄 VM 运行时切片：只读 live phase。
// *vmm.VMMService（经 controllers.VMRunner）满足它（积木式 + 可测）。
type VMStatusReader interface {
	Status(ctx context.Context, uuid string) (vmm.VM, error)
}

// vmIsCold 报告目标 VM 对 qemu-img 冷操作（snapshot/resize）是否安全，即没有
// QEMU 进程持有 qcow2（上下一致：live 是唯一事实源，不信 VM 对象的 status 投影）。
// vmm 运行时按 VM 的 UID 索引（与 VM 控制器同一身份）。
//
// "Cold" = 进程已死 AND 非运行意图：PhaseStopped（运行后停）或 PhaseDefined
// （powerState=Off，从未启动）——两者进程已死且意图非运行，控制器不会在冷操作
// 期间 Start 它（无重启竞态）。PhaseFailed 意图=运行、控制器可能重启它，故非 cold。
// vmm.ErrNotFound（运行时 vm.json 缺失）= 根本无进程，等价 cold。
func vmIsCold(ctx context.Context, reader VMStatusReader, vm vmv1.VM) (bool, error) {
	live, err := reader.Status(ctx, vm.UID)
	if err != nil {
		if errors.Is(err, vmm.ErrNotFound) {
			return true, nil
		}
		return false, fmt.Errorf("cold gate: read VM %q live phase: %w", vm.Name, err)
	}
	switch live.Phase {
	case vmm.PhaseStopped, vmm.PhaseDefined:
		return true, nil
	default:
		return false, nil
	}
}
