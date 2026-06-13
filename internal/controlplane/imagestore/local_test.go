package imagestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLocalStorePutCommitsAtomically(t *testing.T) {
	store := newTestStore(t)
	content := "image-bytes"

	ref, err := store.Put(context.Background(), putRequest("ubuntu", "24.04", content))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if ref.Name != "ubuntu" || ref.Version != "24.04" || ref.Format != "qcow2" || ref.SizeBytes != int64(len(content)) || ref.SHA256 != sha256Hex(content) {
		t.Fatalf("Put() ref = %#v", ref)
	}
	if _, err := os.Stat(ref.Path); err != nil {
		t.Fatalf("committed image missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(ref.Path), metadataFileName)); err != nil {
		t.Fatalf("metadata missing: %v", err)
	}
	assertNoUploadTemps(t, filepath.Dir(ref.Path))

	got, err := store.Get(context.Background(), "ubuntu", "24.04")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != ref {
		t.Fatalf("Get() = %#v, want %#v", got, ref)
	}
	reader, openRef, err := store.Open(context.Background(), "ubuntu", "24.04")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(data) != content || openRef != ref {
		t.Fatalf("Open() data/ref = %q %#v", data, openRef)
	}
}

func TestLocalStoreRejectsInvalidRoots(t *testing.T) {
	target := t.TempDir()
	symlinkRoot := filepath.Join(t.TempDir(), "store-link")
	if err := os.Symlink(target, symlinkRoot); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	tests := []struct {
		name    string
		root    string
		wantErr error
	}{
		{name: "empty", root: "", wantErr: ErrInvalidRequest},
		{name: "relative", root: "relative-store", wantErr: ErrInvalidRequest},
		{name: "symlink root", root: symlinkRoot, wantErr: ErrUnsafePath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewLocal(tt.root)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewLocal() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestLocalStorePutRejectsChecksumMismatch(t *testing.T) {
	store := newTestStore(t)
	req := putRequest("ubuntu", "24.04", "image-bytes")
	req.SHA256 = strings.Repeat("0", sha256.Size*2)

	_, err := store.Put(context.Background(), req)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Put() error = %v, want ErrChecksumMismatch", err)
	}
	objectDir := filepath.Join(store.root, "images", req.Name, req.Version)
	if _, err := os.Stat(filepath.Join(objectDir, imageFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("image path stat error = %v, want not exist", err)
	}
	assertNoUploadTemps(t, objectDir)
}

func TestLocalStorePutIdempotentForSameContent(t *testing.T) {
	store := newTestStore(t)
	req := putRequest("ubuntu", "24.04", "image-bytes")

	first, err := store.Put(context.Background(), req)
	if err != nil {
		t.Fatalf("first Put() error = %v", err)
	}
	if err := os.WriteFile(first.Path+".should-not-appear", []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	second, err := store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-bytes"))
	if err != nil {
		t.Fatalf("second Put() error = %v", err)
	}
	if second != first {
		t.Fatalf("second ref = %#v, want %#v", second, first)
	}
}

func TestLocalStorePutRejectsSameVersionDifferentHash(t *testing.T) {
	store := newTestStore(t)
	first, err := store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-one"))
	if err != nil {
		t.Fatalf("first Put() error = %v", err)
	}

	_, err = store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-two"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second Put() error = %v, want ErrConflict", err)
	}
	data, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "image-one" {
		t.Fatalf("committed image was overwritten: %q", data)
	}
}

func TestLocalStorePutConcurrentDifferentContentConflicts(t *testing.T) {
	store := newTestStore(t)
	start := make(chan struct{})
	contents := []string{"image-one", "image-two"}
	errs := make([]error, len(contents))
	refs := make([]ObjectRef, len(contents))

	var wg sync.WaitGroup
	for i, content := range contents {
		wg.Add(1)
		go func(index int, upload string) {
			defer wg.Done()
			<-start
			refs[index], errs[index] = store.Put(context.Background(), putRequest("ubuntu", "24.04", upload))
		}(i, content)
	}
	close(start)
	wg.Wait()

	successes := 0
	conflicts := 0
	var successRef ObjectRef
	for i, err := range errs {
		if err == nil {
			successes++
			successRef = refs[i]
			continue
		}
		if errors.Is(err, ErrConflict) {
			conflicts++
			continue
		}
		t.Fatalf("Put() error[%d] = %v, want nil or ErrConflict", i, err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes/conflicts = %d/%d, want 1/1; errs=%v", successes, conflicts, errs)
	}

	stored, err := store.Get(context.Background(), "ubuntu", "24.04")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored != successRef {
		t.Fatalf("stored ref = %#v, want successful ref %#v", stored, successRef)
	}
	data, err := os.ReadFile(stored.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if sha256Hex(string(data)) != stored.SHA256 || int64(len(data)) != stored.SizeBytes {
		t.Fatalf("stored data does not match metadata: len=%d sha=%s ref=%#v", len(data), sha256Hex(string(data)), stored)
	}
}

func TestLocalStorePutRecoversMissingMetadataForMatchingImage(t *testing.T) {
	store := newTestStore(t)
	ref, err := store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-bytes"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	metadataPath := filepath.Join(filepath.Dir(ref.Path), metadataFileName)
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("Remove() metadata error = %v", err)
	}

	recovered, err := store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-bytes"))
	if err != nil {
		t.Fatalf("recover Put() error = %v", err)
	}
	if recovered != ref {
		t.Fatalf("recovered ref = %#v, want %#v", recovered, ref)
	}
	if _, err := os.Stat(metadataPath); err != nil {
		t.Fatalf("metadata was not restored: %v", err)
	}
}

func TestLocalStorePutRejectsMissingMetadataForSameBytesDifferentFormat(t *testing.T) {
	store := newTestStore(t)
	ref, err := store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-bytes"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	metadataPath := filepath.Join(filepath.Dir(ref.Path), metadataFileName)
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("Remove() metadata error = %v", err)
	}
	req := putRequest("ubuntu", "24.04", "image-bytes")
	req.Format = "raw"

	_, err = store.Put(context.Background(), req)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Put() error = %v, want ErrConflict", err)
	}
}

func TestLocalStorePutRejectsMissingMetadataWithoutRecoverySidecar(t *testing.T) {
	store := newTestStore(t)
	ref, err := store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-bytes"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	objectDir := filepath.Dir(ref.Path)
	if err := os.Remove(filepath.Join(objectDir, metadataFileName)); err != nil {
		t.Fatalf("Remove() metadata error = %v", err)
	}
	if err := os.Remove(filepath.Join(objectDir, recoveryMetadataFileName)); err != nil {
		t.Fatalf("Remove() recovery metadata error = %v", err)
	}

	_, err = store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-bytes"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Put() error = %v, want ErrConflict", err)
	}
}

func TestLocalStorePutRejectsMissingMetadataForDifferentImage(t *testing.T) {
	store := newTestStore(t)
	ref, err := store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-one"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	metadataPath := filepath.Join(filepath.Dir(ref.Path), metadataFileName)
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("Remove() metadata error = %v", err)
	}

	_, err = store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-two"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Put() error = %v, want ErrConflict", err)
	}
	data, err := os.ReadFile(ref.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "image-one" {
		t.Fatalf("committed image was overwritten: %q", data)
	}
}

func TestLocalStoreRejectsUnsafeNames(t *testing.T) {
	store := newTestStore(t)
	unsafeValues := []string{"", ".", "..", "../escape", "escape/name", `escape\name`, "space name"}
	for _, value := range unsafeValues {
		t.Run(value, func(t *testing.T) {
			req := putRequest("ubuntu", "24.04", "image-bytes")
			req.Name = value
			_, err := store.Put(context.Background(), req)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Put() error = %v, want ErrInvalidRequest", err)
			}
			if _, err := store.Get(context.Background(), value, "24.04"); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Get() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestLocalStoreDeleteRequiresMatchingSHA256(t *testing.T) {
	store := newTestStore(t)
	ref, err := store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-bytes"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	if err := store.Delete(context.Background(), "ubuntu", "24.04", strings.Repeat("0", sha256.Size*2)); !errors.Is(err, ErrConflict) {
		t.Fatalf("Delete() mismatch error = %v, want ErrConflict", err)
	}
	if _, err := os.Stat(ref.Path); err != nil {
		t.Fatalf("image missing after rejected delete: %v", err)
	}
	if err := store.Delete(context.Background(), "ubuntu", "24.04", ref.SHA256); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(context.Background(), "ubuntu", "24.04"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(context.Background(), "ubuntu", "24.04", ref.SHA256); err != nil {
		t.Fatalf("second Delete() error = %v", err)
	}
}

func TestLocalStoreOpenRejectsSymlinkImage(t *testing.T) {
	store := newTestStore(t)
	ref, err := store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-bytes"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Remove(ref.Path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if err := os.Symlink(outside, ref.Path); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	reader, _, err := store.Open(context.Background(), "ubuntu", "24.04")
	if reader != nil {
		_ = reader.Close()
	}
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Open() error = %v, want ErrUnsafePath", err)
	}
}

func TestLocalStoreOpenReturnsNotFoundForMissingImageFile(t *testing.T) {
	store := newTestStore(t)
	ref, err := store.Put(context.Background(), putRequest("ubuntu", "24.04", "image-bytes"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := os.Remove(ref.Path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	reader, _, err := store.Open(context.Background(), "ubuntu", "24.04")
	if reader != nil {
		_ = reader.Close()
		t.Fatalf("Open() reader is non-nil for missing image file")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open() error = %v, want ErrNotFound", err)
	}
}

func TestLocalStoreGetRejectsAncestorSymlinkEscape(t *testing.T) {
	store := newTestStore(t)
	createNameSymlinkEscape(t, store, "ubuntu")

	_, err := store.Get(context.Background(), "ubuntu", "24.04")
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Get() error = %v, want ErrUnsafePath", err)
	}
}

func TestLocalStoreOpenRejectsAncestorSymlinkEscape(t *testing.T) {
	store := newTestStore(t)
	createNameSymlinkEscape(t, store, "ubuntu")

	reader, _, err := store.Open(context.Background(), "ubuntu", "24.04")
	if reader != nil {
		_ = reader.Close()
	}
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Open() error = %v, want ErrUnsafePath", err)
	}
}

func TestLocalStoreDeleteRejectsAncestorSymlinkEscape(t *testing.T) {
	store := newTestStore(t)
	outside := createNameSymlinkEscape(t, store, "ubuntu")

	err := store.Delete(context.Background(), "ubuntu", "24.04", strings.Repeat("0", sha256.Size*2))
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Delete() error = %v, want ErrUnsafePath", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside symlink target was removed or changed: %v", err)
	}
}

func newTestStore(t *testing.T) *LocalStore {
	t.Helper()
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal() error = %v", err)
	}
	return store
}

func putRequest(name, version, content string) PutRequest {
	return PutRequest{
		Name:              name,
		Version:           version,
		Format:            "qcow2",
		DeclaredSizeBytes: int64(len(content)),
		SHA256:            sha256Hex(content),
		Reader:            strings.NewReader(content),
	}
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func createNameSymlinkEscape(t *testing.T, store *LocalStore, name string) string {
	t.Helper()
	imagesRoot := filepath.Join(store.root, "images")
	if err := os.MkdirAll(imagesRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(imagesRoot, name)); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	return outside
}

func assertNoUploadTemps(t *testing.T, objectDir string) {
	t.Helper()
	entries, err := os.ReadDir(objectDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "upload.tmp.") {
			t.Fatalf("temporary upload left behind: %s", entry.Name())
		}
	}
}
