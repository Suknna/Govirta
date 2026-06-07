package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

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
// needs: derive an independent root qcow2 volume from an image byte reader.
// *storage.VolumeService satisfies it (积木式 + 可测).
type RootVolumeCreator interface {
	CreateRootVolumeFromReader(ctx context.Context, req storage.CreateRootVolumeFromReaderRequest) (volume.Volume, error)
}

// ImageGetter is the narrow slice of the image service the controller needs:
// open a ready image for reading from a file pool. *storage.ImageService
// satisfies it.
type ImageGetter interface {
	GetImage(ctx context.Context, req storage.GetImageRequest) (io.ReadCloser, error)
}

// DependencyReader is the narrow slice of the master client the controller
// needs: read a referenced object's status for gating and PATCH this volume's
// own status. *client.Client satisfies it. A Get that maps to client.ErrNotFound
// means the dependency does not exist yet and the volume must wait (requeue).
type DependencyReader interface {
	Get(ctx context.Context, kind, name string) ([]byte, error)
	PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error)
}

// 编译期证明真实生产类型满足窄接口。
var (
	_ RootVolumeCreator     = (*storage.VolumeService)(nil)
	_ ImageGetter           = (*storage.ImageService)(nil)
	_ DependencyReader      = (*client.Client)(nil)
	_ controller.Controller = (*VolumeController)(nil)
)

// VolumeController reconciles Volume objects: it gates on the referenced block
// StoragePool, the image file StoragePool, and the source Image all being live
// Ready, then streams the image bytes (via ImageService.GetImage) into
// VolumeService.CreateRootVolumeFromReader to derive an independent qcow2 root
// volume, and reports a ready status carrying the volume's host path.
//
// The byte format authority is the referenced Image's spec format
// (Image.Spec.Format), mapped explicitly to a storage diskformat.
type VolumeController struct {
	volumes RootVolumeCreator
	images  ImageGetter
	client  DependencyReader
}

// NewVolumeController wires a VolumeController against the volume service, the
// image service, and the master dependency/status client.
func NewVolumeController(volumes RootVolumeCreator, images ImageGetter, client DependencyReader) *VolumeController {
	return &VolumeController{volumes: volumes, images: images, client: client}
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
//   - GetImage / CreateRootVolumeFromReader fails → transient: patch failed and
//     requeue with a wrapped error.
func (c *VolumeController) Reconcile(ctx context.Context, ev controller.Event) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("volume controller: context done before reconcile: %w", err)
	}

	logger := zerolog.Ctx(ctx)

	if ev.Type == controller.EventDeleted {
		logger.Info().
			Str("kind", c.Kind()).
			Str("key", ev.Key).
			Msg("volume deleted; delete is a no-op in this slice")
		return false, nil
	}

	var vol volumev1.Volume
	if err := json.Unmarshal(ev.Object, &vol); err != nil {
		return false, fmt.Errorf("volume controller: decode object %q: %w", ev.Key, err)
	}

	img, ready, err := c.gate(ctx, vol)
	if err != nil {
		// A dependency read failed transiently: requeue without patching failed
		// (the volume itself has not failed; readiness could not be assessed).
		return true, err
	}
	if !ready {
		logger.Info().
			Str("key", ev.Key).
			Msg("volume dependencies not all ready; requeueing")
		return true, nil
	}

	// Format authority = Image.Spec.Format. An unmappable format is a permanent
	// config error mapped before any byte work: report failed, do not requeue.
	imageFormat, err := mapImageFormat(img.Spec.Format)
	if err != nil {
		if perr := c.reportFailure(ctx, vol.Name, err); perr != nil {
			return false, fmt.Errorf("volume controller: map image format for %q failed and status report failed: %w", vol.Name, errors.Join(err, perr))
		}
		logger.Error().Err(err).Str("key", ev.Key).Msg("volume image format unmappable (config error); not requeuing")
		return false, nil
	}

	created, err := c.createRootVolume(ctx, vol, imageFormat)
	if err != nil {
		if perr := c.reportFailure(ctx, vol.Name, err); perr != nil {
			return true, fmt.Errorf("volume controller: create root volume %q failed and status report failed: %w", vol.Name, errors.Join(err, perr))
		}
		return true, fmt.Errorf("volume controller: create root volume %q: %w", vol.Name, err)
	}

	path := created.Context[local.PathKey]
	if path == "" {
		// A created volume without a host path is an internal inconsistency:
		// treat it as transient (report failed and requeue).
		missing := fmt.Errorf("volume controller: created volume %q reports no host path", vol.Name)
		if perr := c.reportFailure(ctx, vol.Name, missing); perr != nil {
			return true, fmt.Errorf("volume controller: %q missing path and status report failed: %w", vol.Name, errors.Join(missing, perr))
		}
		return true, missing
	}

	status := volumev1.VolumeStatus{
		Phase:      volumev1.VolumePhaseReady,
		VolumePath: path,
	}
	if err := c.patchStatus(ctx, vol.Name, status); err != nil {
		return true, err
	}

	logger.Info().
		Str("key", ev.Key).
		Str("volumePath", path).
		Msg("volume ready")
	return false, nil
}

// gate reports whether every referenced dependency is live Ready. When all are
// ready it returns the referenced Image so the caller can read its authoritative
// format. ready=false with a nil error means a dependency is missing or not yet
// ready (wait); a non-nil error is a transient read failure.
func (c *VolumeController) gate(ctx context.Context, vol volumev1.Volume) (imagev1.Image, bool, error) {
	poolReady, err := c.storagePoolReady(ctx, vol.Spec.PoolRef)
	if err != nil {
		return imagev1.Image{}, false, err
	}
	if !poolReady {
		return imagev1.Image{}, false, nil
	}

	filePoolReady, err := c.storagePoolReady(ctx, vol.Spec.ImageFilePoolRef)
	if err != nil {
		return imagev1.Image{}, false, err
	}
	if !filePoolReady {
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

// createRootVolume opens the image byte reader and derives an independent root
// volume from it. The reader is always closed after the copy completes; its
// close error is joined with any create error (项目铁律: 不吞 close 错误).
func (c *VolumeController) createRootVolume(ctx context.Context, vol volumev1.Volume, format diskformat.Format) (volume.Volume, error) {
	reader, err := c.images.GetImage(ctx, storage.GetImageRequest{
		PoolName: vol.Spec.ImageFilePoolRef,
		ImageID:  vol.Spec.ImageRef,
	})
	if err != nil {
		return volume.Volume{}, fmt.Errorf("volume controller: get image %q from pool %q: %w", vol.Spec.ImageRef, vol.Spec.ImageFilePoolRef, err)
	}

	created, createErr := c.volumes.CreateRootVolumeFromReader(ctx, storage.CreateRootVolumeFromReaderRequest{
		VMID:          vol.Spec.VMRef,
		VMName:        vol.Spec.VMName,
		PoolName:      vol.Spec.PoolRef,
		Name:          vol.Name,
		DiskIndex:     vol.Spec.DiskIndex,
		CapacityBytes: vol.Spec.CapacityBytes,
		Reader:        reader,
		Format:        format,
	})

	// Close after the copy completes; the close error must not be swallowed.
	var closeErr error
	if cerr := reader.Close(); cerr != nil {
		closeErr = fmt.Errorf("volume controller: close image reader for %q: %w", vol.Spec.ImageRef, cerr)
	}

	if createErr != nil || closeErr != nil {
		return created, errors.Join(createErr, closeErr)
	}
	return created, nil
}

// reportFailure patches a failed status carrying cause's message.
func (c *VolumeController) reportFailure(ctx context.Context, name string, cause error) error {
	return c.patchStatus(ctx, name, volumev1.VolumeStatus{
		Phase:   volumev1.VolumePhaseFailed,
		Message: cause.Error(),
	})
}

// patchStatus marshals status and PATCHes it to the master's /status sub-resource.
func (c *VolumeController) patchStatus(ctx context.Context, name string, status volumev1.VolumeStatus) error {
	body, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("volume controller: marshal status for %q: %w", name, err)
	}
	if _, err := c.client.PatchStatus(ctx, c.Kind(), name, body); err != nil {
		return fmt.Errorf("volume controller: patch status for %q: %w", name, err)
	}
	return nil
}
