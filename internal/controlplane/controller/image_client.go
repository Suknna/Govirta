package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
	"github.com/suknna/govirta/internal/controlplane/store"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

type ImageClient struct {
	store store.Store
}

func NewImageClient(st store.Store) *ImageClient {
	return &ImageClient{store: st}
}

func (c *ImageClient) ListImages(ctx context.Context) ([]imagev1.Image, error) {
	if c == nil || c.store == nil {
		return nil, fmt.Errorf("controlplane image controller: image store is required")
	}
	raws, err := c.store.List(ctx, imageKey(""))
	if err != nil {
		return nil, fmt.Errorf("controlplane image controller: list images: %w", err)
	}
	images := make([]imagev1.Image, 0, len(raws))
	for _, raw := range raws {
		img, err := decodeStoredImage(raw)
		if err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	return images, nil
}

func (c *ImageClient) PatchImage(ctx context.Context, image imagev1.Image) (imagev1.Image, error) {
	if c == nil || c.store == nil {
		return imagev1.Image{}, fmt.Errorf("controlplane image controller: image store is required")
	}
	if err := image.Spec.Validate(); err != nil {
		return imagev1.Image{}, err
	}
	if err := image.Status.Validate(); err != nil {
		return imagev1.Image{}, err
	}
	key := imageKey(image.Name)
	for attempt := 0; attempt < 3; attempt++ {
		raw, err := c.store.Get(ctx, key)
		if err != nil {
			return imagev1.Image{}, fmt.Errorf("controlplane image controller: get image %q: %w", image.Name, err)
		}
		current, err := decodeStoredImage(raw)
		if err != nil {
			return imagev1.Image{}, err
		}
		merged, shouldWrite, err := mergeImageControllerFields(current, image)
		if err != nil {
			return imagev1.Image{}, err
		}
		if !shouldWrite {
			return current, nil
		}
		if shouldHardDeleteImage(merged) {
			if err := c.store.DeleteIfVersion(ctx, key, raw.ResourceVersion); err != nil {
				if errors.Is(err, store.ErrRevisionConflict) {
					continue
				}
				return imagev1.Image{}, fmt.Errorf("controlplane image controller: delete finalized image %q: %w", image.Name, err)
			}
			return merged, nil
		}
		data, err := json.Marshal(merged)
		if err != nil {
			return imagev1.Image{}, fmt.Errorf("controlplane image controller: marshal image %q: %w", image.Name, err)
		}
		written, err := c.store.Put(ctx, key, data, raw.ResourceVersion)
		if err == nil {
			return decodeStoredImage(written)
		}
		if errors.Is(err, store.ErrRevisionConflict) {
			continue
		}
		return imagev1.Image{}, fmt.Errorf("controlplane image controller: patch image %q: %w", image.Name, err)
	}
	return imagev1.Image{}, fmt.Errorf("controlplane image controller: patch image %q: %w", image.Name, store.ErrRevisionConflict)
}

func mergeImageControllerFields(current imagev1.Image, desired imagev1.Image) (imagev1.Image, bool, error) {
	if current.Name != desired.Name || current.UID != desired.UID {
		return imagev1.Image{}, false, fmt.Errorf("controlplane image controller: image identity changed while patching %q", desired.Name)
	}
	if !reflect.DeepEqual(current.Spec, desired.Spec) {
		return current, false, nil
	}
	if desired.DeletionTimestamp == "" && current.DeletionTimestamp != "" {
		return current, false, nil
	}

	merged := current
	merged.Status = desired.Status
	desiredHasFinalizer := hasImageCacheFinalizer(desired.Finalizers)
	currentHasFinalizer := hasImageCacheFinalizer(current.Finalizers)
	if desiredHasFinalizer && !currentHasFinalizer {
		merged.Finalizers = append(append([]metav1.Finalizer(nil), current.Finalizers...), metav1.FinalizerImageCache)
	}
	if !desiredHasFinalizer && currentHasFinalizer {
		merged.Finalizers = removeFinalizerCopy(current.Finalizers, metav1.FinalizerImageCache)
	}
	merged.ResourceVersion = ""
	return merged, !reflect.DeepEqual(current.Status, merged.Status) || !reflect.DeepEqual(current.Finalizers, merged.Finalizers), nil
}

func hasImageCacheFinalizer(finalizers []metav1.Finalizer) bool {
	for _, finalizer := range finalizers {
		if finalizer == metav1.FinalizerImageCache {
			return true
		}
	}
	return false
}

func shouldHardDeleteImage(image imagev1.Image) bool {
	return image.DeletionTimestamp != "" && len(image.Finalizers) == 0
}

func removeFinalizerCopy(finalizers []metav1.Finalizer, target metav1.Finalizer) []metav1.Finalizer {
	kept := make([]metav1.Finalizer, 0, len(finalizers))
	for _, finalizer := range finalizers {
		if finalizer != target {
			kept = append(kept, finalizer)
		}
	}
	return kept
}

func decodeStoredImage(raw store.RawObject) (imagev1.Image, error) {
	var image imagev1.Image
	if err := json.Unmarshal(raw.Value, &image); err != nil {
		return imagev1.Image{}, fmt.Errorf("controlplane image controller: decode image %q: %w", raw.Key, err)
	}
	image.ResourceVersion = raw.ResourceVersion
	if err := image.Spec.Validate(); err != nil {
		return imagev1.Image{}, fmt.Errorf("controlplane image controller: stored image %q: %w", raw.Key, err)
	}
	if err := image.Status.Validate(); err != nil {
		return imagev1.Image{}, fmt.Errorf("controlplane image controller: stored image %q: %w", raw.Key, err)
	}
	return image, nil
}

func imageKey(name string) string {
	return admission.StoreKey(metav1.KindImage, name)
}
