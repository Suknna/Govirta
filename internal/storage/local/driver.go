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
	"syscall"

	"github.com/suknna/govirta/internal/storage/block"
	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/volume"
	"github.com/suknna/govirta/pkg/virt/qemuimg"
)

const (
	driverName = "local-qcow2"
	formatKey  = "format"
	// PathKey is the volume.Context map key under which this driver records the
	// host filesystem path of a created volume. It is exported because the node
	// VM/volume controller reads the published volume's host path from
	// Context[PathKey]; sharing one constant removes a silent cross-layer string
	// duplication (a drifted key would otherwise degrade the controller to a
	// permanent not-ready without a compile error).
	PathKey = "path"
)

var safeNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var walkDir = filepath.WalkDir
var removePath = os.Remove
var removeAllPath = os.RemoveAll

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
			CreateDelete:  true,
			Publish:       true,
			Snapshot:      true,
			ResizeOffline: true,
		},
	}, nil
}

// Create allocates a new local qcow2 path and delegates image creation to qemu-img.
// It writes through a driver-owned temporary file and commits with no-overwrite
// link semantics so a late final-path replacement cannot be overwritten.
func (d *Driver) Create(ctx context.Context, req block.CreateRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}

	path, volumeDir, err := d.pathForCreate(req)
	if err != nil {
		return volume.Volume{}, err
	}
	if err := d.ensureOwnedDir(volumeDir); err != nil {
		return volume.Volume{}, err
	}
	if err := ensureCreateTargetAvailable(path); err != nil {
		return volume.Volume{}, err
	}

	tmpDir, err := makePrivateTempDir(volumeDir)
	if err != nil {
		cleanupErr := removeDirIfEmpty(volumeDir)
		return volume.Volume{}, errors.Join(err, cleanupErr)
	}
	tmpPath := filepath.Join(tmpDir, "output.qcow2")
	if err := d.qemuimg.QCOW2().Create().Target(tmpPath).SizeBytes(req.CapacityBytes).Do(ctx); err != nil {
		cleanupErr := cleanupFailedCreate(tmpDir, volumeDir)
		return volume.Volume{}, errors.Join(err, cleanupErr)
	}
	if err := ensureRegularFile(tmpPath); err != nil {
		cleanupErr := cleanupFailedCreate(tmpDir, volumeDir)
		return volume.Volume{}, errors.Join(err, cleanupErr)
	}
	if err := commitTempImage(tmpPath, path); err != nil {
		cleanupErr := cleanupFailedCreate(tmpDir, volumeDir)
		return volume.Volume{}, errors.Join(err, cleanupErr)
	}
	created := volume.Volume{
		ID:            req.VolumeID,
		Name:          req.Name,
		VMID:          req.VMID,
		VMName:        req.VMName,
		PoolName:      req.PoolName,
		Role:          req.Role,
		DiskIndex:     req.DiskIndex,
		Backend:       driverName,
		CapacityBytes: req.CapacityBytes,
		State:         volume.StateAvailable,
		Context: map[string]string{
			PathKey:   path,
			formatKey: string(volume.DiskFormatQCOW2),
		},
	}
	if err := removeAllPath(tmpDir); err != nil {
		return created, errors.Join(volume.ErrVolumeCleanupFailed, err)
	}

	return created, nil
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
	if err := d.ensureOwnedDir(volumeDir); err != nil {
		return volume.Volume{}, err
	}
	if err := ensureCreateTargetAvailable(path); err != nil {
		return volume.Volume{}, err
	}

	tmpDir, err := makePrivateTempDir(volumeDir)
	if err != nil {
		cleanupErr := removeDirIfEmpty(volumeDir)
		return volume.Volume{}, errors.Join(err, cleanupErr)
	}
	tmpPath := filepath.Join(tmpDir, "output.qcow2")
	inputTmpPath := filepath.Join(tmpDir, "input.raw")
	cleanupTemps := func() error {
		return cleanupFailedCreate(tmpDir, volumeDir)
	}

	if req.Format == diskformat.FormatQCOW2 {
		if err := copyReaderToPath(ctx, req.Reader, tmpPath); err != nil {
			cleanupErr := cleanupTemps()
			return volume.Volume{}, errors.Join(err, cleanupErr)
		}
	} else {
		if err := copyReaderToPath(ctx, req.Reader, inputTmpPath); err != nil {
			cleanupErr := cleanupTemps()
			return volume.Volume{}, errors.Join(err, cleanupErr)
		}
		if err := d.qemuimg.QCOW2().Convert().SourceFormat("raw").Source(inputTmpPath).Target(tmpPath).Do(ctx); err != nil {
			cleanupErr := cleanupTemps()
			return volume.Volume{}, errors.Join(err, cleanupErr)
		}
	}

	if err := ensureRegularFile(tmpPath); err != nil {
		cleanupErr := cleanupTemps()
		return volume.Volume{}, errors.Join(err, cleanupErr)
	}
	if req.CapacityBytes > 0 {
		if err := d.qemuimg.QCOW2().Resize().Path(tmpPath).SizeBytes(req.CapacityBytes).Do(ctx); err != nil {
			cleanupErr := cleanupTemps()
			return volume.Volume{}, errors.Join(err, cleanupErr)
		}
		if err := ensureRegularFile(tmpPath); err != nil {
			cleanupErr := cleanupTemps()
			return volume.Volume{}, errors.Join(err, cleanupErr)
		}
	}
	if err := commitTempImage(tmpPath, path); err != nil {
		cleanupErr := cleanupTemps()
		return volume.Volume{}, errors.Join(err, cleanupErr)
	}
	created := d.newVolume(req, path)
	if err := removeAllPath(tmpDir); err != nil {
		return created, errors.Join(volume.ErrVolumeCleanupFailed, err)
	}

	return created, nil
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
	if err := d.ensureExistingOwnedDir(volumeDir); err != nil {
		return err
	}
	if err := ensurePublishableImage(path); err != nil {
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
	if err := d.ensureExistingOwnedDir(d.poolRoot); err != nil {
		// A freshly registered pool has no poolRoot yet: it is created lazily on
		// the first volume create. Until then the pool holds nothing, so its used
		// bytes are 0 rather than an error — otherwise GetPoolUsage (called at
		// registration, before any volume exists) fails forever and the node
		// controller spins re-reconciling. This mirrors the file driver's
		// GetActualUsedBytes, which treats a missing image root as 0 used bytes.
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
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
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
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
	path, volumeDir, err := d.pathFromVolume(vol)
	if err != nil {
		return volume.PublishedVolume{}, err
	}
	if err := d.ensureExistingOwnedDir(volumeDir); err != nil {
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

// Snapshot creates a qcow2 internal snapshot on the volume's owned image. It
// resolves and validates the path the same way Delete/Publish do — via
// pathFromVolume + ensureExistingOwnedDir + ensurePublishableImage — so the
// raw Context[PathKey] is never trusted without the ownership/expected-path
// check.
func (d *Driver) Snapshot(ctx context.Context, vol volume.Volume, req block.SnapshotRequest) (volume.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return volume.Snapshot{}, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return volume.Snapshot{}, fmt.Errorf("%w: snapshot name is required", volume.ErrInvalidRequest)
	}
	path, volumeDir, err := d.pathFromVolume(vol)
	if err != nil {
		return volume.Snapshot{}, err
	}
	if err := d.ensureExistingOwnedDir(volumeDir); err != nil {
		return volume.Snapshot{}, err
	}
	if err := ensurePublishableImage(path); err != nil {
		return volume.Snapshot{}, err
	}
	if err := d.qemuimg.QCOW2().Snapshot().Path(path).Name(req.Name).Do(ctx); err != nil {
		return volume.Snapshot{}, fmt.Errorf("local: create snapshot %q on %q: %w", req.Name, path, err)
	}
	return volume.Snapshot{Name: req.Name, VolumeID: vol.ID}, nil
}

// DeleteSnapshot deletes a named internal snapshot. It is idempotent on a missing
// snapshot: qemu-img `snapshot -d` errors non-zero when the named snapshot does
// not exist, so we list first and skip the delete if absent. This keeps teardown
// from sticking forever on a re-driven delete or on a disk that was never
// snapshotted (a create that failed mid-fan-out left some disks untouched).
func (d *Driver) DeleteSnapshot(ctx context.Context, vol volume.Volume, req block.DeleteSnapshotRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("%w: snapshot name is required", volume.ErrInvalidRequest)
	}
	path, volumeDir, err := d.pathFromVolume(vol)
	if err != nil {
		return err
	}
	if err := d.ensureExistingOwnedDir(volumeDir); err != nil {
		return err
	}
	if err := ensurePublishableImage(path); err != nil {
		return err
	}
	// List-before-delete idempotency: only delete when the snapshot is present.
	listing, err := d.qemuimg.QCOW2().SnapshotList().Path(path).Do(ctx)
	if err != nil {
		return fmt.Errorf("local: list snapshots on %q: %w", path, err)
	}
	if !snapshotListContains(listing, req.Name) {
		return nil // already gone — idempotent success
	}
	if err := d.qemuimg.QCOW2().SnapshotDelete().Path(path).Name(req.Name).Do(ctx); err != nil {
		return fmt.Errorf("local: delete snapshot %q on %q: %w", req.Name, path, err)
	}
	return nil
}

// snapshotListContains reports whether name appears as an internal snapshot tag
// in qemu-img `snapshot -l` output. The output is a fixed-column table whose data
// rows carry the tag in the second whitespace-delimited field (ID is first); a
// header/empty output yields no match. Matching the exact tag token (not a
// substring) avoids a prefix collision between two snapshot names.
func snapshotListContains(listing, name string) bool {
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == name {
			return true
		}
	}
	return false
}

// Resize grows the volume's qcow2 to req.CapacityBytes when the live virtual
// size is below the target. 为什么在 driver 这一层读 live virtual size：qcow2
// 文件本身是容量的唯一事实来源，以它判断幂等让 resize 天然可重入——live 已
// >= 目标时直接视为成功的 no-op，于是 level-triggered 反复对账与崩溃重试都
// 能收敛而不报错。缩容永远不会发生（admission 拒绝容量下降，且这里从不传
// --shrink）。路径解析与 Delete/Snapshot/Publish 一致：pathFromVolume +
// ensureExistingOwnedDir + ensurePublishableImage，绝不裸信 Context[PathKey]。
func (d *Driver) Resize(ctx context.Context, vol volume.Volume, req block.ResizeRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	if req.CapacityBytes <= 0 {
		return volume.Volume{}, volume.ErrInvalidRequest
	}
	path, volumeDir, err := d.pathFromVolume(vol)
	if err != nil {
		return volume.Volume{}, err
	}
	if err := d.ensureExistingOwnedDir(volumeDir); err != nil {
		return volume.Volume{}, err
	}
	if err := ensurePublishableImage(path); err != nil {
		return volume.Volume{}, err
	}
	info, err := d.qemuimg.QCOW2().Info().Path(path).Do(ctx)
	if err != nil {
		return volume.Volume{}, fmt.Errorf("resize volume %q: read live size: %w", vol.Name, err)
	}
	// 幂等：live 已达到或超过目标，接受为 no-op，返回声明后的容量。
	if info.VirtualSize >= req.CapacityBytes {
		resized := vol
		resized.CapacityBytes = req.CapacityBytes
		return resized, nil
	}
	if err := d.qemuimg.QCOW2().Resize().Path(path).SizeBytes(req.CapacityBytes).Do(ctx); err != nil {
		return volume.Volume{}, fmt.Errorf("resize volume %q to %d: %w", vol.Name, req.CapacityBytes, err)
	}
	resized := vol
	resized.CapacityBytes = req.CapacityBytes
	return resized, nil
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

func (d *Driver) ensureOwnedDir(path string) error {
	return ensureOwnedDirUnder(d.storageRoot, path, volume.ErrInvalidRequest)
}

func (d *Driver) ensureExistingOwnedDir(path string) error {
	return ensureExistingOwnedDirUnder(d.storageRoot, path, volume.ErrInvalidRequest)
}

func ensureOwnedDirUnder(root, path string, baseErr error) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !pathWithinOrEqual(path, root) {
		return baseErr
	}
	if err := ensureExistingOwnedDirPath(root, baseErr); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return baseErr
	}
	if rel == "." {
		return nil
	}

	current := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("%w: path must be a real directory: %s", baseErr, current)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := ensureExistingOwnedDirPath(current, baseErr); err != nil {
			return err
		}
	}
	return nil
}

func ensureExistingOwnedDirUnder(root, path string, baseErr error) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !pathWithinOrEqual(path, root) {
		return baseErr
	}
	if err := ensureExistingOwnedDirPath(root, baseErr); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return baseErr
	}
	if rel == "." {
		return nil
	}

	current := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		if err := ensureExistingOwnedDirPath(current, baseErr); err != nil {
			return err
		}
	}
	return nil
}

func ensureExistingOwnedDirPath(path string, baseErr error) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: path must be a real directory: %s", baseErr, path)
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
	if vol.Context[formatKey] != string(volume.DiskFormatQCOW2) {
		return "", "", volume.ErrInvalidRequest
	}
	volumeDir := filepath.Join(d.poolRoot, vol.VMID)
	expectedPath := filepath.Join(volumeDir, fmt.Sprintf("%s-disk-%d.qcow2", vol.VMName, vol.DiskIndex))
	path := vol.Context[PathKey]
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

func pathWithinOrEqual(path, dir string) bool {
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(os.PathSeparator))
}

func removeIfExists(path string) error {
	if err := removePath(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func removeDirIfEmpty(path string) error {
	if err := removePath(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if errors.Is(err, syscall.ENOTEMPTY) {
			return nil
		}
		return err
	}
	return nil
}

func makePrivateTempDir(volumeDir string) (string, error) {
	tmpDir, err := os.MkdirTemp(volumeDir, ".govirta-tmp-*")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(tmpDir, 0o700); err != nil {
		cleanupErr := removeAllPath(tmpDir)
		return "", errors.Join(err, cleanupErr)
	}
	return tmpDir, nil
}

func cleanupFailedCreate(tmpDir, volumeDir string) error {
	return errors.Join(removeAllPath(tmpDir), removeDirIfEmpty(volumeDir))
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

func ensureRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: path must be a regular file: %s", volume.ErrInvalidRequest, path)
	}
	return nil
}

func commitTempImage(tmpPath, targetPath string) error {
	return os.Link(tmpPath, targetPath)
}

func (d *Driver) newVolume(req block.CreateFromReaderRequest, path string) volume.Volume {
	return volume.Volume{
		ID:            req.VolumeID,
		Name:          req.Name,
		VMID:          req.VMID,
		VMName:        req.VMName,
		PoolName:      req.PoolName,
		Role:          req.Role,
		DiskIndex:     req.DiskIndex,
		Backend:       driverName,
		CapacityBytes: req.CapacityBytes,
		State:         volume.StateAvailable,
		Context: map[string]string{
			PathKey:   path,
			formatKey: string(volume.DiskFormatQCOW2),
		},
	}
}
