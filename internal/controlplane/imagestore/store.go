package imagestore

import (
	"context"
	"errors"
	"io"
)

var (
	ErrInvalidRequest   = errors.New("imagestore: invalid request")
	ErrNotFound         = errors.New("imagestore: not found")
	ErrConflict         = errors.New("imagestore: conflict")
	ErrChecksumMismatch = errors.New("imagestore: checksum mismatch")
	ErrUnsafePath       = errors.New("imagestore: unsafe path")
)

// PutRequest describes one explicit image object upload into the control-plane store.
type PutRequest struct {
	Name              string
	Version           string
	Format            string
	DeclaredSizeBytes int64
	SHA256            string
	Reader            io.Reader
}

// ObjectRef is the stable metadata exposed for a stored image object.
type ObjectRef struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Format    string `json:"format"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
	Path      string `json:"path"`
}

// Store is the swappable control-plane image object storage boundary.
type Store interface {
	Put(ctx context.Context, req PutRequest) (ObjectRef, error)
	Get(ctx context.Context, name, version string) (ObjectRef, error)
	Open(ctx context.Context, name, version string) (io.ReadCloser, ObjectRef, error)
	Delete(ctx context.Context, name, version string, sha256 string) error
}
