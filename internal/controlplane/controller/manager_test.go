package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/controlplane/store/fake"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

func TestManagerCreatesNodeTaskAndCompletesClusterTask(t *testing.T) {
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	mgr := NewManager(st, testConfig())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- mgr.Run(ctx) }()

	select {
	case <-mgr.Ready():
	case <-time.After(2 * time.Second):
		t.Fatalf("manager did not become ready")
	}

	nodeTask := mustGetTask(t, st, "phase-one-node-task")
	if nodeTask.Spec.Scope != taskv1.TaskScopeNode || nodeTask.NodeName != "node-1" || nodeTask.Status.Phase != taskv1.TaskPhasePending {
		t.Fatalf("node task mismatch: %+v", nodeTask)
	}
	clusterTask := mustGetTask(t, st, "phase-one-cluster-task")
	if clusterTask.Spec.Scope != taskv1.TaskScopeCluster || clusterTask.NodeName != "" || clusterTask.Status.Phase != taskv1.TaskPhaseSucceeded {
		t.Fatalf("cluster task mismatch: %+v", clusterTask)
	}
	if len(clusterTask.Status.Observed) == 0 || clusterTask.Status.ErrorClass != taskv1.TaskErrorClassNone {
		t.Fatalf("cluster task status missing observed/no-error result: %+v", clusterTask.Status)
	}

	client := NewTaskClient(st)
	if _, err := client.PatchStatus(context.Background(), nodeTask.Name, succeededNodeTaskStatus(t)); err != nil {
		t.Fatalf("patch node task: %v", err)
	}
	select {
	case <-mgr.NodeTaskTerminal():
	case <-time.After(2 * time.Second):
		t.Fatalf("manager did not observe terminal node task")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("manager did not return after terminal node task")
	}
}

func TestManagerObservesNodeTaskAlreadyTerminalBeforeWatch(t *testing.T) {
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	mgr := NewManager(st, testConfig())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- mgr.Run(ctx) }()

	select {
	case <-mgr.Ready():
	case <-time.After(2 * time.Second):
		t.Fatalf("manager did not become ready")
	}
	nodeTask := mustGetTask(t, st, "phase-one-node-task")
	client := NewTaskClient(st)
	if _, err := client.PatchStatus(context.Background(), nodeTask.Name, succeededNodeTaskStatus(t)); err != nil {
		t.Fatalf("patch node task: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("manager did not return after node task was terminal")
	}
}

func TestTaskClientRejectsExistingTaskWithDifferentSpec(t *testing.T) {
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	mgr := NewManager(st, testConfig())
	desired, err := mgr.newNodeTask()
	if err != nil {
		t.Fatalf("new node task: %v", err)
	}
	client := NewTaskClient(st)
	if _, err := client.CreateOrGetTask(context.Background(), desired); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	drifted := desired
	drifted.NodeName = "node-2"
	if _, err := client.CreateOrGetTask(context.Background(), drifted); err == nil {
		t.Fatalf("CreateOrGetTask() with drifted explicit spec: want error, got nil")
	}
}

func testConfig() Config {
	return Config{NodeTaskName: "phase-one-node-task", NodeTaskNode: "node-1", ClusterTaskName: "phase-one-cluster-task", OwnerName: "phase-one-owner", OwnerUID: "phase-one-owner-uid", ExecutorID: "govirtad-test", NoopMarker: "phase-one"}
}

func mustGetTask(t *testing.T, st *fake.Store, name string) taskv1.Task {
	t.Helper()
	raw, err := st.Get(context.Background(), taskKey(name))
	if err != nil {
		t.Fatalf("get task %s: %v", name, err)
	}
	var task taskv1.Task
	if err := json.Unmarshal(raw.Value, &task); err != nil {
		t.Fatalf("decode task %s: %v", name, err)
	}
	if task.Kind != metav1.KindTask {
		t.Fatalf("kind = %q, want %q", task.Kind, metav1.KindTask)
	}
	return task
}

func succeededNodeTaskStatus(t *testing.T) taskv1.TaskStatus {
	t.Helper()
	observed, err := json.Marshal(taskv1.NoopObserved{Executor: "node-1", Marker: "phase-one"})
	if err != nil {
		t.Fatalf("marshal observed: %v", err)
	}
	return taskv1.TaskStatus{Phase: taskv1.TaskPhaseSucceeded, Observed: observed, ErrorClass: taskv1.TaskErrorClassNone}
}
