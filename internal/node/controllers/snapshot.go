package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/storage"
	"github.com/suknna/govirta/internal/storage/volume"
	"github.com/suknna/govirta/internal/vmm"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// VolumeSnapshotter is the narrow slice of the volume service the snapshot
// controller needs: create and delete a qcow2 internal snapshot on a volume.
// *storage.VolumeService satisfies it (积木式 + 可测).
type VolumeSnapshotter interface {
	SnapshotVolume(ctx context.Context, req storage.SnapshotVolumeRequest) error
	DeleteVolumeSnapshot(ctx context.Context, req storage.DeleteVolumeSnapshotRequest) error
}

// 编译期证明真实生产类型满足窄接口。
var (
	_ VolumeSnapshotter     = (*storage.VolumeService)(nil)
	_ controller.Controller = (*SnapshotController)(nil)
)

// SnapshotController reconciles Snapshot objects: a whole-VM cold snapshot. It
// reads the target VM's live phase (must be cold — qemu-img snapshot is unsafe
// while a QEMU process holds the qcow2), resolves the VM's volumeRefs to
// (pool, derived volume id), and fans out one qcow2 internal snapshot per disk,
// all named by the Snapshot's UID. The fan-out is all-or-nothing: a mid-disk
// failure rolls back the already-created snapshots so etcd never holds a
// half-complete whole-VM snapshot.
type SnapshotController struct {
	volumes VolumeSnapshotter
	vmm     VMRunner
	client  DependencyReader
}

// NewSnapshotController wires a SnapshotController against the volume snapshot
// service, the VM process manager (for the live cold gate), and the master
// dependency/status client.
func NewSnapshotController(volumes VolumeSnapshotter, runner VMRunner, client DependencyReader) *SnapshotController {
	return &SnapshotController{volumes: volumes, vmm: runner, client: client}
}

// Kind is the apis kind this controller watches.
func (c *SnapshotController) Kind() string { return string(metav1.KindSnapshot) }

// snapshotRequeueDelay is the delayed requeue interval for the cold gate and for
// transient errors (a tight-but-bounded poll until the VM goes cold or a flake
// clears). The framework's queue has no backoff (最薄实现), so this keeps the
// re-drive cadence sane.
const snapshotRequeueDelay = 5 * time.Second

// Reconcile drives one Snapshot event toward its desired state.
//
// DELETED is a no-op: the apiserver sends deletion intent as a normal object
// carrying deletionTimestamp/finalizers, so teardown is driven by that stamp, not
// by the DELETED event. For ADDED/MODIFIED it decodes the object, resolves the
// target VM (object + live cold phase), and dispatches to the create or delete
// path.
func (c *SnapshotController) Reconcile(ctx context.Context, ev controller.Event) (controller.ReconcileResult, error) {
	if err := ctx.Err(); err != nil {
		return controller.Done(), fmt.Errorf("snapshot controller: context done before reconcile: %w", err)
	}
	logger := zerolog.Ctx(ctx)

	if ev.Type == controller.EventDeleted {
		logger.Info().Str("kind", c.Kind()).Str("key", ev.Key).Msg("snapshot deleted; teardown driven by deletionTimestamp")
		return controller.Done(), nil
	}

	var snap snapshotv1.Snapshot
	if err := json.Unmarshal(ev.Object, &snap); err != nil {
		return controller.Done(), fmt.Errorf("snapshot controller: decode object %q: %w", ev.Key, err)
	}

	// Resolve the target VM object (for volumeRefs) and its live phase (cold gate).
	vm, err := c.targetVM(ctx, snap.Spec.VMRef)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			// VM gone: nothing to snapshot/delete against. On teardown, drop the
			// finalizer (the qcow2 files are gone with the VM). On create, requeue.
			if isDeleting(snap.ObjectMeta) {
				if rerr := removeTeardownFinalizer(ctx, c.client, c.Kind(), snap.Name); rerr != nil {
					return controller.Requeue(), fmt.Errorf("snapshot controller: remove finalizer %q: %w", snap.Name, rerr)
				}
				return controller.Done(), nil
			}
			return controller.RequeueAfter(snapshotRequeueDelay), nil
		}
		return controller.RequeueAfter(snapshotRequeueDelay), err
	}

	cold, err := c.vmIsCold(ctx, vm)
	if err != nil {
		return controller.RequeueAfter(snapshotRequeueDelay), err
	}

	if isDeleting(snap.ObjectMeta) {
		return c.reconcileDelete(ctx, snap, vm, cold)
	}
	return c.reconcileCreate(ctx, snap, vm, cold)
}

// reconcileCreate fans out one qcow2 internal snapshot per the VM's volumeRefs.
// It is level-triggered idempotent (a ready snapshot is a no-op), cold-gated, and
// all-or-nothing (a mid-disk failure rolls back the already-created snapshots).
func (c *SnapshotController) reconcileCreate(ctx context.Context, snap snapshotv1.Snapshot, vm vmv1.VM, cold bool) (controller.ReconcileResult, error) {
	// Level-triggered idempotence: a ready snapshot is already at desired state.
	if snap.Status.Phase == snapshotv1.SnapshotPhaseReady {
		return controller.Done(), nil
	}
	// Cold gate: qemu-img snapshot is unsafe while a QEMU process holds the qcow2
	// (QEMU hard constraint). "Cold" = process-dead AND non-running intent
	// (PhaseStopped/PhaseDefined) or runtime absent — see vmIsCold + spec §5.0.
	if !cold {
		pending := snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhasePending, Message: "waiting for VM cold (stopped/defined)"}
		if err := c.patchStatus(ctx, snap.Name, snap.Status, pending); err != nil {
			return controller.RequeueAfter(snapshotRequeueDelay), err
		}
		return controller.RequeueAfter(snapshotRequeueDelay), nil
	}

	created := make([]volumeTarget, 0, len(vm.Spec.VolumeRefs))
	results := make([]snapshotv1.DiskSnapshotResult, 0, len(vm.Spec.VolumeRefs))
	for _, volRef := range vm.Spec.VolumeRefs {
		target, err := c.resolveVolumeTarget(ctx, volRef)
		if err != nil {
			return c.failFanOut(ctx, snap, created, results, volRef, err)
		}
		if serr := c.volumes.SnapshotVolume(ctx, storage.SnapshotVolumeRequest{
			PoolName:     target.poolName,
			VolumeID:     target.volumeID,
			SnapshotName: snap.UID,
		}); serr != nil {
			return c.failFanOut(ctx, snap, created, results, volRef, serr)
		}
		created = append(created, target)
		results = append(results, snapshotv1.DiskSnapshotResult{VolumeRef: volRef, Result: snapshotv1.DiskSnapshotStateCreated})
	}

	ready := snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhaseReady, DiskSnapshots: results}
	if err := c.patchStatus(ctx, snap.Name, snap.Status, ready); err != nil {
		return controller.Requeue(), err
	}
	return controller.Done(), nil
}

// failFanOut rolls back already-created disk snapshots (all-or-nothing), then
// patches Failed with the per-disk results, and requeues for retry. Rollback
// errors are joined with the original cause (项目铁律: errors.Join).
func (c *SnapshotController) failFanOut(ctx context.Context, snap snapshotv1.Snapshot, created []volumeTarget, results []snapshotv1.DiskSnapshotResult, failedRef string, cause error) (controller.ReconcileResult, error) {
	rollbackErr := c.rollback(ctx, snap.UID, created)
	results = append(results, snapshotv1.DiskSnapshotResult{VolumeRef: failedRef, Result: snapshotv1.DiskSnapshotStateFailed})
	failed := snapshotv1.SnapshotStatus{
		Phase:         snapshotv1.SnapshotPhaseFailed,
		DiskSnapshots: results,
		Message:       cause.Error(),
	}
	patchErr := c.patchStatus(ctx, snap.Name, snap.Status, failed)
	return controller.RequeueAfter(snapshotRequeueDelay), errors.Join(cause, rollbackErr, patchErr)
}

// rollback deletes the snapshots already created in this fan-out so a partial
// whole-VM snapshot never persists. Each per-disk delete error is collected and
// joined (项目铁律: 不吞错 / errors.Join).
func (c *SnapshotController) rollback(ctx context.Context, snapUID string, created []volumeTarget) error {
	var errs []error
	for _, t := range created {
		if err := c.volumes.DeleteVolumeSnapshot(ctx, storage.DeleteVolumeSnapshotRequest{
			PoolName:     t.poolName,
			VolumeID:     t.volumeID,
			SnapshotName: snapUID,
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// reconcileDelete tears the whole-VM snapshot down: it cold-gates (same QEMU hard
// constraint as create), deletes every disk's internal snapshot (idempotent on a
// missing snapshot), and drops the node-teardown finalizer once all disks drain.
func (c *SnapshotController) reconcileDelete(ctx context.Context, snap snapshotv1.Snapshot, vm vmv1.VM, cold bool) (controller.ReconcileResult, error) {
	// Cold gate also applies to delete: qemu-img snapshot -d is unsafe while a
	// QEMU process holds the qcow2 (same hard constraint as create). "Cold" =
	// process-dead non-running intent or runtime absent — see vmIsCold + spec §5.0.
	if !cold {
		deleting := snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhaseDeleting, Message: "waiting for VM cold (stopped/defined)"}
		if err := c.patchStatus(ctx, snap.Name, snap.Status, deleting); err != nil {
			return controller.RequeueAfter(snapshotRequeueDelay), err
		}
		return controller.RequeueAfter(snapshotRequeueDelay), nil
	}

	logger := zerolog.Ctx(ctx)
	var errs []error
	for _, volRef := range vm.Spec.VolumeRefs {
		target, err := c.resolveVolumeTarget(ctx, volRef)
		if err != nil {
			if errors.Is(err, client.ErrNotFound) {
				// Volume object gone: its qcow2 file (and therefore the internal
				// snapshot living inside it) has been destroyed, so this disk's
				// snapshot is already torn down — skip it and keep draining the
				// rest. This is symmetric with the VM-gone branch in Reconcile
				// (parent gone → snapshot gone): the node converges on live
				// reality (上下一致铁律), so a Volume that disappears first must
				// not permanently wedge the finalizer.
				logger.Info().Str("volRef", volRef).Msg("snapshot teardown: Volume gone; disk snapshot already drained, skipping")
				continue
			}
			errs = append(errs, err)
			continue
		}
		// DeleteVolumeSnapshot is idempotent on a missing internal snapshot (the
		// driver lists before deleting), so a re-driven teardown or a disk that was
		// never snapshotted (create failed mid-fan-out) does not error here — the
		// finalizer can still drain (spec §5.2/§5.3).
		if derr := c.volumes.DeleteVolumeSnapshot(ctx, storage.DeleteVolumeSnapshotRequest{
			PoolName:     target.poolName,
			VolumeID:     target.volumeID,
			SnapshotName: snap.UID,
		}); derr != nil {
			errs = append(errs, derr)
		}
	}
	if err := errors.Join(errs...); err != nil {
		// Keep the finalizer and requeue. The status patch error is joined with the
		// teardown cause so neither is swallowed (项目铁律: 不吞错).
		deleting := snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhaseDeleting, Message: err.Error()}
		patchErr := c.patchStatus(ctx, snap.Name, snap.Status, deleting)
		return controller.RequeueAfter(snapshotRequeueDelay), fmt.Errorf("snapshot controller: delete %q: %w", snap.Name, errors.Join(err, patchErr))
	}
	if err := removeTeardownFinalizer(ctx, c.client, c.Kind(), snap.Name); err != nil {
		return controller.Requeue(), fmt.Errorf("snapshot controller: remove finalizer %q: %w", snap.Name, err)
	}
	return controller.Done(), nil
}

// volumeTarget is a resolved (pool, derived volume id) pair for one of the VM's
// volumeRefs. The volume id is derived via deriveVolumeID (volume.go, same
// package) so the snapshot controller and the volume controller teardown key the
// qcow2 identically.
type volumeTarget struct {
	poolName string
	volumeID volume.ID
}

// resolveVolumeTarget reads the named Volume object from the master and derives
// its storage key (pool + VMRef-role-diskIndex id). The qcow2 file the snapshot
// runs on is owned by that volume in that pool.
func (c *SnapshotController) resolveVolumeTarget(ctx context.Context, volName string) (volumeTarget, error) {
	raw, err := c.client.Get(ctx, string(metav1.KindVolume), volName)
	if err != nil {
		return volumeTarget{}, fmt.Errorf("snapshot controller: get Volume %q: %w", volName, err)
	}
	var vol volumev1.Volume
	if err := json.Unmarshal(raw, &vol); err != nil {
		return volumeTarget{}, fmt.Errorf("snapshot controller: decode Volume %q: %w", volName, err)
	}
	return volumeTarget{poolName: vol.Spec.PoolRef, volumeID: deriveVolumeID(vol.Spec)}, nil
}

// targetVM reads the named VM object from the master. A client.ErrNotFound is
// returned verbatim for the caller to handle (drop finalizer on teardown, requeue
// on create); any other read/decode failure is wrapped.
func (c *SnapshotController) targetVM(ctx context.Context, vmName string) (vmv1.VM, error) {
	raw, err := c.client.Get(ctx, string(metav1.KindVM), vmName)
	if err != nil {
		return vmv1.VM{}, err // ErrNotFound handled by caller
	}
	var vm vmv1.VM
	if err := json.Unmarshal(raw, &vm); err != nil {
		return vmv1.VM{}, fmt.Errorf("snapshot controller: decode VM %q: %w", vmName, err)
	}
	return vm, nil
}

// vmIsCold reports whether the target VM is safe for qemu-img snapshot, i.e. no
// QEMU process holds the qcow2 (上下一致: live is the single source of truth, not
// the VM object's status projection). The vmm runtime is keyed by the VM's UID
// (the same identity the VM controller uses: c.vmm.Status(ctx, obj.UID)).
//
// "Cold" = process-dead AND non-running intent. That is PhaseStopped (stopped
// after a run) or PhaseDefined (powerState=Off, never started) — both have a dead
// process and an intent that is not running, so the VM controller will not Start
// it during the snapshot (no restart race). PhaseFailed is intent=running and the
// VM controller may re-Start it, so it is NOT cold. A vmm.ErrNotFound (the runtime
// vm.json is absent) means no process exists at all, which is equivalent to cold —
// critical on the delete path where the VM object still exists but its runtime is
// already gone (otherwise teardown would requeue forever). See spec §5.0.
func (c *SnapshotController) vmIsCold(ctx context.Context, vm vmv1.VM) (bool, error) {
	live, err := c.vmm.Status(ctx, vm.UID)
	if err != nil {
		if errors.Is(err, vmm.ErrNotFound) {
			return true, nil
		}
		return false, fmt.Errorf("snapshot controller: read VM %q live phase: %w", vm.Name, err)
	}
	switch live.Phase {
	case vmm.PhaseStopped, vmm.PhaseDefined:
		return true, nil
	default:
		return false, nil
	}
}

// patchStatus marshals desired and PATCHes it to the master's /status
// sub-resource, but only when it differs from observed (the status carried by the
// watched object). Skipping an identical PATCH breaks the status→MODIFIED→watch→
// reconcile→PATCH feedback loop (level-triggered idempotence).
func (c *SnapshotController) patchStatus(ctx context.Context, name string, observed, desired snapshotv1.SnapshotStatus) error {
	if snapshotStatusEqual(observed, desired) {
		return nil
	}
	body, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("snapshot controller: marshal status for %q: %w", name, err)
	}
	if _, err := c.client.PatchStatus(ctx, c.Kind(), name, body); err != nil {
		return fmt.Errorf("snapshot controller: patch status for %q: %w", name, err)
	}
	return nil
}

// snapshotStatusEqual compares two statuses including the DiskSnapshots slice
// (SnapshotStatus is not == comparable because it holds a slice). Used by the
// no-op patch guard to break the status->MODIFIED->reconcile->PATCH loop.
func snapshotStatusEqual(a, b snapshotv1.SnapshotStatus) bool {
	if a.Phase != b.Phase || a.Message != b.Message || len(a.DiskSnapshots) != len(b.DiskSnapshots) {
		return false
	}
	for i := range a.DiskSnapshots {
		if a.DiskSnapshots[i] != b.DiskSnapshots[i] {
			return false
		}
	}
	return true
}
