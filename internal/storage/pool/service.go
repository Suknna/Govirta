package pool

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/volume"
)

// Service registers storage pools and reports pool capacity accounting.
type Service struct {
	mu    sync.RWMutex
	pools map[string]*Pool
}

// NewService creates an empty storage pool service.
func NewService() *Service {
	return &Service{
		pools: make(map[string]*Pool),
	}
}

// RegisterPool registers a block storage pool without creating or replacing defaults.
func (s *Service) RegisterPool(p *Pool) error {
	if p == nil || p.Config.Name == "" || p.Config.Type == "" || p.Config.Backend == "" || p.Config.StorageRoot == "" || p.Config.CapacityBytes <= 0 || p.Driver == nil {
		return volume.ErrInvalidRequest
	}
	if p.Config.Type != PoolTypeBlock {
		return volume.ErrUnsupported
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.pools[p.Config.Name]; exists {
		return ErrPoolAlreadyExists
	}

	p.mu.Lock()
	if p.volumes == nil {
		p.volumes = make(map[volume.ID]volume.Volume)
	}
	p.mu.Unlock()

	s.pools[p.Config.Name] = p
	return nil
}

// GetPool returns the registered pool with the requested explicit name.
func (s *Service) GetPool(name string) (*Pool, error) {
	p, err := s.getPool(name)
	if err != nil {
		return nil, err
	}

	clone := p.clone()
	return &clone, nil
}

func (s *Service) getPool(name string) (*Pool, error) {
	if name == "" {
		return nil, ErrPoolRequired
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	p, exists := s.pools[name]
	if !exists {
		return nil, ErrPoolNotFound
	}
	return p, nil
}

// ListPools returns detached pool snapshots sorted by pool name.
func (s *Service) ListPools(ctx context.Context) ([]Pool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.pools))
	for name := range s.pools {
		names = append(names, name)
	}
	sort.Strings(names)

	pools := make([]Pool, 0, len(names))
	for _, name := range names {
		pools = append(pools, s.pools[name].clone())
	}
	return pools, nil
}

// GetPoolUsage returns overcommit allocation and backend usage for a registered pool.
func (s *Service) GetPoolUsage(ctx context.Context, poolName string) (Usage, error) {
	if err := ctx.Err(); err != nil {
		return Usage{}, err
	}

	p, err := s.getPool(poolName)
	if err != nil {
		return Usage{}, err
	}

	p.mu.RLock()
	config := p.Config
	driver := p.Driver
	allocatedBytes := p.allocatedLocked()
	p.mu.RUnlock()

	actualUsedBytes, err := driver.GetActualUsedBytes(ctx)
	if err != nil {
		return Usage{}, err
	}

	allocationLimitBytes := allocationLimit(config.CapacityBytes)
	return Usage{
		PoolName:               config.Name,
		Type:                   config.Type,
		Backend:                config.Backend,
		CapacityBytes:          config.CapacityBytes,
		OvercommitRatio:        DefaultOvercommitRatio,
		AllocationLimitBytes:   allocationLimitBytes,
		AllocatedBytes:         allocatedBytes,
		ActualUsedBytes:        actualUsedBytes,
		AvailableForAllocation: allocationLimitBytes - allocatedBytes,
	}, nil
}

// CreateVolume creates or returns an idempotent existing volume within a named pool.
func (s *Service) CreateVolume(ctx context.Context, poolName string, req block.CreateRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}

	p, err := s.getPool(poolName)
	if err != nil {
		return volume.Volume{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	existing, exists := p.volumes[req.VolumeID]
	driver := p.Driver
	if exists {
		if createRequestMatchesVolume(req, existing) {
			return cloneVolume(existing), nil
		}
		return volume.Volume{}, volume.ErrVolumeConflict
	}
	if existing, exists := findVolumeByNameLocked(p.volumes, req.Name); exists {
		if createRequestMatchesVolume(req, existing) {
			return cloneVolume(existing), nil
		}
		return volume.Volume{}, volume.ErrVolumeConflict
	}
	if err := reserveCapacityLocked(p, req.CapacityBytes); err != nil {
		return volume.Volume{}, err
	}

	created, err := driver.Create(ctx, req)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("create volume %q in pool %q: %w", req.Name, poolName, err)
	}
	created = normalizeCreatedVolume(created, req)

	if p.volumes == nil {
		p.volumes = make(map[volume.ID]volume.Volume)
	}
	p.volumes[created.ID] = cloneVolume(created)
	return cloneVolume(created), nil
}

func reserveCapacityLocked(p *Pool, bytes int64) error {
	if bytes <= 0 {
		return volume.ErrInvalidRequest
	}

	limit := allocationLimit(p.Config.CapacityBytes)
	allocated := p.allocatedLocked()
	if allocated > limit || bytes > limit-allocated {
		return ErrPoolCapacityExceeded
	}
	return nil
}

// PublishVolume prepares runtime access to a volume and records the attachment state.
func (s *Service) PublishVolume(ctx context.Context, poolName string, volumeID volume.ID, req block.PublishRequest) (volume.PublishedVolume, error) {
	if err := ctx.Err(); err != nil {
		return volume.PublishedVolume{}, err
	}
	if req.VolumeID != volumeID {
		return volume.PublishedVolume{}, volume.ErrInvalidRequest
	}

	p, err := s.getPool(poolName)
	if err != nil {
		return volume.PublishedVolume{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	vol, exists := p.volumes[volumeID]
	driver := p.Driver
	if !exists {
		return volume.PublishedVolume{}, volume.ErrVolumeNotFound
	}
	if vol.Attachment != nil {
		if vol.Attachment.VMID == req.VMID && vol.Attachment.ReadOnly == req.ReadOnly {
			return publishedFromVolume(vol), nil
		}
		return volume.PublishedVolume{}, volume.ErrVolumeInUse
	}
	if vol.VMID != req.VMID {
		return volume.PublishedVolume{}, volume.ErrInvalidRequest
	}

	published, err := driver.Publish(ctx, cloneVolume(vol), req)
	if err != nil {
		return volume.PublishedVolume{}, fmt.Errorf("publish volume %q in pool %q: %w", volumeID, poolName, err)
	}
	published = normalizePublishedVolume(published, vol, req)

	vol.State = volume.StatePublished
	vol.Attachment = &volume.AttachmentState{
		VMID:       req.VMID,
		ReadOnly:   req.ReadOnly,
		Attachment: cloneAttachment(published.Attachment),
	}
	p.volumes[volumeID] = vol
	return publishedFromVolume(vol), nil
}

// UnpublishVolume releases a runtime attachment if the volume is currently published.
func (s *Service) UnpublishVolume(ctx context.Context, poolName string, volumeID volume.ID, req block.UnpublishRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.VolumeID != volumeID {
		return volume.ErrInvalidRequest
	}

	p, err := s.getPool(poolName)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	vol, exists := p.volumes[volumeID]
	driver := p.Driver
	if !exists {
		return volume.ErrVolumeNotFound
	}
	if vol.VMID != req.VMID {
		return volume.ErrInvalidRequest
	}
	if vol.Attachment == nil {
		return nil
	}
	if vol.Attachment.VMID != req.VMID {
		return volume.ErrInvalidRequest
	}

	if err := driver.Unpublish(ctx, cloneVolume(vol), req); err != nil {
		return fmt.Errorf("unpublish volume %q in pool %q: %w", volumeID, poolName, err)
	}

	vol.State = volume.StateAvailable
	vol.Attachment = nil
	p.volumes[volumeID] = vol
	return nil
}

// DeleteVolume deletes an unpublished volume and removes it from the pool index.
func (s *Service) DeleteVolume(ctx context.Context, poolName string, volumeID volume.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p, err := s.getPool(poolName)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	vol, exists := p.volumes[volumeID]
	driver := p.Driver
	if !exists {
		return volume.ErrVolumeNotFound
	}
	if vol.Attachment != nil {
		return volume.ErrVolumeInUse
	}

	if err := driver.Delete(ctx, cloneVolume(vol)); err != nil {
		return fmt.Errorf("delete volume %q in pool %q: %w", volumeID, poolName, err)
	}

	delete(p.volumes, volumeID)
	return nil
}

func findVolumeByNameLocked(volumes map[volume.ID]volume.Volume, name string) (volume.Volume, bool) {
	for _, vol := range volumes {
		if vol.Name == name {
			return vol, true
		}
	}
	return volume.Volume{}, false
}

func createRequestMatchesVolume(req block.CreateRequest, vol volume.Volume) bool {
	return vol.Name == req.Name &&
		vol.PoolName == req.PoolName &&
		vol.VMID == req.VMID &&
		vol.VMName == req.VMName &&
		vol.DiskIndex == req.DiskIndex &&
		vol.CapacityBytes == req.CapacityBytes &&
		vol.ID == req.VolumeID
}

func normalizeCreatedVolume(vol volume.Volume, req block.CreateRequest) volume.Volume {
	vol.ID = req.VolumeID
	vol.Name = req.Name
	vol.PoolName = req.PoolName
	vol.VMID = req.VMID
	vol.VMName = req.VMName
	vol.DiskIndex = req.DiskIndex
	vol.CapacityBytes = req.CapacityBytes
	if vol.State == "" {
		vol.State = volume.StateAvailable
	}
	return vol
}

func normalizePublishedVolume(published volume.PublishedVolume, vol volume.Volume, req block.PublishRequest) volume.PublishedVolume {
	published.VolumeID = vol.ID
	if published.VMID == "" {
		published.VMID = req.VMID
	}
	if published.PoolName == "" {
		published.PoolName = vol.PoolName
	}
	published.Attachment.ReadOnly = req.ReadOnly
	return published
}

func publishedFromVolume(vol volume.Volume) volume.PublishedVolume {
	if vol.Attachment == nil {
		return volume.PublishedVolume{}
	}
	return volume.PublishedVolume{
		VolumeID:   vol.ID,
		VMID:       vol.Attachment.VMID,
		PoolName:   vol.PoolName,
		Attachment: cloneAttachment(vol.Attachment.Attachment),
	}
}

func cloneAttachment(attachment volume.Attachment) volume.Attachment {
	attachment.Attributes = cloneStringMap(attachment.Attributes)
	return attachment
}
