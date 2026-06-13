package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

var safeCacheSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var (
	errImageCacheChecksumMismatch = errors.New("image cache checksum mismatch")
	errImageCacheContentConflict  = errors.New("image cache content conflict")
	errImageCacheInvalidInput     = errors.New("image cache invalid input")
)

// ImageCache stores node-local image bytes under one explicit safe root.
type ImageCache struct {
	root string
	mu   sync.Mutex
}

// NewImageCache constructs a node-local image cache rooted at cacheRoot.
func NewImageCache(cacheRoot string) (*ImageCache, error) {
	if cacheRoot == "" {
		return nil, fmt.Errorf("image cache: cache root is required")
	}
	if !filepath.IsAbs(cacheRoot) {
		return nil, fmt.Errorf("image cache: cache root must be absolute: %q", cacheRoot)
	}
	root := filepath.Clean(cacheRoot)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("image cache: create root %q: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("image cache: resolve root %q: %w", root, err)
	}
	return &ImageCache{root: resolved}, nil
}

// Root returns the resolved cache root used by the service.
func (c *ImageCache) Root() string {
	return c.root
}

// Cache streams source into the deterministic cache path and verifies size/hash.
func (c *ImageCache) Cache(ctx context.Context, nodeName string, input taskv1.CacheImageInput, source io.Reader) (taskv1.CacheImageObserved, error) {
	if err := input.Validate(); err != nil {
		return taskv1.CacheImageObserved{}, err
	}
	if err := ctx.Err(); err != nil {
		return taskv1.CacheImageObserved{}, fmt.Errorf("image cache: context done before cache: %w", err)
	}
	if source == nil {
		return taskv1.CacheImageObserved{}, fmt.Errorf("image cache: source reader is required")
	}

	versionDir, imagePath, err := c.cachePaths(input.ImageName, input.Version)
	if err != nil {
		return taskv1.CacheImageObserved{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.rejectExistingAncestorSymlink(versionDir); err != nil {
		return taskv1.CacheImageObserved{}, err
	}
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		return taskv1.CacheImageObserved{}, fmt.Errorf("image cache: create version dir %q: %w", versionDir, err)
	}
	if err := c.rejectSymlinkEscape(versionDir); err != nil {
		return taskv1.CacheImageObserved{}, err
	}

	if existing, err := c.observedExisting(imagePath); err == nil {
		if existing.SizeBytes == input.DeclaredSizeBytes && existing.SHA256 == input.SHA256 {
			return taskv1.CacheImageObserved{NodeName: nodeName, ImageName: input.ImageName, Version: input.Version, Format: input.Format, CachedPath: imagePath, SizeBytes: existing.SizeBytes, SHA256: existing.SHA256}, nil
		}
		return taskv1.CacheImageObserved{}, fmt.Errorf("%w: existing cached image %q has size %d sha256 %s, requested size %d sha256 %s", errImageCacheContentConflict, imagePath, existing.SizeBytes, existing.SHA256, input.DeclaredSizeBytes, input.SHA256)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return taskv1.CacheImageObserved{}, err
	}

	tmp, err := os.CreateTemp(versionDir, ".image-*.tmp")
	if err != nil {
		return taskv1.CacheImageObserved{}, fmt.Errorf("image cache: create temp image in %q: %w", versionDir, err)
	}
	tmpPath := tmp.Name()
	commit := false
	defer func() {
		if !commit {
			_ = os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	limited := &contextReader{ctx: ctx, reader: source}
	size, copyErr := io.Copy(io.MultiWriter(tmp, h), limited)
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		return taskv1.CacheImageObserved{}, fmt.Errorf("image cache: write temp image: %w", errors.Join(copyErr, closeErr))
	}
	actualSHA := hex.EncodeToString(h.Sum(nil))
	if size != input.DeclaredSizeBytes {
		return taskv1.CacheImageObserved{}, fmt.Errorf("%w: size mismatch for %q: got %d want %d", errImageCacheChecksumMismatch, input.ImageName, size, input.DeclaredSizeBytes)
	}
	if actualSHA != input.SHA256 {
		return taskv1.CacheImageObserved{}, fmt.Errorf("%w: sha256 mismatch for %q: got %s want %s", errImageCacheChecksumMismatch, input.ImageName, actualSHA, input.SHA256)
	}
	if err := c.rejectSymlinkEscape(versionDir); err != nil {
		return taskv1.CacheImageObserved{}, err
	}
	if err := os.Link(tmpPath, imagePath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			observed, obsErr := c.observedExisting(imagePath)
			if obsErr != nil {
				return taskv1.CacheImageObserved{}, obsErr
			}
			if observed.SizeBytes == input.DeclaredSizeBytes && observed.SHA256 == input.SHA256 {
				return taskv1.CacheImageObserved{NodeName: nodeName, ImageName: input.ImageName, Version: input.Version, Format: input.Format, CachedPath: imagePath, SizeBytes: observed.SizeBytes, SHA256: observed.SHA256}, nil
			}
		}
		return taskv1.CacheImageObserved{}, fmt.Errorf("image cache: commit image %q: %w", imagePath, err)
	}
	if err := os.Remove(tmpPath); err != nil {
		return taskv1.CacheImageObserved{}, fmt.Errorf("image cache: remove committed temp image %q: %w", tmpPath, err)
	}
	commit = true
	return taskv1.CacheImageObserved{NodeName: nodeName, ImageName: input.ImageName, Version: input.Version, Format: input.Format, CachedPath: imagePath, SizeBytes: size, SHA256: actualSHA}, nil
}

// Delete removes the matching cached image. Missing targets are idempotent success.
func (c *ImageCache) Delete(ctx context.Context, nodeName string, input taskv1.DeleteCachedImageInput) (taskv1.DeleteCachedImageObserved, error) {
	if err := input.Validate(); err != nil {
		return taskv1.DeleteCachedImageObserved{}, err
	}
	if err := ctx.Err(); err != nil {
		return taskv1.DeleteCachedImageObserved{}, fmt.Errorf("image cache: context done before delete: %w", err)
	}
	_, imagePath, err := c.cachePaths(input.ImageName, input.Version)
	if err != nil {
		return taskv1.DeleteCachedImageObserved{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if observed, err := c.observedExisting(imagePath); err == nil {
		if observed.SHA256 != input.SHA256 {
			return taskv1.DeleteCachedImageObserved{}, fmt.Errorf("image cache: refusing to delete %q: sha256 %s does not match requested %s", imagePath, observed.SHA256, input.SHA256)
		}
		if err := os.Remove(imagePath); err != nil {
			return taskv1.DeleteCachedImageObserved{}, fmt.Errorf("image cache: remove image %q: %w", imagePath, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return taskv1.DeleteCachedImageObserved{}, err
	}
	return taskv1.DeleteCachedImageObserved{NodeName: nodeName, ImageName: input.ImageName, Version: input.Version, Deleted: true}, nil
}

func (c *ImageCache) cachePaths(imageName, version string) (string, string, error) {
	if err := validateCacheSegment("imageName", imageName); err != nil {
		return "", "", err
	}
	if err := validateCacheSegment("version", version); err != nil {
		return "", "", err
	}
	versionDir := filepath.Join(c.root, imageName, version)
	if !pathWithin(c.root, versionDir) {
		return "", "", fmt.Errorf("image cache: computed path escapes cache root")
	}
	return versionDir, filepath.Join(versionDir, "image"), nil
}

func (c *ImageCache) observedExisting(path string) (struct {
	SizeBytes int64
	SHA256    string
}, error) {
	var observed struct {
		SizeBytes int64
		SHA256    string
	}
	if err := c.rejectSymlinkEscape(path); err != nil {
		return observed, err
	}
	f, err := os.Open(path)
	if err != nil {
		return observed, err
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	closeErr := f.Close()
	if err != nil {
		return observed, fmt.Errorf("image cache: hash existing image %q: %w", path, err)
	}
	if closeErr != nil {
		return observed, fmt.Errorf("image cache: close existing image %q: %w", path, closeErr)
	}
	observed.SizeBytes = size
	observed.SHA256 = hex.EncodeToString(h.Sum(nil))
	return observed, nil
}

func (c *ImageCache) rejectSymlinkEscape(path string) error {
	clean := filepath.Clean(path)
	if !pathWithin(c.root, clean) {
		return fmt.Errorf("image cache: path %q escapes cache root %q", clean, c.root)
	}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		if !pathWithin(c.root, resolved) {
			return fmt.Errorf("image cache: path %q resolves outside cache root", clean)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("image cache: resolve path %q: %w", clean, err)
	}
	return nil
}

func (c *ImageCache) rejectExistingAncestorSymlink(path string) error {
	clean := filepath.Clean(path)
	if !pathWithin(c.root, clean) {
		return fmt.Errorf("%w: path %q escapes cache root %q", errImageCacheInvalidInput, clean, c.root)
	}
	rel, err := filepath.Rel(c.root, clean)
	if err != nil {
		return fmt.Errorf("%w: compute relative path for %q: %v", errImageCacheInvalidInput, clean, err)
	}
	current := c.root
	if rel == "." {
		return nil
	}
	for _, segment := range splitPath(rel) {
		current = filepath.Join(current, segment)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("image cache: inspect ancestor %q: %w", current, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: ancestor %q is a symlink", errImageCacheInvalidInput, current)
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: ancestor %q is not a directory", errImageCacheInvalidInput, current)
		}
	}
	return nil
}

func validateCacheSegment(name, value string) error {
	if !safeCacheSegmentPattern.MatchString(value) || value == "." || value == ".." {
		return fmt.Errorf("%w: unsafe %s segment %q", errImageCacheInvalidInput, name, value)
	}
	return nil
}

func splitPath(path string) []string {
	var segments []string
	for _, segment := range strings.Split(path, string(os.PathSeparator)) {
		if segment != "" && segment != "." {
			segments = append(segments, segment)
		}
	}
	return segments
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == ".." || rel == "."+string(os.PathSeparator)+".." || len(rel) >= 3 && rel[:3] == ".."+string(os.PathSeparator) {
		return false
	}
	return true
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err != nil {
		return n, err
	}
	if cerr := r.ctx.Err(); cerr != nil {
		return n, cerr
	}
	return n, nil
}
