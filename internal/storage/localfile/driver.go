package localfile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"unicode"

	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/image"
	"github.com/suknna/govirta/internal/storage/volume"
)

const driverName = "local-file-image"

var safeNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var walkDir = filepath.WalkDir

var removeCommittedTemp = os.Remove

const noFollowSupported = true

var _ image.Driver = (*Driver)(nil)

// Config configures a host-local file image driver for one storage pool.
type Config struct {
	PoolName    string
	StorageRoot string
}

// Driver stores qcow2 and raw image bytes under one trusted local file pool.
type Driver struct {
	poolName string
	root     string
}

// NewDriver creates a local file image driver rooted at StorageRoot/pool/PoolName/images.
func NewDriver(config Config) (*Driver, error) {
	if !safeName(config.PoolName) {
		return nil, volume.ErrInvalidRequest
	}
	root, err := cleanStorageRoot(config.StorageRoot)
	if err != nil {
		return nil, err
	}
	return &Driver{poolName: config.PoolName, root: root}, nil
}

// DriverInfo reports the local image formats supported by this driver.
func (d *Driver) DriverInfo(ctx context.Context) (image.DriverInfo, error) {
	if err := ctx.Err(); err != nil {
		return image.DriverInfo{}, err
	}
	return image.DriverInfo{
		Name: driverName,
		Capabilities: image.Capabilities{
			SupportsRaw:   true,
			SupportsQCOW2: true,
		},
	}, nil
}

// Put creates a pending image writer that commits bytes on Close.
func (d *Driver) Put(ctx context.Context, req image.PutRequest) (image.ImageWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !safeName(req.ImageID) || !req.Format.Valid() || req.DeclaredSizeBytes <= 0 {
		return nil, image.ErrInvalidImage
	}

	target, imageDir, err := d.imagePath(req.ImageID, req.Format)
	if err != nil {
		return nil, err
	}
	if err := d.ensureImageRoot(d.imageRoot()); err != nil {
		return nil, err
	}
	if err := os.Mkdir(imageDir, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			if err := ensureExistingImageDir(imageDir); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %s", image.ErrImageExists, req.ImageID)
		}
		return nil, err
	}
	if err := ensureExistingImageDir(imageDir); err != nil {
		cleanupErr := removeDirIfEmpty(imageDir)
		return nil, errors.Join(err, cleanupErr)
	}
	tmp := target + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		cleanupErr := removeDirIfEmpty(imageDir)
		return nil, errors.Join(err, cleanupErr)
	}
	return &imageWriter{file: file, tmp: tmp, target: target, imageDir: imageDir}, nil
}

// Get opens committed image bytes for reading.
func (d *Driver) Get(ctx context.Context, req image.GetRequest) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !safeName(req.ImageID) {
		return nil, image.ErrInvalidImage
	}
	if err := d.ensureExistingImageRoot(d.imageRoot()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", image.ErrImageNotFound, req.ImageID)
		}
		return nil, err
	}
	path, err := d.existingImagePath(req.ImageID)
	if err != nil {
		return nil, err
	}
	file, err := openRegularImage(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", image.ErrImageNotFound, req.ImageID)
		}
		return nil, err
	}
	return file, nil
}

// Delete removes committed image bytes and then removes the empty image directory.
func (d *Driver) Delete(ctx context.Context, req image.DeleteRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !safeName(req.ImageID) || !req.Format.Valid() {
		return image.ErrInvalidImage
	}
	if err := d.ensureExistingImageRoot(d.imageRoot()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", image.ErrImageNotFound, req.ImageID)
		}
		return err
	}
	path, imageDir, err := d.imagePath(req.ImageID, req.Format)
	if err != nil {
		return err
	}
	if err := ensureExistingImageDir(imageDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", image.ErrImageNotFound, req.ImageID)
		}
		return err
	}
	if err := ensureDeletableImageFile(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", image.ErrImageNotFound, req.ImageID)
		}
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", image.ErrImageNotFound, req.ImageID)
		}
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%w: image path still exists after delete: %s", image.ErrInvalidImage, path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	cleanupErr := errors.Join(
		cleanupCommittedTemp(path+".tmp"),
		cleanupImageDir(filepath.Dir(path)),
	)
	if cleanupErr != nil {
		return errors.Join(image.ErrImageCleanupFailed, cleanupErr)
	}
	return nil
}

// GetActualUsedBytes walks the driver image root and sums regular file sizes.
func (d *Driver) GetActualUsedBytes(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := d.ensureExistingImageRoot(d.imageRoot()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	var total int64
	err := walkDir(d.imageRoot(), func(path string, entry fs.DirEntry, err error) error {
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
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			return nil
		}
		if math.MaxInt64-total < info.Size() {
			total = math.MaxInt64
			return nil
		}
		total += info.Size()
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

type imageWriter struct {
	mu       sync.Mutex
	file     *os.File
	tmp      string
	target   string
	imageDir string
	done     bool
}

func (w *imageWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return 0, image.ErrInvalidImage
	}
	return w.file.Write(p)
}

func (w *imageWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return image.ErrInvalidImage
	}
	w.done = true

	if err := w.file.Close(); err != nil {
		cleanupErr := cleanupPendingImage(w.tmp, w.imageDir)
		return errors.Join(err, cleanupErr)
	}
	if err := os.Link(w.tmp, w.target); err != nil {
		cleanupErr := cleanupPendingImage(w.tmp, w.imageDir)
		if errors.Is(err, fs.ErrExist) {
			err = errors.Join(fmt.Errorf("%w: %s", image.ErrImageExists, w.target), err)
		}
		return errors.Join(err, cleanupErr)
	}
	removeErr := removeCommittedTemp(w.tmp)
	if removeErr != nil {
		return fmt.Errorf("%w: remove committed image temp %s: %w", image.ErrImageCleanupFailed, w.tmp, removeErr)
	}
	return nil
}

func (w *imageWriter) Cancel() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return image.ErrInvalidImage
	}
	w.done = true

	closeErr := w.file.Close()
	cleanupErr := cleanupPendingImage(w.tmp, w.imageDir)
	return errors.Join(closeErr, cleanupErr)
}

func (d *Driver) imageRoot() string {
	return filepath.Join(d.root, "pool", d.poolName, "images")
}

func (d *Driver) imagePath(imageID string, format diskformat.Format) (string, string, error) {
	if !safeName(imageID) || !format.Valid() {
		return "", "", image.ErrInvalidImage
	}
	imageDir := filepath.Join(d.imageRoot(), imageID)
	target := filepath.Join(imageDir, fmt.Sprintf("%s.%s", imageID, format))
	if !pathWithinDir(imageDir, d.imageRoot()) || !pathWithinDir(target, imageDir) || !pathWithinDir(target, d.imageRoot()) {
		return "", "", image.ErrInvalidImage
	}
	return target, imageDir, nil
}

func (d *Driver) existingImagePath(imageID string) (string, error) {
	if !safeName(imageID) {
		return "", image.ErrInvalidImage
	}
	for _, format := range []diskformat.Format{diskformat.FormatQCOW2, diskformat.FormatRaw} {
		path, imageDir, err := d.imagePath(imageID, format)
		if err != nil {
			return "", err
		}
		if err := ensureExistingImageDir(imageDir); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return "", err
		}
		info, err := os.Lstat(path)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || info.IsDir() || !info.Mode().IsRegular() {
				return "", image.ErrInvalidImage
			}
			return path, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("%w: %s", image.ErrImageNotFound, imageID)
}

func (d *Driver) ensureImageRoot(path string) error {
	return ensureImageDirUnder(d.root, path)
}

func (d *Driver) ensureExistingImageRoot(path string) error {
	return ensureExistingImageDirUnder(d.root, path)
}

func ensureExistingImageDir(path string) error {
	return ensureExistingImageDirPath(path)
}

func ensureImageDirUnder(root, path string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !pathWithinOrEqual(path, root) {
		return image.ErrInvalidImage
	}
	if err := ensureExistingImageDirPath(root); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return image.ErrInvalidImage
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
				return fmt.Errorf("%w: image directory must be a real directory: %s", image.ErrInvalidImage, current)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := ensureExistingImageDirPath(current); err != nil {
			return err
		}
	}
	return nil
}

func ensureExistingImageDirUnder(root, path string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !pathWithinOrEqual(path, root) {
		return image.ErrInvalidImage
	}
	if err := ensureExistingImageDirPath(root); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return image.ErrInvalidImage
	}
	if rel == "." {
		return nil
	}

	current := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		if err := ensureExistingImageDirPath(current); err != nil {
			return err
		}
	}
	return nil
}

func ensureExistingImageDirPath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: image directory must be a real directory: %s", image.ErrInvalidImage, path)
	}
	return nil
}

func openRegularImage(path string) (*os.File, error) {
	flags := os.O_RDONLY
	if noFollowSupported {
		flags |= syscall.O_NOFOLLOW
	}
	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		if noFollowSupported && errors.Is(err, syscall.ELOOP) {
			return nil, image.ErrInvalidImage
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		closeErr := file.Close()
		return nil, errors.Join(err, closeErr)
	}
	if !info.Mode().IsRegular() {
		closeErr := file.Close()
		return nil, errors.Join(image.ErrInvalidImage, closeErr)
	}
	return file, nil
}

func ensureDeletableImageFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() || !info.Mode().IsRegular() {
		return image.ErrInvalidImage
	}
	return nil
}

func cleanStorageRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" || hasControlRune(root) || strings.ContainsRune(root, 0) {
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
	if value == "" || value == "." || value == ".." || hasControlRune(value) {
		return false
	}
	return safeNamePattern.MatchString(value)
}

func hasControlRune(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
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

func cleanupPendingImage(tmp, imageDir string) error {
	removeTmpErr := os.Remove(tmp)
	removeDirErr := removeDirIfEmpty(imageDir)
	return errors.Join(removeTmpErr, removeDirErr)
}

func cleanupCommittedTemp(path string) error {
	if err := removeCommittedTemp(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove stale committed image temp %s: %w", path, err)
	}
	return nil
}

func cleanupImageDir(path string) error {
	if err := removeDirIfEmpty(path); err != nil {
		return fmt.Errorf("remove empty image dir %s: %w", path, err)
	}
	return nil
}

func removeDirIfEmpty(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if errors.Is(err, syscall.ENOTEMPTY) {
			return nil
		}
		return err
	}
	return nil
}
