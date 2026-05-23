package remove

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

func TestDoRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(path, []byte("qcow2"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := New("qemu-img", nil).Path(path).Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat() error = %v, want not exist", err)
	}
}

func TestDoRequiresPath(t *testing.T) {
	err := New("qemu-img", nil).Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
}

func TestDoRejectsNonQCOW2Path(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.raw")
	if err := os.WriteFile(path, []byte("raw"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := New("qemu-img", nil).Path(path).Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat() error = %v, want file to remain", err)
	}
}

func TestDoRejectsDirectoryEvenWithQCOW2Suffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	err := New("qemu-img", nil).Path(path).Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("Stat() = (%v, %v), want directory to remain", info, err)
	}
}

func TestDoReturnsErrorForMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.qcow2")

	err := New("qemu-img", nil).Path(path).Do(context.Background())

	if err == nil {
		t.Fatal("Do() error = nil, want error")
	}
}

func TestDoDoesNotRemoveFileWhenContextCanceled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(path, []byte("qcow2"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := New("qemu-img", nil).Path(path).Do(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context canceled", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat() error = %v, want file to remain", err)
	}
}
