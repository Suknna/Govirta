package v1alpha1

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// ImageTaskSourceType names the source protocol for a node image-cache task.
type ImageTaskSourceType string

const (
	// ImageTaskSourceUpload means the image bytes are available from an upload source.
	ImageTaskSourceUpload ImageTaskSourceType = "upload"
	// ImageTaskSourceHTTP means the node should fetch the image from an HTTP location.
	ImageTaskSourceHTTP ImageTaskSourceType = "http"
)

const (
	imageTaskFormatQCOW2 = "qcow2"
	imageTaskFormatRaw   = "raw"
	imageTaskFormatISO   = "iso"
)

var lowercaseSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ImageTaskSource describes where image bytes come from for a cache operation.
type ImageTaskSource struct {
	Type     ImageTaskSourceType `json:"type"`
	Location string              `json:"location"`
}

// CacheImageInput is the typed input for TaskOperationCacheImageNode.
type CacheImageInput struct {
	ImageName         string          `json:"imageName"`
	ImageUID          string          `json:"imageUID"`
	Version           string          `json:"version"`
	Format            string          `json:"format"`
	Source            ImageTaskSource `json:"source"`
	DeclaredSizeBytes int64           `json:"declaredSizeBytes"`
	SHA256            string          `json:"sha256"`
	CacheRoot         string          `json:"cacheRoot"`
}

// Validate reports whether input carries every behavior-affecting image cache field.
func (i CacheImageInput) Validate() error {
	if i.ImageName == "" || i.ImageUID == "" || i.Version == "" || i.CacheRoot == "" {
		return fmt.Errorf("%w: imageName, imageUID, version, and cacheRoot are required", ErrInvalidTask)
	}
	if !validImageTaskFormat(i.Format) {
		return fmt.Errorf("%w: image format %q is invalid", ErrInvalidTask, i.Format)
	}
	if !i.Source.Type.Valid() {
		return fmt.Errorf("%w: image source type %q is invalid", ErrInvalidTask, i.Source.Type)
	}
	if i.Source.Location == "" {
		return fmt.Errorf("%w: image source location is required", ErrInvalidTask)
	}
	if i.DeclaredSizeBytes <= 0 {
		return fmt.Errorf("%w: declaredSizeBytes must be positive", ErrInvalidTask)
	}
	if !validLowercaseSHA256(i.SHA256) {
		return fmt.Errorf("%w: sha256 must be lowercase 64-hex", ErrInvalidTask)
	}
	return nil
}

// CacheImageObserved is the typed observed result for TaskOperationCacheImageNode.
type CacheImageObserved struct {
	NodeName   string `json:"nodeName"`
	ImageName  string `json:"imageName"`
	Version    string `json:"version"`
	Format     string `json:"format"`
	CachedPath string `json:"cachedPath"`
	SizeBytes  int64  `json:"sizeBytes"`
	SHA256     string `json:"sha256"`
}

func (o CacheImageObserved) validate() error {
	if o.NodeName == "" || o.ImageName == "" || o.Version == "" || o.CachedPath == "" {
		return fmt.Errorf("%w: observed nodeName, imageName, version, and cachedPath are required", ErrInvalidTask)
	}
	if !validImageTaskFormat(o.Format) {
		return fmt.Errorf("%w: observed image format %q is invalid", ErrInvalidTask, o.Format)
	}
	if o.SizeBytes <= 0 {
		return fmt.Errorf("%w: observed sizeBytes must be positive", ErrInvalidTask)
	}
	if !validLowercaseSHA256(o.SHA256) {
		return fmt.Errorf("%w: observed sha256 must be lowercase 64-hex", ErrInvalidTask)
	}
	return nil
}

// DeleteCachedImageInput is the typed input for TaskOperationDeleteCachedImageNode.
type DeleteCachedImageInput struct {
	ImageName string `json:"imageName"`
	ImageUID  string `json:"imageUID"`
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	CacheRoot string `json:"cacheRoot"`
}

// Validate reports whether input carries every behavior-affecting cached-image deletion field.
func (i DeleteCachedImageInput) Validate() error {
	if i.ImageName == "" || i.ImageUID == "" || i.Version == "" || i.CacheRoot == "" {
		return fmt.Errorf("%w: imageName, imageUID, version, and cacheRoot are required", ErrInvalidTask)
	}
	if !validLowercaseSHA256(i.SHA256) {
		return fmt.Errorf("%w: sha256 must be lowercase 64-hex", ErrInvalidTask)
	}
	return nil
}

// DeleteCachedImageObserved is the typed observed result for TaskOperationDeleteCachedImageNode.
type DeleteCachedImageObserved struct {
	NodeName  string `json:"nodeName"`
	ImageName string `json:"imageName"`
	Version   string `json:"version"`
	Deleted   bool   `json:"deleted"`
}

func (o DeleteCachedImageObserved) validate() error {
	if o.NodeName == "" || o.ImageName == "" || o.Version == "" {
		return fmt.Errorf("%w: observed nodeName, imageName, and version are required", ErrInvalidTask)
	}
	if !o.Deleted {
		return fmt.Errorf("%w: observed deleted must be true", ErrInvalidTask)
	}
	return nil
}

// Valid reports whether t is a supported image task source type.
func (t ImageTaskSourceType) Valid() bool {
	return t == ImageTaskSourceUpload || t == ImageTaskSourceHTTP
}

// DecodeCacheImageObserved decodes and validates a cache-image observed payload.
func DecodeCacheImageObserved(raw json.RawMessage) (CacheImageObserved, error) {
	var observed CacheImageObserved
	if err := json.Unmarshal(raw, &observed); err != nil {
		return CacheImageObserved{}, fmt.Errorf("%w: decode cache image observed: %v", ErrInvalidTask, err)
	}
	if err := observed.validate(); err != nil {
		return CacheImageObserved{}, err
	}
	return observed, nil
}

// DecodeDeleteCachedImageObserved decodes and validates a delete-cached-image observed payload.
func DecodeDeleteCachedImageObserved(raw json.RawMessage) (DeleteCachedImageObserved, error) {
	var observed DeleteCachedImageObserved
	if err := json.Unmarshal(raw, &observed); err != nil {
		return DeleteCachedImageObserved{}, fmt.Errorf("%w: decode delete cached image observed: %v", ErrInvalidTask, err)
	}
	if err := observed.validate(); err != nil {
		return DeleteCachedImageObserved{}, err
	}
	return observed, nil
}

func validImageTaskFormat(format string) bool {
	return format == imageTaskFormatQCOW2 || format == imageTaskFormatRaw || format == imageTaskFormatISO
}

func validLowercaseSHA256(checksum string) bool {
	return lowercaseSHA256Pattern.MatchString(checksum)
}
