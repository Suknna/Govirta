package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/storage"
	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/image"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// bufferImageWriter is a buffer-backed image.ImageWriter that records the bytes
// written and whether it was committed (Close) or cancelled (Cancel). It is
// faithful to a real writer: Close commits pending→ready, Cancel discards.
type bufferImageWriter struct {
	buf       bytes.Buffer
	closed    bool
	cancelled bool
	closeErr  error
}

func (w *bufferImageWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *bufferImageWriter) Close() error {
	w.closed = true
	return w.closeErr
}

func (w *bufferImageWriter) Cancel() error {
	w.cancelled = true
	return nil
}

// fakeImagePutter records the PutImage request and hands back a configured
// writer/error. It honours ctx cancellation before returning, faithful to
// *storage.ImageService.
type fakeImagePutter struct {
	writer   *bufferImageWriter
	putErr   error
	gotReq   storage.PutImageRequest
	putCalls int
}

func (f *fakeImagePutter) PutImage(ctx context.Context, req storage.PutImageRequest) (image.ImageWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.putCalls++
	f.gotReq = req
	if f.putErr != nil {
		return nil, f.putErr
	}
	return f.writer, nil
}

// fakeImageStatusReporter captures the last ImageStatus patched and honours ctx
// cancellation, faithful to *client.Client. It is image-specific so it can
// decode into ImageStatus (the package's StoragePool fake decodes a different
// status type).
type fakeImageStatusReporter struct {
	patches    []capturedImagePatch
	patchErr   error
	patchCalls int
}

type capturedImagePatch struct {
	kind   string
	name   string
	status imagev1.ImageStatus
}

func (f *fakeImageStatusReporter) PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.patchCalls++
	if f.patchErr != nil {
		return nil, f.patchErr
	}
	var decoded imagev1.ImageStatus
	if err := json.Unmarshal(status, &decoded); err != nil {
		return nil, err
	}
	f.patches = append(f.patches, capturedImagePatch{kind: kind, name: name, status: decoded})
	return status, nil
}

func newImageEvent(t *testing.T, evType controller.EventType, img imagev1.Image) controller.Event {
	t.Helper()
	raw, err := json.Marshal(img)
	if err != nil {
		t.Fatalf("marshal Image: %v", err)
	}
	return controller.Event{Type: evType, Key: img.Name, Object: raw}
}

func fileSourceImage(name, poolRef, location string) imagev1.Image {
	return imagev1.Image{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindImage},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name},
		Spec: imagev1.ImageSpec{
			FilePoolRef:       poolRef,
			Source:            imagev1.ImageSource{Type: imagev1.ImageSourceFile, Location: location},
			Format:            imagev1.ImageFormatQCOW2,
			DeclaredSizeBytes: 1 << 20,
		},
	}
}

// writeTempSource writes content to a temp file and returns its absolute path
// plus the directory used as the controller's allowed source root.
func writeTempSource(t *testing.T, content []byte) (path, root string) {
	t.Helper()
	root = t.TempDir()
	path = filepath.Join(root, "source.img")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write temp source: %v", err)
	}
	return path, root
}

func TestImageReconcileFileSourceReady(t *testing.T) {
	content := []byte("qcow2-image-bytes-payload")
	path, root := writeTempSource(t, content)

	writer := &bufferImageWriter{}
	putter := &fakeImagePutter{writer: writer}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, http.DefaultClient, root)

	img := fileSourceImage("img-a", "pool-a", "file://"+path)
	ev := newImageEvent(t, controller.EventAdded, img)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}

	// PutImage 收到完整请求，源字节格式权威 = Image.Spec.Format。
	if putter.putCalls != 1 {
		t.Fatalf("PutImage called %d times, want 1", putter.putCalls)
	}
	if putter.gotReq.PoolName != "pool-a" {
		t.Errorf("PutImage PoolName = %q, want %q", putter.gotReq.PoolName, "pool-a")
	}
	if putter.gotReq.ImageID != "img-a" {
		t.Errorf("PutImage ImageID = %q, want %q", putter.gotReq.ImageID, "img-a")
	}
	if putter.gotReq.Format != diskformat.FormatQCOW2 {
		t.Errorf("PutImage Format = %q, want %q", putter.gotReq.Format, diskformat.FormatQCOW2)
	}
	if putter.gotReq.DeclaredSizeBytes != 1<<20 {
		t.Errorf("PutImage DeclaredSizeBytes = %d, want %d", putter.gotReq.DeclaredSizeBytes, int64(1<<20))
	}

	// writer 收到完整字节并被提交（commit），不是取消。
	if got := writer.buf.Bytes(); !bytes.Equal(got, content) {
		t.Errorf("writer received %q, want %q", got, content)
	}
	if !writer.closed {
		t.Errorf("writer.Close not called; bytes were not committed")
	}
	if writer.cancelled {
		t.Errorf("writer.Cancel called; commit path should not cancel")
	}

	if len(reporter.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(reporter.patches))
	}
	patch := reporter.patches[0]
	if patch.kind != string(metav1.KindImage) {
		t.Errorf("patch kind = %q, want %q", patch.kind, metav1.KindImage)
	}
	if patch.name != "img-a" {
		t.Errorf("patch name = %q, want %q", patch.name, "img-a")
	}
	if patch.status.Phase != imagev1.ImagePhaseReady {
		t.Errorf("patch phase = %q, want %q", patch.status.Phase, imagev1.ImagePhaseReady)
	}
	if patch.status.LocalSizeBytes != int64(len(content)) {
		t.Errorf("patch localSizeBytes = %d, want %d", patch.status.LocalSizeBytes, len(content))
	}
	if patch.status.LocalSizeBytes <= 0 {
		t.Errorf("patch localSizeBytes = %d, want > 0", patch.status.LocalSizeBytes)
	}
	if patch.status.Message != "" {
		t.Errorf("patch message = %q, want empty on ready", patch.status.Message)
	}
}

// TestImageReconcileReadyIsNoOp guards the level-triggered idempotence fix: a
// ready image re-reconciled (e.g. on the MODIFIED event the ready-patch itself
// produced) must NOT re-fetch. Without the early return PutImage returns
// ErrImageExists before the no-op-guarded patch, so the controller spins forever
// — the exact e2e blind-spot loop that fired 13592 times. The reconcile must be
// a clean no-op: no PutImage, no patch.
func TestImageReconcileReadyIsNoOp(t *testing.T) {
	putter := &fakeImagePutter{writer: &bufferImageWriter{}}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, http.DefaultClient, t.TempDir())

	img := fileSourceImage("img-a", "pool-a", "file:///x")
	img.Status.Phase = imagev1.ImagePhaseReady
	ev := newImageEvent(t, controller.EventAdded, img)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false for a ready image")
	}
	if putter.putCalls != 0 {
		t.Errorf("PutImage called %d times on a ready image, want 0", putter.putCalls)
	}
	if len(reporter.patches) != 0 {
		t.Errorf("PatchStatus called %d times on a ready image, want 0", len(reporter.patches))
	}
}

func TestImageReconcilePlainFilePathReady(t *testing.T) {
	// Location 不带 file:// 前缀的绝对路径也应被接受。
	content := []byte("raw-bytes")
	path, root := writeTempSource(t, content)

	writer := &bufferImageWriter{}
	putter := &fakeImagePutter{writer: writer}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, http.DefaultClient, root)

	img := fileSourceImage("img-plain", "pool-a", path)
	img.Spec.Format = imagev1.ImageFormatRaw
	ev := newImageEvent(t, controller.EventAdded, img)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}
	if putter.gotReq.Format != diskformat.FormatRaw {
		t.Errorf("PutImage Format = %q, want %q", putter.gotReq.Format, diskformat.FormatRaw)
	}
	if len(reporter.patches) != 1 || reporter.patches[0].status.Phase != imagev1.ImagePhaseReady {
		t.Fatalf("expected one ready patch, got %+v", reporter.patches)
	}
}

func TestImageReconcileHTTPSourceReady(t *testing.T) {
	content := []byte("http-served-image-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	writer := &bufferImageWriter{}
	putter := &fakeImagePutter{writer: writer}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, srv.Client(), "")

	img := imagev1.Image{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindImage},
		ObjectMeta: metav1.ObjectMeta{Name: "img-http", UID: "uid-http"},
		Spec: imagev1.ImageSpec{
			FilePoolRef:       "pool-a",
			Source:            imagev1.ImageSource{Type: imagev1.ImageSourceHTTP, Location: srv.URL},
			Format:            imagev1.ImageFormatQCOW2,
			DeclaredSizeBytes: 1 << 20,
		},
	}
	ev := newImageEvent(t, controller.EventAdded, img)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}
	if got := writer.buf.Bytes(); !bytes.Equal(got, content) {
		t.Errorf("writer received %q, want %q", got, content)
	}
	if !writer.closed {
		t.Errorf("writer.Close not called; bytes were not committed")
	}
	if len(reporter.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(reporter.patches))
	}
	if reporter.patches[0].status.Phase != imagev1.ImagePhaseReady {
		t.Errorf("patch phase = %q, want %q", reporter.patches[0].status.Phase, imagev1.ImagePhaseReady)
	}
	if reporter.patches[0].status.LocalSizeBytes != int64(len(content)) {
		t.Errorf("patch localSizeBytes = %d, want %d", reporter.patches[0].status.LocalSizeBytes, len(content))
	}
}

func TestImageReconcileUnsupportedSchemeIsPermanentFailure(t *testing.T) {
	putter := &fakeImagePutter{writer: &bufferImageWriter{}}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, http.DefaultClient, t.TempDir())

	img := imagev1.Image{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindImage},
		ObjectMeta: metav1.ObjectMeta{Name: "img-bad", UID: "uid-bad"},
		Spec: imagev1.ImageSpec{
			FilePoolRef:       "pool-a",
			Source:            imagev1.ImageSource{Type: imagev1.ImageSourceHTTP, Location: "ftp://example.com/x.img"},
			Format:            imagev1.ImageFormatQCOW2,
			DeclaredSizeBytes: 1 << 20,
		},
	}
	ev := newImageEvent(t, controller.EventAdded, img)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for permanent config failure", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false for unsupported scheme (config error)")
	}
	if putter.putCalls != 0 {
		t.Errorf("PutImage called %d times, want 0 when source scheme is unsupported", putter.putCalls)
	}
	if len(reporter.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(reporter.patches))
	}
	patch := reporter.patches[0]
	if patch.status.Phase != imagev1.ImagePhaseFailed {
		t.Errorf("patch phase = %q, want %q", patch.status.Phase, imagev1.ImagePhaseFailed)
	}
	if patch.status.Message == "" {
		t.Errorf("patch message empty, want failure cause")
	}
	if patch.status.LocalSizeBytes != 0 {
		t.Errorf("patch localSizeBytes = %d, want 0 on failure", patch.status.LocalSizeBytes)
	}
}

func TestImageReconcileUnknownSourceTypeIsPermanentFailure(t *testing.T) {
	putter := &fakeImagePutter{writer: &bufferImageWriter{}}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, http.DefaultClient, t.TempDir())

	img := fileSourceImage("img-type", "pool-a", "/whatever")
	img.Spec.Source.Type = imagev1.ImageSourceType("registry")
	ev := newImageEvent(t, controller.EventAdded, img)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for permanent config failure", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false for unknown source type")
	}
	if putter.putCalls != 0 {
		t.Errorf("PutImage called %d times, want 0", putter.putCalls)
	}
	if len(reporter.patches) != 1 || reporter.patches[0].status.Phase != imagev1.ImagePhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reporter.patches)
	}
}

func TestImageReconcileUnsafeFilePathIsPermanentFailure(t *testing.T) {
	// 源文件在允许根之外：路径安全校验必须拒绝，且这是永久错误（不 requeue）。
	root := t.TempDir()
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "escape.img")
	if err := os.WriteFile(outsidePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write outside source: %v", err)
	}

	putter := &fakeImagePutter{writer: &bufferImageWriter{}}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, http.DefaultClient, root)

	img := fileSourceImage("img-escape", "pool-a", "file://"+outsidePath)
	ev := newImageEvent(t, controller.EventAdded, img)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for permanent config failure", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false for unsafe path")
	}
	if putter.putCalls != 0 {
		t.Errorf("PutImage called %d times, want 0 for unsafe path", putter.putCalls)
	}
	if len(reporter.patches) != 1 || reporter.patches[0].status.Phase != imagev1.ImagePhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reporter.patches)
	}
}

func TestImageReconcileUnsupportedFormatIsPermanentFailure(t *testing.T) {
	path, root := writeTempSource(t, []byte("bytes"))

	putter := &fakeImagePutter{writer: &bufferImageWriter{}}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, http.DefaultClient, root)

	img := fileSourceImage("img-fmt", "pool-a", "file://"+path)
	img.Spec.Format = imagev1.ImageFormat("vmdk")
	ev := newImageEvent(t, controller.EventAdded, img)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for permanent config failure", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false for unsupported format")
	}
	if putter.putCalls != 0 {
		t.Errorf("PutImage called %d times, want 0; format maps before opening source", putter.putCalls)
	}
	if len(reporter.patches) != 1 || reporter.patches[0].status.Phase != imagev1.ImagePhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reporter.patches)
	}
}

func TestImageReconcilePutImageFailureRequeues(t *testing.T) {
	path, root := writeTempSource(t, []byte("bytes"))

	putErr := errors.New("pool full")
	putter := &fakeImagePutter{putErr: putErr}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, http.DefaultClient, root)

	img := fileSourceImage("img-put", "pool-a", "file://"+path)
	ev := newImageEvent(t, controller.EventAdded, img)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err == nil {
		t.Fatalf("Reconcile() error = nil, want non-nil on PutImage failure")
	}
	if !errors.Is(err, putErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, putErr)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on transient PutImage failure")
	}
	if len(reporter.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(reporter.patches))
	}
	patch := reporter.patches[0]
	if patch.status.Phase != imagev1.ImagePhaseFailed {
		t.Errorf("patch phase = %q, want %q", patch.status.Phase, imagev1.ImagePhaseFailed)
	}
	if patch.status.Message == "" {
		t.Errorf("patch message empty, want failure cause")
	}
}

func TestImageReconcileCommitFailureRequeues(t *testing.T) {
	path, root := writeTempSource(t, []byte("bytes"))

	commitErr := errors.New("commit rename failed")
	writer := &bufferImageWriter{closeErr: commitErr}
	putter := &fakeImagePutter{writer: writer}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, http.DefaultClient, root)

	img := fileSourceImage("img-commit", "pool-a", "file://"+path)
	ev := newImageEvent(t, controller.EventAdded, img)

	requeue, err := c.Reconcile(context.Background(), ev)
	if err == nil || !errors.Is(err, commitErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, commitErr)
	}
	if !requeue {
		t.Fatalf("Reconcile() requeue = false, want true on transient commit failure")
	}
	if len(reporter.patches) != 1 || reporter.patches[0].status.Phase != imagev1.ImagePhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", reporter.patches)
	}
}

func TestImageReconcileDeletedIsNoOp(t *testing.T) {
	putter := &fakeImagePutter{writer: &bufferImageWriter{}}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, http.DefaultClient, t.TempDir())

	ev := newImageEvent(t, controller.EventDeleted, fileSourceImage("img-del", "pool-a", "/x"))

	requeue, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false")
	}
	if putter.putCalls != 0 {
		t.Errorf("PutImage called %d times on DELETED, want 0", putter.putCalls)
	}
	if reporter.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times on DELETED, want 0", reporter.patchCalls)
	}
}

func TestImageReconcileContextCancelledPropagates(t *testing.T) {
	putter := &fakeImagePutter{writer: &bufferImageWriter{}}
	reporter := &fakeImageStatusReporter{}
	c := NewImageController(putter, reporter, http.DefaultClient, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ev := newImageEvent(t, controller.EventAdded, fileSourceImage("img-ctx", "pool-a", "/x"))

	requeue, err := c.Reconcile(ctx, ev)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconcile() error = %v, want wrapped context.Canceled", err)
	}
	if requeue {
		t.Fatalf("Reconcile() requeue = true, want false when context cancelled before work")
	}
	if putter.putCalls != 0 {
		t.Errorf("PutImage called %d times after ctx cancel, want 0", putter.putCalls)
	}
	if reporter.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times after ctx cancel, want 0", reporter.patchCalls)
	}
}
