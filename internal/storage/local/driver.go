package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/volume"
	"github.com/suknna/govirta/internal/virt/qemuimg"
)

const (
	driverName = "local-qcow2"
	formatKey  = "format"
	pathKey    = "path"
)

var safeNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var walkDir = filepath.WalkDir
var removePath = os.Remove

// Config configures a host-local qcow2 block driver for one storage pool.
type Config struct {
	PoolName    string
	StorageRoot string
	QEMUImg     qemuimg.Client
}

// Driver manages qcow2 volumes under a single trusted local pool directory.
type Driver struct {
	poolName    string
	storageRoot string
	poolRoot    string
	qemuimg     qemuimg.Client
}

// NewDriver creates a local qcow2 driver rooted at StorageRoot/pool/PoolName.
func NewDriver(config Config) (*Driver, error) {
	if config.PoolName == "" || config.StorageRoot == "" {
		return nil, volume.ErrInvalidRequest
	}
	if !safeName(config.PoolName) {
		return nil, volume.ErrInvalidRequest
	}

	client := config.QEMUImg
	if client == nil {
		client = qemuimg.NewClient(qemuimg.Config{})
	}

	storageRoot, err := cleanStorageRoot(config.StorageRoot)
	if err != nil {
		return nil, err
	}
	poolRoot := filepath.Join(storageRoot, "pool", config.PoolName)
	return &Driver{
		poolName:    config.PoolName,
		storageRoot: storageRoot,
		poolRoot:    poolRoot,
		qemuimg:     client,
	}, nil
}

// DriverInfo reports the local qcow2 capabilities supported in this phase.
func (d *Driver) DriverInfo(ctx context.Context) (block.DriverInfo, error) {
	if err := ctx.Err(); err != nil {
		return block.DriverInfo{}, err
	}
	return block.DriverInfo{
		Name: driverName,
		Capabilities: block.Capabilities{
			CreateDelete: true,
			Publish:      true,
		},
	}, nil
}

// Create allocates a new local qcow2 path and delegates image creation to qemu-img.
// It refuses any pre-existing target path before invoking the backend process.
func (d *Driver) Create(ctx context.Context, req block.CreateRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}

	path, volumeDir, err := d.pathForCreate(req)
	if err != nil {
		return volume.Volume{}, err
	}
	if err := os.MkdirAll(volumeDir, 0o755); err != nil {
		return volume.Volume{}, err
	}
	if err := ensureCreateTargetAvailable(path); err != nil {
		return volume.Volume{}, err
	}
	if err := d.qemuimg.QCOW2().Create().Target(path).SizeBytes(req.CapacityBytes).Do(ctx); err != nil {
		cleanupErr := errors.Join(removeIfExists(path), removeDirIfEmpty(volumeDir))
		return volume.Volume{}, errors.Join(err, cleanupErr)
	}

	return volume.Volume{
		ID:            req.VolumeID,
		Name:          req.Name,
		VMID:          req.VMID,
		VMName:        req.VMName,
		PoolName:      req.PoolName,
		DiskIndex:     req.DiskIndex,
		Backend:       driverName,
		CapacityBytes: req.CapacityBytes,
		State:         volume.StateAvailable,
		Context: map[string]string{
			pathKey:   path,
			formatKey: string(volume.DiskFormatQCOW2),
		},
	}, nil
}

// CreateFromReader creates a standalone qcow2 root volume from explicit image bytes.
// It never creates qcow2 backing chains: qcow2 input is committed as a full file,
// and raw input is converted through qemu-img with an explicit raw source format.
func (d *Driver) CreateFromReader(ctx context.Context, req block.CreateFromReaderRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	if req.Reader == nil || !req.Format.Valid() {
		return volume.Volume{}, volume.ErrInvalidRequest
	}

	path, volumeDir, err := d.pathForCreateFromReader(req)
	if err != nil {
		return volume.Volume{}, err
	}
	if req.CapacityBytes < 0 {
		return volume.Volume{}, volume.ErrInvalidRequest
	}
	if err := os.MkdirAll(volumeDir, 0o755); err != nil {
		return volume.Volume{}, err
	}
	if err := ensureCreateTargetAvailable(path); err != nil {
		return volume.Volume{}, err
	}

	tmpPath := path + ".tmp"
	if err := ensureCreateTargetAvailable(tmpPath); err != nil {
		return volume.Volume{}, err
	}
	if err := copyReaderToPath(ctx, req.Reader, tmpPath); err != nil {
		cleanupErr := errors.Join(removeIfExists(tmpPath), removeDirIfEmpty(volumeDir))
		return volume.Volume{}, errors.Join(err, cleanupErr)
	}

	if req.Format == diskformat.FormatQCOW2 {
		if err := commitCopiedImage(tmpPath, path); err != nil {
			cleanupErr := errors.Join(removeIfExists(tmpPath), removeIfExists(path), removeDirIfEmpty(volumeDir))
			return volume.Volume{}, errors.Join(err, cleanupErr)
		}
	} else {
		if err := d.qemuimg.QCOW2().Convert().SourceFormat("raw").Source(tmpPath).Target(path).Do(ctx); err != nil {
			cleanupErr := errors.Join(removeIfExists(tmpPath), removeIfExists(path), removeDirIfEmpty(volumeDir))
			return volume.Volume{}, errors.Join(err, cleanupErr)
		}
	}

	if req.CapacityBytes > 0 {
		if err := d.qemuimg.QCOW2().Resize().Path(path).SizeBytes(req.CapacityBytes).Do(ctx); err != nil {
			cleanupErr := errors.Join(removeIfExists(tmpPath), removeIfExists(path), removeDirIfEmpty(volumeDir))
			return volume.Volume{}, errors.Join(err, cleanupErr)
		}
	}

	if err := removeIfExists(tmpPath); err != nil {
		cleanupErr := errors.Join(removeIfExists(path), removeIfExists(tmpPath), removeDirIfEmpty(volumeDir))
		return volume.Volume{}, errors.Join(err, cleanupErr)
	}

	return d.newVolume(req, path), nil
}

// Delete removes a local qcow2 file and then removes its empty VM directory.
// The volume path must be the driver-owned path derived from volume metadata.
func (d *Driver) Delete(ctx context.Context, vol volume.Volume) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, volumeDir, err := d.pathFromVolume(vol)
	if err != nil {
		return err
	}
	if err := d.qemuimg.QCOW2().Remove().Path(path).Do(ctx); err != nil {
		return err
	}
	return removeDirIfEmpty(volumeDir)
}

// GetActualUsedBytes walks the local pool root and sums file sizes.
// The walk checks ctx during traversal so cancellation can interrupt large pools.
func (d *Driver) GetActualUsedBytes(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	var total int64
	err := walkDir(d.poolRoot, func(path string, entry fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > 0 {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// Publish validates volume ownership and exposes the qcow2 as a file attachment.
// It only publishes an existing regular .qcow2 image after qemu-img info succeeds.
func (d *Driver) Publish(ctx context.Context, vol volume.Volume, req block.PublishRequest) (volume.PublishedVolume, error) {
	if err := ctx.Err(); err != nil {
		return volume.PublishedVolume{}, err
	}
	if req.VolumeID != vol.ID || req.VMID != vol.VMID {
		return volume.PublishedVolume{}, volume.ErrInvalidRequest
	}
	path, _, err := d.pathFromVolume(vol)
	if err != nil {
		return volume.PublishedVolume{}, err
	}
	if err := ensurePublishableImage(path); err != nil {
		return volume.PublishedVolume{}, err
	}
	if _, err := d.qemuimg.QCOW2().Info().Path(path).Do(ctx); err != nil {
		return volume.PublishedVolume{}, err
	}

	return volume.PublishedVolume{
		VolumeID: vol.ID,
		VMID:     req.VMID,
		PoolName: vol.PoolName,
		Attachment: volume.Attachment{
			Kind:     volume.AttachmentFile,
			Format:   volume.DiskFormatQCOW2,
			Path:     path,
			ReadOnly: req.ReadOnly,
		},
	}, nil
}

// Unpublish is a no-op for local files; there is no mount or export to tear down.
func (d *Driver) Unpublish(ctx context.Context, vol volume.Volume, req block.UnpublishRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// Snapshot is unsupported for the local driver until offline snapshot policy lands.
func (d *Driver) Snapshot(ctx context.Context, vol volume.Volume, req block.SnapshotRequest) (volume.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return volume.Snapshot{}, err
	}
	return volume.Snapshot{}, volume.ErrUnsupported
}

// Resize is unsupported for the local driver until offline resize policy lands.
func (d *Driver) Resize(ctx context.Context, vol volume.Volume, req block.ResizeRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	return volume.Volume{}, volume.ErrUnsupported
}

func ensureCreateTargetAvailable(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return fmt.Errorf("%w: target path already exists: %s", volume.ErrInvalidRequest, path)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func ensurePublishableImage(path string) error {
	if filepath.Ext(path) != ".qcow2" {
		return fmt.Errorf("%w: path must be a .qcow2 file", volume.ErrInvalidRequest)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: path must be a regular .qcow2 file, not a symlink", volume.ErrInvalidRequest)
	}
	if info.IsDir() {
		return fmt.Errorf("%w: path must be a regular .qcow2 file, not a directory", volume.ErrInvalidRequest)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: path must be a regular .qcow2 file", volume.ErrInvalidRequest)
	}
	return nil
}

func (d *Driver) pathForCreate(req block.CreateRequest) (string, string, error) {
	if req.PoolName != d.poolName || req.VolumeID == "" || req.VMID == "" || req.VMName == "" || req.Name == "" || req.CapacityBytes <= 0 || req.DiskIndex < 0 {
		return "", "", volume.ErrInvalidRequest
	}
	if !safeName(req.PoolName) || !safeName(req.VMID) || !safeName(req.VMName) {
		return "", "", volume.ErrInvalidRequest
	}

	volumeDir := filepath.Join(d.poolRoot, req.VMID)
	path := filepath.Join(volumeDir, fmt.Sprintf("%s-disk-%d.qcow2", req.VMName, req.DiskIndex))
	if !pathWithinDir(path, volumeDir) || !pathWithinDir(path, d.poolRoot) {
		return "", "", volume.ErrInvalidRequest
	}
	return path, volumeDir, nil
}

func (d *Driver) pathForCreateFromReader(req block.CreateFromReaderRequest) (string, string, error) {
	if req.PoolName != d.poolName || req.VolumeID == "" || req.VMID == "" || req.VMName == "" || req.Name == "" || req.CapacityBytes < 0 || req.DiskIndex < 0 {
		return "", "", volume.ErrInvalidRequest
	}
	if !safeName(req.PoolName) || !safeName(req.VMID) || !safeName(req.VMName) {
		return "", "", volume.ErrInvalidRequest
	}

	volumeDir := filepath.Join(d.poolRoot, req.VMID)
	path := filepath.Join(volumeDir, fmt.Sprintf("%s-disk-%d.qcow2", req.VMName, req.DiskIndex))
	if !pathWithinDir(path, volumeDir) || !pathWithinDir(path, d.poolRoot) {
		return "", "", volume.ErrInvalidRequest
	}
	return path, volumeDir, nil
}

func (d *Driver) pathFromVolume(vol volume.Volume) (string, string, error) {
	if vol.PoolName != d.poolName || vol.VMID == "" || !safeName(vol.VMID) || vol.VMName == "" || !safeName(vol.VMName) || vol.DiskIndex < 0 {
		return "", "", volume.ErrInvalidRequest
	}
	volumeDir := filepath.Join(d.poolRoot, vol.VMID)
	expectedPath := filepath.Join(volumeDir, fmt.Sprintf("%s-disk-%d.qcow2", vol.VMName, vol.DiskIndex))
	path := vol.Context[pathKey]
	if path == "" {
		return "", "", volume.ErrInvalidRequest
	}
	path = filepath.Clean(path)
	if path != expectedPath || !pathWithinDir(path, volumeDir) || !pathWithinDir(path, d.poolRoot) {
		return "", "", volume.ErrInvalidRequest
	}
	return path, volumeDir, nil
}

func cleanStorageRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.ContainsRune(root, 0) {
		return "", volume.ErrInvalidRequest
	}
	if !filepath.IsAbs(root) {
		return "", volume.ErrInvalidRequest
	}
	clean, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", volume.ErrInvalidRequest
	}
	if clean == string(filepath.Separator) {
		return "", volume.ErrInvalidRequest
	}
	return clean, nil
}

func safeName(value string) bool {
	if value == "." || value == ".." {
		return false
	}
	return safeNamePattern.MatchString(value)
}

func pathWithinDir(path, dir string) bool {
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	return strings.HasPrefix(path, dir+string(os.PathSeparator))
}

func removeIfExists(path string) error {
	if err := removePath(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func removeDirIfEmpty(path string) error {
	if err := removePath(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func copyReaderToPath(ctx context.Context, reader io.Reader, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		return errors.Join(copyErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	return ctx.Err()
}

func commitCopiedImage(tmpPath, targetPath string) error {
	return os.Link(tmpPath, targetPath)
}

func (d *Driver) newVolume(req block.CreateFromReaderRequest, path string) volume.Volume {
	return volume.Volume{
		ID:            req.VolumeID,
		Name:          req.Name,
		VMID:          req.VMID,
		VMName:        req.VMName,
		PoolName:      req.PoolName,
		DiskIndex:     req.DiskIndex,
		Backend:       driverName,
		CapacityBytes: req.CapacityBytes,
		State:         volume.StateAvailable,
		Context: map[string]string{
			pathKey:   path,
			formatKey: string(volume.DiskFormatQCOW2),
		},
	}
}
