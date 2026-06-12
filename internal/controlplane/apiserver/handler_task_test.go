package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/suknna/govirta/internal/controlplane/store"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

func TestTaskApplyIsRejectedAsInternalResource(t *testing.T) {
	srv, _ := newTestServer(t)
	task := validTask(t, "node-task", "node-1", taskv1.TaskScopeNode, taskv1.TaskOperationNoopNode)

	rec := doApplyWithoutReferenceSeeds(t, srv, metav1.KindTask, task.Name, task)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestTaskReplaceIsRejectedAsInternalResource(t *testing.T) {
	srv, _ := newTestServer(t)
	task := validTask(t, "node-task", "node-1", taskv1.TaskScopeNode, taskv1.TaskOperationNoopNode)

	rec := doReplace(t, srv, metav1.KindTask, task.Name, task)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestTaskDeleteAndFinalizersAreRejectedAsInternalResource(t *testing.T) {
	srv, st := newTestServer(t)
	task := validTask(t, "node-task", "node-1", taskv1.TaskScopeNode, taskv1.TaskOperationNoopNode)
	seedStoreObject(t, st, metav1.KindTask, task.Name, task)

	deleteRec := doDelete(t, srv, metav1.KindTask, task.Name)
	if deleteRec.Code != http.StatusForbidden {
		t.Fatalf("delete status = %d, want 403; body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	finalizersRec := doPatchFinalizers(t, srv, metav1.KindTask, task.Name, "govirta.io/node-teardown")
	if finalizersRec.Code != http.StatusForbidden {
		t.Fatalf("finalizers status = %d, want 403; body=%s", finalizersRec.Code, finalizersRec.Body.String())
	}
	raw, err := st.Get(context.Background(), storeKey(metav1.KindTask, task.Name))
	if err != nil {
		t.Fatalf("get task after rejected delete/finalizers: %v", err)
	}
	var got taskv1.Task
	if err := json.Unmarshal(raw.Value, &got); err != nil {
		t.Fatalf("decode stored task: %v", err)
	}
	if got.DeletionTimestamp != "" || len(got.Finalizers) != 0 {
		t.Fatalf("task metadata mutated by rejected delete/finalizers: %+v", got.ObjectMeta)
	}
}

func TestTaskWatchDeliversOnlyMatchingNodeTask(t *testing.T) {
	srv, st := newTestServer(t)
	const watchedNode = "node-1"

	resp, cancel := startWatch(t, srv, "/apis/"+string(metav1.KindTask)+"?watch=true&nodeName="+watchedNode)
	defer cancel()
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close watch body: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	clusterTask := validTask(t, "cluster-task", "", taskv1.TaskScopeCluster, taskv1.TaskOperationNoopCluster)
	seedStoreObject(t, st, metav1.KindTask, clusterTask.Name, clusterTask)
	otherNodeTask := validTask(t, "other-node-task", "node-2", taskv1.TaskScopeNode, taskv1.TaskOperationNoopNode)
	seedStoreObject(t, st, metav1.KindTask, otherNodeTask.Name, otherNodeTask)
	watchedTask := validTask(t, "watched-node-task", watchedNode, taskv1.TaskScopeNode, taskv1.TaskOperationNoopNode)
	seedStoreObject(t, st, metav1.KindTask, watchedTask.Name, watchedTask)

	dec := json.NewDecoder(resp.Body)
	ev := readWatchLine(t, dec)
	if ev.Type != store.EventAdded {
		t.Fatalf("event type = %q, want %q", ev.Type, store.EventAdded)
	}
	var got taskv1.Task
	if err := json.Unmarshal(ev.Object, &got); err != nil {
		t.Fatalf("decode task event: %v", err)
	}
	if got.Name != watchedTask.Name || got.NodeName != watchedNode {
		t.Fatalf("delivered task = %s/%s, want %s/%s", got.Name, got.NodeName, watchedTask.Name, watchedNode)
	}
	if got.ResourceVersion == "" {
		t.Fatalf("delivered task resourceVersion is empty; watch resume requires injected resourceVersion")
	}
}

func TestTaskStatusPatchUpdatesInternalTask(t *testing.T) {
	srv, st := newTestServer(t)
	task := validTask(t, "node-task", "node-1", taskv1.TaskScopeNode, taskv1.TaskOperationNoopNode)
	seedStoreObject(t, st, metav1.KindTask, task.Name, task)

	status := taskv1.TaskStatus{Phase: taskv1.TaskPhaseSucceeded, Observed: json.RawMessage(`{"executor":"node-1","marker":"phase-one"}`), ErrorClass: taskv1.TaskErrorClassNone}
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	rec := doPatchStatus(t, srv, metav1.KindTask, task.Name, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	raw, err := st.Get(context.Background(), storeKey(metav1.KindTask, task.Name))
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	var got taskv1.Task
	if err := json.Unmarshal(raw.Value, &got); err != nil {
		t.Fatalf("decode stored task: %v", err)
	}
	if got.Status.Phase != taskv1.TaskPhaseSucceeded {
		t.Fatalf("stored phase = %q, want %q", got.Status.Phase, taskv1.TaskPhaseSucceeded)
	}
	if got.Spec.Operation != taskv1.TaskOperationNoopNode {
		t.Fatalf("spec operation changed: %+v", got.Spec)
	}
}
