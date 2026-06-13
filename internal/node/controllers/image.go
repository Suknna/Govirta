package controllers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/storage"
	"github.com/suknna/govirta/internal/storage/image"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// ImagePutter is retained only so older constructor call sites compile during
// the migration. Image resources are now driven by control-plane-created Tasks,
// not by the node ImageController.
type ImagePutter interface {
	PutImage(ctx context.Context, req storage.PutImageRequest) (image.ImageWriter, error)
	DeleteImage(ctx context.Context, req storage.DeleteImageRequest) error
}

var _ ImagePutter = (*storage.ImageService)(nil)

// ImageController is a migration no-op. Node image cache work is executed by
// TaskController via CacheImageNode/DeleteCachedImageNode tasks.
type ImageController struct{}

var _ controller.Controller = (*ImageController)(nil)

// NewImageController keeps the old construction seam available while disabling
// Image-resource execution on nodes.
func NewImageController(_ ImagePutter, _ StatusReporter, _ *http.Client, _ string) *ImageController {
	return &ImageController{}
}

// Kind is the API kind this controller would watch if registered.
func (c *ImageController) Kind() string {
	return string(metav1.KindImage)
}

// Reconcile intentionally performs no work; Image state is no longer node-owned.
func (c *ImageController) Reconcile(ctx context.Context, _ controller.Event) (controller.ReconcileResult, error) {
	if err := ctx.Err(); err != nil {
		return controller.Done(), fmt.Errorf("image controller: context done before reconcile: %w", err)
	}
	return controller.Done(), nil
}
