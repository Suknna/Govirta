package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/pool"
	"github.com/suknna/govirta/internal/storage/volume"
)

// VolumeService is the VM-facing storage API for block volume operations.
type VolumeService struct {
	pools *pool.Service
}

// NewVolumeService creates a VM-facing volume service backed by an explicit pool service.
func NewVolumeService(pools *pool.Service) *VolumeService {
	return &VolumeService{pools: pools}
}

// CreateVolumeRequest describes a VM-scoped block volume create operation.
type CreateVolumeRequest struct {
	VMID     string
	VMName   string
	PoolName string
	Spec     volume.Spec
}

// CreateRootVolumeRequest describes a convenience root or data volume create operation.
type CreateRootVolumeRequest struct {
	VMID          string
	VMName        string
	PoolName      string
	Name          string
	DiskIndex     int
	CapacityBytes int64
	ReadOnly      bool
}

// CreateDataVolumeRequest is the data-disk counterpart to CreateRootVolumeRequest.
type CreateDataVolumeRequest = CreateRootVolumeRequest

// CreateRootVolumeFromReaderRequest describes a root volume copied from image bytes.
type CreateRootVolumeFromReaderRequest struct {
	VMID          string
	VMName        string
	PoolName      string
	Name          string
	DiskIndex     int
	CapacityBytes int64
	ReadOnly      bool
	Reader        io.Reader
	Format        diskformat.Format
}

// PublishVolumeRequest identifies a block volume publish operation for a VM.
type PublishVolumeRequest struct {
	VolumeID volume.ID
	VMID     string
	PoolName string
	ReadOnly bool
}

// UnpublishVolumeRequest identifies a previously published block volume for release.
type UnpublishVolumeRequest struct {
	VolumeID volume.ID
	VMID     string
	PoolName string
}

// DeleteVolumeRequest identifies a volume deletion within an explicit pool.
type DeleteVolumeRequest struct {
	VolumeID volume.ID
	PoolName string
}

// CreateVolume validates VM-facing input and delegates creation to the named pool.
func (s *VolumeService) CreateVolume(ctx context.Context, req CreateVolumeRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	if err := validateCreateRequest(req); err != nil {
		return volume.Volume{}, err
	}

	volID := volume.ID(fmt.Sprintf("%s-%s-%d", req.VMID, req.Spec.Role, req.Spec.DiskIndex))
	created, err := s.pools.CreateVolume(ctx, req.PoolName, block.CreateRequest{
		Name:          req.Spec.Name,
		PoolName:      req.PoolName,
		VMID:          req.VMID,
		VMName:        req.VMName,
		VolumeID:      volID,
		Role:          req.Spec.Role,
		DiskIndex:     req.Spec.DiskIndex,
		CapacityBytes: req.Spec.CapacityBytes,
		ReadOnly:      req.Spec.ReadOnly,
	})
	if err != nil {
		if errors.Is(err, volume.ErrVolumeCleanupFailed) {
			return created, err
		}
		return volume.Volume{}, err
	}
	return created, nil
}

// CreateRootVolume creates a root disk volume by setting volume.RoleRoot.
func (s *VolumeService) CreateRootVolume(ctx context.Context, req CreateRootVolumeRequest) (volume.Volume, error) {
	return s.CreateVolume(ctx, CreateVolumeRequest{
		VMID:     req.VMID,
		VMName:   req.VMName,
		PoolName: req.PoolName,
		Spec: volume.Spec{
			Name:          req.Name,
			Role:          volume.RoleRoot,
			DiskIndex:     req.DiskIndex,
			CapacityBytes: req.CapacityBytes,
			ReadOnly:      req.ReadOnly,
		},
	})
}

// CreateRootVolumeFromReader creates a root disk volume as a full copy of source bytes.
func (s *VolumeService) CreateRootVolumeFromReader(ctx context.Context, req CreateRootVolumeFromReaderRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	if req.PoolName == "" {
		return volume.Volume{}, pool.ErrPoolRequired
	}
	if req.Reader == nil || !req.Format.Valid() || req.VMID == "" || req.VMName == "" || req.Name == "" || req.DiskIndex < 0 || req.CapacityBytes <= 0 {
		return volume.Volume{}, volume.ErrInvalidRequest
	}

	volID := volume.ID(fmt.Sprintf("%s-%s-%d", req.VMID, volume.RoleRoot, req.DiskIndex))
	created, err := s.pools.CreateVolumeFromReader(ctx, req.PoolName, block.CreateFromReaderRequest{
		Reader:        req.Reader,
		Format:        req.Format,
		Name:          req.Name,
		PoolName:      req.PoolName,
		VMID:          req.VMID,
		VMName:        req.VMName,
		VolumeID:      volID,
		Role:          volume.RoleRoot,
		DiskIndex:     req.DiskIndex,
		CapacityBytes: req.CapacityBytes,
		ReadOnly:      req.ReadOnly,
	})
	if err != nil {
		if errors.Is(err, volume.ErrVolumeCleanupFailed) {
			return created, err
		}
		return volume.Volume{}, err
	}
	return created, nil
}

// CreateDataVolume creates a data disk volume by setting volume.RoleData.
func (s *VolumeService) CreateDataVolume(ctx context.Context, req CreateDataVolumeRequest) (volume.Volume, error) {
	return s.CreateVolume(ctx, CreateVolumeRequest{
		VMID:     req.VMID,
		VMName:   req.VMName,
		PoolName: req.PoolName,
		Spec: volume.Spec{
			Name:          req.Name,
			Role:          volume.RoleData,
			DiskIndex:     req.DiskIndex,
			CapacityBytes: req.CapacityBytes,
			ReadOnly:      req.ReadOnly,
		},
	})
}

// PublishVolume prepares runtime access and returns the storage attachment contract.
func (s *VolumeService) PublishVolume(ctx context.Context, req PublishVolumeRequest) (volume.PublishedVolume, error) {
	if err := ctx.Err(); err != nil {
		return volume.PublishedVolume{}, err
	}
	if req.PoolName == "" {
		return volume.PublishedVolume{}, pool.ErrPoolRequired
	}
	if req.VolumeID == "" || req.VMID == "" {
		return volume.PublishedVolume{}, volume.ErrInvalidRequest
	}
	return s.pools.PublishVolume(ctx, req.PoolName, req.VolumeID, block.PublishRequest{
		VolumeID: req.VolumeID,
		VMID:     req.VMID,
		ReadOnly: req.ReadOnly,
	})
}

// UnpublishVolume releases a runtime attachment if present.
func (s *VolumeService) UnpublishVolume(ctx context.Context, req UnpublishVolumeRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.PoolName == "" {
		return pool.ErrPoolRequired
	}
	if req.VolumeID == "" || req.VMID == "" {
		return volume.ErrInvalidRequest
	}
	return s.pools.UnpublishVolume(ctx, req.PoolName, req.VolumeID, block.UnpublishRequest{
		VolumeID: req.VolumeID,
		VMID:     req.VMID,
	})
}

// DeleteVolume deletes an unpublished volume from the named pool.
func (s *VolumeService) DeleteVolume(ctx context.Context, req DeleteVolumeRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.PoolName == "" {
		return pool.ErrPoolRequired
	}
	if req.VolumeID == "" {
		return volume.ErrInvalidRequest
	}
	return s.pools.DeleteVolume(ctx, req.PoolName, req.VolumeID)
}

// SnapshotVolumeRequest identifies a volume and the internal snapshot name to create.
type SnapshotVolumeRequest struct {
	PoolName     string
	VolumeID     volume.ID
	SnapshotName string
}

// SnapshotVolume creates a qcow2 internal snapshot on an unpublished volume.
func (s *VolumeService) SnapshotVolume(ctx context.Context, req SnapshotVolumeRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.PoolName == "" {
		return pool.ErrPoolRequired
	}
	if req.VolumeID == "" || req.SnapshotName == "" {
		return volume.ErrInvalidRequest
	}
	return s.pools.SnapshotVolume(ctx, req.PoolName, req.VolumeID, req.SnapshotName)
}

// DeleteVolumeSnapshotRequest identifies a volume and the internal snapshot name to delete.
type DeleteVolumeSnapshotRequest struct {
	PoolName     string
	VolumeID     volume.ID
	SnapshotName string
}

// DeleteVolumeSnapshot deletes a qcow2 internal snapshot from an unpublished volume.
func (s *VolumeService) DeleteVolumeSnapshot(ctx context.Context, req DeleteVolumeSnapshotRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.PoolName == "" {
		return pool.ErrPoolRequired
	}
	if req.VolumeID == "" || req.SnapshotName == "" {
		return volume.ErrInvalidRequest
	}
	return s.pools.DeleteVolumeSnapshot(ctx, req.PoolName, req.VolumeID, req.SnapshotName)
}

func validateCreateRequest(req CreateVolumeRequest) error {
	if req.PoolName == "" {
		return pool.ErrPoolRequired
	}
	if req.VMID == "" || req.VMName == "" || req.Spec.Name == "" || req.Spec.DiskIndex < 0 || req.Spec.CapacityBytes <= 0 {
		return volume.ErrInvalidRequest
	}
	if req.Spec.Role != volume.RoleRoot && req.Spec.Role != volume.RoleData {
		return volume.ErrInvalidRequest
	}
	return nil
}
