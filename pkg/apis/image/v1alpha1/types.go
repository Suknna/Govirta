// Package v1alpha1 defines the Image API object. The image format here is the
// authoritative source-byte format used by the volume controller when deriving
// an independent root volume (spec: Format 权威 = Image.Spec.Format).
package v1alpha1

import (
	"errors"
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// ImageFormat is the byte format of the source image. Mirrors
// internal/storage/diskformat by string but defined independently.
type ImageFormat string

const (
	// ImageFormatQCOW2 identifies QEMU qcow2 image bytes.
	ImageFormatQCOW2 ImageFormat = "qcow2"
	// ImageFormatRaw identifies raw disk image bytes.
	ImageFormatRaw ImageFormat = "raw"
)

// Valid reports whether f is a known image format.
func (f ImageFormat) Valid() bool {
	return f == ImageFormatQCOW2 || f == ImageFormatRaw
}

// ImageSourceType names how the node controller fetches image bytes. The first
// slice supports only an explicit local file path and an HTTP(S) URL; no
// registry/container-image source (spec §3 镜像分发).
type ImageSourceType string

const (
	// ImageSourceFile fetches bytes from a node-local file path.
	ImageSourceFile ImageSourceType = "file"
	// ImageSourceHTTP fetches bytes from an http(s) URL.
	ImageSourceHTTP ImageSourceType = "http"
)

// Valid reports whether t is a known source type.
func (t ImageSourceType) Valid() bool {
	return t == ImageSourceFile || t == ImageSourceHTTP
}

// ImagePhase is the observed lifecycle phase. Mirrors internal/storage/pool
// ImageState (pending/ready/deleting) by string but defined independently.
type ImagePhase string

const (
	// ImagePhasePending means bytes are reserved but not yet fully fetched.
	ImagePhasePending ImagePhase = "pending"
	// ImagePhaseReady means bytes are committed and available for reads.
	ImagePhaseReady ImagePhase = "ready"
	// ImagePhaseDeleting means a ready image is being removed.
	ImagePhaseDeleting ImagePhase = "deleting"
	// ImagePhaseFailed means the fetch failed.
	ImagePhaseFailed ImagePhase = "failed"
)

// ImageSource is the explicit external byte source for an image.
type ImageSource struct {
	Type     ImageSourceType `json:"type"`
	Location string          `json:"location"` // file path (file) or url (http)
}

// ErrInvalidSpec is returned when an ImageSpec is not internally valid.
var ErrInvalidSpec = errors.New("image: invalid spec")

// ImageSpec is the desired state of an image.
type ImageSpec struct {
	FilePoolRef       string      `json:"filePoolRef"` // file pool object name
	Source            ImageSource `json:"source"`
	Format            ImageFormat `json:"format"`
	DeclaredSizeBytes int64       `json:"declaredSizeBytes"`
}

// Validate reports whether the spec carries explicit, internally consistent fields.
func (s ImageSpec) Validate() error {
	if s.FilePoolRef == "" {
		return fmt.Errorf("%w: filePoolRef is required", ErrInvalidSpec)
	}
	if !s.Source.Type.Valid() {
		return fmt.Errorf("%w: source type %q", ErrInvalidSpec, s.Source.Type)
	}
	if s.Source.Location == "" {
		return fmt.Errorf("%w: source location is required", ErrInvalidSpec)
	}
	if !s.Format.Valid() {
		return fmt.Errorf("%w: format %q", ErrInvalidSpec, s.Format)
	}
	if s.DeclaredSizeBytes <= 0 {
		return fmt.Errorf("%w: declaredSizeBytes must be positive", ErrInvalidSpec)
	}
	return nil
}

// ImageStatus is the observed state written by the node Image controller.
type ImageStatus struct {
	Phase          ImagePhase `json:"phase"`
	LocalSizeBytes int64      `json:"localSizeBytes,omitempty"`
	Message        string     `json:"message,omitempty"`
}

// Image is a first-class image API object. The caller provides ObjectMeta.Name
// as the image ID; a duplicate ID in the same file pool is rejected downstream.
type Image struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              ImageSpec   `json:"spec"`
	Status            ImageStatus `json:"status"`
}
