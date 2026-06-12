package node

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/controlplane/apiserver"
	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
	controlcontroller "github.com/suknna/govirta/internal/controlplane/controller"
	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	"github.com/suknna/govirta/internal/node/client"
	nodectrl "github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/node/controllers"
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
	srv := apiserver.NewServer(st, mac.NewAllocator(pool, st), scheduler.NewNoopScheduler(), []string{"node-1"}, "127.0.0.1:0")
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
