package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/image"
	"github.com/suknna/govirta/internal/storage/pool"
	"github.com/suknna/govirta/internal/storage/volume"
)

func TestCreateVolumeValidation(t *testing.T) {
	tests := []struct {
		name string
		req  CreateVolumeRequest
		want error
	}{
		{name: "pool required", req: newCreateVolumeRequest(func(req *CreateVolumeRequest) { req.PoolName = "" }), want: ErrPoolRequired},
		{name: "vm id required", req: newCreateVolumeRequest(func(req *CreateVolumeRequest) { req.VMID = "" }), want: ErrInvalidRequest},
		{name: "vm name required", req: newCreateVolumeRequest(func(req *CreateVolumeRequest) { req.VMName = "" }), want: ErrInvalidRequest},
		{name: "role required", req: newCreateVolumeRequest(func(req *CreateVolumeRequest) { req.Spec.Role = "" }), want: ErrInvalidRequest},
		{name: "unknown role rejected", req: newCreateVolumeRequest(func(req *CreateVolumeRequest) { req.Spec.Role = volume.Role("swap") }), want: ErrInvalidRequest},
		{name: "disk index non-negative", req: newCreateVolumeRequest(func(req *CreateVolumeRequest) { req.Spec.DiskIndex = -1 }), want: ErrInvalidRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, driver := newTestVolumeService(t)
			if _, err := service.CreateVolume(context.Background(), tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("CreateVolume() error = %v, want %v", err, tc.want)
			}
			if driver.createCalls != 0 {
				t.Fatalf("driver Create calls = %d, want 0", driver.createCalls)
			}
		})
	}
}

func TestCreateRootVolumeAndCreateDataVolumeSetRoles(t *testing.T) {
	service, driver := newTestVolumeService(t)

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
	if driver.lastCreate.Role != volume.RoleRoot {
		t.Fatalf("root Create() role = %q, want %q", driver.lastCreate.Role, volume.RoleRoot)
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
	if driver.lastCreate.Role != volume.RoleData {
		t.Fatalf("data Create() role = %q, want %q", driver.lastCreate.Role, volume.RoleData)
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

func TestCreateVolumeReturnsCommittedVolumeOnCleanupFailure(t *testing.T) {
	service, driver := newTestVolumeService(t)
	cleanupErr := errors.Join(volume.ErrVolumeCleanupFailed, errors.New("remove committed temp failed"))
	driver.createErr = cleanupErr

	created, err := service.CreateRootVolume(context.Background(), CreateRootVolumeRequest{
		VMID:          "vm-a",
		VMName:        "vm-a",
		PoolName:      "pool-a",
		Name:          "root",
		DiskIndex:     0,
		CapacityBytes: 10,
	})
	if !errors.Is(err, volume.ErrVolumeCleanupFailed) {
		t.Fatalf("CreateRootVolume() error = %v, want %v", err, volume.ErrVolumeCleanupFailed)
	}
	if created.ID != volume.ID("vm-a-root-0") || created.Name != "root" || created.Role != volume.RoleRoot {
		t.Fatalf("CreateRootVolume() volume = %+v, want committed root volume", created)
	}
}

func TestCreateVolumeDropsVolumeOnOrdinaryFailure(t *testing.T) {
	service, driver := newTestVolumeService(t)
	createErr := errors.New("create failed")
	driver.createErr = createErr

	created, err := service.CreateRootVolume(context.Background(), CreateRootVolumeRequest{
		VMID:          "vm-a",
		VMName:        "vm-a",
		PoolName:      "pool-a",
		Name:          "root",
		DiskIndex:     0,
		CapacityBytes: 10,
	})
	if !errors.Is(err, createErr) {
		t.Fatalf("CreateRootVolume() error = %v, want %v", err, createErr)
	}
	if created.ID != "" {
		t.Fatalf("CreateRootVolume() volume = %+v, want zero volume", created)
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

func TestImageServicePutImageRequiresPoolName(t *testing.T) {
	service, driver := newTestImageService(t)

	_, err := service.PutImage(context.Background(), PutImageRequest{
		ImageID:           "cirros",
		Format:            diskformat.FormatQCOW2,
		DeclaredSizeBytes: 10,
	})
	if !errors.Is(err, ErrPoolRequired) {
		t.Fatalf("PutImage() error = %v, want %v", err, ErrPoolRequired)
	}
	if driver.putCalls != 0 {
		t.Fatalf("image driver put calls = %d, want 0", driver.putCalls)
	}
}

func TestImageServicePutImageCanceledContextDoesNotCallDriver(t *testing.T) {
	service, driver := newTestImageService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.PutImage(ctx, PutImageRequest{
		PoolName:          "image-pool",
		ImageID:           "cirros",
		Format:            diskformat.FormatQCOW2,
		DeclaredSizeBytes: 10,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PutImage() error = %v, want %v", err, context.Canceled)
	}
	if driver.putCalls != 0 || driver.getCalls != 0 || driver.deleteCalls != 0 {
		t.Fatalf("image driver calls after canceled ctx = put:%d get:%d delete:%d, want all zero", driver.putCalls, driver.getCalls, driver.deleteCalls)
	}
}

func TestImageServiceForwardsImageLifecycleRequests(t *testing.T) {
	service, driver := newTestImageService(t)
	closeErr := errors.New("reader close failed")
	driver.getReader = &errorReadCloser{Reader: bytes.NewReader([]byte("image-bytes")), closeErr: closeErr}

	writer, err := service.PutImage(context.Background(), PutImageRequest{
		PoolName:          "image-pool",
		ImageID:           "cirros",
		Format:            diskformat.FormatQCOW2,
		DeclaredSizeBytes: 10,
	})
	if err != nil {
		t.Fatalf("PutImage() error = %v, want nil", err)
	}
	if _, err := writer.Write([]byte("image-bytes")); err != nil {
		t.Fatalf("writer.Write() error = %v, want nil", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v, want nil", err)
	}
	if driver.putCalls != 1 || driver.lastPut.ImageID != "cirros" || driver.lastPut.Format != diskformat.FormatQCOW2 || driver.lastPut.DeclaredSizeBytes != 10 {
		t.Fatalf("PutImage() forwarded request = calls:%d req:%+v, want qcow2 cirros", driver.putCalls, driver.lastPut)
	}

	reader, err := service.GetImage(context.Background(), GetImageRequest{PoolName: "image-pool", ImageID: "cirros"})
	if err != nil {
		t.Fatalf("GetImage() error = %v, want nil", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v, want nil", err)
	}
	if string(data) != "image-bytes" {
		t.Fatalf("GetImage() data = %q, want image-bytes", data)
	}
	if err := reader.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("reader.Close() error = %v, want %v", err, closeErr)
	}
	if driver.getCalls != 1 || driver.lastGet.ImageID != "cirros" {
		t.Fatalf("GetImage() forwarded request = calls:%d req:%+v, want cirros", driver.getCalls, driver.lastGet)
	}

	if err := service.DeleteImage(context.Background(), DeleteImageRequest{PoolName: "image-pool", ImageID: "cirros", Format: diskformat.FormatQCOW2}); err != nil {
		t.Fatalf("DeleteImage() error = %v, want nil", err)
	}
	if driver.deleteCalls != 1 || driver.lastDelete.ImageID != "cirros" || driver.lastDelete.Format != diskformat.FormatQCOW2 {
		t.Fatalf("DeleteImage() forwarded request = calls:%d req:%+v, want qcow2 cirros", driver.deleteCalls, driver.lastDelete)
	}
}

func TestCreateRootVolumeFromReaderValidation(t *testing.T) {
	service, _ := newTestVolumeService(t)

	tests := []struct {
		name string
		req  CreateRootVolumeFromReaderRequest
		want error
	}{
		{name: "pool required", req: newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) { req.PoolName = "" }), want: ErrPoolRequired},
		{name: "reader required", req: newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) { req.Reader = nil }), want: ErrInvalidRequest},
		{name: "format required", req: newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) { req.Format = "" }), want: ErrInvalidRequest},
		{name: "vm id required", req: newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) { req.VMID = "" }), want: ErrInvalidRequest},
		{name: "vm name required", req: newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) { req.VMName = "" }), want: ErrInvalidRequest},
		{name: "name required", req: newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) { req.Name = "" }), want: ErrInvalidRequest},
		{name: "disk index non-negative", req: newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) { req.DiskIndex = -1 }), want: ErrInvalidRequest},
		{name: "capacity positive", req: newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) { req.CapacityBytes = 0 }), want: ErrInvalidRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.CreateRootVolumeFromReader(context.Background(), tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("CreateRootVolumeFromReader() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCreateRootVolumeFromReaderSetsRoleAndDeterministicID(t *testing.T) {
	service, driver := newTestVolumeService(t)
	req := newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) {
		req.ReadOnly = true
	})

	vol, err := service.CreateRootVolumeFromReader(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateRootVolumeFromReader() error = %v, want nil", err)
	}
	if vol.ID != volume.ID("vm-a-root-0") {
		t.Fatalf("volume ID = %q, want vm-a-root-0", vol.ID)
	}
	if vol.Role != volume.RoleRoot {
		t.Fatalf("volume role = %q, want %q", vol.Role, volume.RoleRoot)
	}
	if driver.createFromReaderCalls != 1 || driver.lastCreateFromReader.Format != diskformat.FormatQCOW2 || driver.lastCreateFromReader.Reader == nil {
		t.Fatalf("CreateFromReader() forwarded request = calls:%d req:%+v, want qcow2 reader", driver.createFromReaderCalls, driver.lastCreateFromReader)
	}
	if driver.lastCreateFromReader.Role != volume.RoleRoot {
		t.Fatalf("CreateFromReader() role = %q, want %q", driver.lastCreateFromReader.Role, volume.RoleRoot)
	}
}

func TestCreateRootVolumeFromReaderReturnsCommittedVolumeOnCleanupFailure(t *testing.T) {
	service, driver := newTestVolumeService(t)
	cleanupErr := errors.Join(volume.ErrVolumeCleanupFailed, errors.New("remove committed temp failed"))
	driver.createFromReaderErr = cleanupErr

	created, err := service.CreateRootVolumeFromReader(context.Background(), newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) {}))
	if !errors.Is(err, volume.ErrVolumeCleanupFailed) {
		t.Fatalf("CreateRootVolumeFromReader() error = %v, want %v", err, volume.ErrVolumeCleanupFailed)
	}
	if created.ID != volume.ID("vm-a-root-0") || created.Name != "root" || created.Role != volume.RoleRoot {
		t.Fatalf("CreateRootVolumeFromReader() volume = %+v, want committed root volume", created)
	}
}

func TestCreateRootVolumeFromReaderDropsVolumeOnOrdinaryFailure(t *testing.T) {
	service, driver := newTestVolumeService(t)
	createErr := errors.New("create from reader failed")
	driver.createFromReaderErr = createErr

	created, err := service.CreateRootVolumeFromReader(context.Background(), newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) {}))
	if !errors.Is(err, createErr) {
		t.Fatalf("CreateRootVolumeFromReader() error = %v, want %v", err, createErr)
	}
	if created.ID != "" {
		t.Fatalf("CreateRootVolumeFromReader() volume = %+v, want zero volume", created)
	}
}

func TestCreateRootVolumeFromReaderCanceledContextDoesNotCallDriver(t *testing.T) {
	service, driver := newTestVolumeService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.CreateRootVolumeFromReader(ctx, newCreateRootVolumeFromReaderRequest(func(req *CreateRootVolumeFromReaderRequest) {}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateRootVolumeFromReader() error = %v, want %v", err, context.Canceled)
	}
	if driver.createCalls != 0 || driver.createFromReaderCalls != 0 || driver.publishCalls != 0 || driver.deleteCalls != 0 || driver.unpublishCalls != 0 {
		t.Fatalf("driver calls after canceled ctx = create:%d createFromReader:%d publish:%d unpublish:%d delete:%d, want all zero", driver.createCalls, driver.createFromReaderCalls, driver.publishCalls, driver.unpublishCalls, driver.deleteCalls)
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

func newTestImageService(t *testing.T) (*ImageService, *storageImageDriver) {
	t.Helper()
	driver := &storageImageDriver{}
	pools := pool.NewService()
	if err := pools.RegisterPool(&pool.Pool{
		Config: pool.Config{
			Name:          "image-pool",
			Type:          pool.PoolTypeFile,
			Backend:       pool.BackendLocalFile,
			StorageRoot:   "/var/lib/govirta/images/image-pool",
			CapacityBytes: 100,
		},
		ImageDriver: driver,
	}); err != nil {
		t.Fatalf("RegisterPool() error = %v, want nil", err)
	}
	return NewImageService(pools), driver
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

func newCreateRootVolumeFromReaderRequest(mutate func(*CreateRootVolumeFromReaderRequest)) CreateRootVolumeFromReaderRequest {
	req := CreateRootVolumeFromReaderRequest{
		VMID:          "vm-a",
		VMName:        "vm-a",
		PoolName:      "pool-a",
		Name:          "root",
		DiskIndex:     0,
		CapacityBytes: 10,
		Reader:        strings.NewReader("qcow2-bytes"),
		Format:        diskformat.FormatQCOW2,
	}
	mutate(&req)
	return req
}

type storageLifecycleDriver struct {
	createCalls           int
	createFromReaderCalls int
	deleteCalls           int
	publishCalls          int
	unpublishCalls        int
	lastCreate            block.CreateRequest
	lastCreateFromReader  block.CreateFromReaderRequest
	createErr             error
	createFromReaderErr   error
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
	d.lastCreate = req
	return volume.Volume{
		ID:            req.VolumeID,
		Name:          req.Name,
		VMID:          req.VMID,
		VMName:        req.VMName,
		PoolName:      req.PoolName,
		Role:          req.Role,
		DiskIndex:     req.DiskIndex,
		CapacityBytes: req.CapacityBytes,
		State:         volume.StateAvailable,
	}, d.createErr
}

func (d *storageLifecycleDriver) CreateFromReader(ctx context.Context, req block.CreateFromReaderRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	d.createFromReaderCalls++
	d.lastCreateFromReader = req
	return volume.Volume{
		ID:            req.VolumeID,
		Name:          req.Name,
		VMID:          req.VMID,
		VMName:        req.VMName,
		PoolName:      req.PoolName,
		Role:          req.Role,
		DiskIndex:     req.DiskIndex,
		CapacityBytes: req.CapacityBytes,
		State:         volume.StateAvailable,
	}, d.createFromReaderErr
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

type storageImageDriver struct {
	putCalls    int
	getCalls    int
	deleteCalls int
	lastPut     image.PutRequest
	lastGet     image.GetRequest
	lastDelete  image.DeleteRequest
	getReader   io.ReadCloser
}

func (d *storageImageDriver) DriverInfo(ctx context.Context) (image.DriverInfo, error) {
	if err := ctx.Err(); err != nil {
		return image.DriverInfo{}, err
	}
	return image.DriverInfo{Name: "storage-image"}, nil
}

func (d *storageImageDriver) Put(ctx context.Context, req image.PutRequest) (image.ImageWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.putCalls++
	d.lastPut = req
	return &storageImageWriter{}, nil
}

func (d *storageImageDriver) Get(ctx context.Context, req image.GetRequest) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.getCalls++
	d.lastGet = req
	if d.getReader != nil {
		return d.getReader, nil
	}
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (d *storageImageDriver) Delete(ctx context.Context, req image.DeleteRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.deleteCalls++
	d.lastDelete = req
	return nil
}

func (d *storageImageDriver) GetActualUsedBytes(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}

type storageImageWriter struct {
	bytes.Buffer
	canceled bool
}

func (w *storageImageWriter) Close() error {
	return nil
}

func (w *storageImageWriter) Cancel() error {
	w.canceled = true
	return nil
}

type errorReadCloser struct {
	io.Reader
	closeErr error
}

func (r *errorReadCloser) Close() error {
	return r.closeErr
}
