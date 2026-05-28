package pool

import (
	"math"
	"sync"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/volume"
)

// BackendType names the storage backend family used by a pool.
type BackendType string

const (
	// BackendLocalBlock identifies host-local block storage.
	BackendLocalBlock BackendType = "local-block"
	// BackendNFSBlock identifies NFS-backed block storage.
	BackendNFSBlock BackendType = "nfs-block"
	// BackendRBDBlock identifies RBD-backed block storage.
	BackendRBDBlock BackendType = "rbd-block"
)

// PoolType describes the storage object model exposed by a pool.
type PoolType string

const (
	// PoolTypeBlock identifies pools that manage block volumes.
	PoolTypeBlock PoolType = "block"
	// PoolTypeFile identifies file-oriented pools, which are not supported in this phase.
	PoolTypeFile PoolType = "file"
)

// DefaultOvercommitRatio is the allocation multiplier applied to raw pool capacity.
const DefaultOvercommitRatio = 1.5

// Config defines a storage pool registration contract.
type Config struct {
	Name          string
	Type          PoolType
	Backend       BackendType
	StorageRoot   string
	CapacityBytes int64
}

// Pool binds storage pool configuration to a backend driver and indexed volumes.
type Pool struct {
	Config  Config
	Driver  block.Driver
	mu      sync.RWMutex
	volumes map[volume.ID]volume.Volume
}

// Usage reports allocation and actual backend usage for a registered pool.
type Usage struct {
	PoolName               string
	Type                   PoolType
	Backend                BackendType
	CapacityBytes          int64
	OvercommitRatio        float64
	AllocationLimitBytes   int64
	AllocatedBytes         int64
	ActualUsedBytes        int64
	AvailableForAllocation int64
}

// ReserveCapacity verifies capacity admission without recording a reservation.
func (p *Pool) ReserveCapacity(bytes int64) error {
	if bytes <= 0 {
		return volume.ErrInvalidRequest
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	limit := allocationLimit(p.Config.CapacityBytes)
	allocated := p.allocatedLocked()
	if allocated > limit || bytes > limit-allocated {
		return ErrPoolCapacityExceeded
	}
	return nil
}

func (p *Pool) clone() Pool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return Pool{
		Config:  p.Config,
		Driver:  p.Driver,
		volumes: cloneVolumes(p.volumes),
	}
}

func cloneVolumes(volumes map[volume.ID]volume.Volume) map[volume.ID]volume.Volume {
	if volumes == nil {
		return nil
	}

	cloned := make(map[volume.ID]volume.Volume, len(volumes))
	for id, vol := range volumes {
		cloned[id] = cloneVolume(vol)
	}
	return cloned
}

func cloneVolume(vol volume.Volume) volume.Volume {
	vol.Context = cloneStringMap(vol.Context)
	vol.Attachment = cloneAttachmentState(vol.Attachment)
	return vol
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneAttachmentState(state *volume.AttachmentState) *volume.AttachmentState {
	if state == nil {
		return nil
	}

	cloned := *state
	cloned.Attachment.Attributes = cloneStringMap(state.Attachment.Attributes)
	return &cloned
}

func allocationLimit(capacity int64) int64 {
	if capacity <= 0 {
		return 0
	}

	limit := float64(capacity) * DefaultOvercommitRatio
	if limit >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(limit)
}

func (p *Pool) allocatedLocked() int64 {
	var allocatedBytes int64
	for _, vol := range p.volumes {
		if vol.CapacityBytes <= 0 {
			continue
		}
		if vol.CapacityBytes > math.MaxInt64-allocatedBytes {
			return math.MaxInt64
		}
		allocatedBytes += vol.CapacityBytes
	}
	return allocatedBytes
}
