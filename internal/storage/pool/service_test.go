package pool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"testing"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/image"
	"github.com/suknna/govirta/internal/storage/volume"
)

type fakeDriver struct {
	actualUsedBytes int64
	actualUsedErr   error
}

func (d fakeDriver) DriverInfo(ctx context.Context) (block.DriverInfo, error) {
	if err := ctx.Err(); err != nil {
		return block.DriverInfo{}, err
	}
	return block.DriverInfo{Name: "fake"}, nil
}

func (d fakeDriver) Create(ctx context.Context, req block.CreateRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	return volume.Volume{ID: req.VolumeID, Name: req.Name, PoolName: req.PoolName, CapacityBytes: req.CapacityBytes}, nil
}

func (d fakeDriver) CreateFromReader(ctx context.Context, req block.CreateFromReaderRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	return volume.Volume{ID: req.VolumeID, Name: req.Name, PoolName: req.PoolName, CapacityBytes: req.CapacityBytes}, nil
}

func (d fakeDriver) Delete(ctx context.Context, vol volume.Volume) error {
	return ctx.Err()
}

func (d fakeDriver) GetActualUsedBytes(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if d.actualUsedErr != nil {
		return 0, d.actualUsedErr
	}
	return d.actualUsedBytes, nil
}

func (d fakeDriver) Publish(ctx context.Context, vol volume.Volume, req block.PublishRequest) (volume.PublishedVolume, error) {
	if err := ctx.Err(); err != nil {
		return volume.PublishedVolume{}, err
	}
	return volume.PublishedVolume{VolumeID: req.VolumeID, VMID: req.VMID, PoolName: vol.PoolName}, nil
}

func (d fakeDriver) Unpublish(ctx context.Context, vol volume.Volume, req block.UnpublishRequest) error {
	return ctx.Err()
}

func (d fakeDriver) Snapshot(ctx context.Context, vol volume.Volume, req block.SnapshotRequest) (volume.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return volume.Snapshot{}, err
	}
	return volume.Snapshot{Name: req.Name, VolumeID: vol.ID}, nil
}

func (d fakeDriver) Resize(ctx context.Context, vol volume.Volume, req block.ResizeRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	vol.CapacityBytes = req.CapacityBytes
	return vol, nil
}

func TestRegisterPoolRejectsInvalidAndDuplicate(t *testing.T) {
	service := NewService()

	invalidPools := []struct {
		name string
		pool *Pool
	}{
		{name: "nil pool", pool: nil},
		{name: "empty name", pool: newTestPool("", PoolTypeBlock, BackendLocalBlock, 1, fakeDriver{})},
		{name: "empty type", pool: newTestPool("pool-a", "", BackendLocalBlock, 1, fakeDriver{})},
		{name: "empty backend", pool: newTestPool("pool-a", PoolTypeBlock, "", 1, fakeDriver{})},
		{name: "empty storage root", pool: newTestPoolWithoutStorageRoot("pool-a")},
		{name: "zero capacity", pool: newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 0, fakeDriver{})},
		{name: "nil driver", pool: newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 1, nil)},
	}

	for _, tc := range invalidPools {
		t.Run(tc.name, func(t *testing.T) {
			if err := service.RegisterPool(tc.pool); !errors.Is(err, volume.ErrInvalidRequest) {
				t.Fatalf("RegisterPool() error = %v, want %v", err, volume.ErrInvalidRequest)
			}
		})
	}

	if err := service.RegisterPool(newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 1, fakeDriver{})); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}
	if err := service.RegisterPool(newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 1, fakeDriver{})); !errors.Is(err, ErrPoolAlreadyExists) {
		t.Fatalf("RegisterPool() error = %v, want %v", err, ErrPoolAlreadyExists)
	}
}

func TestRegisterPoolSupportsFilePoolAndRejectsDriverMismatch(t *testing.T) {
	service := NewService()
	if err := service.RegisterPool(newTestFilePool("file-pool", 100, &fakeImageDriver{})); err != nil {
		t.Fatalf("RegisterPool(file) error = %v, want nil", err)
	}

	if err := service.RegisterPool(&Pool{Config: testConfig("file-with-block", PoolTypeFile, BackendLocalFile, 100), Driver: fakeDriver{}}); !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("RegisterPool(file with block driver) error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if err := service.RegisterPool(&Pool{Config: testConfig("block-with-image", PoolTypeBlock, BackendLocalBlock, 100), ImageDriver: &fakeImageDriver{}}); !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("RegisterPool(block with image driver) error = %v, want %v", err, volume.ErrInvalidRequest)
	}
}

func TestErrPoolCapacityExceededSupportsWrapping(t *testing.T) {
	if !errors.Is(fmt.Errorf("wrap: %w", ErrPoolCapacityExceeded), ErrPoolCapacityExceeded) {
		t.Fatalf("wrapped ErrPoolCapacityExceeded does not match sentinel")
	}
}

func TestErrPoolRequiredSupportsWrapping(t *testing.T) {
	if !errors.Is(fmt.Errorf("wrap: %w", ErrPoolRequired), ErrPoolRequired) {
		t.Fatalf("wrapped ErrPoolRequired does not match sentinel")
	}
}

func TestCapacityAdmission(t *testing.T) {
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, fakeDriver{})
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: 100},
	}

	if err := p.ReserveCapacity(50); err != nil {
		t.Fatalf("ReserveCapacity(50) error = %v, want nil", err)
	}
	if err := p.ReserveCapacity(51); !errors.Is(err, ErrPoolCapacityExceeded) {
		t.Fatalf("ReserveCapacity(51) error = %v, want %v", err, ErrPoolCapacityExceeded)
	}
	if err := p.ReserveCapacity(0); !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("ReserveCapacity(0) error = %v, want %v", err, volume.ErrInvalidRequest)
	}
}

func TestCapacityAdmissionAvoidsAdditionOverflow(t *testing.T) {
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, math.MaxInt64, fakeDriver{})
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: math.MaxInt64 - 10},
	}

	if err := p.ReserveCapacity(10); err != nil {
		t.Fatalf("ReserveCapacity(10) error = %v, want nil", err)
	}
	if err := p.ReserveCapacity(11); !errors.Is(err, ErrPoolCapacityExceeded) {
		t.Fatalf("ReserveCapacity(11) error = %v, want %v", err, ErrPoolCapacityExceeded)
	}
}

func TestAllocatedBytesSaturatesAtMaxInt64(t *testing.T) {
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, math.MaxInt64, fakeDriver{})
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: math.MaxInt64 - 10},
		"vol-b": {ID: "vol-b", CapacityBytes: 20},
	}

	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}
	if err := p.ReserveCapacity(1); !errors.Is(err, ErrPoolCapacityExceeded) {
		t.Fatalf("ReserveCapacity(1) error = %v, want %v", err, ErrPoolCapacityExceeded)
	}

	usage, err := service.GetPoolUsage(context.Background(), "pool-a")
	if err != nil {
		t.Fatalf("GetPoolUsage() error = %v, want nil", err)
	}
	if usage.AllocatedBytes != math.MaxInt64 {
		t.Fatalf("AllocatedBytes = %d, want %d", usage.AllocatedBytes, int64(math.MaxInt64))
	}
}

func TestAllocationLimitSaturatesAtMaxInt64(t *testing.T) {
	if got := allocationLimit(math.MaxInt64); got != math.MaxInt64 {
		t.Fatalf("allocationLimit(math.MaxInt64) = %d, want %d", got, int64(math.MaxInt64))
	}
}

func TestGetPoolRequiresExplicitName(t *testing.T) {
	service := NewService()

	if _, err := service.GetPool(""); !errors.Is(err, ErrPoolRequired) {
		t.Fatalf("GetPool() error = %v, want %v", err, ErrPoolRequired)
	}
	if _, err := service.GetPool("missing"); !errors.Is(err, ErrPoolNotFound) {
		t.Fatalf("GetPool() error = %v, want %v", err, ErrPoolNotFound)
	}
}

func TestGetPoolReturnsCopy(t *testing.T) {
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, fakeDriver{})
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: 30},
	}

	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	got, err := service.GetPool("pool-a")
	if err != nil {
		t.Fatalf("GetPool() error = %v, want nil", err)
	}
	got.Config.Name = "mutated"
	got.volumes["vol-a"] = volume.Volume{ID: "vol-a", CapacityBytes: 90}

	registered, err := service.GetPool("pool-a")
	if err != nil {
		t.Fatalf("GetPool() after mutation error = %v, want nil", err)
	}
	if registered.Config.Name != "pool-a" {
		t.Fatalf("registered pool name = %q, want pool-a", registered.Config.Name)
	}
	if got := registered.volumes["vol-a"].CapacityBytes; got != 30 {
		t.Fatalf("registered volume capacity = %d, want 30", got)
	}
}

func TestGetPoolReturnsDeepCopiedVolumeState(t *testing.T) {
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, fakeDriver{})
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {
			ID:            "vol-a",
			CapacityBytes: 30,
			Context: map[string]string{
				"source": "registered",
			},
			Attachment: &volume.AttachmentState{
				VMID: "vm-a",
				Attachment: volume.Attachment{
					Kind: volume.AttachmentFile,
					Attributes: map[string]string{
						"path": "/var/lib/govirta/vol-a.img",
					},
				},
			},
		},
	}

	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	got, err := service.GetPool("pool-a")
	if err != nil {
		t.Fatalf("GetPool() error = %v, want nil", err)
	}
	gotVol := got.volumes["vol-a"]
	gotVol.Context["source"] = "mutated"
	gotVol.Attachment.VMID = "vm-mutated"
	gotVol.Attachment.Attachment.Attributes["path"] = "/mutated"

	registered, err := service.GetPool("pool-a")
	if err != nil {
		t.Fatalf("GetPool() after mutation error = %v, want nil", err)
	}
	registeredVol := registered.volumes["vol-a"]
	if got := registeredVol.Context["source"]; got != "registered" {
		t.Fatalf("registered volume context = %q, want registered", got)
	}
	if got := registeredVol.Attachment.VMID; got != "vm-a" {
		t.Fatalf("registered attachment VMID = %q, want vm-a", got)
	}
	if got := registeredVol.Attachment.Attachment.Attributes["path"]; got != "/var/lib/govirta/vol-a.img" {
		t.Fatalf("registered attachment path = %q, want /var/lib/govirta/vol-a.img", got)
	}
}

func TestGetPoolUsageReportsOvercommitAccounting(t *testing.T) {
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, fakeDriver{actualUsedBytes: 40})
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: 30},
		"vol-b": {ID: "vol-b", CapacityBytes: 80},
	}

	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	usage, err := service.GetPoolUsage(context.Background(), "pool-a")
	if err != nil {
		t.Fatalf("GetPoolUsage() error = %v, want nil", err)
	}

	want := Usage{
		PoolName:               "pool-a",
		Type:                   PoolTypeBlock,
		Backend:                BackendLocalBlock,
		CapacityBytes:          100,
		OvercommitRatio:        DefaultOvercommitRatio,
		AllocationLimitBytes:   150,
		AllocatedBytes:         110,
		ActualUsedBytes:        40,
		AvailableForAllocation: 40,
	}
	if usage != want {
		t.Fatalf("GetPoolUsage() = %+v, want %+v", usage, want)
	}
}

func TestGetPoolUsageForFilePoolIncludesPendingImages(t *testing.T) {
	service := NewService()
	imageDriver := &fakeImageDriver{actualUsedBytes: 12}
	p := newTestFilePool("file-pool", 100, imageDriver)
	p.images = map[string]ImageRecord{
		"pending": {ID: "pending", Format: diskformat.FormatQCOW2, DeclaredSizeBytes: 30, State: ImageStatePending},
		"ready":   {ID: "ready", Format: diskformat.FormatRaw, DeclaredSizeBytes: 40, State: ImageStateReady},
	}
	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	usage, err := service.GetPoolUsage(context.Background(), "file-pool")
	if err != nil {
		t.Fatalf("GetPoolUsage() error = %v, want nil", err)
	}

	want := Usage{
		PoolName:               "file-pool",
		Type:                   PoolTypeFile,
		Backend:                BackendLocalFile,
		CapacityBytes:          100,
		OvercommitRatio:        DefaultFileOvercommitRatio,
		AllocationLimitBytes:   100,
		AllocatedBytes:         70,
		ActualUsedBytes:        12,
		AvailableForAllocation: 30,
	}
	if usage != want {
		t.Fatalf("GetPoolUsage() = %+v, want %+v", usage, want)
	}
}

func TestListPoolsReturnsCopies(t *testing.T) {
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, fakeDriver{})
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {ID: "vol-a", CapacityBytes: 30},
	}

	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	pools, err := service.ListPools(context.Background())
	if err != nil {
		t.Fatalf("ListPools() error = %v, want nil", err)
	}
	if len(pools) != 1 {
		t.Fatalf("ListPools() returned %d pools, want 1", len(pools))
	}

	pools[0].Config.Name = "mutated"
	pools[0].volumes["vol-a"] = volume.Volume{ID: "vol-a", CapacityBytes: 90}

	registered, err := service.GetPool("pool-a")
	if err != nil {
		t.Fatalf("GetPool() error = %v, want nil", err)
	}
	if registered.Config.Name != "pool-a" {
		t.Fatalf("registered pool name = %q, want pool-a", registered.Config.Name)
	}
	if got := registered.volumes["vol-a"].CapacityBytes; got != 30 {
		t.Fatalf("registered volume capacity = %d, want 30", got)
	}
}

func TestListPoolsHonorsCanceledContext(t *testing.T) {
	service := NewService()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.ListPools(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListPools() error = %v, want %v", err, context.Canceled)
	}
}

func TestGetPoolUsagePropagatesDriverError(t *testing.T) {
	driverErr := errors.New("actual usage unavailable")
	service := NewService()
	if err := service.RegisterPool(newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, fakeDriver{actualUsedErr: driverErr})); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	if _, err := service.GetPoolUsage(context.Background(), "pool-a"); !errors.Is(err, driverErr) {
		t.Fatalf("GetPoolUsage() error = %v, want %v", err, driverErr)
	}
}

func TestGetPoolUsageHonorsCanceledContext(t *testing.T) {
	service := NewService()
	if err := service.RegisterPool(newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, fakeDriver{})); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.GetPoolUsage(ctx, "pool-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetPoolUsage() error = %v, want %v", err, context.Canceled)
	}
	if _, err := service.GetPoolUsage(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetPoolUsage() with empty pool name error = %v, want %v", err, context.Canceled)
	}
}

func TestCreateVolumeAdmitsCapacityAndWritesIndex(t *testing.T) {
	driver := &lifecycleDriver{}
	service := NewService()
	if err := service.RegisterPool(newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	req := newCreateRequest("vol-a", "vol-a", 50)
	created, err := service.CreateVolume(context.Background(), "pool-a", req)
	if err != nil {
		t.Fatalf("CreateVolume() error = %v, want nil", err)
	}
	if created.ID != "vol-a" || created.State != volume.StateAvailable {
		t.Fatalf("CreateVolume() = %+v, want indexed available volume", created)
	}
	if driver.createCalls != 1 {
		t.Fatalf("create driver calls = %d, want 1", driver.createCalls)
	}

	registered, err := service.GetPool("pool-a")
	if err != nil {
		t.Fatalf("GetPool() error = %v, want nil", err)
	}
	if _, exists := registered.volumes["vol-a"]; !exists {
		t.Fatalf("indexed volume missing after CreateVolume")
	}

	if _, err := service.CreateVolume(context.Background(), "pool-a", newCreateRequest("vol-b", "vol-b", 101)); !errors.Is(err, ErrPoolCapacityExceeded) {
		t.Fatalf("CreateVolume() capacity error = %v, want %v", err, ErrPoolCapacityExceeded)
	}
	if driver.createCalls != 1 {
		t.Fatalf("create driver calls after rejected capacity = %d, want 1", driver.createCalls)
	}
}

func TestCreateVolumeFromReaderAdmitsCapacityAndWritesIndex(t *testing.T) {
	driver := &lifecycleDriver{}
	service := NewService()
	if err := service.RegisterPool(newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	req := newCreateFromReaderRequest("vol-a", "vol-a", 50)
	created, err := service.CreateVolumeFromReader(context.Background(), "pool-a", req)
	if err != nil {
		t.Fatalf("CreateVolumeFromReader() error = %v, want nil", err)
	}
	if created.ID != "vol-a" || created.State != volume.StateAvailable {
		t.Fatalf("CreateVolumeFromReader() = %+v, want indexed available volume", created)
	}
	if driver.createFromReaderCalls != 1 {
		t.Fatalf("create from reader driver calls = %d, want 1", driver.createFromReaderCalls)
	}
}

func TestCreateVolumeRejectsFilePool(t *testing.T) {
	service := NewService()
	if err := service.RegisterPool(newTestFilePool("file-pool", 100, &fakeImageDriver{})); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	_, err := service.CreateVolume(context.Background(), "file-pool", newCreateRequest("vol-a", "vol-a", 50))
	if !errors.Is(err, volume.ErrUnsupported) {
		t.Fatalf("CreateVolume(file pool) error = %v, want %v", err, volume.ErrUnsupported)
	}
}

func TestPutImageDuplicateIDReturnsImageExists(t *testing.T) {
	service := NewService()
	if err := service.RegisterPool(newTestFilePool("file-pool", 100, &fakeImageDriver{})); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	writer, err := service.PutImage(context.Background(), "file-pool", newPutRequest("image-a", 40))
	if err != nil {
		t.Fatalf("PutImage() first error = %v, want nil", err)
	}
	if _, err := service.PutImage(context.Background(), "file-pool", newPutRequest("image-a", 40)); !errors.Is(err, image.ErrImageExists) {
		t.Fatalf("PutImage() duplicate pending error = %v, want %v", err, image.ErrImageExists)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if _, err := service.PutImage(context.Background(), "file-pool", newPutRequest("image-a", 40)); !errors.Is(err, image.ErrImageExists) {
		t.Fatalf("PutImage() duplicate ready error = %v, want %v", err, image.ErrImageExists)
	}
}

func TestPutImagePendingCapacityPreventsOvercommit(t *testing.T) {
	service := NewService()
	imageDriver := &fakeImageDriver{}
	if err := service.RegisterPool(newTestFilePool("file-pool", 100, imageDriver)); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	writer, err := service.PutImage(context.Background(), "file-pool", newPutRequest("image-a", 80))
	if err != nil {
		t.Fatalf("PutImage() first error = %v, want nil", err)
	}
	if _, err := service.PutImage(context.Background(), "file-pool", newPutRequest("image-b", 30)); !errors.Is(err, ErrPoolCapacityExceeded) {
		t.Fatalf("PutImage() capacity error = %v, want %v", err, ErrPoolCapacityExceeded)
	}
	if imageDriver.putCalls != 1 {
		t.Fatalf("image driver Put calls = %d, want 1", imageDriver.putCalls)
	}
	if err := writer.Cancel(); err != nil {
		t.Fatalf("Cancel() error = %v, want nil", err)
	}
}

func TestPutImageCloseMovesReadyAndGetImageSucceeds(t *testing.T) {
	service := NewService()
	imageDriver := &fakeImageDriver{}
	if err := service.RegisterPool(newTestFilePool("file-pool", 100, imageDriver)); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	writer, err := service.PutImage(context.Background(), "file-pool", newPutRequest("image-a", 40))
	if err != nil {
		t.Fatalf("PutImage() error = %v, want nil", err)
	}
	if _, err := writer.Write([]byte("image-bytes")); err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if err := writer.Close(); !errors.Is(err, image.ErrInvalidImage) {
		t.Fatalf("second Close() error = %v, want %v", err, image.ErrInvalidImage)
	}

	reader, err := service.GetImage(context.Background(), "file-pool", image.GetRequest{ImageID: "image-a"})
	if err != nil {
		t.Fatalf("GetImage() error = %v, want nil", err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		t.Fatalf("ReadAll() error = %v, want nil", readErr)
	}
	if closeErr != nil {
		t.Fatalf("reader Close() error = %v, want nil", closeErr)
	}
	if string(got) != "image-bytes" {
		t.Fatalf("GetImage() bytes = %q, want image-bytes", got)
	}
}

func TestPutImageCancelRemovesPendingAndReleasesCapacity(t *testing.T) {
	service := NewService()
	if err := service.RegisterPool(newTestFilePool("file-pool", 100, &fakeImageDriver{})); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	writer, err := service.PutImage(context.Background(), "file-pool", newPutRequest("image-a", 80))
	if err != nil {
		t.Fatalf("PutImage() error = %v, want nil", err)
	}
	if err := writer.Cancel(); err != nil {
		t.Fatalf("Cancel() error = %v, want nil", err)
	}
	if err := writer.Cancel(); !errors.Is(err, image.ErrInvalidImage) {
		t.Fatalf("second Cancel() error = %v, want %v", err, image.ErrInvalidImage)
	}
	if _, err := service.GetImage(context.Background(), "file-pool", image.GetRequest{ImageID: "image-a"}); !errors.Is(err, image.ErrImageNotFound) {
		t.Fatalf("GetImage() canceled image error = %v, want %v", err, image.ErrImageNotFound)
	}
	if writer, err := service.PutImage(context.Background(), "file-pool", newPutRequest("image-b", 100)); err != nil {
		t.Fatalf("PutImage() after cancel error = %v, want nil", err)
	} else if err := writer.Cancel(); err != nil {
		t.Fatalf("Cancel() image-b error = %v, want nil", err)
	}
}

func TestPutImageCanceledContextDoesNotCallDriver(t *testing.T) {
	service := NewService()
	imageDriver := &fakeImageDriver{}
	if err := service.RegisterPool(newTestFilePool("file-pool", 100, imageDriver)); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.PutImage(ctx, "file-pool", newPutRequest("image-a", 40)); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutImage() error = %v, want %v", err, context.Canceled)
	}
	if imageDriver.putCalls != 0 {
		t.Fatalf("image driver Put calls = %d, want 0", imageDriver.putCalls)
	}
}

func TestDeleteImageRemovesReadyImageAndFreesAllocation(t *testing.T) {
	service := NewService()
	if err := service.RegisterPool(newTestFilePool("file-pool", 100, &fakeImageDriver{})); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}
	writer, err := service.PutImage(context.Background(), "file-pool", newPutRequest("image-a", 80))
	if err != nil {
		t.Fatalf("PutImage() error = %v, want nil", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}

	if err := service.DeleteImage(context.Background(), "file-pool", image.DeleteRequest{ImageID: "image-a"}); err != nil {
		t.Fatalf("DeleteImage() error = %v, want nil", err)
	}
	if _, err := service.GetImage(context.Background(), "file-pool", image.GetRequest{ImageID: "image-a"}); !errors.Is(err, image.ErrImageNotFound) {
		t.Fatalf("GetImage() deleted image error = %v, want %v", err, image.ErrImageNotFound)
	}
	writer, err = service.PutImage(context.Background(), "file-pool", newPutRequest("image-b", 100))
	if err != nil {
		t.Fatalf("PutImage() after delete error = %v, want nil", err)
	}
	if err := writer.Cancel(); err != nil {
		t.Fatalf("Cancel() image-b error = %v, want nil", err)
	}
}

func TestCreateVolumeDuplicateSameSpecIsIdempotent(t *testing.T) {
	driver := &lifecycleDriver{}
	service := NewService()
	if err := service.RegisterPool(newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	req := newCreateRequest("vol-a", "vol-a", 50)
	first, err := service.CreateVolume(context.Background(), "pool-a", req)
	if err != nil {
		t.Fatalf("CreateVolume() first error = %v, want nil", err)
	}
	second, err := service.CreateVolume(context.Background(), "pool-a", req)
	if err != nil {
		t.Fatalf("CreateVolume() second error = %v, want nil", err)
	}
	if second.ID != first.ID || second.Name != first.Name || second.CapacityBytes != first.CapacityBytes {
		t.Fatalf("second volume = %+v, want %+v", second, first)
	}
	if driver.createCalls != 1 {
		t.Fatalf("create driver calls = %d, want 1", driver.createCalls)
	}
}

func TestCreateVolumeDuplicateConflict(t *testing.T) {
	driver := &lifecycleDriver{}
	service := NewService()
	if err := service.RegisterPool(newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	if _, err := service.CreateVolume(context.Background(), "pool-a", newCreateRequest("vol-a", "vol-a", 50)); err != nil {
		t.Fatalf("CreateVolume() first error = %v, want nil", err)
	}
	if _, err := service.CreateVolume(context.Background(), "pool-a", newCreateRequest("vol-conflict", "vol-a", 50)); !errors.Is(err, volume.ErrVolumeConflict) {
		t.Fatalf("CreateVolume() conflict error = %v, want %v", err, volume.ErrVolumeConflict)
	}
	if driver.createCalls != 1 {
		t.Fatalf("create driver calls = %d, want 1", driver.createCalls)
	}
}

func TestCreateVolumeConcurrentCapacityAdmissionDoesNotOverAllocate(t *testing.T) {
	driver := &lifecycleDriver{}
	service := NewService()
	if err := service.RegisterPool(newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, req := range []block.CreateRequest{
		newCreateRequest("vol-a", "vol-a", 100),
		newCreateRequest("vol-b", "vol-b", 100),
	} {
		wg.Add(1)
		go func(req block.CreateRequest) {
			defer wg.Done()
			_, err := service.CreateVolume(context.Background(), "pool-a", req)
			errs <- err
		}(req)
	}
	wg.Wait()
	close(errs)

	var successes int
	var capacityFailures int
	var conflictFailures int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrPoolCapacityExceeded):
			capacityFailures++
		case errors.Is(err, volume.ErrVolumeConflict):
			conflictFailures++
		default:
			t.Fatalf("CreateVolume() concurrent error = %v, want nil, %v, or %v", err, ErrPoolCapacityExceeded, volume.ErrVolumeConflict)
		}
	}
	if successes != 1 || capacityFailures+conflictFailures != 1 {
		t.Fatalf("concurrent results = success:%d capacity:%d conflict:%d, want one success and one rejected", successes, capacityFailures, conflictFailures)
	}
	if driver.createCalls != 1 {
		t.Fatalf("create driver calls = %d, want 1", driver.createCalls)
	}

	usage, err := service.GetPoolUsage(context.Background(), "pool-a")
	if err != nil {
		t.Fatalf("GetPoolUsage() error = %v, want nil", err)
	}
	if usage.AllocatedBytes > usage.AllocationLimitBytes {
		t.Fatalf("AllocatedBytes = %d exceeds limit %d", usage.AllocatedBytes, usage.AllocationLimitBytes)
	}
}

func TestCreateVolumeOverridesDriverReturnedID(t *testing.T) {
	driver := &lifecycleDriver{createID: "driver-wrong-id"}
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)
	p.volumes = map[volume.ID]volume.Volume{
		"driver-wrong-id": {
			ID:            "driver-wrong-id",
			Name:          "existing",
			PoolName:      "pool-a",
			VMID:          "vm-existing",
			VMName:        "vm-existing",
			CapacityBytes: 10,
			State:         volume.StateAvailable,
		},
	}
	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	created, err := service.CreateVolume(context.Background(), "pool-a", newCreateRequest("vol-a", "vol-a", 50))
	if err != nil {
		t.Fatalf("CreateVolume() error = %v, want nil", err)
	}
	if created.ID != "vol-a" {
		t.Fatalf("created ID = %q, want vol-a", created.ID)
	}

	registered, err := service.GetPool("pool-a")
	if err != nil {
		t.Fatalf("GetPool() error = %v, want nil", err)
	}
	if stored := registered.volumes["vol-a"]; stored.ID != "vol-a" || stored.Name != "vol-a" {
		t.Fatalf("stored created volume = %+v, want ID/name vol-a", stored)
	}
	if stored := registered.volumes["driver-wrong-id"]; stored.Name != "existing" {
		t.Fatalf("existing wrong-id volume = %+v, want unchanged existing volume", stored)
	}
}

func TestDeleteVolumeNotFoundAndInUse(t *testing.T) {
	driver := &lifecycleDriver{}
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {
			ID:            "vol-a",
			Name:          "vol-a",
			PoolName:      "pool-a",
			VMID:          "vm-a",
			VMName:        "vm-a",
			CapacityBytes: 50,
			State:         volume.StatePublished,
			Attachment: &volume.AttachmentState{
				VMID: "vm-a",
			},
		},
	}
	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	if err := service.DeleteVolume(context.Background(), "pool-a", "missing"); !errors.Is(err, volume.ErrVolumeNotFound) {
		t.Fatalf("DeleteVolume() missing error = %v, want %v", err, volume.ErrVolumeNotFound)
	}
	if err := service.DeleteVolume(context.Background(), "pool-a", "vol-a"); !errors.Is(err, volume.ErrVolumeInUse) {
		t.Fatalf("DeleteVolume() in-use error = %v, want %v", err, volume.ErrVolumeInUse)
	}
	if driver.deleteCalls != 0 {
		t.Fatalf("delete driver calls = %d, want 0", driver.deleteCalls)
	}
}

func TestPublishAndUnpublishRejectMismatchedRequestID(t *testing.T) {
	driver := &lifecycleDriver{}
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {
			ID:            "vol-a",
			Name:          "vol-a",
			PoolName:      "pool-a",
			VMID:          "vm-a",
			VMName:        "vm-a",
			CapacityBytes: 50,
			State:         volume.StateAvailable,
		},
	}
	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	if _, err := service.PublishVolume(context.Background(), "pool-a", "vol-a", block.PublishRequest{VolumeID: "vol-b", VMID: "vm-a"}); !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("PublishVolume() mismatched ID error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if err := service.UnpublishVolume(context.Background(), "pool-a", "vol-a", block.UnpublishRequest{VolumeID: "vol-b", VMID: "vm-a"}); !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("UnpublishVolume() mismatched ID error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if driver.publishCalls != 0 || driver.unpublishCalls != 0 {
		t.Fatalf("driver calls = publish:%d unpublish:%d, want zero", driver.publishCalls, driver.unpublishCalls)
	}
}

func TestPublishVolumeRejectsMismatchedVMID(t *testing.T) {
	driver := &lifecycleDriver{}
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {
			ID:            "vol-a",
			Name:          "vol-a",
			PoolName:      "pool-a",
			VMID:          "vm-a",
			VMName:        "vm-a",
			CapacityBytes: 50,
			State:         volume.StateAvailable,
		},
	}
	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	_, err := service.PublishVolume(context.Background(), "pool-a", "vol-a", block.PublishRequest{VolumeID: "vol-a", VMID: "vm-b"})
	if !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("PublishVolume() error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if driver.publishCalls != 0 {
		t.Fatalf("publish driver calls = %d, want 0", driver.publishCalls)
	}
}

func TestUnpublishVolumeRejectsMismatchedVMID(t *testing.T) {
	driver := &lifecycleDriver{}
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {
			ID:            "vol-a",
			Name:          "vol-a",
			PoolName:      "pool-a",
			VMID:          "vm-a",
			VMName:        "vm-a",
			CapacityBytes: 50,
			State:         volume.StatePublished,
			Attachment: &volume.AttachmentState{
				VMID: "vm-a",
			},
		},
	}
	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	err := service.UnpublishVolume(context.Background(), "pool-a", "vol-a", block.UnpublishRequest{VolumeID: "vol-a", VMID: "vm-b"})
	if !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("UnpublishVolume() error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if driver.unpublishCalls != 0 {
		t.Fatalf("unpublish driver calls = %d, want 0", driver.unpublishCalls)
	}
}

func TestPublishAndUnpublishVolumeAreIdempotent(t *testing.T) {
	driver := &lifecycleDriver{}
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {
			ID:            "vol-a",
			Name:          "vol-a",
			PoolName:      "pool-a",
			VMID:          "vm-a",
			VMName:        "vm-a",
			CapacityBytes: 50,
			State:         volume.StateAvailable,
		},
	}
	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	req := block.PublishRequest{VolumeID: "vol-a", VMID: "vm-a", ReadOnly: true}
	first, err := service.PublishVolume(context.Background(), "pool-a", "vol-a", req)
	if err != nil {
		t.Fatalf("PublishVolume() first error = %v, want nil", err)
	}
	second, err := service.PublishVolume(context.Background(), "pool-a", "vol-a", req)
	if err != nil {
		t.Fatalf("PublishVolume() second error = %v, want nil", err)
	}
	if first.Attachment.Path != second.Attachment.Path || !second.Attachment.ReadOnly {
		t.Fatalf("second publish = %+v, want idempotent attachment %+v", second, first)
	}
	if driver.publishCalls != 1 {
		t.Fatalf("publish driver calls = %d, want 1", driver.publishCalls)
	}

	unpublishReq := block.UnpublishRequest{VolumeID: "vol-a", VMID: "vm-a"}
	if err := service.UnpublishVolume(context.Background(), "pool-a", "vol-a", unpublishReq); err != nil {
		t.Fatalf("UnpublishVolume() first error = %v, want nil", err)
	}
	if err := service.UnpublishVolume(context.Background(), "pool-a", "vol-a", unpublishReq); err != nil {
		t.Fatalf("UnpublishVolume() second error = %v, want nil", err)
	}
	if driver.unpublishCalls != 1 {
		t.Fatalf("unpublish driver calls = %d, want 1", driver.unpublishCalls)
	}
}

func TestConcurrentPublishSameVolumeCallsDriverOnce(t *testing.T) {
	driver := &lifecycleDriver{}
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {
			ID:            "vol-a",
			Name:          "vol-a",
			PoolName:      "pool-a",
			VMID:          "vm-a",
			VMName:        "vm-a",
			CapacityBytes: 50,
			State:         volume.StateAvailable,
		},
	}
	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.PublishVolume(context.Background(), "pool-a", "vol-a", block.PublishRequest{VolumeID: "vol-a", VMID: "vm-a", ReadOnly: true})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("PublishVolume() concurrent error = %v, want nil", err)
		}
	}
	if driver.publishCalls != 1 {
		t.Fatalf("publish driver calls = %d, want 1", driver.publishCalls)
	}
	registered, err := service.GetPool("pool-a")
	if err != nil {
		t.Fatalf("GetPool() error = %v, want nil", err)
	}
	stored := registered.volumes["vol-a"]
	if stored.State != volume.StatePublished || stored.Attachment == nil || stored.Attachment.VMID != "vm-a" || !stored.Attachment.ReadOnly {
		t.Fatalf("stored volume after concurrent publish = %+v, want published attachment", stored)
	}
}

func TestPublishAndDeleteConcurrencyDoesNotDeletePublishedVolume(t *testing.T) {
	driver := &lifecycleDriver{
		publishStarted: make(chan struct{}),
		releasePublish: make(chan struct{}),
	}
	service := NewService()
	p := newTestPool("pool-a", PoolTypeBlock, BackendLocalBlock, 100, driver)
	p.volumes = map[volume.ID]volume.Volume{
		"vol-a": {
			ID:            "vol-a",
			Name:          "vol-a",
			PoolName:      "pool-a",
			VMID:          "vm-a",
			VMName:        "vm-a",
			CapacityBytes: 50,
			State:         volume.StateAvailable,
		},
	}
	if err := service.RegisterPool(p); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}

	publishDone := make(chan error, 1)
	go func() {
		_, err := service.PublishVolume(context.Background(), "pool-a", "vol-a", block.PublishRequest{VolumeID: "vol-a", VMID: "vm-a"})
		publishDone <- err
	}()
	<-driver.publishStarted

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- service.DeleteVolume(context.Background(), "pool-a", "vol-a")
	}()
	close(driver.releasePublish)

	if err := <-publishDone; err != nil {
		t.Fatalf("PublishVolume() error = %v, want nil", err)
	}
	if err := <-deleteDone; !errors.Is(err, volume.ErrVolumeInUse) {
		t.Fatalf("DeleteVolume() concurrent error = %v, want %v", err, volume.ErrVolumeInUse)
	}
	if driver.publishCalls != 1 {
		t.Fatalf("publish driver calls = %d, want 1", driver.publishCalls)
	}
	if driver.deleteCalls != 0 {
		t.Fatalf("delete driver calls = %d, want 0", driver.deleteCalls)
	}
}

func newTestPool(name string, typ PoolType, backend BackendType, capacityBytes int64, driver block.Driver) *Pool {
	return &Pool{
		Config: Config{
			Name:          name,
			Type:          typ,
			Backend:       backend,
			StorageRoot:   "/var/lib/govirta/storage/" + name,
			CapacityBytes: capacityBytes,
		},
		Driver: driver,
	}
}

func newTestFilePool(name string, capacityBytes int64, driver image.Driver) *Pool {
	return &Pool{
		Config:      testConfig(name, PoolTypeFile, BackendLocalFile, capacityBytes),
		ImageDriver: driver,
	}
}

func testConfig(name string, typ PoolType, backend BackendType, capacityBytes int64) Config {
	return Config{
		Name:          name,
		Type:          typ,
		Backend:       backend,
		StorageRoot:   "/var/lib/govirta/storage/" + name,
		CapacityBytes: capacityBytes,
	}
}

func newTestPoolWithoutStorageRoot(name string) *Pool {
	p := newTestPool(name, PoolTypeBlock, BackendLocalBlock, 1, fakeDriver{})
	p.Config.StorageRoot = ""
	return p
}

type lifecycleDriver struct {
	createCalls           int
	createFromReaderCalls int
	deleteCalls           int
	publishCalls          int
	unpublishCalls        int
	createID              volume.ID
	publishStarted        chan struct{}
	releasePublish        chan struct{}
}

func (d *lifecycleDriver) DriverInfo(ctx context.Context) (block.DriverInfo, error) {
	if err := ctx.Err(); err != nil {
		return block.DriverInfo{}, err
	}
	return block.DriverInfo{Name: "lifecycle"}, nil
}

func (d *lifecycleDriver) Create(ctx context.Context, req block.CreateRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	d.createCalls++
	id := req.VolumeID
	if d.createID != "" {
		id = d.createID
	}
	return volume.Volume{
		ID:            id,
		Name:          req.Name,
		VMID:          req.VMID,
		VMName:        req.VMName,
		PoolName:      req.PoolName,
		DiskIndex:     req.DiskIndex,
		CapacityBytes: req.CapacityBytes,
		State:         volume.StateAvailable,
	}, nil
}

func (d *lifecycleDriver) CreateFromReader(ctx context.Context, req block.CreateFromReaderRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	d.createFromReaderCalls++
	id := req.VolumeID
	if d.createID != "" {
		id = d.createID
	}
	return volume.Volume{
		ID:            id,
		Name:          req.Name,
		VMID:          req.VMID,
		VMName:        req.VMName,
		PoolName:      req.PoolName,
		DiskIndex:     req.DiskIndex,
		CapacityBytes: req.CapacityBytes,
		State:         volume.StateAvailable,
	}, nil
}

func (d *lifecycleDriver) Delete(ctx context.Context, vol volume.Volume) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.deleteCalls++
	return nil
}

func (d *lifecycleDriver) GetActualUsedBytes(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}

func (d *lifecycleDriver) Publish(ctx context.Context, vol volume.Volume, req block.PublishRequest) (volume.PublishedVolume, error) {
	if err := ctx.Err(); err != nil {
		return volume.PublishedVolume{}, err
	}
	d.publishCalls++
	if d.publishStarted != nil {
		close(d.publishStarted)
	}
	if d.releasePublish != nil {
		<-d.releasePublish
	}
	return volume.PublishedVolume{
		VolumeID: req.VolumeID,
		VMID:     req.VMID,
		PoolName: vol.PoolName,
		Attachment: volume.Attachment{
			Kind:     volume.AttachmentFile,
			Format:   volume.DiskFormatQCOW2,
			Path:     "/var/lib/govirta/storage/" + string(req.VolumeID) + ".qcow2",
			ReadOnly: req.ReadOnly,
		},
	}, nil
}

func (d *lifecycleDriver) Unpublish(ctx context.Context, vol volume.Volume, req block.UnpublishRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.unpublishCalls++
	return nil
}

func (d *lifecycleDriver) Snapshot(ctx context.Context, vol volume.Volume, req block.SnapshotRequest) (volume.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return volume.Snapshot{}, err
	}
	return volume.Snapshot{}, volume.ErrUnsupported
}

func (d *lifecycleDriver) Resize(ctx context.Context, vol volume.Volume, req block.ResizeRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	return volume.Volume{}, volume.ErrUnsupported
}

func newCreateRequest(name string, id volume.ID, capacityBytes int64) block.CreateRequest {
	return block.CreateRequest{
		Name:          name,
		PoolName:      "pool-a",
		VMID:          "vm-a",
		VMName:        "vm-a",
		VolumeID:      id,
		DiskIndex:     0,
		CapacityBytes: capacityBytes,
	}
}

func newCreateFromReaderRequest(name string, id volume.ID, capacityBytes int64) block.CreateFromReaderRequest {
	return block.CreateFromReaderRequest{
		Reader:        bytes.NewReader([]byte("image")),
		Format:        diskformat.FormatQCOW2,
		Name:          name,
		PoolName:      "pool-a",
		VMID:          "vm-a",
		VMName:        "vm-a",
		VolumeID:      id,
		DiskIndex:     0,
		CapacityBytes: capacityBytes,
	}
}

func newPutRequest(id string, declaredSizeBytes int64) image.PutRequest {
	return image.PutRequest{ImageID: id, Format: diskformat.FormatQCOW2, DeclaredSizeBytes: declaredSizeBytes}
}

type fakeImageDriver struct {
	actualUsedBytes int64
	putCalls        int
	deleteCalls     int
	images          map[string][]byte
}

func (d *fakeImageDriver) DriverInfo(ctx context.Context) (image.DriverInfo, error) {
	if err := ctx.Err(); err != nil {
		return image.DriverInfo{}, err
	}
	return image.DriverInfo{Name: "fake-image"}, nil
}

func (d *fakeImageDriver) Put(ctx context.Context, req image.PutRequest) (image.ImageWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.putCalls++
	return &fakeImageWriter{driver: d, imageID: req.ImageID}, nil
}

func (d *fakeImageDriver) Get(ctx context.Context, req image.GetRequest) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, exists := d.images[req.ImageID]
	if !exists {
		return nil, image.ErrImageNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (d *fakeImageDriver) Delete(ctx context.Context, req image.DeleteRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.deleteCalls++
	delete(d.images, req.ImageID)
	return nil
}

func (d *fakeImageDriver) GetActualUsedBytes(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return d.actualUsedBytes, nil
}

type fakeImageWriter struct {
	driver  *fakeImageDriver
	imageID string
	data    bytes.Buffer
}

func (w *fakeImageWriter) Write(p []byte) (int, error) {
	return w.data.Write(p)
}

func (w *fakeImageWriter) Close() error {
	if w.driver.images == nil {
		w.driver.images = make(map[string][]byte)
	}
	w.driver.images[w.imageID] = append([]byte(nil), w.data.Bytes()...)
	return nil
}

func (w *fakeImageWriter) Cancel() error {
	return nil
}
