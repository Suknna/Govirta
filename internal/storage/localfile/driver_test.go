package localfile

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/image"
	"github.com/suknna/govirta/internal/storage/volume"
)

func TestNewDriverRejectsInvalidConfig(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		config Config
	}{
		{name: "empty pool", config: Config{StorageRoot: root}},
		{name: "dot pool", config: Config{PoolName: ".", StorageRoot: root}},
		{name: "dotdot pool", config: Config{PoolName: "..", StorageRoot: root}},
		{name: "slash pool", config: Config{PoolName: "a/b", StorageRoot: root}},
		{name: "backslash pool", config: Config{PoolName: `a\b`, StorageRoot: root}},
		{name: "control pool", config: Config{PoolName: "a\nb", StorageRoot: root}},
		{name: "empty root", config: Config{PoolName: "pool"}},
		{name: "relative root", config: Config{PoolName: "pool", StorageRoot: "relative"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewDriver(tc.config)
			if !errors.Is(err, volume.ErrInvalidRequest) {
				t.Fatalf("NewDriver() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestPutWriteCloseGetReadsSameBytes(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	w, err := driver.Put(ctx, image.PutRequest{ImageID: "cirros", Format: diskformat.FormatQCOW2, DeclaredSizeBytes: 5})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	r, err := driver.Get(ctx, image.GetRequest{ImageID: "cirros"})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Fatalf("reader Close() error = %v", err)
		}
	}()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("read bytes = %q, want hello", got)
	}
}

func TestCancelRemovesTempAndLeavesNoImage(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	w, err := driver.Put(ctx, image.PutRequest{ImageID: "cancelled", Format: diskformat.FormatRaw, DeclaredSizeBytes: 4})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	internalWriter := w.(*imageWriter)
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Cancel(); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if _, err := os.Stat(internalWriter.tmp); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temp stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(internalWriter.imageDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("image dir stat error = %v, want not exist", err)
	}
	if _, err := driver.Get(ctx, image.GetRequest{ImageID: "cancelled"}); !errors.Is(err, image.ErrImageNotFound) {
		t.Fatalf("Get() error = %v, want ErrImageNotFound", err)
	}
}

func TestPutReservesImageIDBeforeClose(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	w, err := driver.Put(ctx, image.PutRequest{ImageID: "reserved", Format: diskformat.FormatQCOW2, DeclaredSizeBytes: 4})
	if err != nil {
		t.Fatalf("Put(qcow2) error = %v", err)
	}
	internalWriter := w.(*imageWriter)

	if _, err := driver.Put(ctx, image.PutRequest{ImageID: "reserved", Format: diskformat.FormatRaw, DeclaredSizeBytes: 4}); !errors.Is(err, image.ErrImageExists) {
		t.Fatalf("Put(raw duplicate) error = %v, want ErrImageExists", err)
	}
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := os.ReadDir(internalWriter.imageDir)
	if err != nil {
		t.Fatalf("ReadDir(imageDir) error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "reserved.qcow2" {
		t.Fatalf("image dir entries = %v, want only reserved.qcow2", entryNames(entries))
	}
	if _, err := os.Stat(filepath.Join(internalWriter.imageDir, "reserved.raw")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("raw target stat error = %v, want not exist", err)
	}
}

func TestPutDuplicateImageReturnsExists(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	writeImage(t, driver, "dup", diskformat.FormatQCOW2, "one")
	_, err := driver.Put(ctx, image.PutRequest{ImageID: "dup", Format: diskformat.FormatQCOW2, DeclaredSizeBytes: 3})
	if !errors.Is(err, image.ErrImageExists) {
		t.Fatalf("Put() error = %v, want ErrImageExists", err)
	}
}

func TestPutDuplicateImageIDWithDifferentFormatReturnsExists(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	writeImage(t, driver, "dup-format", diskformat.FormatRaw, "one")
	_, err := driver.Put(ctx, image.PutRequest{ImageID: "dup-format", Format: diskformat.FormatQCOW2, DeclaredSizeBytes: 3})
	if !errors.Is(err, image.ErrImageExists) {
		t.Fatalf("Put() error = %v, want ErrImageExists", err)
	}
}

func TestDeleteRemovesImageAndEmptyDir(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	writeImage(t, driver, "delete-me", diskformat.FormatRaw, "gone")
	path, _, err := driver.imagePath("delete-me", diskformat.FormatRaw)
	if err != nil {
		t.Fatalf("imagePath() error = %v", err)
	}
	imageDir := filepath.Dir(path)

	if err := driver.Delete(ctx, image.DeleteRequest{ImageID: "delete-me"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("image stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(imageDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("image dir stat error = %v, want not exist", err)
	}
}

func TestUnsafeImageIDRejectedAndCannotEscapeStorageRoot(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	for _, imageID := range []string{"", ".", "..", "a/b", `a\b`, "a\nb"} {
		t.Run(strings.ReplaceAll(imageID, "\n", "\\n"), func(t *testing.T) {
			_, err := driver.Put(ctx, image.PutRequest{ImageID: imageID, Format: diskformat.FormatQCOW2, DeclaredSizeBytes: 1})
			if !errors.Is(err, image.ErrInvalidImage) {
				t.Fatalf("Put() error = %v, want ErrInvalidImage", err)
			}
			if _, err := driver.Get(ctx, image.GetRequest{ImageID: imageID}); !errors.Is(err, image.ErrInvalidImage) {
				t.Fatalf("Get() error = %v, want ErrInvalidImage", err)
			}
			if err := driver.Delete(ctx, image.DeleteRequest{ImageID: imageID}); !errors.Is(err, image.ErrInvalidImage) {
				t.Fatalf("Delete() error = %v, want ErrInvalidImage", err)
			}
		})
	}
	if entries, err := os.ReadDir(driver.root); err != nil {
		t.Fatalf("ReadDir(root) error = %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("storage root entries = %d, want 0", len(entries))
	}
}

func TestCanceledContextReturnsBeforeFilesystemMutation(t *testing.T) {
	driver := newTestDriver(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := driver.Put(ctx, image.PutRequest{ImageID: "ctx", Format: diskformat.FormatQCOW2, DeclaredSizeBytes: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put() error = %v, want context.Canceled", err)
	}
	if _, err := driver.Get(ctx, image.GetRequest{ImageID: "ctx"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get() error = %v, want context.Canceled", err)
	}
	if err := driver.Delete(ctx, image.DeleteRequest{ImageID: "ctx"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete() error = %v, want context.Canceled", err)
	}
	if _, err := driver.GetActualUsedBytes(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetActualUsedBytes() error = %v, want context.Canceled", err)
	}
	if entries, err := os.ReadDir(driver.root); err != nil {
		t.Fatalf("ReadDir(root) error = %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("storage root entries = %d, want 0", len(entries))
	}
}

func TestGetActualUsedBytesCountsRegularFilesAndObservesCanceledContext(t *testing.T) {
	driver := newTestDriver(t)
	writeImage(t, driver, "one", diskformat.FormatQCOW2, "1234")
	writeImage(t, driver, "two", diskformat.FormatRaw, "abcdef")
	if err := os.Symlink("missing", filepath.Join(driver.imageRoot(), "ignored-link")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	got, err := driver.GetActualUsedBytes(context.Background())
	if err != nil {
		t.Fatalf("GetActualUsedBytes() error = %v", err)
	}
	if got != 10 {
		t.Fatalf("GetActualUsedBytes() = %d, want 10", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := driver.GetActualUsedBytes(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetActualUsedBytes(canceled) error = %v, want context.Canceled", err)
	}
}

func TestCloseConflictRemovesTempAndReturnsExists(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	w, err := driver.Put(ctx, image.PutRequest{ImageID: "race", Format: diskformat.FormatQCOW2, DeclaredSizeBytes: 4})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	internalWriter := w.(*imageWriter)
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := os.WriteFile(internalWriter.target, []byte("existing"), 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	if err := w.Close(); !errors.Is(err, image.ErrImageExists) {
		t.Fatalf("Close() error = %v, want ErrImageExists", err)
	}
	if _, err := os.Stat(internalWriter.tmp); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temp stat error = %v, want not exist", err)
	}
	got, err := os.ReadFile(internalWriter.target)
	if err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	}
	if string(got) != "existing" {
		t.Fatalf("target bytes = %q, want existing", got)
	}
	if _, err := os.Stat(internalWriter.imageDir); err != nil {
		t.Fatalf("image dir stat error = %v, want existing conflict dir retained", err)
	}
}

func TestCloseFailureRemovesTempAndEmptyImageDir(t *testing.T) {
	driver := newTestDriver(t)
	w, err := driver.Put(context.Background(), image.PutRequest{ImageID: "close-fail", Format: diskformat.FormatRaw, DeclaredSizeBytes: 4})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	internalWriter := w.(*imageWriter)
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := internalWriter.file.Close(); err != nil {
		t.Fatalf("manual file Close() error = %v", err)
	}
	if err := w.Close(); err == nil {
		t.Fatal("Close() error = nil, want close failure")
	}
	if _, err := os.Stat(internalWriter.tmp); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temp stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(internalWriter.imageDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("image dir stat error = %v, want not exist", err)
	}
}

func TestClosePreservesCommittedImageWhenTempCleanupFails(t *testing.T) {
	driver := newTestDriver(t)
	w, err := driver.Put(context.Background(), image.PutRequest{ImageID: "cleanup-fail", Format: diskformat.FormatQCOW2, DeclaredSizeBytes: 4})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	internalWriter := w.(*imageWriter)
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	cleanupErr := errors.New("remove temp failed")
	originalRemoveCommittedTemp := removeCommittedTemp
	removeCommittedTemp = func(path string) error {
		if path == internalWriter.tmp {
			return cleanupErr
		}
		return originalRemoveCommittedTemp(path)
	}
	t.Cleanup(func() {
		removeCommittedTemp = originalRemoveCommittedTemp
	})

	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil after committed target link", err)
	}
	got, err := os.ReadFile(internalWriter.target)
	if err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	}
	if string(got) != "data" {
		t.Fatalf("target bytes = %q, want data", got)
	}
	if _, err := os.Stat(internalWriter.tmp); err != nil {
		t.Fatalf("temp stat error = %v, want tmp retained after cleanup failure", err)
	}

	r, err := driver.Get(context.Background(), image.GetRequest{ImageID: "cleanup-fail"})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Fatalf("reader Close() error = %v", err)
		}
	}()
	readBack, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(readBack) != "data" {
		t.Fatalf("read bytes = %q, want data", readBack)
	}
}

func TestWriterRejectsRepeatedTerminalOperationsAndWrites(t *testing.T) {
	driver := newTestDriver(t)
	w, err := driver.Put(context.Background(), image.PutRequest{ImageID: "terminal", Format: diskformat.FormatRaw, DeclaredSizeBytes: 1})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := w.Cancel(); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, image.ErrInvalidImage) {
		t.Fatalf("Write() error = %v, want ErrInvalidImage", err)
	}
	if err := w.Close(); !errors.Is(err, image.ErrInvalidImage) {
		t.Fatalf("Close() error = %v, want ErrInvalidImage", err)
	}
	if err := w.Cancel(); !errors.Is(err, image.ErrInvalidImage) {
		t.Fatalf("Cancel() error = %v, want ErrInvalidImage", err)
	}
}

func newTestDriver(t *testing.T) *Driver {
	t.Helper()
	driver, err := NewDriver(Config{PoolName: "pool-a", StorageRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("NewDriver() error = %v", err)
	}
	return driver
}

func writeImage(t *testing.T, driver *Driver, imageID string, format diskformat.Format, data string) {
	t.Helper()
	w, err := driver.Put(context.Background(), image.PutRequest{ImageID: imageID, Format: format, DeclaredSizeBytes: int64(len(data))})
	if err != nil {
		t.Fatalf("Put(%q) error = %v", imageID, err)
	}
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatalf("Write(%q) error = %v", imageID, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close(%q) error = %v", imageID, err)
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
