package pool

import (
	"context"
	"fmt"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/volume"
)

// ResizeVolume grows an existing block volume's declared capacity to
// capacityBytes (cold offline resize). Accounting is pre-allocation only and
// matches CreateVolume: the delta (new - old) passes the same overcommit
// admission via reserveCapacityLocked. The allocated total is a live sum over
// p.volumes (no counter), so updating the map entry's CapacityBytes is the only
// ledger mutation needed. Ordering is critical: reserve -> driver.Resize
// success -> mutate the map. If driver.Resize fails the map is untouched, so
// the next reconcile recomputes the same delta and retries (level-triggered).
//
// The whole reserve->resize->record sequence runs under p.mu (same critical
// section discipline as CreateVolume) to prevent concurrent over-commit.
func (s *Service) ResizeVolume(ctx context.Context, poolName string, volumeID volume.ID, capacityBytes int64) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	if capacityBytes <= 0 {
		return volume.Volume{}, volume.ErrInvalidRequest
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

	existing, ok := p.volumes[volumeID]
	if !ok {
		return volume.Volume{}, volume.ErrVolumeNotFound
	}

	delta := capacityBytes - existing.CapacityBytes
	if delta < 0 {
		return volume.Volume{}, volume.ErrInvalidRequest
	}
	if delta > 0 {
		if err := reserveCapacityLocked(p, delta); err != nil {
			return volume.Volume{}, err
		}
	}

	driver := p.Driver
	if _, err := driver.Resize(ctx, existing, block.ResizeRequest{CapacityBytes: capacityBytes}); err != nil {
		return volume.Volume{}, fmt.Errorf("resize volume %q in pool %q: %w", volumeID, poolName, err)
	}

	existing.CapacityBytes = capacityBytes
	p.volumes[volumeID] = cloneVolume(existing)
	return cloneVolume(existing), nil
}
