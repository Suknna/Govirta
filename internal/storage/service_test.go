package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/pool"
	"github.com/suknna/govirta/internal/storage/volume"
)

func TestCreateVolumeValidation(t *testing.T) {
	service, _ := newTestVolumeService(t)

	tests := []struct {
		name string
		req  CreateVolumeRequest
		want error
	}{
		{name: "pool required", req: newCreateVolumeRequest(func(req *CreateVolumeRequest) { req.PoolName = "" }), want: ErrPoolRequired},
		{name: "vm id required", req: newCreateVolumeRequest(func(req *CreateVolumeRequest) { req.VMID = "" }), want: ErrInvalidRequest},
		{name: "vm name required", req: newCreateVolumeRequest(func(req *CreateVolumeRequest) { req.VMName = "" }), want: ErrInvalidRequest},
		{name: "disk index non-negative", req: newCreateVolumeRequest(func(req *CreateVolumeRequest) { req.Spec.DiskIndex = -1 }), want: ErrInvalidRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.CreateVolume(context.Background(), tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("CreateVolume() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCreateRootVolumeAndCreateDataVolumeSetRoles(t *testing.T) {
	service, _ := newTestVolumeService(t)

	root, err := service.CreateRootVolume(context.Background(), CreateRootVolumeRequest{
		VMID:          "vm-a",
		VMName:        "vm-a",
		PoolName:      "pool-a",
		Name:          "root",
		DiskIndex:     0,
		CapacityBytes: 10,
	})
	if err != nil {
		t.Fatalf("CreateRootVolume() error = %v, want nil", err)
	}
	if root.Role != volume.RoleRoot {
		t.Fatalf("root role = %q, want %q", root.Role, volume.RoleRoot)
	}

	data, err := service.CreateDataVolume(context.Background(), CreateDataVolumeRequest{
		VMID:          "vm-a",
		VMName:        "vm-a",
		PoolName:      "pool-a",
		Name:          "data",
		DiskIndex:     1,
		CapacityBytes: 10,
	})
	if err != nil {
		t.Fatalf("CreateDataVolume() error = %v, want nil", err)
	}
	if data.Role != volume.RoleData {
		t.Fatalf("data role = %q, want %q", data.Role, volume.RoleData)
	}
}

func TestCreateVolumeSameIdentityDifferentNameConflicts(t *testing.T) {
	service, driver := newTestVolumeService(t)

	if _, err := service.CreateRootVolume(context.Background(), CreateRootVolumeRequest{
		VMID:          "vm-a",
		VMName:        "vm-a",
		PoolName:      "pool-a",
		Name:          "root",
		DiskIndex:     0,
		CapacityBytes: 10,
	}); err != nil {
		t.Fatalf("CreateRootVolume() first error = %v, want nil", err)
	}
	if _, err := service.CreateRootVolume(context.Background(), CreateRootVolumeRequest{
		VMID:          "vm-a",
		VMName:        "vm-a",
		PoolName:      "pool-a",
		Name:          "root-renamed",
		DiskIndex:     0,
		CapacityBytes: 10,
	}); !errors.Is(err, ErrVolumeConflict) {
		t.Fatalf("CreateRootVolume() conflict error = %v, want %v", err, ErrVolumeConflict)
	}
	if driver.createCalls != 1 {
		t.Fatalf("create driver calls = %d, want 1", driver.createCalls)
	}
}

func TestPublishVolumeReturnsAttachmentAndIsIdempotent(t *testing.T) {
	service, driver := newTestVolumeService(t)
	vol := createRootVolume(t, service)

	req := PublishVolumeRequest{PoolName: "pool-a", VolumeID: vol.ID, VMID: "vm-a", ReadOnly: true}
	first, err := service.PublishVolume(context.Background(), req)
	if err != nil {
		t.Fatalf("PublishVolume() first error = %v, want nil", err)
	}
	if first.Attachment.Kind != volume.AttachmentFile || first.Attachment.Path == "" || !first.Attachment.ReadOnly {
		t.Fatalf("PublishVolume() attachment = %+v, want file attachment", first.Attachment)
	}

	second, err := service.PublishVolume(context.Background(), req)
	if err != nil {
		t.Fatalf("PublishVolume() second error = %v, want nil", err)
	}
	if second.Attachment.Path != first.Attachment.Path || !second.Attachment.ReadOnly {
		t.Fatalf("second publish = %+v, want idempotent attachment %+v", second, first)
	}
	if driver.publishCalls != 1 {
		t.Fatalf("publish driver calls = %d, want 1", driver.publishCalls)
	}
}

func TestPublishVolumeConflictReturnsInUse(t *testing.T) {
	service, _ := newTestVolumeService(t)
	vol := createRootVolume(t, service)

	if _, err := service.PublishVolume(context.Background(), PublishVolumeRequest{PoolName: "pool-a", VolumeID: vol.ID, VMID: "vm-a"}); err != nil {
		t.Fatalf("PublishVolume() first error = %v, want nil", err)
	}
	if _, err := service.PublishVolume(context.Background(), PublishVolumeRequest{PoolName: "pool-a", VolumeID: vol.ID, VMID: "vm-b"}); !errors.Is(err, ErrVolumeInUse) {
		t.Fatalf("PublishVolume() conflict error = %v, want %v", err, ErrVolumeInUse)
	}
	if _, err := service.PublishVolume(context.Background(), PublishVolumeRequest{PoolName: "pool-a", VolumeID: vol.ID, VMID: "vm-a", ReadOnly: true}); !errors.Is(err, ErrVolumeInUse) {
		t.Fatalf("PublishVolume() readOnly conflict error = %v, want %v", err, ErrVolumeInUse)
	}
}

func TestDeleteVolumeRejectsPublishedVolume(t *testing.T) {
	service, driver := newTestVolumeService(t)
	vol := createRootVolume(t, service)
	if _, err := service.PublishVolume(context.Background(), PublishVolumeRequest{PoolName: "pool-a", VolumeID: vol.ID, VMID: "vm-a"}); err != nil {
		t.Fatalf("PublishVolume() error = %v, want nil", err)
	}

	if err := service.DeleteVolume(context.Background(), DeleteVolumeRequest{PoolName: "pool-a", VolumeID: vol.ID}); !errors.Is(err, ErrVolumeInUse) {
		t.Fatalf("DeleteVolume() error = %v, want %v", err, ErrVolumeInUse)
	}
	if driver.deleteCalls != 0 {
		t.Fatalf("delete driver calls = %d, want 0", driver.deleteCalls)
	}
}

func TestCanceledContextDoesNotCallDriver(t *testing.T) {
	service, driver := newTestVolumeService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.CreateRootVolume(ctx, CreateRootVolumeRequest{
		VMID:          "vm-a",
		VMName:        "vm-a",
		PoolName:      "pool-a",
		Name:          "root",
		DiskIndex:     0,
		CapacityBytes: 10,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateRootVolume() error = %v, want %v", err, context.Canceled)
	}
	if driver.createCalls != 0 || driver.publishCalls != 0 || driver.deleteCalls != 0 || driver.unpublishCalls != 0 {
		t.Fatalf("driver calls after canceled ctx = create:%d publish:%d unpublish:%d delete:%d, want all zero", driver.createCalls, driver.publishCalls, driver.unpublishCalls, driver.deleteCalls)
	}
}

func newTestVolumeService(t *testing.T) (*VolumeService, *storageLifecycleDriver) {
	t.Helper()
	driver := &storageLifecycleDriver{}
	pools := pool.NewService()
	if err := pools.RegisterPool(&pool.Pool{
		Config: pool.Config{
			Name:          "pool-a",
			Type:          pool.PoolTypeBlock,
			Backend:       pool.BackendLocalBlock,
			StorageRoot:   "/var/lib/govirta/storage/pool-a",
			CapacityBytes: 100,
		},
		Driver: driver,
	}); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}
	return NewVolumeService(pools), driver
}

func createRootVolume(t *testing.T, service *VolumeService) volume.Volume {
	t.Helper()
	vol, err := service.CreateRootVolume(context.Background(), CreateRootVolumeRequest{
		VMID:          "vm-a",
		VMName:        "vm-a",
		PoolName:      "pool-a",
		Name:          "root",
		DiskIndex:     0,
		CapacityBytes: 10,
	})
	if err != nil {
		t.Fatalf("CreateRootVolume() error = %v, want nil", err)
	}
	return vol
}

func newCreateVolumeRequest(mutate func(*CreateVolumeRequest)) CreateVolumeRequest {
	req := CreateVolumeRequest{
		VMID:     "vm-a",
		VMName:   "vm-a",
		PoolName: "pool-a",
		Spec: volume.Spec{
			Name:          "root",
			Role:          volume.RoleRoot,
			DiskIndex:     0,
			CapacityBytes: 10,
		},
	}
	mutate(&req)
	return req
}

type storageLifecycleDriver struct {
	createCalls    int
	deleteCalls    int
	publishCalls   int
	unpublishCalls int
}

func (d *storageLifecycleDriver) DriverInfo(ctx context.Context) (block.DriverInfo, error) {
	if err := ctx.Err(); err != nil {
		return block.DriverInfo{}, err
	}
	return block.DriverInfo{Name: "storage-lifecycle"}, nil
}

func (d *storageLifecycleDriver) Create(ctx context.Context, req block.CreateRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	d.createCalls++
	return volume.Volume{
		ID:            req.VolumeID,
		Name:          req.Name,
		VMID:          req.VMID,
		VMName:        req.VMName,
		PoolName:      req.PoolName,
		DiskIndex:     req.DiskIndex,
		CapacityBytes: req.CapacityBytes,
		State:         volume.StateAvailable,
	}, nil
}

func (d *storageLifecycleDriver) Delete(ctx context.Context, vol volume.Volume) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.deleteCalls++
	return nil
}

func (d *storageLifecycleDriver) GetActualUsedBytes(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}

func (d *storageLifecycleDriver) Publish(ctx context.Context, vol volume.Volume, req block.PublishRequest) (volume.PublishedVolume, error) {
	if err := ctx.Err(); err != nil {
		return volume.PublishedVolume{}, err
	}
	d.publishCalls++
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

func (d *storageLifecycleDriver) Unpublish(ctx context.Context, vol volume.Volume, req block.UnpublishRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.unpublishCalls++
	return nil
}

func (d *storageLifecycleDriver) Snapshot(ctx context.Context, vol volume.Volume, req block.SnapshotRequest) (volume.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return volume.Snapshot{}, err
	}
	return volume.Snapshot{}, volume.ErrUnsupported
}

func (d *storageLifecycleDriver) Resize(ctx context.Context, vol volume.Volume, req block.ResizeRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	return volume.Volume{}, volume.ErrUnsupported
}
