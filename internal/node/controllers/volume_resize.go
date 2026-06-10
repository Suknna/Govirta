package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/storage"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// volumeResizeRequeueDelay paces the cold gate and transient-failure re-drive
// (the framework queue has no backoff). Same cadence as snapshotRequeueDelay.
const volumeResizeRequeueDelay = snapshotRequeueDelay

// reconcileResize drives cold-resize convergence for a ready volume. 声明式强制
// 收敛: 不比对容量——直接把绝对目标 (spec.CapacityBytes) 交给
// VolumeService.ResizeVolume, driver 内部读 live size 决定是否真 resize (C′)。
// 成功和失败都保持 phase=Ready (A2: resize 失败不否定卷已达成的可用性);
// status no-op guard 防 PATCH 抖动。
func (c *VolumeController) reconcileResize(ctx context.Context, vol volumev1.Volume) (controller.ReconcileResult, error) {
	logger := zerolog.Ctx(ctx)

	vmRaw, err := c.client.Get(ctx, string(metav1.KindVM), vol.Spec.VMName)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			// 孤儿卷 (owning VM 对象已删): 等待, 不给没有 VM 的卷扩容 (决策6)。
			logger.Info().Str("volume", vol.Name).Str("vm", vol.Spec.VMName).
				Msg("owning VM object not found; waiting before resize")
			return controller.RequeueAfter(volumeResizeRequeueDelay), nil
		}
		return controller.Requeue(), fmt.Errorf("volume controller: read owning VM %q for resize: %w", vol.Spec.VMName, err)
	}

	var vm vmv1.VM
	if err := json.Unmarshal(vmRaw, &vm); err != nil {
		return controller.Requeue(), fmt.Errorf("volume controller: decode owning VM %q: %w", vol.Spec.VMName, err)
	}

	cold, err := vmIsCold(ctx, c.vmm, vm)
	if err != nil {
		return controller.Requeue(), fmt.Errorf("volume controller: cold gate for volume %q: %w", vol.Name, err)
	}
	if !cold {
		// cold-mutable 暂缓: 接受 spec 变更但等停机才落地。
		logger.Info().Str("volume", vol.Name).Str("vm", vol.Spec.VMName).
			Msg("owning VM not cold; deferring volume resize until stopped")
		return controller.RequeueAfter(volumeResizeRequeueDelay), nil
	}

	// 声明式强制收敛: 交绝对目标; driver 读 live size, 已 >= 目标则 no-op。
	if err := c.volumes.ResizeVolume(ctx, storage.ResizeVolumeRequest{
		PoolName:      vol.Spec.PoolRef,
		VolumeID:      deriveVolumeID(vol.Spec),
		CapacityBytes: vol.Spec.CapacityBytes,
	}); err != nil {
		// A2: 保持 Ready, requeue。不翻 Failed。
		logger.Error().Err(err).Str("volume", vol.Name).
			Msg("volume resize failed; volume remains usable, will retry")
		return controller.RequeueAfter(volumeResizeRequeueDelay), fmt.Errorf("volume controller: resize %q: %w", vol.Name, err)
	}

	logger.Info().Str("volume", vol.Name).Int64("capacityBytes", vol.Spec.CapacityBytes).
		Msg("volume resize converged")
	return controller.Done(), nil
}
