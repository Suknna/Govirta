package pool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/image"
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

// RegisterPool registers a storage pool without creating or replacing defaults.
func (s *Service) RegisterPool(p *Pool) error {
	if p == nil || p.Config.Name == "" || p.Config.Type == "" || p.Config.Backend == "" || p.Config.StorageRoot == "" || p.Config.CapacityBytes <= 0 {
		return volume.ErrInvalidRequest
	}
	switch p.Config.Type {
	case PoolTypeBlock:
		if p.Driver == nil || p.ImageDriver != nil {
			return volume.ErrInvalidRequest
		}
	case PoolTypeFile:
		if p.ImageDriver == nil || p.Driver != nil {
			return volume.ErrInvalidRequest
		}
	default:
		return volume.ErrInvalidRequest
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.pools[p.Config.Name]; exists {
		return ErrPoolAlreadyExists
	}

	p.mu.Lock()
	if p.Config.Type == PoolTypeBlock && p.volumes == nil {
		p.volumes = make(map[volume.ID]volume.Volume)
	}
	if p.Config.Type == PoolTypeFile && p.images == nil {
		p.images = make(map[string]ImageRecord)
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
	blockDriver := p.Driver
	imageDriver := p.ImageDriver
	allocatedBytes := p.allocatedLocked()
	overcommitRatio := overcommitRatioForType(config.Type)
	p.mu.RUnlock()

	var actualUsedBytes int64
	if config.Type == PoolTypeFile {
		actualUsedBytes, err = imageDriver.GetActualUsedBytes(ctx)
	} else {
		actualUsedBytes, err = blockDriver.GetActualUsedBytes(ctx)
	}
	if err != nil {
		return Usage{}, err
	}

	allocationLimitBytes := ratioAllocationLimit(config.CapacityBytes, overcommitRatio)
	return Usage{
		PoolName:               config.Name,
		Type:                   config.Type,
		Backend:                config.Backend,
		CapacityBytes:          config.CapacityBytes,
		OvercommitRatio:        overcommitRatio,
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
	if p.Config.Type != PoolTypeBlock {
		return volume.Volume{}, volume.ErrUnsupported
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

// CreateVolumeFromReader creates or returns an idempotent existing volume from source bytes within a named block pool.
func (s *Service) CreateVolumeFromReader(ctx context.Context, poolName string, req block.CreateFromReaderRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}

	p, err := s.getPool(poolName)
	if err != nil {
		return volume.Volume{}, err
	}
	if p.Config.Type != PoolTypeBlock {
		return volume.Volume{}, volume.ErrUnsupported
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	existing, exists := p.volumes[req.VolumeID]
	driver := p.Driver
	if exists {
		if createFromReaderRequestMatchesVolume(req, existing) {
			return cloneVolume(existing), nil
		}
		return volume.Volume{}, volume.ErrVolumeConflict
	}
	if existing, exists := findVolumeByNameLocked(p.volumes, req.Name); exists {
		if createFromReaderRequestMatchesVolume(req, existing) {
			return cloneVolume(existing), nil
		}
		return volume.Volume{}, volume.ErrVolumeConflict
	}
	if err := reserveCapacityLocked(p, req.CapacityBytes); err != nil {
		return volume.Volume{}, err
	}

	created, err := driver.CreateFromReader(ctx, req)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("create volume %q from reader in pool %q: %w", req.Name, poolName, err)
	}
	created = normalizeCreatedVolumeFromReader(created, req)

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

	limit := ratioAllocationLimit(p.Config.CapacityBytes, overcommitRatioForType(p.Config.Type))
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
	if p.Config.Type != PoolTypeBlock {
		return volume.PublishedVolume{}, volume.ErrUnsupported
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
	if p.Config.Type != PoolTypeBlock {
		return volume.ErrUnsupported
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
	if p.Config.Type != PoolTypeBlock {
		return volume.ErrUnsupported
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

// PutImage reserves and starts writing a new image in a named file pool.
func (s *Service) PutImage(ctx context.Context, poolName string, req image.PutRequest) (image.ImageWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.ImageID == "" || !req.Format.Valid() || req.DeclaredSizeBytes <= 0 {
		return nil, image.ErrInvalidImage
	}

	p, err := s.getPool(poolName)
	if err != nil {
		return nil, err
	}
	if p.Config.Type != PoolTypeFile {
		return nil, volume.ErrUnsupported
	}

	p.mu.Lock()
	if _, exists := p.images[req.ImageID]; exists {
		p.mu.Unlock()
		return nil, image.ErrImageExists
	}
	if err := reserveCapacityLocked(p, req.DeclaredSizeBytes); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if p.images == nil {
		p.images = make(map[string]ImageRecord)
	}
	p.images[req.ImageID] = ImageRecord{ID: req.ImageID, Format: req.Format, DeclaredSizeBytes: req.DeclaredSizeBytes, State: ImageStatePending}
	driver := p.ImageDriver
	p.mu.Unlock()

	writer, err := driver.Put(ctx, req)
	if err != nil {
		p.mu.Lock()
		delete(p.images, req.ImageID)
		p.mu.Unlock()
		return nil, err
	}

	return &pendingImageWriter{pool: p, imageID: req.ImageID, writer: writer}, nil
}

// GetImage opens a ready image for reading from a named file pool.
func (s *Service) GetImage(ctx context.Context, poolName string, req image.GetRequest) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.ImageID == "" {
		return nil, image.ErrInvalidImage
	}

	p, err := s.getPool(poolName)
	if err != nil {
		return nil, err
	}
	if p.Config.Type != PoolTypeFile {
		return nil, volume.ErrUnsupported
	}

	p.mu.RLock()
	record, exists := p.images[req.ImageID]
	driver := p.ImageDriver
	p.mu.RUnlock()
	if !exists || record.State != ImageStateReady {
		return nil, image.ErrImageNotFound
	}

	return driver.Get(ctx, req)
}

// DeleteImage deletes a ready image from a named file pool and releases its allocation.
func (s *Service) DeleteImage(ctx context.Context, poolName string, req image.DeleteRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.ImageID == "" {
		return image.ErrInvalidImage
	}

	p, err := s.getPool(poolName)
	if err != nil {
		return err
	}
	if p.Config.Type != PoolTypeFile {
		return volume.ErrUnsupported
	}

	p.mu.RLock()
	record, exists := p.images[req.ImageID]
	driver := p.ImageDriver
	p.mu.RUnlock()
	if !exists || record.State != ImageStateReady {
		return image.ErrImageNotFound
	}

	if err := driver.Delete(ctx, req); err != nil {
		return err
	}

	p.mu.Lock()
	delete(p.images, req.ImageID)
	p.mu.Unlock()
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

func createFromReaderRequestMatchesVolume(req block.CreateFromReaderRequest, vol volume.Volume) bool {
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

func normalizeCreatedVolumeFromReader(vol volume.Volume, req block.CreateFromReaderRequest) volume.Volume {
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

func overcommitRatioForType(typ PoolType) float64 {
	if typ == PoolTypeFile {
		return DefaultFileOvercommitRatio
	}
	return DefaultOvercommitRatio
}

type pendingImageWriter struct {
	pool    *Pool
	imageID string
	writer  image.ImageWriter
	mu      sync.Mutex
	done    bool
}

func (w *pendingImageWriter) Write(p []byte) (int, error) {
	return w.writer.Write(p)
}

func (w *pendingImageWriter) Close() error {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		return image.ErrInvalidImage
	}
	w.done = true
	w.mu.Unlock()

	if err := w.writer.Close(); err != nil {
		w.pool.mu.Lock()
		delete(w.pool.images, w.imageID)
		w.pool.mu.Unlock()
		return err
	}

	w.pool.mu.Lock()
	record, exists := w.pool.images[w.imageID]
	if !exists || record.State != ImageStatePending {
		w.pool.mu.Unlock()
		return image.ErrInvalidImage
	}
	record.State = ImageStateReady
	w.pool.images[w.imageID] = record
	w.pool.mu.Unlock()
	return nil
}

func (w *pendingImageWriter) Cancel() error {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		return image.ErrInvalidImage
	}
	w.done = true
	w.mu.Unlock()

	cancelErr := w.writer.Cancel()
	w.pool.mu.Lock()
	delete(w.pool.images, w.imageID)
	w.pool.mu.Unlock()
	if cancelErr != nil {
		return errors.Join(cancelErr)
	}
	return nil
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
