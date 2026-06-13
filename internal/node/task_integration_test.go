package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/controlplane/apiserver"
	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
	controlcontroller "github.com/suknna/govirta/internal/controlplane/controller"
	"github.com/suknna/govirta/internal/controlplane/imagestore"
	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	"github.com/suknna/govirta/internal/node/client"
	nodectrl "github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/node/controllers"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

func TestTaskWatchExecutorPatchStatusClosure(t *testing.T) {
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	pool, err := mac.NewPool([]byte{0x02, 0x00, 0x00}, 0, 255)
	if err != nil {
		t.Fatalf("new mac pool: %v", err)
	}
	srv := apiserver.NewServer(apiserver.ServerConfig{Store: st, MACAllocator: mac.NewAllocator(pool, st), Scheduler: scheduler.NewNoopScheduler(), NodeNames: []string{"node-1"}, ListenAddr: "127.0.0.1:0", ImageStorePublicURL: "http://images.example"})
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)

	taskMgr := controlcontroller.NewTaskClient(st)
	if _, err := taskMgr.CreateOrGetTask(context.Background(), phaseOneNodeTask(t, "phase-one-node-task", "node-1")); err != nil {
		t.Fatalf("create node task: %v", err)
	}

	watchCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	source := client.NewWatchSource(httpSrv.URL, httpSrv.Client(), "node-1")
	events, err := source.Watch(watchCtx, string(metav1.KindTask), "0")
	if err != nil {
		t.Fatalf("watch task: %v", err)
	}
	var ev nodectrl.Event
	select {
	case ev = <-events:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for task watch event")
	}

	executor := controllers.NewTaskController("node-1", client.New(httpSrv.URL, httpSrv.Client()))
	if _, err := executor.Reconcile(context.Background(), ev); err != nil {
		t.Fatalf("reconcile task: %v", err)
	}

	stored := mustGetStoredTask(t, st, "phase-one-node-task")
	if stored.Status.Phase != taskv1.TaskPhaseSucceeded {
		t.Fatalf("stored phase = %q, want %q", stored.Status.Phase, taskv1.TaskPhaseSucceeded)
	}
	var observed taskv1.NoopObserved
	if err := json.Unmarshal(stored.Status.Observed, &observed); err != nil {
		t.Fatalf("decode observed: %v", err)
	}
	if observed.Executor != "node-1" || observed.Marker != "phase-one" {
		t.Fatalf("observed = %+v, want node-1/phase-one", observed)
	}
}

func TestDistributedImageCacheTaskIntegration(t *testing.T) {
	ctx := context.Background()
	imageBytes := []byte("distributed image cache bytes")
	imageSHA := sha256Hex(imageBytes)
	imageSource := httptest.NewServer(httpBytesHandler(t, imageBytes))
	t.Cleanup(imageSource.Close)

	st := newIntegrationStore(t)
	api := newIntegrationAPIServer(t, st, "node-1")
	cacheRoot := filepath.Join(t.TempDir(), "image-cache")
	cache, err := controllers.NewImageCache(cacheRoot)
	if err != nil {
		t.Fatalf("new image cache: %v", err)
	}
	imageController := controlcontroller.NewImageController(st, &integrationImageStore{}, controlcontroller.ImageControllerConfig{NodeNames: []string{"node-1"}, CacheRoot: cache.Root(), SyncPeriod: time.Second})
	seedIntegrationImage(t, st, integrationImage(imageSource.URL, int64(len(imageBytes)), imageSHA))

	if err := imageController.Reconcile(ctx); err != nil {
		t.Fatalf("initial image reconcile: %v", err)
	}
	task := mustGetOnlyImageTask(t, st, taskv1.TaskOperationCacheImageNode)
	assertCacheTaskInput(t, task, imageSource.URL, cache.Root(), int64(len(imageBytes)), imageSHA)

	watchCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	events, err := client.NewWatchSource(api.URL, api.Client(), "node-1").Watch(watchCtx, string(metav1.KindTask), "0")
	if err != nil {
		t.Fatalf("watch tasks: %v", err)
	}
	executor := controllers.NewTaskControllerWithImageCache("node-1", client.New(api.URL, api.Client()), cache, imageSource.Client())
	if _, err := executor.Reconcile(ctx, waitForTaskEvent(t, events, task.Name)); err != nil {
		t.Fatalf("execute cache task: %v", err)
	}

	storedTask := mustGetStoredTask(t, st, task.Name)
	if storedTask.Status.Phase != taskv1.TaskPhaseSucceeded {
		t.Fatalf("cache task phase = %q, want %q", storedTask.Status.Phase, taskv1.TaskPhaseSucceeded)
	}
	observed, err := taskv1.DecodeCacheImageObserved(storedTask.Status.Observed)
	if err != nil {
		t.Fatalf("decode cache observed: %v", err)
	}
	if observed.NodeName != "node-1" || observed.ImageName != "cirros" || observed.SizeBytes != int64(len(imageBytes)) || observed.SHA256 != imageSHA {
		t.Fatalf("cache observed = %+v, want node-1/cirros size+sha", observed)
	}
	assertCachedFile(t, observed.CachedPath, imageBytes, imageSHA)

	if err := imageController.Reconcile(ctx); err != nil {
		t.Fatalf("ready image reconcile: %v", err)
	}
	img := mustGetIntegrationImage(t, st, "cirros")
	if img.Status.Phase != imagev1.ImagePhaseReady || len(img.Status.NodeCaches) != 1 {
		t.Fatalf("image status = %+v, want one ready node cache", img.Status)
	}
	cacheStatus := img.Status.NodeCaches[0]
	if cacheStatus.Phase != imagev1.ImageCachePhaseReady || cacheStatus.CachedPath != observed.CachedPath || cacheStatus.SHA256 != imageSHA || cacheStatus.SizeBytes != int64(len(imageBytes)) {
		t.Fatalf("node cache status = %+v, want observed ready cache", cacheStatus)
	}
	if cacheStatus.TaskRef.Name != storedTask.Name || cacheStatus.TaskRef.UID != storedTask.UID {
		t.Fatalf("task ref = %+v, want %s/%s", cacheStatus.TaskRef, storedTask.Name, storedTask.UID)
	}

	markIntegrationImageDeleting(t, st, img)
	if err := imageController.Reconcile(ctx); err != nil {
		t.Fatalf("delete image reconcile: %v", err)
	}
	deleteTask := mustGetOnlyImageTask(t, st, taskv1.TaskOperationDeleteCachedImageNode)
	if _, err := executor.Reconcile(ctx, waitForTaskEvent(t, events, deleteTask.Name)); err != nil {
		t.Fatalf("execute delete task: %v", err)
	}
	deleteStored := mustGetStoredTask(t, st, deleteTask.Name)
	if deleteStored.Status.Phase != taskv1.TaskPhaseSucceeded {
		t.Fatalf("delete task phase = %q, want %q", deleteStored.Status.Phase, taskv1.TaskPhaseSucceeded)
	}
	if _, err := os.Stat(observed.CachedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cached file stat err = %v, want not exist", err)
	}
	if err := imageController.Reconcile(ctx); err != nil {
		t.Fatalf("delete completion reconcile: %v", err)
	}
	deleted := mustGetIntegrationImage(t, st, "cirros")
	if hasIntegrationFinalizer(deleted, metav1.FinalizerImageCache) {
		t.Fatalf("finalizers = %v, want image cache finalizer removed", deleted.Finalizers)
	}
}

func phaseOneNodeTask(t *testing.T, name, nodeName string) taskv1.Task {
	t.Helper()
	input, err := json.Marshal(taskv1.NoopInput{Marker: "phase-one"})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return taskv1.Task{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindTask},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name, NodeName: nodeName},
		Spec:       taskv1.TaskSpec{Scope: taskv1.TaskScopeNode, OwnerKind: metav1.KindTask, OwnerName: "phase-one-owner", OwnerUID: "phase-one-owner-uid", Operation: taskv1.TaskOperationNoopNode, Input: input},
		Status:     taskv1.TaskStatus{Phase: taskv1.TaskPhasePending},
	}
}

func mustGetStoredTask(t *testing.T, st *fake.Store, name string) taskv1.Task {
	t.Helper()
	raw, err := st.Get(context.Background(), admission.StoreKey(metav1.KindTask, name))
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	var task taskv1.Task
	if err := json.Unmarshal(raw.Value, &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return task
}

func newIntegrationStore(t *testing.T) *fake.Store {
	t.Helper()
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return st
}

func newIntegrationAPIServer(t *testing.T, st *fake.Store, nodeName string) *httptest.Server {
	t.Helper()
	pool, err := mac.NewPool([]byte{0x02, 0x00, 0x00}, 0, 255)
	if err != nil {
		t.Fatalf("new mac pool: %v", err)
	}
	srv := apiserver.NewServer(apiserver.ServerConfig{Store: st, MACAllocator: mac.NewAllocator(pool, st), Scheduler: scheduler.NewNoopScheduler(), NodeNames: []string{nodeName}, ListenAddr: "127.0.0.1:0", ImageStorePublicURL: "http://images.example"})
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)
	return httpSrv
}

func httpBytesHandler(t *testing.T, body []byte) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			t.Errorf("write image bytes: %v", err)
		}
	}
}

func integrationImage(source string, size int64, sha string) imagev1.Image {
	return imagev1.Image{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindImage},
		ObjectMeta: metav1.ObjectMeta{Name: "cirros", UID: "image-uid"},
		Spec:       imagev1.ImageSpec{Source: imagev1.ImageSource{Type: imagev1.ImageSourceHTTP, Location: source}, Format: imagev1.ImageFormatQCOW2, Version: "v1", DeclaredSizeBytes: size, SHA256: sha},
		Status:     imagev1.ImageStatus{Phase: imagev1.ImagePhasePending},
	}
}

func seedIntegrationImage(t *testing.T, st *fake.Store, image imagev1.Image) {
	t.Helper()
	data, err := json.Marshal(image)
	if err != nil {
		t.Fatalf("marshal image: %v", err)
	}
	if _, err := st.Put(context.Background(), admission.StoreKey(metav1.KindImage, image.Name), data, ""); err != nil {
		t.Fatalf("seed image: %v", err)
	}
}

func mustGetIntegrationImage(t *testing.T, st *fake.Store, name string) imagev1.Image {
	t.Helper()
	raw, err := st.Get(context.Background(), admission.StoreKey(metav1.KindImage, name))
	if err != nil {
		t.Fatalf("get image %s: %v", name, err)
	}
	var image imagev1.Image
	if err := json.Unmarshal(raw.Value, &image); err != nil {
		t.Fatalf("decode image %s: %v", name, err)
	}
	return image
}

func markIntegrationImageDeleting(t *testing.T, st *fake.Store, img imagev1.Image) {
	t.Helper()
	img.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(img)
	if err != nil {
		t.Fatalf("marshal deleting image: %v", err)
	}
	raw, err := st.Get(context.Background(), admission.StoreKey(metav1.KindImage, img.Name))
	if err != nil {
		t.Fatalf("get image before delete mark: %v", err)
	}
	if _, err := st.Put(context.Background(), admission.StoreKey(metav1.KindImage, img.Name), data, raw.ResourceVersion); err != nil {
		t.Fatalf("mark image deleting: %v", err)
	}
}

func mustGetOnlyImageTask(t *testing.T, st *fake.Store, operation taskv1.TaskOperation) taskv1.Task {
	t.Helper()
	raws, err := st.List(context.Background(), admission.ListPrefix(metav1.KindTask))
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	var matched []taskv1.Task
	for _, raw := range raws {
		var task taskv1.Task
		if err := json.Unmarshal(raw.Value, &task); err != nil {
			t.Fatalf("decode task %s: %v", raw.Key, err)
		}
		if task.Spec.Operation == operation {
			matched = append(matched, task)
		}
	}
	if len(matched) != 1 {
		t.Fatalf("matched %d tasks for operation %s, want 1", len(matched), operation)
	}
	return matched[0]
}

func waitForTaskEvent(t *testing.T, events <-chan nodectrl.Event, taskName string) nodectrl.Event {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("task watch closed before %s", taskName)
			}
			if ev.Key == taskName {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for task %s watch event", taskName)
		}
	}
}

func assertCacheTaskInput(t *testing.T, task taskv1.Task, source string, cacheRoot string, size int64, sha string) {
	t.Helper()
	if task.Spec.Operation != taskv1.TaskOperationCacheImageNode || task.NodeName != "node-1" || task.Spec.OwnerKind != metav1.KindImage || task.Spec.OwnerName != "cirros" || task.Spec.OwnerUID != "image-uid" {
		t.Fatalf("cache task = %+v, want image-owned node cache task", task)
	}
	var input taskv1.CacheImageInput
	if err := json.Unmarshal(task.Spec.Input, &input); err != nil {
		t.Fatalf("decode cache input: %v", err)
	}
	if input.ImageName != "cirros" || input.ImageUID != "image-uid" || input.Version != "v1" || input.Source.Location != source || input.CacheRoot != cacheRoot || input.DeclaredSizeBytes != size || input.SHA256 != sha {
		t.Fatalf("cache input = %+v, want explicit image cache input", input)
	}
}

func assertCachedFile(t *testing.T, path string, want []byte, wantSHA string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cached file: %v", err)
	}
	if string(data) != string(want) || sha256Hex(data) != wantSHA {
		t.Fatalf("cached file content/sha mismatch: len=%d sha=%s", len(data), sha256Hex(data))
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hasIntegrationFinalizer(img imagev1.Image, finalizer metav1.Finalizer) bool {
	for _, existing := range img.Finalizers {
		if existing == finalizer {
			return true
		}
	}
	return false
}

type integrationImageStore struct{}

func (s *integrationImageStore) Put(context.Context, imagestore.PutRequest) (imagestore.ObjectRef, error) {
	return imagestore.ObjectRef{}, nil
}
func (s *integrationImageStore) Get(context.Context, string, string) (imagestore.ObjectRef, error) {
	return imagestore.ObjectRef{}, nil
}
func (s *integrationImageStore) Open(context.Context, string, string) (io.ReadCloser, imagestore.ObjectRef, error) {
	return io.NopCloser(strings.NewReader("")), imagestore.ObjectRef{}, nil
}
func (s *integrationImageStore) Delete(context.Context, string, string, string) error {
	return nil
}
