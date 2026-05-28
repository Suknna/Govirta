package local

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/volume"
	"github.com/suknna/govirta/internal/virt/qemuimg"
)

func TestNewDriverValidatesConfig(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{name: "unsafe pool name", config: Config{PoolName: "../pool", StorageRoot: t.TempDir()}},
		{name: "dot pool name", config: Config{PoolName: ".", StorageRoot: t.TempDir()}},
		{name: "dot dot pool name", config: Config{PoolName: "..", StorageRoot: t.TempDir()}},
		{name: "empty storage root", config: Config{PoolName: "pool-a"}},
		{name: "relative storage root", config: Config{PoolName: "pool-a", StorageRoot: "var/lib/govirta"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewDriver(tt.config)
			if !errors.Is(err, volume.ErrInvalidRequest) {
				t.Fatalf("NewDriver() error = %v, want %v", err, volume.ErrInvalidRequest)
			}
		})
	}
}

func TestCreateCreatesEmptyQCOW2AtSafePath(t *testing.T) {
	driver, runner := newTestDriver(t)

	created, err := driver.Create(context.Background(), newCreateRequest())
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}

	wantPath := filepath.Join(driver.storageRoot, "pool", "pool-a", "vm-a", "vm-a-disk-0.qcow2")
	if got := created.Context[pathKey]; got != wantPath {
		t.Fatalf("created path = %q, want %q", got, wantPath)
	}
	if got := created.Context[formatKey]; got != string(volume.DiskFormatQCOW2) {
		t.Fatalf("created format = %q, want qcow2", got)
	}
	if created.ID != volume.ID("vol-a") || created.Backend != driverName || created.State != volume.StateAvailable {
		t.Fatalf("created volume = %+v, want normalized local volume", created)
	}
	wantArgs := [][]string{{"create", "-f", "qcow2", wantPath, "1024"}}
	if calls := runner.args(); !reflect.DeepEqual(calls, wantArgs) {
		t.Fatalf("qemu-img calls = %#v, want %#v", calls, wantArgs)
	}
	if _, err := os.Stat(filepath.Dir(wantPath)); err != nil {
		t.Fatalf("volume dir stat error = %v, want nil", err)
	}
}

func TestCreateRejectsExistingTarget(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{name: "regular file", setup: func(t *testing.T, path string) { writeFile(t, path, "existing") }},
		{name: "symlink", setup: func(t *testing.T, path string) {
			writeFile(t, filepath.Join(filepath.Dir(path), "target"), "target")
			if err := os.Symlink(filepath.Join(filepath.Dir(path), "target"), path); err != nil {
				t.Fatalf("Symlink() error = %v", err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver, runner := newTestDriver(t)
			path := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
			tt.setup(t, path)

			_, err := driver.Create(context.Background(), newCreateRequest())
			if !errors.Is(err, volume.ErrInvalidRequest) {
				t.Fatalf("Create() error = %v, want %v", err, volume.ErrInvalidRequest)
			}
			if calls := runner.args(); len(calls) != 0 {
				t.Fatalf("qemu-img calls = %#v, want none", calls)
			}
		})
	}
}

func TestCreateRejectsSymlinkVolumeDirectoryBeforeRunner(t *testing.T) {
	driver, runner := newTestDriver(t)
	volumeDir := filepath.Join(driver.poolRoot, "vm-a")
	externalDir := t.TempDir()
	if err := os.MkdirAll(driver.poolRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", driver.poolRoot, err)
	}
	if err := os.Symlink(externalDir, volumeDir); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	_, err := driver.Create(context.Background(), newCreateRequest())
	if !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("Create() error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
	if _, err := os.Stat(filepath.Join(externalDir, "vm-a-disk-0.qcow2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external target stat error = %v, want not exist", err)
	}
}

func TestOperationsRejectSymlinkPoolAncestorBeforeExternalAccess(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *Driver) error
	}{
		{name: "create", run: func(ctx context.Context, driver *Driver) error {
			_, err := driver.Create(ctx, newCreateRequest())
			return err
		}},
		{name: "create from reader", run: func(ctx context.Context, driver *Driver) error {
			_, err := driver.CreateFromReader(ctx, newCreateFromReaderRequest(strings.NewReader("raw"), diskformat.FormatRaw))
			return err
		}},
		{name: "delete", run: func(ctx context.Context, driver *Driver) error {
			return driver.Delete(ctx, newVolumeWithPath(filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")))
		}},
		{name: "publish", run: func(ctx context.Context, driver *Driver) error {
			_, err := driver.Publish(ctx, newVolumeWithPath(filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")), block.PublishRequest{VolumeID: "vol-a", VMID: "vm-a"})
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver, runner := newTestDriver(t)
			externalPoolParent := t.TempDir()
			externalVolumeDir := filepath.Join(externalPoolParent, "pool-a", "vm-a")
			externalPath := filepath.Join(externalVolumeDir, "vm-a-disk-0.qcow2")
			writeFile(t, externalPath, "outside")
			if err := os.Symlink(externalPoolParent, filepath.Join(driver.storageRoot, "pool")); err != nil {
				t.Fatalf("Symlink() error = %v", err)
			}

			err := tt.run(context.Background(), driver)
			if !errors.Is(err, volume.ErrInvalidRequest) {
				t.Fatalf("%s error = %v, want %v", tt.name, err, volume.ErrInvalidRequest)
			}
			if calls := runner.args(); len(calls) != 0 {
				t.Fatalf("qemu-img calls = %#v, want none", calls)
			}
			got, readErr := os.ReadFile(externalPath)
			if readErr != nil {
				t.Fatalf("ReadFile(%s) error = %v, want nil", externalPath, readErr)
			}
			if string(got) != "outside" {
				t.Fatalf("external target bytes = %q, want outside", got)
			}
			if _, err := os.Stat(externalPath + ".tmp"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("external tmp stat error = %v, want not exist", err)
			}
		})
	}
}

func TestRejectsUnsafeCreateInput(t *testing.T) {
	driver, runner := newTestDriver(t)
	tests := []struct {
		name   string
		mutate func(*block.CreateRequest)
	}{
		{name: "vm id", mutate: func(req *block.CreateRequest) { req.VMID = "../vm" }},
		{name: "dot vm id", mutate: func(req *block.CreateRequest) { req.VMID = "." }},
		{name: "dot dot vm id", mutate: func(req *block.CreateRequest) { req.VMID = ".." }},
		{name: "vm name", mutate: func(req *block.CreateRequest) { req.VMName = "vm/name" }},
		{name: "disk index", mutate: func(req *block.CreateRequest) { req.DiskIndex = -1 }},
		{name: "wrong pool", mutate: func(req *block.CreateRequest) { req.PoolName = "pool-b" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newCreateRequest()
			tt.mutate(&req)
			if _, err := driver.Create(context.Background(), req); !errors.Is(err, volume.ErrInvalidRequest) {
				t.Fatalf("Create() error = %v, want %v", err, volume.ErrInvalidRequest)
			}
			if calls := runner.args(); len(calls) != 0 {
				t.Fatalf("qemu-img calls = %#v, want none", calls)
			}
		})
	}
}

func TestCanceledContextDoesNotCallRunner(t *testing.T) {
	driver, runner := newTestDriver(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := driver.Create(ctx, newCreateRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() error = %v, want %v", err, context.Canceled)
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
}

func TestPublishValidatesImageAndReturnsFileAttachment(t *testing.T) {
	driver, runner := newTestDriver(t)
	path := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	writeFile(t, path, "qcow2")
	vol := newVolumeWithPath(path)

	published, err := driver.Publish(context.Background(), vol, block.PublishRequest{VolumeID: vol.ID, VMID: vol.VMID, ReadOnly: true})
	if err != nil {
		t.Fatalf("Publish() error = %v, want nil", err)
	}

	wantArgs := [][]string{{"info", "-f", "qcow2", "--output=json", path}}
	if calls := runner.args(); !reflect.DeepEqual(calls, wantArgs) {
		t.Fatalf("qemu-img calls = %#v, want %#v", calls, wantArgs)
	}
	if published.Attachment.Kind != volume.AttachmentFile || published.Attachment.Format != volume.DiskFormatQCOW2 || published.Attachment.Path != path || !published.Attachment.ReadOnly {
		t.Fatalf("published attachment = %+v, want readonly file/qcow2 at %q", published.Attachment, path)
	}
}

func TestPublishRejectsMismatchedRequestIdentityWithoutRunner(t *testing.T) {
	driver, runner := newTestDriver(t)
	path := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	writeFile(t, path, "qcow2")
	vol := newVolumeWithPath(path)

	requests := []block.PublishRequest{
		{VolumeID: "vol-b", VMID: vol.VMID},
		{VolumeID: vol.ID, VMID: "vm-b"},
	}
	for _, req := range requests {
		if _, err := driver.Publish(context.Background(), vol, req); !errors.Is(err, volume.ErrInvalidRequest) {
			t.Fatalf("Publish(%+v) error = %v, want %v", req, err, volume.ErrInvalidRequest)
		}
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
}

func TestPublishRejectsMissingOrNonQCOW2FormatContextWithoutRunner(t *testing.T) {
	driver, runner := newTestDriver(t)
	path := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	writeFile(t, path, "qcow2")

	tests := []struct {
		name   string
		format string
	}{
		{name: "missing"},
		{name: "raw", format: string(volume.DiskFormatRaw)},
		{name: "vmdk", format: "vmdk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vol := newVolumeWithPath(path)
			if tt.format == "" {
				delete(vol.Context, formatKey)
			} else {
				vol.Context[formatKey] = tt.format
			}
			_, err := driver.Publish(context.Background(), vol, block.PublishRequest{VolumeID: vol.ID, VMID: vol.VMID})
			if !errors.Is(err, volume.ErrInvalidRequest) {
				t.Fatalf("Publish() error = %v, want %v", err, volume.ErrInvalidRequest)
			}
		})
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
}

func TestPublishRejectsUnsafeImageFileType(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{name: "symlink", setup: func(t *testing.T, path string) {
			writeFile(t, filepath.Join(filepath.Dir(path), "target.qcow2"), "target")
			if err := os.Symlink(filepath.Join(filepath.Dir(path), "target.qcow2"), path); err != nil {
				t.Fatalf("Symlink() error = %v", err)
			}
		}},
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
		}},
		{name: "non regular", setup: func(t *testing.T, path string) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := syscall.Mkfifo(path, 0o644); err != nil {
				t.Fatalf("Mkfifo() error = %v", err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver, runner := newTestDriver(t)
			path := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
			tt.setup(t, path)

			_, err := driver.Publish(context.Background(), newVolumeWithPath(path), block.PublishRequest{VolumeID: "vol-a", VMID: "vm-a"})
			if !errors.Is(err, volume.ErrInvalidRequest) {
				t.Fatalf("Publish() error = %v, want %v", err, volume.ErrInvalidRequest)
			}
			if calls := runner.args(); len(calls) != 0 {
				t.Fatalf("qemu-img calls = %#v, want none", calls)
			}
		})
	}
}

func TestDeleteRemovesImageAndVolumeDirectory(t *testing.T) {
	driver, _ := newTestDriver(t)
	path := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	writeFile(t, path, "qcow2")

	if err := driver.Delete(context.Background(), newVolumeWithPath(path)); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("image stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("volume dir stat error = %v, want not exist", err)
	}
}

func TestDeleteRejectsPathOutsidePoolRoot(t *testing.T) {
	driver, _ := newTestDriver(t)
	outside := filepath.Join(t.TempDir(), "vm-a-disk-0.qcow2")

	err := driver.Delete(context.Background(), newVolumeWithPath(outside))
	if !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("Delete() error = %v, want %v", err, volume.ErrInvalidRequest)
	}
}

func TestDeleteRejectsMissingOrNonQCOW2FormatContextWithoutRunner(t *testing.T) {
	driver, runner := newTestDriver(t)
	path := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	writeFile(t, path, "qcow2")

	tests := []struct {
		name   string
		format string
	}{
		{name: "missing"},
		{name: "raw", format: string(volume.DiskFormatRaw)},
		{name: "vmdk", format: "vmdk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vol := newVolumeWithPath(path)
			if tt.format == "" {
				delete(vol.Context, formatKey)
			} else {
				vol.Context[formatKey] = tt.format
			}
			err := driver.Delete(context.Background(), vol)
			if !errors.Is(err, volume.ErrInvalidRequest) {
				t.Fatalf("Delete() error = %v, want %v", err, volume.ErrInvalidRequest)
			}
		})
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
}

func TestDeleteRejectsSymlinkVolumeDirectoryBeforeRunner(t *testing.T) {
	driver, runner := newTestDriver(t)
	volumeDir := filepath.Join(driver.poolRoot, "vm-a")
	externalDir := t.TempDir()
	externalPath := filepath.Join(externalDir, "vm-a-disk-0.qcow2")
	writeFile(t, externalPath, "outside")
	if err := os.MkdirAll(driver.poolRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", driver.poolRoot, err)
	}
	if err := os.Symlink(externalDir, volumeDir); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	err := driver.Delete(context.Background(), newVolumeWithPath(filepath.Join(volumeDir, "vm-a-disk-0.qcow2")))
	if !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("Delete() error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
	got, readErr := os.ReadFile(externalPath)
	if readErr != nil {
		t.Fatalf("ReadFile(%s) error = %v, want nil", externalPath, readErr)
	}
	if string(got) != "outside" {
		t.Fatalf("external target bytes = %q, want outside", got)
	}
}

func TestDeleteRejectsSymlinkVolumeFileBeforeRunner(t *testing.T) {
	driver, runner := newTestDriver(t)
	path := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	externalPath := filepath.Join(t.TempDir(), "external.qcow2")
	writeFile(t, externalPath, "outside")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.Symlink(externalPath, path); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	err := driver.Delete(context.Background(), newVolumeWithPath(path))
	if !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("Delete() error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
	got, readErr := os.ReadFile(externalPath)
	if readErr != nil {
		t.Fatalf("ReadFile(%s) error = %v, want nil", externalPath, readErr)
	}
	if string(got) != "outside" {
		t.Fatalf("external target bytes = %q, want outside", got)
	}
}

func TestSnapshotAndResizeUnsupportedAfterContextCheck(t *testing.T) {
	driver, _ := newTestDriver(t)
	vol := newVolumeWithPath(filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := driver.Snapshot(ctx, vol, block.SnapshotRequest{Name: "snap-a"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Snapshot(canceled) error = %v, want %v", err, context.Canceled)
	}
	if _, err := driver.Resize(ctx, vol, block.ResizeRequest{CapacityBytes: 2048}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resize(canceled) error = %v, want %v", err, context.Canceled)
	}
	if _, err := driver.Snapshot(context.Background(), vol, block.SnapshotRequest{Name: "snap-a"}); !errors.Is(err, volume.ErrUnsupported) {
		t.Fatalf("Snapshot() error = %v, want %v", err, volume.ErrUnsupported)
	}
	if _, err := driver.Resize(context.Background(), vol, block.ResizeRequest{CapacityBytes: 2048}); !errors.Is(err, volume.ErrUnsupported) {
		t.Fatalf("Resize() error = %v, want %v", err, volume.ErrUnsupported)
	}
}

func TestGetActualUsedBytesReportsMissingRootAndUsage(t *testing.T) {
	driver, _ := newTestDriver(t)
	if _, err := driver.GetActualUsedBytes(context.Background()); err == nil {
		t.Fatalf("GetActualUsedBytes() error = nil, want missing root error")
	}

	writeFile(t, filepath.Join(driver.poolRoot, "vm-a", "disk.qcow2"), "12345")
	writeFile(t, filepath.Join(driver.poolRoot, "vm-a", "data.qcow2"), "123")
	if err := os.MkdirAll(filepath.Join(driver.poolRoot, "vm-a", "subdir"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v, want nil", err)
	}
	if err := os.Symlink(filepath.Join(driver.poolRoot, "vm-a", "disk.qcow2"), filepath.Join(driver.poolRoot, "vm-a", "link.qcow2")); err != nil {
		t.Fatalf("Symlink() error = %v, want nil", err)
	}
	used, err := driver.GetActualUsedBytes(context.Background())
	if err != nil {
		t.Fatalf("GetActualUsedBytes() error = %v, want nil", err)
	}
	if used != 8 {
		t.Fatalf("GetActualUsedBytes() = %d, want 8", used)
	}
}

func TestGetActualUsedBytesRejectsSymlinkPoolAncestorWithoutCountingExternalFiles(t *testing.T) {
	driver, _ := newTestDriver(t)
	externalPoolParent := t.TempDir()
	externalPath := filepath.Join(externalPoolParent, "pool-a", "vm-a", "disk.qcow2")
	writeFile(t, externalPath, "outside")
	if err := os.Symlink(externalPoolParent, filepath.Join(driver.storageRoot, "pool")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	used, err := driver.GetActualUsedBytes(context.Background())
	if !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("GetActualUsedBytes() error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if used != 0 {
		t.Fatalf("GetActualUsedBytes() = %d, want 0", used)
	}
}

func TestGetActualUsedBytesHonorsContextDuringWalk(t *testing.T) {
	driver, _ := newTestDriver(t)
	if err := os.MkdirAll(driver.poolRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", driver.poolRoot, err)
	}
	originalWalkDir := walkDir
	t.Cleanup(func() { walkDir = originalWalkDir })
	ctx, cancel := context.WithCancel(context.Background())
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		if err := fn(root, fakeDirEntry{name: ".", dir: true}, nil); err != nil {
			return err
		}
		cancel()
		return fn(filepath.Join(root, "disk.qcow2"), fakeDirEntry{name: "disk.qcow2"}, nil)
	}

	if _, err := driver.GetActualUsedBytes(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetActualUsedBytes() error = %v, want %v", err, context.Canceled)
	}
}

func TestCreateFailureCleansVolumeDirectoryAndReturnsMainError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("create failed")}
	driver := newTestDriverWithRunner(t, runner)

	_, err := driver.Create(context.Background(), newCreateRequest())
	if !errors.Is(err, runner.err) {
		t.Fatalf("Create() error = %v, want wrapped %v", err, runner.err)
	}
	volumeDir := filepath.Join(driver.poolRoot, "vm-a")
	if _, statErr := os.Stat(volumeDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("volume dir stat error = %v, want not exist", statErr)
	}
}

func TestCreateFromReaderCopiesQCOW2BytesWithoutConvert(t *testing.T) {
	driver, runner := newTestDriver(t)
	contents := []byte("standalone qcow2 bytes")
	req := newCreateFromReaderRequest(bytes.NewReader(contents), diskformat.FormatQCOW2)

	created, err := driver.CreateFromReader(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateFromReader() error = %v, want nil", err)
	}

	wantPath := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	if created.Context[pathKey] != wantPath || created.Context[formatKey] != string(volume.DiskFormatQCOW2) {
		t.Fatalf("created context = %#v, want qcow2 path %q", created.Context, wantPath)
	}
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v, want nil", wantPath, err)
	}
	if !bytes.Equal(got, contents) {
		t.Fatalf("target bytes = %q, want %q", got, contents)
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
	if _, err := os.Stat(wantPath + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tmp stat error = %v, want not exist", err)
	}
}

func TestCreateFromReaderConvertsRawToQCOW2(t *testing.T) {
	driver, runner := newTestDriver(t)
	req := newCreateFromReaderRequest(strings.NewReader("raw bytes"), diskformat.FormatRaw)

	if _, err := driver.CreateFromReader(context.Background(), req); err != nil {
		t.Fatalf("CreateFromReader() error = %v, want nil", err)
	}

	target := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	tmp := target + ".tmp"
	wantArgs := [][]string{{"convert", "-f", "raw", "-O", "qcow2", tmp, target}}
	if calls := runner.args(); !reflect.DeepEqual(calls, wantArgs) {
		t.Fatalf("qemu-img calls = %#v, want %#v", calls, wantArgs)
	}
	assertNoBackingArgs(t, runner.args())
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tmp stat error = %v, want not exist", err)
	}
}

func TestCreateFromReaderResizesRequestedCapacity(t *testing.T) {
	driver, runner := newTestDriver(t)
	req := newCreateFromReaderRequest(strings.NewReader("qcow2 bytes"), diskformat.FormatQCOW2)
	req.CapacityBytes = 4096

	if _, err := driver.CreateFromReader(context.Background(), req); err != nil {
		t.Fatalf("CreateFromReader() error = %v, want nil", err)
	}

	target := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	wantArgs := [][]string{{"resize", "-f", "qcow2", target, "4096"}}
	if calls := runner.args(); !reflect.DeepEqual(calls, wantArgs) {
		t.Fatalf("qemu-img calls = %#v, want %#v", calls, wantArgs)
	}
	assertNoBackingArgs(t, runner.args())
}

func TestCreateFromReaderRejectsInvalidInput(t *testing.T) {
	driver, runner := newTestDriver(t)
	tests := []struct {
		name string
		req  block.CreateFromReaderRequest
	}{
		{name: "nil reader", req: newCreateFromReaderRequest(nil, diskformat.FormatQCOW2)},
		{name: "invalid format", req: newCreateFromReaderRequest(strings.NewReader("bytes"), diskformat.Format("vmdk"))},
		{name: "unsafe vm id", req: func() block.CreateFromReaderRequest {
			req := newCreateFromReaderRequest(strings.NewReader("bytes"), diskformat.FormatQCOW2)
			req.VMID = "../vm"
			return req
		}()},
		{name: "negative capacity", req: func() block.CreateFromReaderRequest {
			req := newCreateFromReaderRequest(strings.NewReader("bytes"), diskformat.FormatQCOW2)
			req.CapacityBytes = -1
			return req
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := driver.CreateFromReader(context.Background(), tt.req)
			if !errors.Is(err, volume.ErrInvalidRequest) {
				t.Fatalf("CreateFromReader() error = %v, want %v", err, volume.ErrInvalidRequest)
			}
		})
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
}

func TestCreateFromReaderRejectsExistingTargetWithoutOverwrite(t *testing.T) {
	driver, runner := newTestDriver(t)
	target := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	writeFile(t, target, "existing")

	_, err := driver.CreateFromReader(context.Background(), newCreateFromReaderRequest(strings.NewReader("new"), diskformat.FormatQCOW2))
	if !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("CreateFromReader() error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile(%s) error = %v, want nil", target, readErr)
	}
	if string(got) != "existing" {
		t.Fatalf("target bytes = %q, want existing", got)
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
}

func TestCreateFromReaderRejectsSymlinkTargetBeforeMutation(t *testing.T) {
	driver, runner := newTestDriver(t)
	target := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	externalPath := filepath.Join(t.TempDir(), "external.qcow2")
	writeFile(t, externalPath, "outside")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(target), err)
	}
	if err := os.Symlink(externalPath, target); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	_, err := driver.CreateFromReader(context.Background(), newCreateFromReaderRequest(strings.NewReader("raw"), diskformat.FormatRaw))
	if !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("CreateFromReader() error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
	got, readErr := os.ReadFile(externalPath)
	if readErr != nil {
		t.Fatalf("ReadFile(%s) error = %v, want nil", externalPath, readErr)
	}
	if string(got) != "outside" {
		t.Fatalf("external target bytes = %q, want outside", got)
	}
	if _, err := os.Stat(target + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tmp stat error = %v, want not exist", err)
	}
}

func TestCreateFromReaderRejectsSymlinkVolumeDirectoryBeforeMutation(t *testing.T) {
	driver, runner := newTestDriver(t)
	volumeDir := filepath.Join(driver.poolRoot, "vm-a")
	externalDir := t.TempDir()
	if err := os.MkdirAll(driver.poolRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", driver.poolRoot, err)
	}
	if err := os.Symlink(externalDir, volumeDir); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	_, err := driver.CreateFromReader(context.Background(), newCreateFromReaderRequest(strings.NewReader("raw"), diskformat.FormatRaw))
	if !errors.Is(err, volume.ErrInvalidRequest) {
		t.Fatalf("CreateFromReader() error = %v, want %v", err, volume.ErrInvalidRequest)
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
	if _, err := os.Stat(filepath.Join(externalDir, "vm-a-disk-0.qcow2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external target stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(externalDir, "vm-a-disk-0.qcow2.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external tmp stat error = %v, want not exist", err)
	}
}

func TestCreateFromReaderCanceledContextDoesNotCallRunner(t *testing.T) {
	driver, runner := newTestDriver(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := driver.CreateFromReader(ctx, newCreateFromReaderRequest(strings.NewReader("raw"), diskformat.FormatRaw))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateFromReader() error = %v, want %v", err, context.Canceled)
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
}

func TestCreateFromReaderConvertFailureCleansTemporaryFiles(t *testing.T) {
	runner := &fakeRunner{err: errors.New("convert failed")}
	driver := newTestDriverWithRunner(t, runner)

	_, err := driver.CreateFromReader(context.Background(), newCreateFromReaderRequest(strings.NewReader("raw"), diskformat.FormatRaw))
	if !errors.Is(err, runner.err) {
		t.Fatalf("CreateFromReader() error = %v, want wrapped %v", err, runner.err)
	}
	target := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target stat error = %v, want not exist", statErr)
	}
	if _, statErr := os.Stat(target + ".tmp"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("tmp stat error = %v, want not exist", statErr)
	}
	if _, statErr := os.Stat(filepath.Dir(target)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("volume dir stat error = %v, want not exist", statErr)
	}
}

func TestCreateFromReaderCleanupFailureJoinsPrimaryError(t *testing.T) {
	primaryErr := errors.New("convert failed")
	runner := &fakeRunner{err: primaryErr}
	driver := newTestDriverWithRunner(t, runner)
	volumeDir := filepath.Join(driver.poolRoot, "vm-a")
	runner.beforeReturn = func(args []string) {
		if len(args) > 0 && args[0] == "convert" {
			if err := os.Chmod(volumeDir, 0o500); err != nil {
				t.Fatalf("Chmod(%s) error = %v", volumeDir, err)
			}
			t.Cleanup(func() {
				if err := os.Chmod(volumeDir, 0o700); err != nil {
					t.Errorf("Chmod(%s) cleanup error = %v", volumeDir, err)
				}
			})
		}
	}

	_, err := driver.CreateFromReader(context.Background(), newCreateFromReaderRequest(strings.NewReader("raw"), diskformat.FormatRaw))
	if !errors.Is(err, primaryErr) {
		t.Fatalf("CreateFromReader() error = %v, want wrapped %v", err, primaryErr)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("CreateFromReader() error = %v, want joined cleanup PathError", err)
	}
}

func TestCreateFromReaderCommittedTmpCleanupFailureReturnsError(t *testing.T) {
	driver, runner := newTestDriver(t)
	cleanupErr := errors.New("remove tmp failed")
	target := filepath.Join(driver.poolRoot, "vm-a", "vm-a-disk-0.qcow2")
	tmp := target + ".tmp"
	originalRemovePath := removePath
	removePath = func(path string) error {
		if path == tmp {
			return cleanupErr
		}
		return originalRemovePath(path)
	}
	t.Cleanup(func() {
		removePath = originalRemovePath
		if err := originalRemovePath(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Remove(%s) cleanup error = %v", tmp, err)
		}
		if err := originalRemovePath(filepath.Dir(target)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Remove(%s) cleanup error = %v", filepath.Dir(target), err)
		}
	})

	_, err := driver.CreateFromReader(context.Background(), newCreateFromReaderRequest(strings.NewReader("qcow2 bytes"), diskformat.FormatQCOW2))
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("CreateFromReader() error = %v, want wrapped %v", err, cleanupErr)
	}
	if calls := runner.args(); len(calls) != 0 {
		t.Fatalf("qemu-img calls = %#v, want none", calls)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target stat error = %v, want not exist after rollback cleanup", err)
	}
}

func newTestDriver(t *testing.T) (*Driver, *fakeRunner) {
	t.Helper()
	runner := &fakeRunner{}
	return newTestDriverWithRunner(t, runner), runner
}

func newTestDriverWithRunner(t *testing.T, runner qemuimg.Runner) *Driver {
	t.Helper()
	driver, err := NewDriver(Config{
		PoolName:    "pool-a",
		StorageRoot: t.TempDir(),
		QEMUImg:     qemuimg.NewClient(qemuimg.Config{Runner: runner}),
	})
	if err != nil {
		t.Fatalf("NewDriver() error = %v, want nil", err)
	}
	return driver
}

func newCreateRequest() block.CreateRequest {
	return block.CreateRequest{
		Name:          "root",
		PoolName:      "pool-a",
		VMID:          "vm-a",
		VMName:        "vm-a",
		VolumeID:      volume.ID("vol-a"),
		DiskIndex:     0,
		CapacityBytes: 1024,
	}
}

func newVolumeWithPath(path string) volume.Volume {
	return volume.Volume{
		ID:        volume.ID("vol-a"),
		Name:      "root",
		VMID:      "vm-a",
		VMName:    "vm-a",
		PoolName:  "pool-a",
		DiskIndex: 0,
		Context:   map[string]string{pathKey: path, formatKey: string(volume.DiskFormatQCOW2)},
	}
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

type fakeRunner struct {
	calls        [][]string
	err          error
	beforeReturn func(args []string)
}

type fakeDirEntry struct {
	name string
	dir  bool
}

func (e fakeDirEntry) Name() string               { return e.name }
func (e fakeDirEntry) IsDir() bool                { return e.dir }
func (e fakeDirEntry) Type() fs.FileMode          { return 0 }
func (e fakeDirEntry) Info() (fs.FileInfo, error) { return nil, errors.New("unexpected Info call") }

func (r *fakeRunner) Run(ctx context.Context, binary string, args []string) (qemuimg.RunResult, error) {
	if err := ctx.Err(); err != nil {
		return qemuimg.RunResult{}, err
	}
	r.calls = append(r.calls, append([]string(nil), args...))
	if r.beforeReturn != nil {
		r.beforeReturn(args)
	}
	if r.err != nil {
		return qemuimg.RunResult{Stderr: r.err.Error()}, r.err
	}
	if len(args) > 0 && args[0] == "info" {
		return qemuimg.RunResult{Stdout: `{"filename":"disk.qcow2","format":"qcow2","virtual-size":1024,"actual-size":512}`}, nil
	}
	return qemuimg.RunResult{}, nil
}

func newCreateFromReaderRequest(reader io.Reader, format diskformat.Format) block.CreateFromReaderRequest {
	return block.CreateFromReaderRequest{
		Reader:        reader,
		Format:        format,
		Name:          "root",
		PoolName:      "pool-a",
		VMID:          "vm-a",
		VMName:        "vm-a",
		VolumeID:      volume.ID("vol-a"),
		DiskIndex:     0,
		CapacityBytes: 0,
	}
}

func assertNoBackingArgs(t *testing.T, calls [][]string) {
	t.Helper()
	for _, call := range calls {
		for _, arg := range call {
			if arg == "-b" || arg == "-F" || arg == "rebase" {
				t.Fatalf("qemu-img call %v contains forbidden backing/rebase arg %q", call, arg)
			}
		}
	}
}

func (r *fakeRunner) args() [][]string {
	cloned := make([][]string, 0, len(r.calls))
	for _, call := range r.calls {
		cloned = append(cloned, append([]string(nil), call...))
	}
	return cloned
}
