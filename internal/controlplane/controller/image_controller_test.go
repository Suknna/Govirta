package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/controlplane/imagestore"
	"github.com/suknna/govirta/internal/controlplane/store"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

const testSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestImageControllerCreatesCacheTaskPerNode(t *testing.T) {
	st, controller := newTestImageController(t)
	seedImage(t, st, testImage())

	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	for _, nodeName := range []string{"node-a", "node-b"} {
		task := mustGetTask(t, st, imageTaskNameForImage(taskv1.TaskOperationCacheImageNode, imgFromStore(t, st, "cirros"), nodeName))
		if task.Spec.Operation != taskv1.TaskOperationCacheImageNode || task.NodeName != nodeName || task.Spec.OwnerKind != metav1.KindImage {
			t.Fatalf("task for %s mismatch: %+v", nodeName, task)
		}
	}
	img := mustGetImage(t, st, "cirros")
	if !hasFinalizer(img, metav1.FinalizerImageCache) {
		t.Fatalf("image finalizers = %v, want image-cache", img.Finalizers)
	}
	if img.Status.Phase != imagev1.ImagePhaseCaching || len(img.Status.NodeCaches) != 2 {
		t.Fatalf("image status = %+v, want caching with two node caches", img.Status)
	}
}

func TestImageControllerMarksReadyAfterAllNodeTasksSucceed(t *testing.T) {
	st, controller := newTestImageController(t)
	img := testImage()
	seedImage(t, st, img)
	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	patchCacheSucceeded(t, st, img, "node-a")
	patchCacheSucceeded(t, st, img, "node-b")

	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("ready reconcile: %v", err)
	}

	got := mustGetImage(t, st, img.Name)
	if got.Status.Phase != imagev1.ImagePhaseReady || got.Status.ObservedSHA256 != testSHA || got.Status.ObservedVersion != "v1" {
		t.Fatalf("status = %+v, want ready current version/sha", got.Status)
	}
	for _, cache := range got.Status.NodeCaches {
		if cache.Phase != imagev1.ImageCachePhaseReady || cache.SHA256 != testSHA || cache.CachedPath == "" {
			t.Fatalf("cache status = %+v, want ready observed cache", cache)
		}
	}
}

func TestImageControllerIgnoresOldTaskRef(t *testing.T) {
	st, controller := newTestImageController(t)
	img := testImage()
	seedImage(t, st, img)
	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	oldObserved := taskv1.CacheImageObserved{NodeName: "node-a", ImageName: img.Name, Version: "old", Format: string(img.Spec.Format), CachedPath: "/cache/old", SizeBytes: img.Spec.DeclaredSizeBytes, SHA256: testSHA}
	patchTaskStatus(t, st, imageTaskNameForImage(taskv1.TaskOperationCacheImageNode, img, "node-a"), succeededStatus(t, oldObserved))
	patchCacheSucceeded(t, st, img, "node-b")

	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := mustGetImage(t, st, img.Name)
	if got.Status.Phase == imagev1.ImagePhaseReady {
		t.Fatalf("status = %+v, stale task result must not mark image ready", got.Status)
	}
}

func TestImageControllerFailsClosedOnNodeTaskFailure(t *testing.T) {
	st, controller := newTestImageController(t)
	img := testImage()
	seedImage(t, st, img)
	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	patchTaskStatus(t, st, imageTaskNameForImage(taskv1.TaskOperationCacheImageNode, img, "node-a"), taskv1.TaskStatus{Phase: taskv1.TaskPhaseFailed, ErrorClass: taskv1.TaskErrorClassExecutionFailed, Message: "download failed"})

	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := mustGetImage(t, st, img.Name)
	if got.Status.Phase != imagev1.ImagePhaseFailed || !strings.Contains(got.Status.Message, "download failed") {
		t.Fatalf("status = %+v, want failed with task message", got.Status)
	}
}

func TestImageControllerDeleteObservedFalseKeepsFinalizer(t *testing.T) {
	st, controller := newTestImageController(t)
	imageStore := controller.imageStore.(*recordingImageStore)
	img := deletingReadyImage()
	seedImage(t, st, img)
	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	patchDeleteObserved(t, st, img, "node-a", false)
	patchDeleteSucceeded(t, st, img, "node-b")

	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("delete false reconcile: %v", err)
	}

	got := mustGetImage(t, st, img.Name)
	if !hasFinalizer(got, metav1.FinalizerImageCache) || imageStore.deleteCalls != 0 {
		t.Fatalf("Deleted=false removed finalizer or store object: finalizers=%v deleteCalls=%d", got.Finalizers, imageStore.deleteCalls)
	}
}

func TestImageControllerSameVersionDifferentContentGetsDistinctTaskName(t *testing.T) {
	st, controller := newTestImageController(t)
	oldImage := testImage()
	seedImage(t, st, oldImage)
	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("old reconcile: %v", err)
	}
	oldTaskName := imageTaskNameForImage(taskv1.TaskOperationCacheImageNode, oldImage, "node-a")

	newImage := mustGetImage(t, st, oldImage.Name)
	newImage.Spec.Source.Location = "https://example.invalid/new-cirros.raw"
	newImage.Spec.Format = imagev1.ImageFormatRaw
	newImage.Spec.DeclaredSizeBytes = 256
	newImage.Spec.SHA256 = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	newImage.Status = imagev1.ImageStatus{Phase: imagev1.ImagePhasePending}
	seedImage(t, st, newImage)

	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("new content reconcile: %v", err)
	}

	newTaskName := imageTaskNameForImage(taskv1.TaskOperationCacheImageNode, newImage, "node-a")
	if oldTaskName == newTaskName {
		t.Fatalf("task name did not include content identity: old=%s new=%s", oldTaskName, newTaskName)
	}
	if _, err := NewTaskClient(st).GetTask(context.Background(), oldTaskName); err != nil {
		t.Fatalf("old task missing: %v", err)
	}
	if _, err := NewTaskClient(st).GetTask(context.Background(), newTaskName); err != nil {
		t.Fatalf("new task missing: %v", err)
	}
	got := mustGetImage(t, st, newImage.Name)
	if got.Status.NodeCaches[0].TaskRef.Name != newTaskName {
		t.Fatalf("status taskRef = %q, want current task %q", got.Status.NodeCaches[0].TaskRef.Name, newTaskName)
	}
}

func TestImageControllerSameVersionDifferentContentGetsDistinctDeleteTaskName(t *testing.T) {
	st, controller := newTestImageController(t)
	oldImage := deletingReadyImage()
	seedImage(t, st, oldImage)
	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("old delete reconcile: %v", err)
	}
	oldTaskName := imageTaskNameForImage(taskv1.TaskOperationDeleteCachedImageNode, oldImage, "node-a")

	newImage := mustGetImage(t, st, oldImage.Name)
	newImage.Spec.Source.Location = "https://example.invalid/new-cirros.raw"
	newImage.Spec.Format = imagev1.ImageFormatRaw
	newImage.Spec.DeclaredSizeBytes = 256
	newImage.Spec.SHA256 = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	newImage.Status = imagev1.ImageStatus{Phase: imagev1.ImagePhaseReady, ObservedVersion: newImage.Spec.Version, ObservedSHA256: newImage.Spec.SHA256, ObservedSizeBytes: newImage.Spec.DeclaredSizeBytes, NodeCaches: []imagev1.NodeCacheStatus{
		readyCache(newImage, "node-a"),
		readyCache(newImage, "node-b"),
	}}
	seedImage(t, st, newImage)

	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("new content delete reconcile: %v", err)
	}

	newTaskName := imageTaskNameForImage(taskv1.TaskOperationDeleteCachedImageNode, newImage, "node-a")
	if oldTaskName == newTaskName {
		t.Fatalf("delete task name did not include content identity: old=%s new=%s", oldTaskName, newTaskName)
	}
	if _, err := NewTaskClient(st).GetTask(context.Background(), oldTaskName); err != nil {
		t.Fatalf("old delete task missing: %v", err)
	}
	if _, err := NewTaskClient(st).GetTask(context.Background(), newTaskName); err != nil {
		t.Fatalf("new delete task missing: %v", err)
	}
	got := mustGetImage(t, st, newImage.Name)
	if got.Status.NodeCaches[0].TaskRef.Name != newTaskName {
		t.Fatalf("delete status taskRef = %q, want current task %q", got.Status.NodeCaches[0].TaskRef.Name, newTaskName)
	}
}

func TestImageClientMergePreservesConcurrentDeletingImage(t *testing.T) {
	current := testImage()
	current.Spec.Source.Location = "https://example.invalid/concurrent.raw"
	current.Finalizers = []metav1.Finalizer{"example.com/other", metav1.FinalizerImageCache}
	current.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)
	current.Status = imagev1.ImageStatus{Phase: imagev1.ImagePhaseReady, ObservedVersion: current.Spec.Version, ObservedSHA256: current.Spec.SHA256, ObservedSizeBytes: current.Spec.DeclaredSizeBytes, NodeCaches: []imagev1.NodeCacheStatus{readyCache(current, "node-a")}}

	desired := testImage()
	desired.Finalizers = []metav1.Finalizer{metav1.FinalizerImageCache}
	desired.Status = imagev1.ImageStatus{Phase: imagev1.ImagePhaseCaching, NodeCaches: []imagev1.NodeCacheStatus{{NodeName: "node-a", Phase: imagev1.ImageCachePhasePending, TaskRef: imagev1.TaskRef{Name: "task", UID: "task-uid"}}}}

	merged, shouldWrite, err := mergeImageControllerFields(current, desired)
	if err != nil {
		t.Fatalf("mergeImageControllerFields() error = %v", err)
	}
	if shouldWrite {
		t.Fatalf("shouldWrite = true, want false for stale active patch against deleting/spec-updated image")
	}
	if merged.DeletionTimestamp != current.DeletionTimestamp || !hasFinalizer(merged, "example.com/other") || merged.Spec.Source.Location != current.Spec.Source.Location {
		t.Fatalf("merged image did not preserve concurrent fields: %+v", merged)
	}
}

func TestImageClientMergeRemovesOnlyImageCacheFinalizer(t *testing.T) {
	current := deletingReadyImage()
	current.Finalizers = []metav1.Finalizer{"example.com/other", metav1.FinalizerImageCache}
	desired := current
	desired.Finalizers = []metav1.Finalizer{"example.com/other"}
	desired.Status = imagev1.ImageStatus{Phase: imagev1.ImagePhaseDeleting, NodeCaches: []imagev1.NodeCacheStatus{{NodeName: "node-a", Phase: imagev1.ImageCachePhaseDeleting, TaskRef: imagev1.TaskRef{Name: "task", UID: "task-uid"}}}}

	merged, shouldWrite, err := mergeImageControllerFields(current, desired)
	if err != nil {
		t.Fatalf("mergeImageControllerFields() error = %v", err)
	}
	if !shouldWrite {
		t.Fatalf("shouldWrite = false, want true for image-cache finalizer removal")
	}
	if hasFinalizer(merged, metav1.FinalizerImageCache) || !hasFinalizer(merged, "example.com/other") || merged.DeletionTimestamp != current.DeletionTimestamp {
		t.Fatalf("merged finalizers/deletionTimestamp = %v/%q, want only image-cache removed", merged.Finalizers, merged.DeletionTimestamp)
	}
}

func TestImageTaskNameIsBoundedForLongIdentityFields(t *testing.T) {
	img := testImage()
	img.Name = strings.Repeat("image-name-", 20)
	img.UID = strings.Repeat("uid-", 20)
	img.Spec.Version = strings.Repeat("version-", 20)
	img.Spec.Source.Location = "https://example.invalid/" + strings.Repeat("path/", 40) + "image.qcow2"
	nodeName := strings.Repeat("node-name-", 20)

	cacheName := imageTaskNameForImage(taskv1.TaskOperationCacheImageNode, img, nodeName)
	deleteName := imageTaskNameForImage(taskv1.TaskOperationDeleteCachedImageNode, img, nodeName)
	for _, name := range []string{cacheName, deleteName} {
		if len(name) > 128 {
			t.Fatalf("task name length = %d for %q, want <= 128", len(name), name)
		}
		if suffix := taskNameHashSuffix(name); len(suffix) != 32 {
			t.Fatalf("task name hash suffix length = %d for %q, want 32", len(suffix), name)
		}
	}
	changed := img
	changed.Spec.SHA256 = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if cacheName == imageTaskNameForImage(taskv1.TaskOperationCacheImageNode, changed, nodeName) {
		t.Fatalf("bounded cache task name did not change for different content identity")
	}
}

func TestImageControllerDeleteWaitsForCacheDeletionBeforeFinalizerRemoval(t *testing.T) {
	st, controller := newTestImageController(t)
	imageStore := controller.imageStore.(*recordingImageStore)
	img := testImage()
	img.Finalizers = []metav1.Finalizer{metav1.FinalizerImageCache}
	img.Status = imagev1.ImageStatus{Phase: imagev1.ImagePhaseReady, ObservedVersion: img.Spec.Version, ObservedSHA256: img.Spec.SHA256, ObservedSizeBytes: img.Spec.DeclaredSizeBytes, NodeCaches: []imagev1.NodeCacheStatus{
		readyCache(img, "node-a"),
		readyCache(img, "node-b"),
	}}
	img.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)
	seedImage(t, st, img)

	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	got := mustGetImage(t, st, img.Name)
	if !hasFinalizer(got, metav1.FinalizerImageCache) || imageStore.deleteCalls != 0 {
		t.Fatalf("delete before task success removed finalizer or store object: finalizers=%v deleteCalls=%d", got.Finalizers, imageStore.deleteCalls)
	}
	patchDeleteSucceeded(t, st, img, "node-a")
	patchDeleteSucceeded(t, st, img, "node-b")

	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("delete completion reconcile: %v", err)
	}
	if _, err := st.Get(context.Background(), imageKey(img.Name)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get finalized image error = %v, want ErrNotFound", err)
	}
	if imageStore.deleteCalls != 1 {
		t.Fatalf("deleteCalls=%d, want image store deleted once", imageStore.deleteCalls)
	}
}

func newTestImageController(t *testing.T) (*fake.Store, *ImageController) {
	t.Helper()
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	controller := NewImageController(st, &recordingImageStore{}, ImageControllerConfig{NodeNames: []string{"node-a", "node-b"}, CacheRoot: "/var/lib/govirta/image-cache", SyncPeriod: time.Second})
	return st, controller
}

func testImage() imagev1.Image {
	return imagev1.Image{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindImage},
		ObjectMeta: metav1.ObjectMeta{Name: "cirros", UID: "image-uid"},
		Spec:       imagev1.ImageSpec{Source: imagev1.ImageSource{Type: imagev1.ImageSourceHTTP, Location: "https://example.invalid/cirros.qcow2"}, Format: imagev1.ImageFormatQCOW2, Version: "v1", DeclaredSizeBytes: 128, SHA256: testSHA},
		Status:     imagev1.ImageStatus{Phase: imagev1.ImagePhasePending},
	}
}

func deletingReadyImage() imagev1.Image {
	img := testImage()
	img.Finalizers = []metav1.Finalizer{metav1.FinalizerImageCache}
	img.Status = imagev1.ImageStatus{Phase: imagev1.ImagePhaseReady, ObservedVersion: img.Spec.Version, ObservedSHA256: img.Spec.SHA256, ObservedSizeBytes: img.Spec.DeclaredSizeBytes, NodeCaches: []imagev1.NodeCacheStatus{
		readyCache(img, "node-a"),
		readyCache(img, "node-b"),
	}}
	img.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)
	return img
}

func seedImage(t *testing.T, st *fake.Store, image imagev1.Image) {
	t.Helper()
	data, err := json.Marshal(image)
	if err != nil {
		t.Fatalf("marshal image: %v", err)
	}
	if _, err := st.Put(context.Background(), imageKey(image.Name), data, ""); err != nil {
		t.Fatalf("seed image: %v", err)
	}
}

func mustGetImage(t *testing.T, st *fake.Store, name string) imagev1.Image {
	t.Helper()
	raw, err := st.Get(context.Background(), imageKey(name))
	if err != nil {
		t.Fatalf("get image %s: %v", name, err)
	}
	img, err := decodeStoredImage(raw)
	if err != nil {
		t.Fatalf("decode image %s: %v", name, err)
	}
	return img
}

func patchCacheSucceeded(t *testing.T, st *fake.Store, img imagev1.Image, nodeName string) {
	t.Helper()
	observed := taskv1.CacheImageObserved{NodeName: nodeName, ImageName: img.Name, Version: img.Spec.Version, Format: string(img.Spec.Format), CachedPath: "/cache/" + nodeName + "/image", SizeBytes: img.Spec.DeclaredSizeBytes, SHA256: img.Spec.SHA256}
	patchTaskStatus(t, st, imageTaskNameForImage(taskv1.TaskOperationCacheImageNode, img, nodeName), succeededStatus(t, observed))
}

func patchDeleteSucceeded(t *testing.T, st *fake.Store, img imagev1.Image, nodeName string) {
	t.Helper()
	patchDeleteObserved(t, st, img, nodeName, true)
}

func patchDeleteObserved(t *testing.T, st *fake.Store, img imagev1.Image, nodeName string, deleted bool) {
	t.Helper()
	observed := taskv1.DeleteCachedImageObserved{NodeName: nodeName, ImageName: img.Name, Version: img.Spec.Version, Deleted: deleted}
	patchTaskStatus(t, st, imageTaskNameForImage(taskv1.TaskOperationDeleteCachedImageNode, img, nodeName), succeededStatus(t, observed))
}

func patchTaskStatus(t *testing.T, st *fake.Store, name string, status taskv1.TaskStatus) {
	t.Helper()
	client := NewTaskClient(st)
	if _, err := client.PatchStatus(context.Background(), name, status); err != nil {
		t.Fatalf("patch task %s: %v", name, err)
	}
}

func succeededStatus(t *testing.T, observed any) taskv1.TaskStatus {
	t.Helper()
	data, err := json.Marshal(observed)
	if err != nil {
		t.Fatalf("marshal observed: %v", err)
	}
	return taskv1.TaskStatus{Phase: taskv1.TaskPhaseSucceeded, Observed: data, ErrorClass: taskv1.TaskErrorClassNone}
}

func readyCache(img imagev1.Image, nodeName string) imagev1.NodeCacheStatus {
	return imagev1.NodeCacheStatus{NodeName: nodeName, Phase: imagev1.ImageCachePhaseReady, TaskRef: imagev1.TaskRef{Name: imageTaskNameForImage(taskv1.TaskOperationCacheImageNode, img, nodeName), UID: "uid-" + nodeName}, CachedPath: "/cache/" + nodeName + "/image", SizeBytes: img.Spec.DeclaredSizeBytes, SHA256: img.Spec.SHA256}
}

func imageTaskNameForImage(operation taskv1.TaskOperation, img imagev1.Image, nodeName string) string {
	prefix := "image-cache"
	if operation == taskv1.TaskOperationDeleteCachedImageNode {
		prefix = "image-cache-delete"
	}
	return imageTaskName(prefix, img.Name, contentIdentityDigest(operation, img, nodeName), nodeName)
}

func taskNameHashSuffix(name string) string {
	parts := strings.Split(name, "-")
	return parts[len(parts)-1]
}

func imgFromStore(t *testing.T, st *fake.Store, name string) imagev1.Image {
	t.Helper()
	return mustGetImage(t, st, name)
}

func hasFinalizer(img imagev1.Image, finalizer metav1.Finalizer) bool {
	for _, existing := range img.Finalizers {
		if existing == finalizer {
			return true
		}
	}
	return false
}

type recordingImageStore struct{ deleteCalls int }

func (s *recordingImageStore) Put(context.Context, imagestore.PutRequest) (imagestore.ObjectRef, error) {
	return imagestore.ObjectRef{}, nil
}
func (s *recordingImageStore) Get(context.Context, string, string) (imagestore.ObjectRef, error) {
	return imagestore.ObjectRef{}, nil
}
func (s *recordingImageStore) Open(context.Context, string, string) (io.ReadCloser, imagestore.ObjectRef, error) {
	return io.NopCloser(strings.NewReader("")), imagestore.ObjectRef{}, nil
}
func (s *recordingImageStore) Delete(context.Context, string, string, string) error {
	s.deleteCalls++
	return nil
}
