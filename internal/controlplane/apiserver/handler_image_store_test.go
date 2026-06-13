package apiserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/suknna/govirta/internal/controlplane/imagestore"
	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

func TestUploadImageStoresBytesAndCreatesImageMetadata(t *testing.T) {
	srv, st := newImageStoreTestServer(t)
	body := []byte("qcow2-bytes")
	digest := sha256Hex(body)
	rec := doUploadImage(t, srv, "img-upload", "uid-img-upload", "v1", "qcow2", int64(len(body)), digest, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var got imagev1.Image
	if err := json.Unmarshal(storedRaw(t, st, metav1.KindImage, "img-upload").Value, &got); err != nil {
		t.Fatalf("decode stored Image: %v", err)
	}
	if got.Spec.Source.Type != imagev1.ImageSourceUpload {
		t.Fatalf("source.type = %q, want upload", got.Spec.Source.Type)
	}
	if got.UID != "uid-img-upload" {
		t.Fatalf("metadata.uid = %q, want explicit uid", got.UID)
	}
	wantLocation := "http://images.example/apis/Image/img-upload/store/v1"
	if got.Spec.Source.Location != wantLocation {
		t.Fatalf("source.location = %q, want %q", got.Spec.Source.Location, wantLocation)
	}
	if got.Spec.Version != "v1" || got.Spec.Format != imagev1.ImageFormatQCOW2 || got.Spec.DeclaredSizeBytes != int64(len(body)) || got.Spec.SHA256 != digest {
		t.Fatalf("stored spec = %#v, want uploaded identity", got.Spec)
	}
	if got.Status.Phase != imagev1.ImagePhasePending {
		t.Fatalf("status.phase = %q, want pending", got.Status.Phase)
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != metav1.FinalizerImageCache {
		t.Fatalf("finalizers = %v, want image-cache", got.Finalizers)
	}
}

func TestUploadImageRejectsInvalidFormatBeforeMetadataWrite(t *testing.T) {
	srv, st := newImageStoreTestServer(t)
	body := []byte("qcow2-bytes")
	digest := sha256Hex(body)
	rec := doUploadImage(t, srv, "img-bad-format", "uid-img-bad-format", "v1", "bad", int64(len(body)), digest, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	_, err := st.Get(context.Background(), storeKey(metav1.KindImage, "img-bad-format"))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stored Image metadata error = %v, want ErrNotFound", err)
	}
	_, err = srv.imageStore.Get(context.Background(), "img-bad-format", "v1")
	if !errors.Is(err, imagestore.ErrNotFound) {
		t.Fatalf("imageStore.Get error = %v, want imagestore.ErrNotFound", err)
	}
}

func TestUploadImageMissingUIDRejected(t *testing.T) {
	srv, st := newImageStoreTestServer(t)
	body := []byte("missing-uid")
	digest := sha256Hex(body)
	rec := doUploadImageWithoutUID(t, srv, "img-missing-uid", "v1", "qcow2", int64(len(body)), digest, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	_, err := st.Get(context.Background(), storeKey(metav1.KindImage, "img-missing-uid"))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stored Image metadata error = %v, want ErrNotFound", err)
	}
}

func TestDownloadImageStreamsStoredBytes(t *testing.T) {
	srv, _ := newImageStoreTestServer(t)
	body := []byte("download-me")
	digest := sha256Hex(body)
	if rec := doUploadImage(t, srv, "img-download", "uid-img-download", "v1", "raw", int64(len(body)), digest, body); rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/apis/Image/img-download/store/v1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("download body = %q, want %q", rec.Body.Bytes(), body)
	}
	if rec.Header().Get("X-Image-SHA256") != digest {
		t.Fatalf("X-Image-SHA256 = %q, want %q", rec.Header().Get("X-Image-SHA256"), digest)
	}
}

func TestUploadImageMetadataFailureLeavesOrphanBytesInaccessible(t *testing.T) {
	base := fake.New()
	t.Cleanup(func() {
		if err := base.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	imageStore, err := imagestore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("new local image store: %v", err)
	}
	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x0000ff)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	srv := NewServer(ServerConfig{
		Store:               failImagePutStore{Store: base, failName: "img-orphan-fail"},
		MACAllocator:        mac.NewAllocator(pool, base),
		Scheduler:           scheduler.NewNoopScheduler(),
		NodeNames:           []string{"node-1"},
		ImageStore:          imageStore,
		ImageStorePublicURL: "http://images.example",
	})
	body := []byte("orphan-me")
	digest := sha256Hex(body)
	rec := doUploadImage(t, srv, "img-orphan-fail", "uid-img-orphan-fail", "v1", "qcow2", int64(len(body)), digest, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	if _, err = imageStore.Get(context.Background(), "img-orphan-fail", "v1"); err != nil {
		t.Fatalf("imageStore.Get error = %v, want orphan bytes retained", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/apis/Image/img-orphan-fail/store/v1", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK || bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("download status/body = %d/%q, want orphan bytes inaccessible", rec.Code, rec.Body.Bytes())
	}
}

func TestUploadImageMetadataFailureDoesNotDeleteExistingIdempotentBytes(t *testing.T) {
	base := fake.New()
	t.Cleanup(func() {
		if err := base.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	imageStore, err := imagestore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("new local image store: %v", err)
	}
	body := []byte("existing-bytes")
	digest := sha256Hex(body)
	seedRef, err := imageStore.Put(context.Background(), imagestore.PutRequest{Name: "img-existing", Version: "v1", Format: "qcow2", DeclaredSizeBytes: int64(len(body)), SHA256: digest, Reader: bytes.NewReader(body)})
	if err != nil {
		t.Fatalf("seed image store object: %v", err)
	}
	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x0000ff)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	srv := NewServer(ServerConfig{
		Store:               failImagePutStore{Store: base, failName: "img-existing"},
		MACAllocator:        mac.NewAllocator(pool, base),
		Scheduler:           scheduler.NewNoopScheduler(),
		NodeNames:           []string{"node-1"},
		ImageStore:          imageStore,
		ImageStorePublicURL: "http://images.example",
	})
	rec := doUploadImage(t, srv, "img-existing", "uid-img-existing", "v1", "qcow2", int64(len(body)), digest, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	ref, err := imageStore.Get(context.Background(), "img-existing", "v1")
	if err != nil {
		t.Fatalf("imageStore.Get error = %v, want existing object preserved", err)
	}
	if ref.SHA256 != digest || ref != seedRef {
		t.Fatalf("imageStore ref after failed idempotent upload = %#v, want preserved existing ref", ref)
	}
	image := imagev1.Image{
		TypeMeta: metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindImage},
		ObjectMeta: metav1.ObjectMeta{
			Name: "img-existing",
			UID:  "uid-img-existing",
		},
		Spec: imagev1.ImageSpec{
			Source:            imagev1.ImageSource{Type: imagev1.ImageSourceUpload, Location: "http://images.example/apis/Image/img-existing/store/v1"},
			Format:            imagev1.ImageFormatQCOW2,
			Version:           "v1",
			DeclaredSizeBytes: int64(len(body)),
			SHA256:            digest,
		},
	}
	seedStoreObject(t, base, metav1.KindImage, image.Name, image)
	rec = doDownloadImage(t, srv, "img-existing", "v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("download body = %q, want %q", rec.Body.Bytes(), body)
	}
}

func TestDownloadImageRequiresMetadata(t *testing.T) {
	srv, _ := newImageStoreTestServer(t)
	body := []byte("orphan-bytes")
	digest := sha256Hex(body)
	if _, err := srv.imageStore.Put(context.Background(), imagestore.PutRequest{Name: "img-orphan", Version: "v1", Format: "qcow2", DeclaredSizeBytes: int64(len(body)), SHA256: digest, Reader: bytes.NewReader(body)}); err != nil {
		t.Fatalf("seed image store object: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/apis/Image/img-orphan/store/v1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("download exposed orphan bytes %q", rec.Body.Bytes())
	}
}

func TestDownloadImageRejectsDeletingMetadata(t *testing.T) {
	srv, st := newImageStoreTestServer(t)
	body := []byte("deleting-bytes")
	digest := sha256Hex(body)
	if rec := doUploadImage(t, srv, "img-download-deleting", "uid-img-download-deleting", "v1", "qcow2", int64(len(body)), digest, body); rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	stored := storedRaw(t, st, metav1.KindImage, "img-download-deleting")
	var image imagev1.Image
	if err := json.Unmarshal(stored.Value, &image); err != nil {
		t.Fatalf("decode Image: %v", err)
	}
	image.DeletionTimestamp = "2026-06-13T00:00:00Z"
	seedStoreObject(t, st, metav1.KindImage, image.Name, image)
	rec := doDownloadImage(t, srv, "img-download-deleting", "v1")
	assertDownloadRejected(t, rec, body)
}

func TestDownloadImageRejectsNonUploadSource(t *testing.T) {
	srv, st := newImageStoreTestServer(t)
	body := []byte("http-source-bytes")
	digest := sha256Hex(body)
	if _, err := srv.imageStore.Put(context.Background(), imagestore.PutRequest{Name: "img-http-source", Version: "v1", Format: "qcow2", DeclaredSizeBytes: int64(len(body)), SHA256: digest, Reader: bytes.NewReader(body)}); err != nil {
		t.Fatalf("seed image store object: %v", err)
	}
	image := validImage()
	image.Name = "img-http-source"
	image.UID = "uid-img-http-source"
	image.Spec.Source = imagev1.ImageSource{Type: imagev1.ImageSourceHTTP, Location: "https://images.example/base.qcow2"}
	image.Spec.Version = "v1"
	image.Spec.SHA256 = digest
	image.Spec.DeclaredSizeBytes = int64(len(body))
	seedStoreObject(t, st, metav1.KindImage, image.Name, image)
	rec := doDownloadImage(t, srv, "img-http-source", "v1")
	assertDownloadRejected(t, rec, body)
}

func TestDownloadImageRejectsVersionOrLocationMismatch(t *testing.T) {
	tests := []struct {
		name      string
		requestV  string
		mutate    func(*imagev1.Image)
		wantCode  int
		imageName string
	}{
		{name: "path version mismatch", requestV: "v2", imageName: "img-version-mismatch", mutate: func(image *imagev1.Image) {}},
		{name: "source location mismatch", requestV: "v1", imageName: "img-location-mismatch", mutate: func(image *imagev1.Image) {
			image.Spec.Source.Location = "http://images.example/apis/Image/other/store/v1"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st := newImageStoreTestServer(t)
			body := []byte(tt.name)
			digest := sha256Hex(body)
			if _, err := srv.imageStore.Put(context.Background(), imagestore.PutRequest{Name: tt.imageName, Version: "v1", Format: "qcow2", DeclaredSizeBytes: int64(len(body)), SHA256: digest, Reader: bytes.NewReader(body)}); err != nil {
				t.Fatalf("seed image store object: %v", err)
			}
			image := validImage()
			image.Name = tt.imageName
			image.UID = "uid-" + tt.imageName
			image.Spec.Source = imagev1.ImageSource{Type: imagev1.ImageSourceUpload, Location: "http://images.example/apis/Image/" + tt.imageName + "/store/v1"}
			image.Spec.Version = "v1"
			image.Spec.SHA256 = digest
			image.Spec.DeclaredSizeBytes = int64(len(body))
			tt.mutate(&image)
			seedStoreObject(t, st, metav1.KindImage, image.Name, image)
			rec := doDownloadImage(t, srv, tt.imageName, tt.requestV)
			assertDownloadRejected(t, rec, body)
		})
	}
}

func TestDownloadImageRejectsImageStoreSHAMismatch(t *testing.T) {
	srv, st := newImageStoreTestServer(t)
	body := []byte("sha-mismatch")
	actualDigest := sha256Hex(body)
	if _, err := srv.imageStore.Put(context.Background(), imagestore.PutRequest{Name: "img-sha-mismatch", Version: "v1", Format: "qcow2", DeclaredSizeBytes: int64(len(body)), SHA256: actualDigest, Reader: bytes.NewReader(body)}); err != nil {
		t.Fatalf("seed image store object: %v", err)
	}
	image := validImage()
	image.Name = "img-sha-mismatch"
	image.UID = "uid-img-sha-mismatch"
	image.Spec.Source = imagev1.ImageSource{Type: imagev1.ImageSourceUpload, Location: "http://images.example/apis/Image/img-sha-mismatch/store/v1"}
	image.Spec.Version = "v1"
	image.Spec.SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	image.Spec.DeclaredSizeBytes = int64(len(body))
	seedStoreObject(t, st, metav1.KindImage, image.Name, image)
	rec := doDownloadImage(t, srv, "img-sha-mismatch", "v1")
	assertDownloadRejected(t, rec, body)
}

func TestApplyImageRejectsLegacyFilePoolRef(t *testing.T) {
	srv, _ := newTestServer(t)
	body := []byte(`{"apiVersion":"govirta.io/v1alpha1","kind":"Image","metadata":{"name":"img-legacy","uid":"uid-img-legacy"},"spec":{"filePoolRef":"pool-a","source":{"type":"http","location":"https://images.example/base.qcow2"},"format":"qcow2","version":"v1","declaredSizeBytes":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"status":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/apis/Image/img-legacy", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestApplyImageRejectsUnknownLegacyFields(t *testing.T) {
	srv, _ := newTestServer(t)
	body := []byte(`{"apiVersion":"govirta.io/v1alpha1","kind":"Image","metadata":{"name":"img-legacy","uid":"uid-img-legacy"},"spec":{"source":{"type":"http","location":"https://images.example/base.qcow2"},"format":"qcow2","version":"v1","declaredSizeBytes":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","legacyPath":"/srv/base.qcow2"},"status":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/apis/Image/img-legacy", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestReplaceImageRequiresResourceVersionForNewVersion(t *testing.T) {
	srv, _ := newTestServer(t)
	image := validImage()
	if rec := doApply(t, srv, metav1.KindImage, image.Name, image); rec.Code != http.StatusCreated {
		t.Fatalf("apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	image.Spec.Version = "v2"
	image.Spec.SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	rec := doReplaceRaw(t, srv, metav1.KindImage, image.Name, image)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestReplaceImageContentChangeResetsReadyStatus(t *testing.T) {
	srv, st := newTestServer(t)
	image := validImage()
	if rec := doApply(t, srv, metav1.KindImage, image.Name, image); rec.Code != http.StatusCreated {
		t.Fatalf("apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	stored := storedRaw(t, st, metav1.KindImage, image.Name)
	if err := json.Unmarshal(stored.Value, &image); err != nil {
		t.Fatalf("decode stored Image: %v", err)
	}
	image.ResourceVersion = stored.ResourceVersion
	image.Status = readyImageStatus(image.Spec.Version, image.Spec.SHA256, image.Spec.DeclaredSizeBytes)
	image.Spec.Source.Location = "https://images.example/base-v2.qcow2"
	image.Spec.Version = "v2"
	image.Spec.DeclaredSizeBytes = 2 << 28
	image.Spec.SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	rec := doReplaceRaw(t, srv, metav1.KindImage, image.Name, image)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got imagev1.Image
	if err := json.Unmarshal(storedRaw(t, st, metav1.KindImage, image.Name).Value, &got); err != nil {
		t.Fatalf("decode stored Image: %v", err)
	}
	if got.Spec.Version != "v2" || got.Spec.SHA256 != image.Spec.SHA256 || got.Spec.Source.Location != image.Spec.Source.Location || got.Spec.DeclaredSizeBytes != image.Spec.DeclaredSizeBytes {
		t.Fatalf("stored spec = %#v, want replacement content identity", got.Spec)
	}
	if got.Status.Phase != imagev1.ImagePhasePending {
		t.Fatalf("stored status.phase = %q, want pending", got.Status.Phase)
	}
}

func TestUploadImagePublicURLTrailingSlashNormalizesForLaterValidation(t *testing.T) {
	srv, st := newImageStoreTestServerWithPublicURL(t, "http://images.example/")
	body := []byte("trailing-slash")
	digest := sha256Hex(body)
	rec := doUploadImage(t, srv, "img-trailing", "uid-img-trailing", "v1", "qcow2", int64(len(body)), digest, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var uploaded imagev1.Image
	stored := storedRaw(t, st, metav1.KindImage, "img-trailing")
	if err := json.Unmarshal(stored.Value, &uploaded); err != nil {
		t.Fatalf("decode uploaded Image: %v", err)
	}
	if uploaded.Spec.Source.Location != "http://images.example/apis/Image/img-trailing/store/v1" {
		t.Fatalf("source.location = %q, want normalized single slash URL", uploaded.Spec.Source.Location)
	}
	uploaded.ResourceVersion = stored.ResourceVersion
	rec = doReplaceRaw(t, srv, metav1.KindImage, uploaded.Name, uploaded)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUploadExistingImageRejectsDifferentUID(t *testing.T) {
	srv, _ := newImageStoreTestServer(t)
	body := []byte("first")
	digest := sha256Hex(body)
	if rec := doUploadImage(t, srv, "img-uid", "uid-img-a", "v1", "qcow2", int64(len(body)), digest, body); rec.Code != http.StatusCreated {
		t.Fatalf("initial upload status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	body = []byte("second")
	digest = sha256Hex(body)
	rec := doUploadImage(t, srv, "img-uid", "uid-other", "v2", "qcow2", int64(len(body)), digest, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUploadDeletingImageRejectedAndDeletionTimestampPreserved(t *testing.T) {
	srv, st := newImageStoreTestServer(t)
	image := validImage()
	if rec := doApply(t, srv, metav1.KindImage, image.Name, image); rec.Code != http.StatusCreated {
		t.Fatalf("apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodDelete, "/apis/Image/"+image.Name, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	before := storedRaw(t, st, metav1.KindImage, image.Name)
	var deleting imagev1.Image
	if err := json.Unmarshal(before.Value, &deleting); err != nil {
		t.Fatalf("decode deleting Image: %v", err)
	}
	body := []byte("blocked")
	digest := sha256Hex(body)
	rec = doUploadImage(t, srv, image.Name, image.UID, "v2", "qcow2", int64(len(body)), digest, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("upload status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var got imagev1.Image
	if err := json.Unmarshal(storedRaw(t, st, metav1.KindImage, image.Name).Value, &got); err != nil {
		t.Fatalf("decode stored Image: %v", err)
	}
	if got.DeletionTimestamp == "" || got.DeletionTimestamp != deleting.DeletionTimestamp {
		t.Fatalf("deletionTimestamp = %q, want preserved %q", got.DeletionTimestamp, deleting.DeletionTimestamp)
	}
}

func TestDeleteImageKeepsFinalizerUntilControllerCleanup(t *testing.T) {
	srv, st := newTestServer(t)
	image := validImage()
	if rec := doApply(t, srv, metav1.KindImage, image.Name, image); rec.Code != http.StatusCreated {
		t.Fatalf("apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodDelete, "/apis/Image/"+image.Name, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var got imagev1.Image
	if err := json.Unmarshal(storedRaw(t, st, metav1.KindImage, image.Name).Value, &got); err != nil {
		t.Fatalf("decode stored Image: %v", err)
	}
	if got.DeletionTimestamp == "" {
		t.Fatalf("deletionTimestamp is empty, want stamped")
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != metav1.FinalizerImageCache {
		t.Fatalf("finalizers = %v, want image-cache until controller cleanup", got.Finalizers)
	}
}

func newImageStoreTestServer(t *testing.T) (*Server, *fake.Store) {
	t.Helper()
	return newImageStoreTestServerWithPublicURL(t, "http://images.example")
}

func newImageStoreTestServerWithPublicURL(t *testing.T, publicURL string) (*Server, *fake.Store) {
	t.Helper()
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x0000ff)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	local, err := imagestore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("new local image store: %v", err)
	}
	srv := NewServer(ServerConfig{
		Store:               st,
		MACAllocator:        mac.NewAllocator(pool, st),
		Scheduler:           scheduler.NewNoopScheduler(),
		NodeNames:           []string{"node-1"},
		ImageStore:          local,
		ImageStorePublicURL: publicURL,
	})
	return srv, st
}

func doUploadImage(t *testing.T, srv *Server, name, uid, version, format string, size int64, sha string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	path := "/apis/Image/" + name + "/store/" + version + "?uid=" + uid + "&format=" + format + "&sha256=" + sha + "&declaredSizeBytes=" + strconv.FormatInt(size, 10)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func doUploadImageWithoutUID(t *testing.T, srv *Server, name, version, format string, size int64, sha string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	path := "/apis/Image/" + name + "/store/" + version + "?format=" + format + "&sha256=" + sha + "&declaredSizeBytes=" + strconv.FormatInt(size, 10)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func doDownloadImage(t *testing.T, srv *Server, name, version string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/apis/Image/"+name+"/store/"+version, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func assertDownloadRejected(t *testing.T, rec *httptest.ResponseRecorder, forbiddenBody []byte) {
	t.Helper()
	if rec.Code == http.StatusOK {
		t.Fatalf("status = 200, want rejection; body=%s", rec.Body.String())
	}
	if bytes.Equal(rec.Body.Bytes(), forbiddenBody) {
		t.Fatalf("download returned forbidden bytes %q", rec.Body.Bytes())
	}
}

func doReplaceRaw(t *testing.T, srv *Server, kind metav1.Kind, name string, obj any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal replace request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/apis/"+string(kind)+"/"+name, bytes.NewReader(data))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func readyImageStatus(version, sha string, size int64) imagev1.ImageStatus {
	return imagev1.ImageStatus{
		Phase:             imagev1.ImagePhaseReady,
		ObservedVersion:   version,
		ObservedSHA256:    sha,
		ObservedSizeBytes: size,
		NodeCaches: []imagev1.NodeCacheStatus{{
			NodeName:   "node-a",
			Phase:      imagev1.ImageCachePhaseReady,
			TaskRef:    imagev1.TaskRef{Name: "task-a", UID: "uid-task-a"},
			CachedPath: "/var/lib/govirta/images/img-a/" + version,
			SizeBytes:  size,
			SHA256:     sha,
		}},
	}
}

type failImagePutStore struct {
	*fake.Store
	failName string
}

func (s failImagePutStore) Put(ctx context.Context, key string, value []byte, expectedVersion string) (store.RawObject, error) {
	failName := s.failName
	if failName == "" {
		failName = "img-orphan-fail"
	}
	if key == storeKey(metav1.KindImage, failName) {
		return store.RawObject{}, fmt.Errorf("injected metadata put failure")
	}
	return s.Store.Put(ctx, key, value, expectedVersion)
}
