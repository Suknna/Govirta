package image

import (
	"context"
	"errors"
	"io"

	"github.com/suknna/govirta/internal/storage/diskformat"
)

var (
	// ErrInvalidImage marks caller input that cannot identify or store an image.
	ErrInvalidImage = errors.New("invalid image request")
	// ErrImageExists marks duplicate image IDs in the same pool.
	ErrImageExists = errors.New("image already exists")
	// ErrImageNotFound marks lookup or delete requests for unknown images.
	ErrImageNotFound = errors.New("image not found")
	// ErrImageCleanupFailed marks post-commit cleanup failures for image writes.
	ErrImageCleanupFailed = errors.New("image cleanup failed")
)

// DriverInfo describes an image backend implementation.
type DriverInfo struct {
	Name         string
	Capabilities Capabilities
}

// Capabilities describes image backend operations.
type Capabilities struct {
	SupportsRaw   bool
	SupportsQCOW2 bool
}

// Driver defines the backend contract for file-image pool implementations.
type Driver interface {
	DriverInfo(ctx context.Context) (DriverInfo, error)
	Put(ctx context.Context, req PutRequest) (ImageWriter, error)
	Get(ctx context.Context, req GetRequest) (io.ReadCloser, error)
	Delete(ctx context.Context, req DeleteRequest) error
	GetActualUsedBytes(ctx context.Context) (int64, error)
}

// ImageWriter accepts image bytes and either commits or cancels the upload.
type ImageWriter interface {
	io.Writer
	Close() error
	Cancel() error
}

// PutRequest identifies a new image write operation inside a file pool.
type PutRequest struct {
	ImageID           string
	Format            diskformat.Format
	DeclaredSizeBytes int64
}

// GetRequest identifies an image read operation inside a file pool.
type GetRequest struct {
	ImageID string
}

// DeleteRequest identifies an image delete operation inside a file pool.
type DeleteRequest struct {
	ImageID string
	Format  diskformat.Format
}
