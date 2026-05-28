package local

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/suknna/govirta/internal/storage/block"
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

func TestGetActualUsedBytesHonorsContextDuringWalk(t *testing.T) {
	driver, _ := newTestDriver(t)
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
		Context:   map[string]string{pathKey: path},
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
	calls [][]string
	err   error
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
	if r.err != nil {
		return qemuimg.RunResult{Stderr: r.err.Error()}, r.err
	}
	if len(args) > 0 && args[0] == "info" {
		return qemuimg.RunResult{Stdout: `{"filename":"disk.qcow2","format":"qcow2","virtual-size":1024,"actual-size":512}`}, nil
	}
	return qemuimg.RunResult{}, nil
}

func (r *fakeRunner) args() [][]string {
	cloned := make([][]string, 0, len(r.calls))
	for _, call := range r.calls {
		cloned = append(cloned, append([]string(nil), call...))
	}
	return cloned
}
