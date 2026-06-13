package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane/imagestore"
	"github.com/suknna/govirta/internal/controlplane/store"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

type ImageControllerConfig struct {
	NodeNames  []string
	CacheRoot  string
	SyncPeriod time.Duration
}

type ImageController struct {
	images     *ImageClient
	tasks      *TaskClient
	imageStore imagestore.Store
	config     ImageControllerConfig
}

func NewImageController(st store.Store, imageStore imagestore.Store, cfg ImageControllerConfig) *ImageController {
	return &ImageController{images: NewImageClient(st), tasks: NewTaskClient(st), imageStore: imageStore, config: cfg}
}

func (c *ImageController) Run(ctx context.Context) error {
	if err := c.validateConfig(); err != nil {
		return err
	}
	log := zerolog.Ctx(ctx).With().Str("component", "image-controller").Logger()
	if err := c.Reconcile(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(c.config.SyncPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.Reconcile(ctx); err != nil {
				log.Error().Err(err).Str("operation", "reconcile_images").Str("outcome", "failure").Msg("image controller reconcile failed")
				return err
			}
		}
	}
}

func (c *ImageController) Reconcile(ctx context.Context) error {
	if err := c.validateConfig(); err != nil {
		return err
	}
	images, err := c.images.ListImages(ctx)
	if err != nil {
		return err
	}
	for _, image := range images {
		if image.DeletionTimestamp != "" {
			if err := c.reconcileDeleting(ctx, image); err != nil {
				return err
			}
			continue
		}
		if err := c.reconcileActive(ctx, image); err != nil {
			return err
		}
	}
	return nil
}

func (c *ImageController) validateConfig() error {
	if len(c.config.NodeNames) == 0 || c.config.CacheRoot == "" || c.config.SyncPeriod <= 0 {
		return fmt.Errorf("controlplane image controller: NodeNames, CacheRoot, and positive SyncPeriod are required")
	}
	if c.images == nil || c.tasks == nil || c.imageStore == nil {
		return fmt.Errorf("controlplane image controller: image client, task client, and image store are required")
	}
	return nil
}

func (c *ImageController) reconcileActive(ctx context.Context, image imagev1.Image) error {
	changed := ensureImageFinalizer(&image, metav1.FinalizerImageCache)
	created, err := c.ensureCacheTasks(ctx, image)
	if err != nil {
		return err
	}
	status := aggregateImageStatus(image, c.config.NodeNames, created)
	if !reflect.DeepEqual(image.Status, status) {
		image.Status = status
		changed = true
	}
	if changed {
		_, err := c.images.PatchImage(ctx, image)
		return err
	}
	return nil
}

func (c *ImageController) reconcileDeleting(ctx context.Context, image imagev1.Image) error {
	created, err := c.ensureDeleteTasks(ctx, image)
	if err != nil {
		return err
	}
	status, done := deletingImageStatus(image, c.config.NodeNames, created)
	changed := false
	if !reflect.DeepEqual(image.Status, status) {
		image.Status = status
		changed = true
	}
	if done {
		if err := c.imageStore.Delete(ctx, image.Name, image.Spec.Version, image.Spec.SHA256); err != nil {
			return fmt.Errorf("controlplane image controller: delete image store object %q/%q: %w", image.Name, image.Spec.Version, err)
		}
		if removeImageFinalizer(&image, metav1.FinalizerImageCache) {
			changed = true
		}
	}
	if changed {
		_, err := c.images.PatchImage(ctx, image)
		return err
	}
	return nil
}

func (c *ImageController) ensureCacheTasks(ctx context.Context, image imagev1.Image) (map[string]taskv1.Task, error) {
	tasks := make(map[string]taskv1.Task, len(c.config.NodeNames))
	for _, nodeName := range c.config.NodeNames {
		desired, err := cacheImageTask(image, nodeName, c.config.CacheRoot)
		if err != nil {
			return nil, err
		}
		stored, err := c.tasks.CreateOrGetTask(ctx, desired)
		if err != nil {
			return nil, err
		}
		if !cacheTaskMatchesImage(stored, image) {
			return nil, fmt.Errorf("controlplane image controller: task %q does not match image %q content identity", stored.Name, image.Name)
		}
		tasks[nodeName] = stored
	}
	return tasks, nil
}

func (c *ImageController) ensureDeleteTasks(ctx context.Context, image imagev1.Image) (map[string]taskv1.Task, error) {
	tasks := make(map[string]taskv1.Task, len(c.config.NodeNames))
	for _, nodeName := range c.config.NodeNames {
		desired, err := deleteCachedImageTask(image, nodeName, c.config.CacheRoot)
		if err != nil {
			return nil, err
		}
		stored, err := c.tasks.CreateOrGetTask(ctx, desired)
		if err != nil {
			return nil, err
		}
		if !deleteTaskMatchesImage(stored, image) {
			return nil, fmt.Errorf("controlplane image controller: task %q does not match delete image %q content identity", stored.Name, image.Name)
		}
		tasks[nodeName] = stored
	}
	return tasks, nil
}

func ensureImageFinalizer(image *imagev1.Image, finalizer metav1.Finalizer) bool {
	for _, existing := range image.Finalizers {
		if existing == finalizer {
			return false
		}
	}
	image.Finalizers = append(image.Finalizers, finalizer)
	return true
}

func removeImageFinalizer(image *imagev1.Image, finalizer metav1.Finalizer) bool {
	kept := image.Finalizers[:0]
	removed := false
	for _, existing := range image.Finalizers {
		if existing == finalizer {
			removed = true
			continue
		}
		kept = append(kept, existing)
	}
	image.Finalizers = kept
	return removed
}
