package controllers

import (
	"context"
	"encoding/json"
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

func TestTaskControllerRejectsWrongNode(t *testing.T) {
	reporter := &fakeTaskReporter{}
	c := NewTaskController("node-1", reporter)
	task := validNodeTaskForController(t, "node-task", "node-2", taskv1.TaskOperationNoopNode)

	res, err := c.Reconcile(context.Background(), taskEvent(t, task))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.ShouldRequeue() || len(reporter.statuses) != 1 {
		t.Fatalf("wrong-node result=%+v statuses=%d, want one failed status", res, len(reporter.statuses))
	}
	if reporter.statuses[0].Phase != taskv1.TaskPhaseFailed || reporter.statuses[0].ErrorClass != taskv1.TaskErrorClassInvalidInput {
		t.Fatalf("status = %+v, want invalid-input failed", reporter.statuses[0])
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
