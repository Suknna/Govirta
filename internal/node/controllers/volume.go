package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/storage"
	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/local"
	"github.com/suknna/govirta/internal/storage/volume"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// RootVolumeCreator is the narrow slice of the volume service the controller
// needs: derive an independent root qcow2 volume from an image byte reader, and
// delete a volume on teardown. *storage.VolumeService satisfies it (积木式 + 可测).
type RootVolumeCreator interface {
	CreateRootVolumeFromReader(ctx context.Context, req storage.CreateRootVolumeFromReaderRequest) (volume.Volume, error)
	// CreateDataVolume creates a blank data disk volume (no image source). Data
	// volumes are not image-derived, so the controller's data path gates only on
	// the block pool and never opens an image reader.
	CreateDataVolume(ctx context.Context, req storage.CreateDataVolumeRequest) (volume.Volume, error)
	DeleteVolume(ctx context.Context, req storage.DeleteVolumeRequest) error
	// ResizeVolume declares an absolute target capacity on the volume's live
	// qcow2 (cold-resize convergence, 刀5). The driver decides idempotently
	// whether a grow is needed; the controller never reads/compares live size.
	ResizeVolume(ctx context.Context, req storage.ResizeVolumeRequest) error
}

// DependencyReader is the narrow slice of the master client the controller
// needs: read a referenced object's status for gating, PATCH this volume's own
// status, and remove a finalizer once live resources are torn down.
// *client.Client satisfies it. A Get that maps to client.ErrNotFound means the
// dependency does not exist yet and the volume must wait (requeue).
type DependencyReader interface {
	Get(ctx context.Context, kind, name string) ([]byte, error)
	PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error)
	FinalizerRemover
}

// 编译期证明真实生产类型满足窄接口。
var (
	_ RootVolumeCreator     = (*storage.VolumeService)(nil)
	_ DependencyReader      = (*client.Client)(nil)
	_ controller.Controller = (*VolumeController)(nil)
)

// VolumeController reconciles Volume objects: root volumes gate on the referenced
// block StoragePool, source Image, and this node's ready image-cache entry, then
// streams cached image bytes into VolumeService.CreateRootVolumeFromReader to
// derive an independent qcow2 root volume. Data volumes gate only on their block
// StoragePool and create blank qcow2 disks.
//
// The byte format authority is the referenced Image's spec format
// (Image.Spec.Format), mapped explicitly to a storage diskformat.
type VolumeController struct {
	volumes        RootVolumeCreator
	cacheRoot      string
	vmm            VMStatusReader
	client         DependencyReader
	cacheRootValid bool
	openCachedFile func(string, string) (cachedFile, error)
}

type cachedFile interface {
	io.ReadCloser
	Stat() (os.FileInfo, error)
}

// NewVolumeController wires a VolumeController against the volume service, the
// explicit node-local image cache root, the VM process manager (for the live cold
// gate on resize), and the master dependency/status client.
func NewVolumeController(volumes RootVolumeCreator, imageCacheRoot string, runner VMStatusReader, client DependencyReader) *VolumeController {
	cacheRoot, ok := cleanAbsoluteRoot(imageCacheRoot)
	return &VolumeController{volumes: volumes, cacheRoot: cacheRoot, vmm: runner, client: client, cacheRootValid: ok, openCachedFile: openNoFollow}
}

// Kind is the apis kind this controller watches.
func (c *VolumeController) Kind() string {
	return string(metav1.KindVolume)
}

// Reconcile drives one Volume event toward its desired state.
//
// DELETED is a no-op in this slice. For ADDED/MODIFIED it decodes the object and
// gates on its three dependencies. requeue semantics:
//   - a dependency is missing (ErrNotFound) or not yet Ready → requeue, no status
//     patch (just wait);
//   - a dependency read fails for any other reason → transient: requeue with a
//     wrapped error, no status patch (readiness could not be assessed);
//   - the Image's format does not map to a storage format → permanent config
//     failure: patch failed and do NOT requeue;
//   - cached image open / CreateRootVolumeFromReader fails → transient: requeue
//     with a wrapped error and no failed status patch.
func (c *VolumeController) Reconcile(ctx context.Context, ev controller.Event) (controller.ReconcileResult, error) {
	if err := ctx.Err(); err != nil {
		return controller.Done(), fmt.Errorf("volume controller: context done before reconcile: %w", err)
	}

	logger := zerolog.Ctx(ctx)

	if ev.Type == controller.EventDeleted {
		logger.Info().
			Str("kind", c.Kind()).
			Str("key", ev.Key).
			Msg("volume deleted; delete is a no-op in this slice")
		return controller.Done(), nil
	}

	var vol volumev1.Volume
	if err := json.Unmarshal(ev.Object, &vol); err != nil {
		return controller.Done(), fmt.Errorf("volume controller: decode object %q: %w", ev.Key, err)
	}

	// Teardown branch: a deletion-stamped object means apiserver wants this
	// volume gone. Delete the live volume from its block pool before the ensure
	// path runs. A teardown failure keeps the finalizer (object stays "deleting")
	// and requeues.
	if isDeleting(vol.ObjectMeta) {
		if err := c.teardown(ctx, vol); err != nil {
			return controller.Requeue(), fmt.Errorf("volume controller: teardown %q: %w", vol.Name, err)
		}
		if err := removeTeardownFinalizer(ctx, c.client, c.Kind(), vol.Name); err != nil {
			return controller.Requeue(), fmt.Errorf("volume controller: remove finalizer %q: %w", vol.Name, err)
		}
		return controller.Done(), nil
	}

	// Level-triggered idempotence: a ready volume is already at its desired
	// state. Re-reconciling (e.g. on the MODIFIED event our own ready-patch
	// produced) must not re-create — CreateRootVolume would return
	// ErrVolumeAlreadyExists, which fails before reaching the no-op-guarded
	// status patch, so the controller would spin forever (the same blind-spot
	// loop the image controller had). The early return breaks it because the
	// failure happens in create, not in patch.
	// Ready volume: drive cold-resize convergence instead of an unconditional
	// no-op. A grown spec.capacityBytes is applied once the owning VM is cold
	// (上下一致: gate on live phase, not the VM's status projection). 见刀5 spec §6.
	if vol.Status.Phase == volumev1.VolumePhaseReady {
		return c.reconcileResize(ctx, vol)
	}

	// Role-based create dispatch. A root volume is a full copy of this node's
	// cached image bytes (gate on block pool + Image + node cache, format from
	// Image.Spec). A data volume is a blank qcow2 with no image source.
	// 刀6「增删硬件」的第二块盘是 data 盘——这是 e2e 首次走 data 创建路径。
	if vol.Spec.Role == volumev1.VolumeRoleData {
		return c.reconcileCreateData(ctx, ev, vol)
	}
	return c.reconcileCreateRoot(ctx, ev, vol)
}

// reconcileCreateRoot derives an independent root qcow2 by copying this node's
// cached Image bytes. It gates on the block pool and Image readiness first,
// rejects unsupported root formats before waiting for node cache readiness, then
// streams the verified cache file into CreateRootVolumeFromReader.
func (c *VolumeController) reconcileCreateRoot(ctx context.Context, ev controller.Event, vol volumev1.Volume) (controller.ReconcileResult, error) {
	logger := zerolog.Ctx(ctx)

	img, ready, err := c.gateRootDependencies(ctx, vol)
	if err != nil {
		// A dependency read failed transiently: requeue without patching failed
		// (the volume itself has not failed; readiness could not be assessed).
		return controller.Requeue(), err
	}
	if !ready {
		logger.Info().
			Str("key", ev.Key).
			Msg("volume dependencies not all ready; requeueing")
		return controller.Requeue(), nil
	}

	// Format authority = Image.Spec.Format. An unmappable format is a permanent
	// config error mapped before any byte work: report failed, do not requeue.
	imageFormat, err := mapImageFormat(img.Spec.Format)
	if err != nil {
		if perr := c.reportFailure(ctx, vol.Name, vol.Status, err); perr != nil {
			return controller.Done(), fmt.Errorf("volume controller: map image format for %q failed and status report failed: %w", vol.Name, errors.Join(err, perr))
		}
		logger.Error().Err(err).Str("key", ev.Key).Msg("volume image format unmappable (config error); not requeuing")
		return controller.Done(), nil
	}

	cache, cacheReady, err := nodeImageCacheReady(img, vol.NodeName)
	if err != nil {
		return controller.Requeue(), err
	}
	if !cacheReady {
		logger.Info().
			Str("key", ev.Key).
			Msg("volume node image cache not ready; requeueing")
		return controller.Requeue(), nil
	}

	created, err := c.createRootVolume(ctx, vol, cache, imageFormat)
	if err != nil {
		logger.Info().Err(err).Str("key", ev.Key).Msg("root volume image bytes not ready; requeueing")
		return controller.Requeue(), err
	}

	return c.patchCreatedVolumeReady(ctx, ev, vol, created, "volume ready")
}

// reconcileCreateData creates a blank data-disk qcow2. Unlike a root volume it
// derives no bytes from an image, so it gates only on its own block
// StoragePool (no image / image file pool refs — VolumeSpec.Validate forbids a
// data volume from carrying them) and calls CreateDataVolume.
func (c *VolumeController) reconcileCreateData(ctx context.Context, ev controller.Event, vol volumev1.Volume) (controller.ReconcileResult, error) {
	logger := zerolog.Ctx(ctx)

	poolReady, err := c.storagePoolReady(ctx, vol.Spec.PoolRef)
	if err != nil {
		// Transient read failure: requeue without patching failed.
		return controller.Requeue(), err
	}
	if !poolReady {
		logger.Info().
			Str("key", ev.Key).
			Msg("data volume block pool not ready; requeueing")
		return controller.Requeue(), nil
	}

	created, err := c.volumes.CreateDataVolume(ctx, storage.CreateDataVolumeRequest{
		VMID:          vol.Spec.VMRef,
		VMName:        vol.Spec.VMName,
		PoolName:      vol.Spec.PoolRef,
		Name:          vol.Name,
		DiskIndex:     vol.Spec.DiskIndex,
		CapacityBytes: vol.Spec.CapacityBytes,
	})
	if err != nil {
		if perr := c.reportFailure(ctx, vol.Name, vol.Status, err); perr != nil {
			return controller.Requeue(), fmt.Errorf("volume controller: create data volume %q failed and status report failed: %w", vol.Name, errors.Join(err, perr))
		}
		return controller.Requeue(), fmt.Errorf("volume controller: create data volume %q: %w", vol.Name, err)
	}

	return c.patchCreatedVolumeReady(ctx, ev, vol, created, "data volume ready")
}

// patchCreatedVolumeReady extracts the host path from a freshly created volume
// and patches a ready status carrying it. A created volume without a host path
// is an internal inconsistency treated as transient (report failed + requeue).
// Shared by the root and data create paths.
func (c *VolumeController) patchCreatedVolumeReady(ctx context.Context, ev controller.Event, vol volumev1.Volume, created volume.Volume, readyMsg string) (controller.ReconcileResult, error) {
	path := created.Context[local.PathKey]
	if path == "" {
		missing := fmt.Errorf("volume controller: created volume %q reports no host path", vol.Name)
		if perr := c.reportFailure(ctx, vol.Name, vol.Status, missing); perr != nil {
			return controller.Requeue(), fmt.Errorf("volume controller: %q missing path and status report failed: %w", vol.Name, errors.Join(missing, perr))
		}
		return controller.Requeue(), missing
	}

	status := volumev1.VolumeStatus{
		Phase:      volumev1.VolumePhaseReady,
		VolumePath: path,
	}
	if err := c.patchStatus(ctx, vol.Name, vol.Status, status); err != nil {
		return controller.Requeue(), err
	}

	zerolog.Ctx(ctx).Info().
		Str("key", ev.Key).
		Str("volumePath", path).
		Msg(readyMsg)
	return controller.Done(), nil
}

// gate reports whether every referenced dependency is live Ready. When all are
// ready it returns the referenced Image so the caller can read its authoritative
// format. ready=false with a nil error means a dependency is missing or not yet
// ready (wait); a non-nil error is a transient read failure.
func (c *VolumeController) gateRootDependencies(ctx context.Context, vol volumev1.Volume) (imagev1.Image, bool, error) {
	poolReady, err := c.storagePoolReady(ctx, vol.Spec.PoolRef)
	if err != nil {
		return imagev1.Image{}, false, err
	}
	if !poolReady {
		return imagev1.Image{}, false, nil
	}

	img, imgReady, err := c.imageReady(ctx, vol.Spec.ImageRef)
	if err != nil {
		return imagev1.Image{}, false, err
	}
	if !imgReady {
		return imagev1.Image{}, false, nil
	}

	return img, true, nil
}

// storagePoolReady reads the named StoragePool and reports whether its observed
// phase is Ready. A missing object (ErrNotFound) is reported as not-ready with a
// nil error so the caller waits; any other read/decode failure is transient.
func (c *VolumeController) storagePoolReady(ctx context.Context, name string) (bool, error) {
	raw, err := c.client.Get(ctx, string(metav1.KindStoragePool), name)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("volume controller: get StoragePool %q: %w", name, err)
	}
	var sp storagepoolv1.StoragePool
	if err := json.Unmarshal(raw, &sp); err != nil {
		return false, fmt.Errorf("volume controller: decode StoragePool %q: %w", name, err)
	}
	return sp.Status.Phase == storagepoolv1.PoolPhaseReady, nil
}

// imageReady reads the named Image and reports whether its observed phase is
// Ready, returning the decoded object so its format can be read. A missing
// object (ErrNotFound) is not-ready with a nil error; any other failure is
// transient.
func (c *VolumeController) imageReady(ctx context.Context, name string) (imagev1.Image, bool, error) {
	raw, err := c.client.Get(ctx, string(metav1.KindImage), name)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			return imagev1.Image{}, false, nil
		}
		return imagev1.Image{}, false, fmt.Errorf("volume controller: get Image %q: %w", name, err)
	}
	var img imagev1.Image
	if err := json.Unmarshal(raw, &img); err != nil {
		return imagev1.Image{}, false, fmt.Errorf("volume controller: decode Image %q: %w", name, err)
	}
	return img, img.Status.Phase == imagev1.ImagePhaseReady, nil
}

func nodeImageCacheReady(img imagev1.Image, nodeName string) (imagev1.NodeCacheStatus, bool, error) {
	for _, cache := range img.Status.NodeCaches {
		if cache.NodeName != nodeName {
			continue
		}
		if cache.Phase != imagev1.ImageCachePhaseReady || cache.CachedPath == "" {
			return imagev1.NodeCacheStatus{}, false, nil
		}
		if cache.SHA256 != img.Spec.SHA256 {
			return imagev1.NodeCacheStatus{}, false, fmt.Errorf("volume controller: image %q cache for node %q sha256 %q does not match spec %q", img.Name, nodeName, cache.SHA256, img.Spec.SHA256)
		}
		if cache.SizeBytes != img.Spec.DeclaredSizeBytes {
			return imagev1.NodeCacheStatus{}, false, fmt.Errorf("volume controller: image %q cache for node %q size %d does not match spec %d", img.Name, nodeName, cache.SizeBytes, img.Spec.DeclaredSizeBytes)
		}
		return cache, true, nil
	}
	return imagev1.NodeCacheStatus{}, false, nil
}

// createRootVolume opens the image byte reader and derives an independent root
// volume from it. The reader is always closed after the copy completes; its
// close error is joined with any create error (项目铁律: 不吞 close 错误).
func (c *VolumeController) createRootVolume(ctx context.Context, vol volumev1.Volume, cache imagev1.NodeCacheStatus, format diskformat.Format) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	reader, err := c.openCachedImage(cache.CachedPath)
	if err != nil {
		return volume.Volume{}, err
	}
	created, createErr := c.volumes.CreateRootVolumeFromReader(ctx, storage.CreateRootVolumeFromReaderRequest{
		VMID:          vol.Spec.VMRef,
		VMName:        vol.Spec.VMName,
		PoolName:      vol.Spec.PoolRef,
		Name:          vol.Name,
		DiskIndex:     vol.Spec.DiskIndex,
		CapacityBytes: vol.Spec.CapacityBytes,
		Format:        format,
		Reader:        reader,
	})
	closeErr := reader.Close()
	if createErr != nil || closeErr != nil {
		return volume.Volume{}, fmt.Errorf("volume controller: create root volume %q from cached image %q: %w", vol.Name, cache.CachedPath, errors.Join(createErr, closeErr))
	}
	return created, nil
}

func (c *VolumeController) openCachedImage(path string) (io.ReadCloser, error) {
	if !c.cacheRootValid {
		return nil, fmt.Errorf("volume controller: image cache root must be an absolute path")
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, fmt.Errorf("volume controller: cached image path %q escapes cache root %q", path, c.cacheRoot)
	}
	if !pathWithin(c.cacheRoot, clean) {
		return nil, fmt.Errorf("volume controller: cached image path %q escapes cache root %q", clean, c.cacheRoot)
	}
	reader, err := c.openCachedFile(c.cacheRoot, clean)
	if err != nil {
		return nil, fmt.Errorf("volume controller: open cached image %q: %w", clean, err)
	}
	openedInfo, err := reader.Stat()
	if err != nil {
		closeErr := reader.Close()
		return nil, fmt.Errorf("volume controller: stat opened cached image %q: %w", clean, errors.Join(err, closeErr))
	}
	if openedInfo.Mode()&fs.ModeType != 0 || !openedInfo.Mode().IsRegular() {
		closeErr := reader.Close()
		return nil, fmt.Errorf("volume controller: opened cached image %q is not a regular file: %w", clean, closeErr)
	}
	return reader, nil
}

func cleanAbsoluteRoot(root string) (string, bool) {
	if root == "" || !filepath.IsAbs(root) {
		return "", false
	}
	clean := filepath.Clean(root)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved, true
	}
	return clean, true
}

// teardown deletes the live volume from its block pool. The storage layer keys a
// volume by a SERVER-DERIVED id (NOT the object name): the create path stores it
// under volume.ID(fmt.Sprintf("%s-%s-%d", VMRef, role, DiskIndex)) — see
// storage.VolumeService.CreateRootVolumeFromReader (internal/storage/service.go).
// pool.Service.DeleteVolume looks that derived id up with no name fallback, so
// teardown MUST reconstruct the SAME key or the delete misses, returns the
// tolerated volume.ErrVolumeNotFound, drops the finalizer, and silently leaks the
// qcow2 file + pool capacity reservation. The pool comes from the spec's PoolRef.
// Deleting an already-gone volume (volume.ErrVolumeNotFound) is treated as an
// idempotent success so a re-driven teardown still progresses to dropping the
// finalizer. Any other error (e.g. volume.ErrVolumeInUse — still attached to a
// running VM) is a real conflict: it is returned so the finalizer is kept and the
// reconcile requeued, letting the referencing VM tear down first.
func (c *VolumeController) teardown(ctx context.Context, vol volumev1.Volume) error {
	volID := deriveVolumeID(vol.Spec)
	if err := c.volumes.DeleteVolume(ctx, storage.DeleteVolumeRequest{
		VolumeID: volID,
		PoolName: vol.Spec.PoolRef,
	}); err != nil && !errors.Is(err, volume.ErrVolumeNotFound) {
		return fmt.Errorf("volume controller: delete volume %q (id %q) from pool %q: %w", vol.Name, volID, vol.Spec.PoolRef, err)
	}
	return nil
}

// mapVolumeRole converts an apis volume role to the storage volume role,
// converting explicitly (no blind string conversion, mirroring mapImageFormat) so
// teardown derives the SAME key the create path stored the volume under. The two
// role types share string values ("root"/"data"); the explicit switch keeps the
// conversion typed and total over the known roles.
func mapVolumeRole(r volumev1.VolumeRole) volume.Role {
	switch r {
	case volumev1.VolumeRoleRoot:
		return volume.RoleRoot
	case volumev1.VolumeRoleData:
		return volume.RoleData
	default:
		// Unreachable for a persisted object: VolumeSpec.Validate rejects unknown
		// roles before a finalizer is injected. Fall back to the faithful string
		// conversion so an unexpected role still yields a deterministic key.
		return volume.Role(string(r))
	}
}

// deriveVolumeID reconstructs the SERVER-DERIVED storage key for a volume object:
// volume.ID(fmt.Sprintf("%s-%s-%d", VMRef, role, DiskIndex)). This is the single
// source of how the storage layer keys a volume (storage.VolumeService stores it
// under this id on the create path); both the volume controller teardown and the
// snapshot controller's volume resolution MUST derive it identically or they miss
// the qcow2 the object owns. Centralizing it here keeps the derivation from being
// re-spelled per controller (a drifted formula would silently target the wrong
// file with no compile error).
func deriveVolumeID(spec volumev1.VolumeSpec) volume.ID {
	return volume.ID(fmt.Sprintf("%s-%s-%d", spec.VMRef, mapVolumeRole(spec.Role), spec.DiskIndex))
}

// reportFailure patches a failed status carrying cause's message, skipping the
// PATCH when the observed status already matches (no-op guard).
func (c *VolumeController) reportFailure(ctx context.Context, name string, observed volumev1.VolumeStatus, cause error) error {
	return c.patchStatus(ctx, name, observed, volumev1.VolumeStatus{
		Phase:   volumev1.VolumePhaseFailed,
		Message: cause.Error(),
	})
}

// patchStatus marshals desired and PATCHes it to the master's /status
// sub-resource, but only when it differs from observed (the status carried by the
// watched object). Skipping an identical PATCH breaks the status→MODIFIED→watch→
// reconcile→PATCH feedback loop that would otherwise spin every reconcile (level-
// triggered idempotence). The Status structs are comparable (scalar fields only),
// so == is a sound equality test.
func (c *VolumeController) patchStatus(ctx context.Context, name string, observed, desired volumev1.VolumeStatus) error {
	if observed == desired {
		return nil
	}
	body, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("volume controller: marshal status for %q: %w", name, err)
	}
	if _, err := c.client.PatchStatus(ctx, c.Kind(), name, body); err != nil {
		return fmt.Errorf("volume controller: patch status for %q: %w", name, err)
	}
	return nil
}
