package storage

import (
	"context"
	"io"

	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/image"
	"github.com/suknna/govirta/internal/storage/pool"
)

// ImageService is the storage API for file image operations.
type ImageService struct {
	pools *pool.Service
}

// NewImageService creates an image service backed by an explicit pool service.
func NewImageService(pools *pool.Service) *ImageService {
	return &ImageService{pools: pools}
}

// PutImageRequest identifies a new image write in a file pool.
type PutImageRequest struct {
	PoolName          string
	ImageID           string
	Format            diskformat.Format
	DeclaredSizeBytes int64
}

// GetImageRequest identifies an image read from a file pool.
type GetImageRequest struct {
	PoolName string
	ImageID  string
}

// DeleteImageRequest identifies an image deletion from a file pool.
type DeleteImageRequest struct {
	PoolName string
	ImageID  string
	Format   diskformat.Format
}

// PutImage opens a writer for a new image in the named file pool.
func (s *ImageService) PutImage(ctx context.Context, req PutImageRequest) (image.ImageWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.PoolName == "" {
		return nil, pool.ErrPoolRequired
	}
	return s.pools.PutImage(ctx, req.PoolName, image.PutRequest{
		ImageID:           req.ImageID,
		Format:            req.Format,
		DeclaredSizeBytes: req.DeclaredSizeBytes,
	})
}

// GetImage opens a ready image for reading from the named file pool.
func (s *ImageService) GetImage(ctx context.Context, req GetImageRequest) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.PoolName == "" {
		return nil, pool.ErrPoolRequired
	}
	return s.pools.GetImage(ctx, req.PoolName, image.GetRequest{ImageID: req.ImageID})
}

// DeleteImage deletes a ready image from the named file pool.
func (s *ImageService) DeleteImage(ctx context.Context, req DeleteImageRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.PoolName == "" {
		return pool.ErrPoolRequired
	}
	return s.pools.DeleteImage(ctx, req.PoolName, image.DeleteRequest{ImageID: req.ImageID, Format: req.Format})
}
