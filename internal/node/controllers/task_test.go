package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/suknna/govirta/internal/node/controller"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

func TestTaskControllerPatchesRunningThenSucceeded(t *testing.T) {
	reporter := &fakeTaskReporter{}
	c := NewTaskController("node-1", reporter)
	task := validNodeTaskForController(t, "node-task", "node-1", taskv1.TaskOperationNoopNode)
	ev := taskEvent(t, task)

	res, err := c.Reconcile(context.Background(), ev)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() {
		t.Fatalf("Reconcile() result = %+v, want no requeue", res)
	}
	if len(reporter.statuses) != 2 {
		t.Fatalf("patched statuses = %d, want 2", len(reporter.statuses))
	}
	if reporter.statuses[0].Phase != taskv1.TaskPhaseRunning || reporter.statuses[1].Phase != taskv1.TaskPhaseSucceeded {
		t.Fatalf("patched phases = %+v", reporter.statuses)
	}
	var observed taskv1.NoopObserved
	if err := json.Unmarshal(reporter.statuses[1].Observed, &observed); err != nil {
		t.Fatalf("decode observed: %v", err)
	}
	if observed.Executor != "node-1" || observed.Marker != "phase-one" {
		t.Fatalf("observed = %+v, want node-1/phase-one", observed)
	}
}

func TestTaskControllerInvalidNoopInputPatchesInvalidInput(t *testing.T) {
	reporter := &fakeTaskReporter{}
	c := NewTaskController("node-1", reporter)
	task := validNodeTaskForController(t, "node-task", "node-1", taskv1.TaskOperationNoopNode)
	task.Spec.Input = mustMarshalForTask(t, taskv1.NoopInput{})

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || len(reporter.statuses) != 2 {
		t.Fatalf("result=%+v statuses=%d, want failed status", res, len(reporter.statuses))
	}
	failed := reporter.statuses[1]
	if failed.Phase != taskv1.TaskPhaseFailed || failed.ErrorClass != taskv1.TaskErrorClassInvalidInput || failed.Message == "" {
		t.Fatalf("failed status = %+v", failed)
	}
}

func TestTaskControllerTerminalTaskIsNoop(t *testing.T) {
	reporter := &fakeTaskReporter{}
	c := NewTaskController("node-1", reporter)
	task := validNodeTaskForController(t, "node-task", "node-1", taskv1.TaskOperationNoopNode)
	task.Status.Phase = taskv1.TaskPhaseSucceeded

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || len(reporter.statuses) != 0 {
		t.Fatalf("terminal task result=%+v statuses=%d, want no-op", res, len(reporter.statuses))
	}
}

func TestTaskControllerRunningTaskIsNoop(t *testing.T) {
	reporter := &fakeTaskReporter{}
	c := NewTaskController("node-1", reporter)
	task := validNodeTaskForController(t, "node-task", "node-1", taskv1.TaskOperationNoopNode)
	task.Status.Phase = taskv1.TaskPhaseRunning

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || len(reporter.statuses) != 0 {
		t.Fatalf("running task result=%+v statuses=%d, want no-op", res, len(reporter.statuses))
	}
}

func TestTaskControllerExecutesCacheImageNode(t *testing.T) {
	data := []byte("image-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, server.Client())
	task := validCacheTaskForController(t, "cache-task", "node-1", cache.Root(), server.URL, data)

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || len(reporter.statuses) != 2 {
		t.Fatalf("result=%+v statuses=%d, want success", res, len(reporter.statuses))
	}
	var observed taskv1.CacheImageObserved
	if err := json.Unmarshal(reporter.statuses[1].Observed, &observed); err != nil {
		t.Fatalf("decode observed: %v", err)
	}
	if observed.NodeName != "node-1" || observed.ImageName != "ubuntu" || observed.CachedPath != filepath.Join(cache.Root(), "ubuntu", "v1", "image") {
		t.Fatalf("observed = %+v", observed)
	}
}

func TestTaskControllerCacheImageHTTPSourceUsesGET(t *testing.T) {
	data := []byte("image-bytes")
	var method string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, server.Client())
	task := validCacheTaskForController(t, "cache-task", "node-1", cache.Root(), server.URL, data)
	setCacheTaskSourceType(t, &task, taskv1.ImageTaskSourceHTTP)

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || reporter.statuses[1].Phase != taskv1.TaskPhaseSucceeded {
		t.Fatalf("result=%+v statuses=%+v, want success", res, reporter.statuses)
	}
	if method != http.MethodGet {
		t.Fatalf("source method = %q, want GET", method)
	}
}

func TestTaskControllerExecutesDeleteCachedImageNode(t *testing.T) {
	data := []byte("image-bytes")
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	input := cacheInput(t, cache.Root(), "ubuntu", "v1", data)
	if _, err := cache.Cache(context.Background(), "node-1", input, strings.NewReader(string(data))); err != nil {
		t.Fatalf("Cache() error = %v", err)
	}
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, nil)
	task := validDeleteTaskForController(t, "delete-task", "node-1", cache.Root(), input.SHA256)

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || len(reporter.statuses) != 2 {
		t.Fatalf("result=%+v statuses=%d, want success", res, len(reporter.statuses))
	}
	var observed taskv1.DeleteCachedImageObserved
	if err := json.Unmarshal(reporter.statuses[1].Observed, &observed); err != nil {
		t.Fatalf("decode observed: %v", err)
	}
	if observed.NodeName != "node-1" || observed.ImageName != "ubuntu" || !observed.Deleted {
		t.Fatalf("observed = %+v", observed)
	}
}

func TestTaskControllerFailedDownloadPatchesTransientIO(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, server.Client())
	task := validCacheTaskForController(t, "cache-task", "node-1", cache.Root(), server.URL, []byte("image-bytes"))

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || len(reporter.statuses) != 2 {
		t.Fatalf("result=%+v statuses=%d, want failed status", res, len(reporter.statuses))
	}
	failed := reporter.statuses[1]
	if failed.Phase != taskv1.TaskPhaseFailed || failed.ErrorClass != taskv1.TaskErrorClassTransientIO || failed.Message == "" {
		t.Fatalf("failed status = %+v", failed)
	}
}

func TestTaskControllerNon200ClosesResponseBody(t *testing.T) {
	closed := make(chan struct{}, 1)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTeapot, Body: closeNotifyReadCloser{Reader: strings.NewReader("nope"), closed: closed}, Header: make(http.Header)}, nil
	})}
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, client)
	task := validCacheTaskForController(t, "cache-task", "node-1", cache.Root(), "http://images.example/not-found", []byte("image-bytes"))

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || reporter.statuses[1].ErrorClass != taskv1.TaskErrorClassTransientIO {
		t.Fatalf("result=%+v statuses=%+v, want transient IO failed", res, reporter.statuses)
	}
	select {
	case <-closed:
	default:
		t.Fatalf("non-200 response body was not closed")
	}
}

func TestTaskControllerNetworkFailurePatchesTransientIO(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: "http://images.example/image", Err: io.ErrUnexpectedEOF}
	})}
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, client)
	task := validCacheTaskForController(t, "cache-task", "node-1", cache.Root(), "http://images.example/image", []byte("image-bytes"))

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || reporter.statuses[1].ErrorClass != taskv1.TaskErrorClassTransientIO {
		t.Fatalf("result=%+v statuses=%+v, want transient IO failed", res, reporter.statuses)
	}
}

func TestTaskControllerCacheImageCloseFailurePatchesTransientIO(t *testing.T) {
	data := []byte("image-bytes")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: closeErrorReadCloser{Reader: strings.NewReader(string(data)), err: io.ErrUnexpectedEOF}, Header: make(http.Header)}, nil
	})}
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, client)
	task := validCacheTaskForController(t, "cache-task", "node-1", cache.Root(), "http://images.example/image", data)

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	failed := reporter.statuses[1]
	if res.ShouldRequeue() || failed.Phase != taskv1.TaskPhaseFailed || failed.ErrorClass != taskv1.TaskErrorClassTransientIO || !strings.Contains(failed.Message, "close image source body") {
		t.Fatalf("result=%+v failed=%+v, want transient close failure", res, failed)
	}
}

func TestTaskControllerCacheAndCloseFailurePreservesBoth(t *testing.T) {
	data := []byte("image-bytes")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: closeErrorReadCloser{Reader: strings.NewReader(string(data)), err: io.ErrUnexpectedEOF}, Header: make(http.Header)}, nil
	})}
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, client)
	task := validCacheTaskForController(t, "cache-task", "node-1", cache.Root(), "http://images.example/image", data)
	setCacheTaskChecksum(t, &task, strings.Repeat("0", 64))

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	failed := reporter.statuses[1]
	if res.ShouldRequeue() || failed.ErrorClass != taskv1.TaskErrorClassChecksumMismatch || !strings.Contains(failed.Message, "sha256 mismatch") || !strings.Contains(failed.Message, "close image source body") {
		t.Fatalf("result=%+v failed=%+v, want checksum class with both messages", res, failed)
	}
}

func TestTaskControllerCacheImageRejectsMismatchedCacheRoot(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("image-bytes"))
	}))
	t.Cleanup(server.Close)
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, server.Client())
	task := validCacheTaskForController(t, "cache-task", "node-1", filepath.Join(t.TempDir(), "other-cache"), server.URL, []byte("image-bytes"))

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || len(reporter.statuses) != 2 {
		t.Fatalf("result=%+v statuses=%d, want failed status", res, len(reporter.statuses))
	}
	failed := reporter.statuses[1]
	if failed.Phase != taskv1.TaskPhaseFailed || failed.ErrorClass != taskv1.TaskErrorClassInvalidInput || failed.Message == "" {
		t.Fatalf("failed status = %+v", failed)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("source server hit %d times, want 0 before cache-root validation", hits)
	}
}

func TestTaskControllerInvalidInputPatchesInvalidInput(t *testing.T) {
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, nil)
	task := validCacheTaskForController(t, "cache-task", "node-1", cache.Root(), "http://images.example/image", []byte("image-bytes"))
	setCacheTaskSourceType(t, &task, taskv1.ImageTaskSourceType("ftp"))

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || reporter.statuses[1].ErrorClass != taskv1.TaskErrorClassInvalidInput {
		t.Fatalf("result=%+v statuses=%+v, want invalid input failed", res, reporter.statuses)
	}
}

func TestTaskControllerUnsafeCacheSegmentPatchesInvalidInput(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("image-bytes"))
	}))
	t.Cleanup(server.Close)
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, server.Client())
	task := validCacheTaskForController(t, "cache-task", "node-1", cache.Root(), server.URL, []byte("image-bytes"))
	setCacheTaskImageName(t, &task, "../escape")

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || reporter.statuses[1].ErrorClass != taskv1.TaskErrorClassInvalidInput {
		t.Fatalf("result=%+v statuses=%+v, want invalid input failed", res, reporter.statuses)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("source server hit %d times, want 0 before unsafe path validation", hits)
	}
}

func TestTaskControllerChecksumMismatchPatchesChecksumMismatch(t *testing.T) {
	data := []byte("image-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	reporter := &fakeTaskReporter{}
	cache := mustNewTaskTestCache(t)
	c := NewTaskControllerWithImageCache("node-1", reporter, cache, server.Client())
	task := validCacheTaskForController(t, "cache-task", "node-1", cache.Root(), server.URL, data)
	setCacheTaskChecksum(t, &task, strings.Repeat("0", 64))

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || reporter.statuses[1].ErrorClass != taskv1.TaskErrorClassChecksumMismatch {
		t.Fatalf("result=%+v statuses=%+v, want checksum mismatch failed", res, reporter.statuses)
	}
}

func TestTaskControllerWrongNodeIsNoop(t *testing.T) {
	reporter := &fakeTaskReporter{}
	c := NewTaskController("node-1", reporter)
	task := validNodeTaskForController(t, "node-task", "node-2", taskv1.TaskOperationNoopNode)

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || len(reporter.statuses) != 0 {
		t.Fatalf("wrong-node result=%+v statuses=%d, want no-op", res, len(reporter.statuses))
	}
}

func TestTaskControllerRejectsUnsupportedOperation(t *testing.T) {
	reporter := &fakeTaskReporter{}
	c := NewTaskController("node-1", reporter)
	task := validNodeTaskForController(t, "node-task", "node-1", taskv1.TaskOperationNoopCluster)

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || len(reporter.statuses) != 1 {
		t.Fatalf("unsupported-op result=%+v statuses=%d, want one failed status", res, len(reporter.statuses))
	}
	if reporter.statuses[0].Phase != taskv1.TaskPhaseFailed || reporter.statuses[0].ErrorClass != taskv1.TaskErrorClassUnsupportedOperation {
		t.Fatalf("status = %+v, want unsupported-op failed", reporter.statuses[0])
	}
}

func mustNewTaskTestCache(t *testing.T) *ImageCache {
	t.Helper()
	cache, err := NewImageCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewImageCache(): %v", err)
	}
	return cache
}

func validCacheTaskForController(t *testing.T, name, node, root, location string, data []byte) taskv1.Task {
	t.Helper()
	sum := sha256.Sum256(data)
	input, err := json.Marshal(taskv1.CacheImageInput{ImageName: "ubuntu", ImageUID: "uid-ubuntu", Version: "v1", Format: "qcow2", Source: taskv1.ImageTaskSource{Type: taskv1.ImageTaskSourceUpload, Location: location}, DeclaredSizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), CacheRoot: root})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return taskv1.Task{TypeMeta: metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindTask}, ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name, NodeName: node}, Spec: taskv1.TaskSpec{Scope: taskv1.TaskScopeNode, OwnerKind: metav1.KindImage, OwnerName: "ubuntu", OwnerUID: "uid-ubuntu", Operation: taskv1.TaskOperationCacheImageNode, Input: input}, Status: taskv1.TaskStatus{Phase: taskv1.TaskPhasePending}}
}

func setCacheTaskSourceType(t *testing.T, task *taskv1.Task, sourceType taskv1.ImageTaskSourceType) {
	t.Helper()
	var input taskv1.CacheImageInput
	if err := json.Unmarshal(task.Spec.Input, &input); err != nil {
		t.Fatalf("decode cache input: %v", err)
	}
	input.Source.Type = sourceType
	task.Spec.Input = mustMarshalForTask(t, input)
}

func setCacheTaskChecksum(t *testing.T, task *taskv1.Task, checksum string) {
	t.Helper()
	var input taskv1.CacheImageInput
	if err := json.Unmarshal(task.Spec.Input, &input); err != nil {
		t.Fatalf("decode cache input: %v", err)
	}
	input.SHA256 = checksum
	task.Spec.Input = mustMarshalForTask(t, input)
}

func setCacheTaskImageName(t *testing.T, task *taskv1.Task, imageName string) {
	t.Helper()
	var input taskv1.CacheImageInput
	if err := json.Unmarshal(task.Spec.Input, &input); err != nil {
		t.Fatalf("decode cache input: %v", err)
	}
	input.ImageName = imageName
	task.Spec.Input = mustMarshalForTask(t, input)
}

func mustMarshalForTask(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal task input: %v", err)
	}
	return raw
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type closeNotifyReadCloser struct {
	io.Reader
	closed chan<- struct{}
}

type closeErrorReadCloser struct {
	io.Reader
	err error
}

func (r closeErrorReadCloser) Close() error { return r.err }

func (r closeNotifyReadCloser) Close() error {
	r.closed <- struct{}{}
	return nil
}

func validDeleteTaskForController(t *testing.T, name, node, root, checksum string) taskv1.Task {
	t.Helper()
	input, err := json.Marshal(taskv1.DeleteCachedImageInput{ImageName: "ubuntu", ImageUID: "uid-ubuntu", Version: "v1", SHA256: checksum, CacheRoot: root})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return taskv1.Task{TypeMeta: metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindTask}, ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name, NodeName: node}, Spec: taskv1.TaskSpec{Scope: taskv1.TaskScopeNode, OwnerKind: metav1.KindImage, OwnerName: "ubuntu", OwnerUID: "uid-ubuntu", Operation: taskv1.TaskOperationDeleteCachedImageNode, Input: input}, Status: taskv1.TaskStatus{Phase: taskv1.TaskPhasePending}}
}

type fakeTaskReporter struct {
	statuses []taskv1.TaskStatus
}

func (f *fakeTaskReporter) PatchStatus(_ context.Context, _, _ string, status []byte) ([]byte, error) {
	var decoded taskv1.TaskStatus
	if err := json.Unmarshal(status, &decoded); err != nil {
		return nil, err
	}
	f.statuses = append(f.statuses, decoded)
	return nil, nil
}

func validNodeTaskForController(t *testing.T, name, node string, operation taskv1.TaskOperation) taskv1.Task {
	t.Helper()
	input, err := json.Marshal(taskv1.NoopInput{Marker: "phase-one"})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return taskv1.Task{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindTask},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name, NodeName: node},
		Spec:       taskv1.TaskSpec{Scope: taskv1.TaskScopeNode, OwnerKind: metav1.KindTask, OwnerName: "phase-one-owner", OwnerUID: "phase-one-owner-uid", Operation: operation, Input: input},
		Status:     taskv1.TaskStatus{Phase: taskv1.TaskPhasePending},
	}
}

func taskEvent(t *testing.T, task taskv1.Task) controller.Event {
	t.Helper()
	body, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	return controller.Event{Type: controller.EventAdded, Key: task.Name, Object: body}
}
