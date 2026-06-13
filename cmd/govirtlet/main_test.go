package main

import (
	"path/filepath"
	"testing"
)

func TestParseConfigDerivesImageCacheRootAsRuntimeRootSibling(t *testing.T) {
	parent := t.TempDir()
	runtimeRoot := filepath.Join(parent, "vms")
	cfg, err := parseConfig([]string{
		"--master-url", "http://127.0.0.1:8080",
		"--node-name", "node-1",
		"--runtime-root", runtimeRoot,
		"--owner-uid", "1000",
		"--owner-gid", "1000",
		"--qemu-binary", "/usr/bin/qemu-system-x86_64",
	})
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if cfg.ImageCacheRoot != filepath.Join(parent, "image-cache") {
		t.Fatalf("ImageCacheRoot = %q", cfg.ImageCacheRoot)
	}
}

func TestParseConfigAcceptsExplicitImageCacheRoot(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "cache")
	cfg, err := parseConfig([]string{
		"--master-url", "http://127.0.0.1:8080",
		"--node-name", "node-1",
		"--runtime-root", filepath.Join(t.TempDir(), "runtime"),
		"--image-cache-root", explicit,
		"--owner-uid", "1000",
		"--owner-gid", "1000",
		"--qemu-binary", "/usr/bin/qemu-system-x86_64",
	})
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if cfg.ImageCacheRoot != explicit {
		t.Fatalf("ImageCacheRoot = %q", cfg.ImageCacheRoot)
	}
}

func TestParseConfigDoesNotRequireImageSourceRoot(t *testing.T) {
	_, err := parseConfig([]string{
		"--master-url", "http://127.0.0.1:8080",
		"--node-name", "node-1",
		"--runtime-root", filepath.Join(t.TempDir(), "runtime"),
		"--owner-uid", "1000",
		"--owner-gid", "1000",
		"--qemu-binary", "/usr/bin/qemu-system-x86_64",
	})
	if err != nil {
		t.Fatalf("parseConfig() error = %v, want nil without --image-source-root", err)
	}
}
