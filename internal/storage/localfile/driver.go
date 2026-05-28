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
	if _, err := d.existingImagePath(req.ImageID); err == nil {
		return nil, fmt.Errorf("%w: %s", image.ErrImageExists, req.ImageID)
	} else if !errors.Is(err, image.ErrImageNotFound) {
		return nil, err
	}
	if err := ensureTargetMissing(target); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return nil, err
	}
	tmp := target + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		cleanupErr := removeDirIfEmpty(imageDir)
		return nil, errors.Join(err, cleanupErr)
	}
	return &imageWriter{file: file, tmp: tmp, target: target}, nil
}

// Get opens committed image bytes for reading.
func (d *Driver) Get(ctx context.Context, req image.GetRequest) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := d.existingImagePath(req.ImageID)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
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
	path, err := d.existingImagePath(req.ImageID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", image.ErrImageNotFound, req.ImageID)
		}
		return err
	}
	return removeDirIfEmpty(filepath.Dir(path))
}

// GetActualUsedBytes walks the driver image root and sums regular file sizes.
func (d *Driver) GetActualUsedBytes(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
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
	mu     sync.Mutex
	file   *os.File
	tmp    string
	target string
	done   bool
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
		cleanupErr := os.Remove(w.tmp)
		return errors.Join(err, cleanupErr)
	}
	if err := ensureTargetMissing(w.target); err != nil {
		cleanupErr := os.Remove(w.tmp)
		return errors.Join(err, cleanupErr)
	}
	if err := os.Rename(w.tmp, w.target); err != nil {
		cleanupErr := os.Remove(w.tmp)
		return errors.Join(err, cleanupErr)
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
	removeErr := os.Remove(w.tmp)
	return errors.Join(closeErr, removeErr)
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
		path, _, err := d.imagePath(imageID, format)
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(path)
		if err == nil {
			if info.IsDir() || !info.Mode().IsRegular() {
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

func ensureTargetMissing(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return fmt.Errorf("%w: %s", image.ErrImageExists, path)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
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
