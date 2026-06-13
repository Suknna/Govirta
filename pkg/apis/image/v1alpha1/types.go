// Package v1alpha1 defines the Image API object. The image format here is the
// authoritative source-byte format used by the volume controller when deriving
// an independent root volume (spec: Format 权威 = Image.Spec.Format).
package v1alpha1

import (
	"errors"
	"fmt"
	"regexp"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ImageFormat is the byte format of the source image. Mirrors
// internal/storage/diskformat by string but defined independently.
type ImageFormat string

const (
	// ImageFormatQCOW2 identifies QEMU qcow2 image bytes.
	ImageFormatQCOW2 ImageFormat = "qcow2"
	// ImageFormatRaw identifies raw disk image bytes.
	ImageFormatRaw ImageFormat = "raw"
	// ImageFormatISO identifies ISO9660 image bytes for VM CD-ROM attachment.
	ImageFormatISO ImageFormat = "iso"
)

// Valid reports whether f is a known image format.
func (f ImageFormat) Valid() bool {
	return f == ImageFormatQCOW2 || f == ImageFormatRaw || f == ImageFormatISO
}

// ImageSourceType names how the image controller receives or fetches image bytes.
type ImageSourceType string

const (
	// ImageSourceUpload receives bytes through an explicit upload flow.
	ImageSourceUpload ImageSourceType = "upload"
	// ImageSourceHTTP fetches bytes from an http(s) URL.
	ImageSourceHTTP ImageSourceType = "http"
)

// Valid reports whether t is a known source type.
func (t ImageSourceType) Valid() bool {
	return t == ImageSourceUpload || t == ImageSourceHTTP
}

// ImagePhase is the observed lifecycle phase.
type ImagePhase string

const (
	// ImagePhasePending means bytes are reserved but not yet fully fetched.
	ImagePhasePending ImagePhase = "pending"
	// ImagePhaseCaching means node cache work is in progress.
	ImagePhaseCaching ImagePhase = "caching"
	// ImagePhaseReady means bytes are committed and available for reads.
	ImagePhaseReady ImagePhase = "ready"
	// ImagePhaseDeleting means a ready image is being removed.
	ImagePhaseDeleting ImagePhase = "deleting"
	// ImagePhaseFailed means the fetch failed.
	ImagePhaseFailed ImagePhase = "failed"
)

// Valid reports whether p is a known image phase.
func (p ImagePhase) Valid() bool {
	switch p {
	case ImagePhasePending, ImagePhaseCaching, ImagePhaseReady, ImagePhaseDeleting, ImagePhaseFailed:
		return true
	default:
		return false
	}
}

// ImageCachePhase is the observed lifecycle phase of one node-local Image cache.
type ImageCachePhase string

const (
	// ImageCachePhasePending means cache work is queued but not yet running.
	ImageCachePhasePending ImageCachePhase = "pending"
	// ImageCachePhaseCaching means node cache work is in progress.
	ImageCachePhaseCaching ImageCachePhase = "caching"
	// ImageCachePhaseReady means image bytes are cached and available on the node.
	ImageCachePhaseReady ImageCachePhase = "ready"
	// ImageCachePhaseDeleting means the node cache is being removed.
	ImageCachePhaseDeleting ImageCachePhase = "deleting"
	// ImageCachePhaseFailed means cache work failed.
	ImageCachePhaseFailed ImageCachePhase = "failed"
)

// Valid reports whether p is a known image cache phase.
func (p ImageCachePhase) Valid() bool {
	switch p {
	case ImageCachePhasePending, ImageCachePhaseCaching, ImageCachePhaseReady, ImageCachePhaseDeleting, ImageCachePhaseFailed:
		return true
	default:
		return false
	}
}

// ImageSource is the explicit external byte source for an image.
type ImageSource struct {
	Type     ImageSourceType `json:"type"`
	Location string          `json:"location"`
}

// TaskRef identifies the Task currently driving or that last drove a node cache.
type TaskRef struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// NodeCacheStatus is the observed per-node cache state for an Image version.
type NodeCacheStatus struct {
	NodeName   string          `json:"nodeName"`
	Phase      ImageCachePhase `json:"phase"`
	TaskRef    TaskRef         `json:"taskRef"`
	CachedPath string          `json:"cachedPath,omitempty"`
	SizeBytes  int64           `json:"sizeBytes,omitempty"`
	SHA256     string          `json:"sha256,omitempty"`
	Message    string          `json:"message,omitempty"`
}

// ErrInvalidSpec is returned when an ImageSpec is not internally valid.
var ErrInvalidSpec = errors.New("image: invalid spec")

// ErrInvalidStatus is returned when an ImageStatus is not internally valid.
var ErrInvalidStatus = errors.New("image: invalid status")

// ImageSpec is the desired state of an image.
type ImageSpec struct {
	Source            ImageSource `json:"source"`
	Format            ImageFormat `json:"format"`
	Version           string      `json:"version"`
	DeclaredSizeBytes int64       `json:"declaredSizeBytes"`
	SHA256            string      `json:"sha256"`
}

// Validate reports whether the spec carries explicit, internally consistent fields.
func (s ImageSpec) Validate() error {
	if !s.Source.Type.Valid() {
		return fmt.Errorf("%w: source type %q", ErrInvalidSpec, s.Source.Type)
	}
	if s.Source.Location == "" {
		return fmt.Errorf("%w: source location is required", ErrInvalidSpec)
	}
	if !s.Format.Valid() {
		return fmt.Errorf("%w: format %q", ErrInvalidSpec, s.Format)
	}
	if s.Version == "" {
		return fmt.Errorf("%w: version is required", ErrInvalidSpec)
	}
	if s.DeclaredSizeBytes <= 0 {
		return fmt.Errorf("%w: declaredSizeBytes must be positive", ErrInvalidSpec)
	}
	if !sha256Pattern.MatchString(s.SHA256) {
		return fmt.Errorf("%w: sha256 must be lowercase 64-hex", ErrInvalidSpec)
	}
	return nil
}

// ImageStatus is the control-plane aggregated observed state for Image caches.
type ImageStatus struct {
	Phase             ImagePhase        `json:"phase"`
	ObservedVersion   string            `json:"observedVersion,omitempty"`
	ObservedSHA256    string            `json:"observedSHA256,omitempty"`
	ObservedSizeBytes int64             `json:"observedSizeBytes,omitempty"`
	NodeCaches        []NodeCacheStatus `json:"nodeCaches,omitempty"`
	Message           string            `json:"message,omitempty"`
}

// Validate reports whether the status carries a known observed phase.
func (s ImageStatus) Validate() error {
	if !s.Phase.Valid() {
		return fmt.Errorf("%w: phase %q", ErrInvalidStatus, s.Phase)
	}
	for _, cache := range s.NodeCaches {
		if err := cache.Validate(); err != nil {
			return err
		}
	}
	if s.Phase == ImagePhaseReady {
		if s.ObservedVersion == "" {
			return fmt.Errorf("%w: observedVersion is required when ready", ErrInvalidStatus)
		}
		if !sha256Pattern.MatchString(s.ObservedSHA256) {
			return fmt.Errorf("%w: observedSha256 must be lowercase 64-hex when ready", ErrInvalidStatus)
		}
		if s.ObservedSizeBytes <= 0 {
			return fmt.Errorf("%w: observedSizeBytes must be positive when ready", ErrInvalidStatus)
		}
		if len(s.NodeCaches) == 0 {
			return fmt.Errorf("%w: ready status requires ready node cache", ErrInvalidStatus)
		}
		for _, cache := range s.NodeCaches {
			if cache.Phase != ImageCachePhaseReady {
				return fmt.Errorf("%w: ready status requires all node caches ready", ErrInvalidStatus)
			}
			if cache.SHA256 != s.ObservedSHA256 {
				return fmt.Errorf("%w: ready node cache sha256 must match observedSha256", ErrInvalidStatus)
			}
		}
	}
	if s.Phase == ImagePhaseFailed && s.Message == "" {
		return fmt.Errorf("%w: failed status requires message", ErrInvalidStatus)
	}
	return nil
}

// Validate reports whether the node cache status carries internally consistent observed state.
func (s NodeCacheStatus) Validate() error {
	if s.NodeName == "" {
		return fmt.Errorf("%w: node cache nodeName is required", ErrInvalidStatus)
	}
	if s.TaskRef.Name == "" {
		return fmt.Errorf("%w: node cache taskRef.name is required", ErrInvalidStatus)
	}
	if s.TaskRef.UID == "" {
		return fmt.Errorf("%w: node cache taskRef.uid is required", ErrInvalidStatus)
	}
	if !s.Phase.Valid() {
		return fmt.Errorf("%w: node cache phase %q", ErrInvalidStatus, s.Phase)
	}
	if s.Phase == ImageCachePhaseReady {
		if s.CachedPath == "" {
			return fmt.Errorf("%w: ready node cache requires cachedPath", ErrInvalidStatus)
		}
		if s.SizeBytes <= 0 {
			return fmt.Errorf("%w: ready node cache requires positive sizeBytes", ErrInvalidStatus)
		}
		if !sha256Pattern.MatchString(s.SHA256) {
			return fmt.Errorf("%w: ready node cache requires lowercase 64-hex sha256", ErrInvalidStatus)
		}
	}
	if s.Phase == ImageCachePhaseFailed && s.Message == "" {
		return fmt.Errorf("%w: failed node cache requires message", ErrInvalidStatus)
	}
	return nil
}

// Image is a first-class image API object. The caller provides ObjectMeta.Name
// as the image ID and Spec.Version identifies the content revision.
type Image struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              ImageSpec   `json:"spec"`
	Status            ImageStatus `json:"status"`
}
