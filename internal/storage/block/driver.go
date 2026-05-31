package block

import (
	"context"
	"io"

	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/volume"
)

// DriverInfo describes a block storage backend instance.
type DriverInfo struct {
	Name         string
	Version      string
	Capabilities Capabilities
}

// Capabilities declares optional backend behavior supported by a block driver.
type Capabilities struct {
	CreateDelete  bool
	Publish       bool
	Snapshot      bool
	ResizeOffline bool
	ResizeOnline  bool
}

// Driver defines the backend contract for pool-bound block volume implementations.
type Driver interface {
	DriverInfo(ctx context.Context) (DriverInfo, error)
	Create(ctx context.Context, req CreateRequest) (volume.Volume, error)
	CreateFromReader(ctx context.Context, req CreateFromReaderRequest) (volume.Volume, error)
	Delete(ctx context.Context, vol volume.Volume) error
	GetActualUsedBytes(ctx context.Context) (int64, error)
	Publish(ctx context.Context, vol volume.Volume, req PublishRequest) (volume.PublishedVolume, error)
	Unpublish(ctx context.Context, vol volume.Volume, req UnpublishRequest) error
	Snapshot(ctx context.Context, vol volume.Volume, req SnapshotRequest) (volume.Snapshot, error)
	Resize(ctx context.Context, vol volume.Volume, req ResizeRequest) (volume.Volume, error)
}

// CreateRequest carries all pool and VM identity required to create a block volume.
type CreateRequest struct {
	Name          string
	PoolName      string
	VMID          string
	VMName        string
	VolumeID      volume.ID
	Role          volume.Role
	DiskIndex     int
	CapacityBytes int64
	ReadOnly      bool
}

// CreateFromReaderRequest carries source bytes and VM identity required to create a block volume copy.
type CreateFromReaderRequest struct {
	Reader        io.Reader
	Format        diskformat.Format
	Name          string
	PoolName      string
	VMID          string
	VMName        string
	VolumeID      volume.ID
	Role          volume.Role
	DiskIndex     int
	CapacityBytes int64
	ReadOnly      bool
}

// PublishRequest identifies a block volume publish operation for a VM.
type PublishRequest struct {
	VolumeID volume.ID
	VMID     string
	ReadOnly bool
}

// UnpublishRequest identifies a previously published block volume for release.
type UnpublishRequest struct {
	VolumeID volume.ID
	VMID     string
}

// SnapshotRequest describes a future offline snapshot request.
type SnapshotRequest struct {
	Name string
}

// ResizeRequest describes a future offline resize request.
type ResizeRequest struct {
	CapacityBytes int64
}
