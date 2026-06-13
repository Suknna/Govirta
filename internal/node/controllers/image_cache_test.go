package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

func TestImageCacheStoresBytesAtomicallyAndIdempotently(t *testing.T) {
	cache := newTestImageCache(t)
	data := []byte("qcow2-bytes")
	input := cacheInput(t, cache.Root(), "ubuntu", "v1", data)

	observed, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("Cache() error = %v", err)
	}
	if observed.CachedPath != filepath.Join(cache.Root(), "ubuntu", "v1", "image") {
		t.Fatalf("CachedPath = %q", observed.CachedPath)
	}
	stored, err := os.ReadFile(observed.CachedPath)
	if err != nil {
		t.Fatalf("read cached image: %v", err)
	}
	if string(stored) != string(data) || observed.SizeBytes != int64(len(data)) || observed.SHA256 != input.SHA256 {
		t.Fatalf("observed/stored mismatch: observed=%+v stored=%q", observed, stored)
	}
	if _, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader(string(data))); err != nil {
		t.Fatalf("idempotent Cache() error = %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(cache.Root(), "ubuntu", "v1", ".image-*.tmp"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp leftovers = %v", leftovers)
	}
}

func TestImageCacheRejectsBadChecksumAndUnsafeSegments(t *testing.T) {
	cache := newTestImageCache(t)
	data := []byte("bytes")
	input := cacheInput(t, cache.Root(), "ubuntu", "v1", data)
	input.SHA256 = strings.Repeat("0", 64)
	if _, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader(string(data))); err == nil {
		t.Fatalf("Cache() expected checksum error")
	}
	if _, err := os.Stat(filepath.Join(cache.Root(), "ubuntu", "v1", "image")); !os.IsNotExist(err) {
		t.Fatalf("cached image exists after checksum failure: %v", err)
	}

	input = cacheInput(t, cache.Root(), "../escape", "v1", data)
	if _, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader(string(data))); err == nil {
		t.Fatalf("Cache() expected unsafe segment error")
	}
}

func TestImageCacheRejectsSameVersionDifferentSHA(t *testing.T) {
	cache := newTestImageCache(t)
	first := []byte("first-bytes")
	if _, err := cache.Cache(context.Background(), "node-1", cacheInput(t, cache.Root(), "ubuntu", "v1", first), strings.NewReader(string(first))); err != nil {
		t.Fatalf("initial Cache() error = %v", err)
	}
	second := []byte("second-bytes")
	if _, err := cache.Cache(context.Background(), "node-1", cacheInput(t, cache.Root(), "ubuntu", "v1", second), strings.NewReader(string(second))); err == nil {
		t.Fatalf("Cache() expected conflict for same image/version with different SHA")
	}
}

func TestImageCacheRejectsExistingSameSHADifferentDeclaredSize(t *testing.T) {
	cache := newTestImageCache(t)
	data := []byte("same-bytes")
	input := cacheInput(t, cache.Root(), "ubuntu", "v1", data)
	if _, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader(string(data))); err != nil {
		t.Fatalf("initial Cache() error = %v", err)
	}
	input.DeclaredSizeBytes++
	if _, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader(string(data))); err == nil {
		t.Fatalf("Cache() expected conflict for same SHA but different declared size")
	}
}

func TestImageCacheRejectsDeclaredSizeMismatch(t *testing.T) {
	cache := newTestImageCache(t)
	data := []byte("bytes")
	input := cacheInput(t, cache.Root(), "ubuntu", "v1", data)
	input.DeclaredSizeBytes++
	if _, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader(string(data))); err == nil {
		t.Fatalf("Cache() expected declared size mismatch error")
	}
	if _, err := os.Stat(filepath.Join(cache.Root(), "ubuntu", "v1", "image")); !os.IsNotExist(err) {
		t.Fatalf("cached image exists after size mismatch: %v", err)
	}
}

func TestImageCacheRejectsSymlinkEscape(t *testing.T) {
	cache := newTestImageCache(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache.Root(), "ubuntu"), 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(cache.Root(), "ubuntu", "v1")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	input := cacheInput(t, cache.Root(), "ubuntu", "v1", []byte("bytes"))
	if _, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader("bytes")); err == nil {
		t.Fatalf("Cache() expected symlink escape error")
	}
}

func TestImageCacheRejectsIntermediateSymlinkBeforeMkdirAll(t *testing.T) {
	cache := newTestImageCache(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cache.Root(), "ubuntu")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	input := cacheInput(t, cache.Root(), "ubuntu", "v1", []byte("bytes"))
	if _, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader("bytes")); err == nil {
		t.Fatalf("Cache() expected intermediate symlink error")
	}
	if _, err := os.Stat(filepath.Join(outside, "v1")); !os.IsNotExist(err) {
		t.Fatalf("MkdirAll created outside dir through symlink: %v", err)
	}
}

func TestImageCacheRejectsIntermediateFileBeforeMkdirAll(t *testing.T) {
	cache := newTestImageCache(t)
	imagePath := filepath.Join(cache.Root(), "ubuntu")
	if err := os.WriteFile(imagePath, []byte("not-a-directory"), 0o644); err != nil {
		t.Fatalf("write intermediate file: %v", err)
	}
	input := cacheInput(t, cache.Root(), "ubuntu", "v1", []byte("bytes"))
	if _, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader("bytes")); err == nil {
		t.Fatalf("Cache() expected intermediate file error")
	}
	info, err := os.Stat(imagePath)
	if err != nil {
		t.Fatalf("stat intermediate file: %v", err)
	}
	if info.IsDir() {
		t.Fatalf("intermediate path became directory, want original file preserved")
	}
	if _, err := os.Stat(filepath.Join(imagePath, "v1")); err == nil {
		t.Fatalf("MkdirAll created child under intermediate file")
	}
}

func TestImageCacheDeleteIsIdempotentAndSHAGuarded(t *testing.T) {
	cache := newTestImageCache(t)
	data := []byte("bytes")
	input := cacheInput(t, cache.Root(), "ubuntu", "v1", data)
	if _, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader(string(data))); err != nil {
		t.Fatalf("Cache() error = %v", err)
	}
	badDelete := taskv1.DeleteCachedImageInput{ImageName: input.ImageName, ImageUID: input.ImageUID, Version: input.Version, CacheRoot: cache.Root(), SHA256: strings.Repeat("0", 64)}
	if _, err := cache.Delete(context.Background(), "node-1", badDelete); err == nil {
		t.Fatalf("Delete() expected sha guard error")
	}
	deleteInput := taskv1.DeleteCachedImageInput{ImageName: input.ImageName, ImageUID: input.ImageUID, Version: input.Version, CacheRoot: cache.Root(), SHA256: input.SHA256}
	observed, err := cache.Delete(context.Background(), "node-1", deleteInput)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !observed.Deleted {
		t.Fatalf("Deleted = false")
	}
	if _, err := cache.Delete(context.Background(), "node-1", deleteInput); err != nil {
		t.Fatalf("idempotent Delete() error = %v", err)
	}
}

func newTestImageCache(t *testing.T) *ImageCache {
	t.Helper()
	cache, err := NewImageCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewImageCache(): %v", err)
	}
	return cache
}

func cacheInput(t *testing.T, root, imageName, version string, data []byte) taskv1.CacheImageInput {
	t.Helper()
	sum := sha256.Sum256(data)
	return taskv1.CacheImageInput{
		ImageName:         imageName,
		ImageUID:          "uid-" + imageName,
		Version:           version,
		Format:            "qcow2",
		Source:            taskv1.ImageTaskSource{Type: taskv1.ImageTaskSourceHTTP, Location: "http://example.invalid/image"},
		DeclaredSizeBytes: int64(len(data)),
		SHA256:            hex.EncodeToString(sum[:]),
		CacheRoot:         root,
	}
}
