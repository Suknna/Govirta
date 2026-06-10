package pool

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/volume"
)

// resizeDriver embeds fakeDriver to inherit the full block.Driver surface and
// overrides Resize to record invocations and optionally fail. A pointer is used
// as the driver so the recording state survives across calls.
type resizeDriver struct {
	fakeDriver
	resizeCalls  int
	lastCapacity int64
	resizeErr    error
}

func (d *resizeDriver) Resize(ctx context.Context, vol volume.Volume, req block.ResizeRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	d.resizeCalls++
	d.lastCapacity = req.CapacityBytes
	if d.resizeErr != nil {
		return volume.Volume{}, d.resizeErr
	}
	vol.CapacityBytes = req.CapacityBytes
	return vol, nil
}

func newResizePool(t *testing.T, capacityBytes int64, driver block.Driver, seed map[volume.ID]volume.Volume) *Service {
	t.Helper()
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, capacityBytes, driver)
	p.volumes = seed
	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}
	return service
}

func TestServiceResizeVolumeAdmitsDeltaAndUpdatesIndex(t *testing.T) {
	driver := &resizeDriver{}
	service := newResizePool(t, 100, driver, map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: 50},
	})

	resized, err := service.ResizeVolume(context.Background(), "pool-a", "vol-a", 120)
	if err != nil {
		t.Fatalf("ResizeVolume() error = %v, want nil", err)
	}
	if resized.CapacityBytes != 120 {
		t.Fatalf("ResizeVolume() CapacityBytes = %d, want 120", resized.CapacityBytes)
	}
	if driver.resizeCalls != 1 || driver.lastCapacity != 120 {
		t.Fatalf("driver Resize calls = %d capacity = %d, want 1 and 120", driver.resizeCalls, driver.lastCapacity)
	}

	usage, err := service.GetPoolUsage(context.Background(), "pool-a")
	if err != nil {
		t.Fatalf("GetPoolUsage() error = %v, want nil", err)
	}
	if usage.AllocatedBytes != 120 {
		t.Fatalf("AllocatedBytes = %d, want 120", usage.AllocatedBytes)
	}
}

func TestServiceResizeVolumeRejectsOvercommit(t *testing.T) {
	// capacity 100, overcommit 1.5 => limit 150. Existing 100 leaves 50 headroom.
	driver := &resizeDriver{}
	service := newResizePool(t, 100, driver, map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: 100},
	})

	if _, err := service.ResizeVolume(context.Background(), "pool-a", "vol-a", 151); !errors.Is(err, ErrPoolCapacityExceeded) {
		t.Fatalf("ResizeVolume() error = %v, want %v", err, ErrPoolCapacityExceeded)
	}
	if driver.resizeCalls != 0 {
		t.Fatalf("driver Resize calls = %d, want 0 after rejected admission", driver.resizeCalls)
	}

	registered, err := service.GetPool("pool-a")
	if err != nil {
		t.Fatalf("GetPool() error = %v, want nil", err)
	}
	if got := registered.volumes["vol-a"].CapacityBytes; got != 100 {
		t.Fatalf("indexed CapacityBytes = %d, want 100 (unchanged)", got)
	}
}

func TestServiceResizeVolumeZeroDeltaStillCallsDriver(t *testing.T) {
	driver := &resizeDriver{}
	service := newResizePool(t, 100, driver, map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: 50},
	})

	resized, err := service.ResizeVolume(context.Background(), "pool-a", "vol-a", 50)
	if err != nil {
		t.Fatalf("ResizeVolume() error = %v, want nil", err)
	}
	if resized.CapacityBytes != 50 {
		t.Fatalf("ResizeVolume() CapacityBytes = %d, want 50", resized.CapacityBytes)
	}
	if driver.resizeCalls != 1 || driver.lastCapacity != 50 {
		t.Fatalf("driver Resize calls = %d capacity = %d, want 1 and 50 (idempotent backstop)", driver.resizeCalls, driver.lastCapacity)
	}
}

func TestServiceResizeVolumeNotFound(t *testing.T) {
	driver := &resizeDriver{}
	service := newResizePool(t, 100, driver, map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: 50},
	})

	if _, err := service.ResizeVolume(context.Background(), "pool-a", "missing", 60); !errors.Is(err, volume.ErrVolumeNotFound) {
		t.Fatalf("ResizeVolume() error = %v, want %v", err, volume.ErrVolumeNotFound)
	}
	if driver.resizeCalls != 0 {
		t.Fatalf("driver Resize calls = %d, want 0 for unknown volume", driver.resizeCalls)
	}
}

func TestServiceResizeVolumeDriverFailureLeavesIndexUnchanged(t *testing.T) {
	driverErr := errors.New("qemu-img resize failed")
	driver := &resizeDriver{resizeErr: driverErr}
	service := newResizePool(t, 100, driver, map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: 50},
	})

	if _, err := service.ResizeVolume(context.Background(), "pool-a", "vol-a", 80); !errors.Is(err, driverErr) {
		t.Fatalf("ResizeVolume() error = %v, want %v", err, driverErr)
	}
	if driver.resizeCalls != 1 {
		t.Fatalf("driver Resize calls = %d, want 1", driver.resizeCalls)
	}

	registered, err := service.GetPool("pool-a")
	if err != nil {
		t.Fatalf("GetPool() error = %v, want nil", err)
	}
	if got := registered.volumes["vol-a"].CapacityBytes; got != 50 {
		t.Fatalf("indexed CapacityBytes = %d, want 50 (ledger not advanced on driver failure)", got)
	}
}

func TestServiceResizeVolumeRejectsNonBlockPool(t *testing.T) {
	service := NewService()
	if err := service.RegisterPool(newTestFilePool("file-pool", 100, &fakeImageDriver{})); err != nil {
		t.Fatalf("RegisterPool(file) error = %v, want nil", err)
	}

	if _, err := service.ResizeVolume(context.Background(), "file-pool", "vol-a", 60); !errors.Is(err, volume.ErrUnsupported) {
		t.Fatalf("ResizeVolume() error = %v, want %v", err, volume.ErrUnsupported)
	}
}

func TestServiceResizeVolumeRejectsShrink(t *testing.T) {
	driver := &resizeDriver{}
	service := newResizePool(t, 100, driver, map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: 50},
	})

	if _, err := service.ResizeVolume(context.Background(), "pool-a", "vol-a", 40); !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("ResizeVolume() shrink error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if driver.resizeCalls != 0 {
		t.Fatalf("driver Resize calls = %d, want 0 for shrink", driver.resizeCalls)
	}
}
