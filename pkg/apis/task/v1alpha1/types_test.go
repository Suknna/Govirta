package v1alpha1

import (
	"encoding/json"
	"errors"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

func TestTaskValidateAcceptsExplicitNodeNoopTask(t *testing.T) {
	task := validTask(t, TaskScopeNode, TaskOperationNoopNode)
	if err := task.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestTaskValidateAcceptsExplicitClusterNoopTask(t *testing.T) {
	task := validTask(t, TaskScopeCluster, TaskOperationNoopCluster)
	if err := task.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestTaskValidateRejectsNodeTaskWithoutNodeName(t *testing.T) {
	task := validTask(t, TaskScopeNode, TaskOperationNoopNode)
	task.NodeName = ""
	err := task.Validate()
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Validate() error = %v, want ErrInvalidTask", err)
	}
}

func TestTaskValidateRejectsClusterTaskWithNodeName(t *testing.T) {
	task := validTask(t, TaskScopeCluster, TaskOperationNoopCluster)
	task.NodeName = "node0"
	err := task.Validate()
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Validate() error = %v, want ErrInvalidTask", err)
	}
}

func TestTaskValidateRejectsMissingNoopInputMarker(t *testing.T) {
	task := validTask(t, TaskScopeNode, TaskOperationNoopNode)
	input, err := json.Marshal(NoopInput{})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	task.Spec.Input = input
	err = task.Validate()
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Validate() error = %v, want ErrInvalidTask", err)
	}
}

func TestTaskValidateRejectsEmptyStatusPhase(t *testing.T) {
	task := validTask(t, TaskScopeNode, TaskOperationNoopNode)
	task.Status.Phase = ""
	err := task.Validate()
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Validate() error = %v, want ErrInvalidTask", err)
	}
}

func TestTaskStatusValidateRequiresSucceededObserved(t *testing.T) {
	status := TaskStatus{Phase: TaskPhaseSucceeded, ErrorClass: TaskErrorClassNone}
	if err := status.Validate(); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Validate() error = %v, want ErrInvalidTask", err)
	}
}

func TestTaskStatusValidateRequiresFailedClassification(t *testing.T) {
	status := TaskStatus{Phase: TaskPhaseFailed, ErrorClass: TaskErrorClassNone, Message: "failed"}
	if err := status.Validate(); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Validate() error = %v, want ErrInvalidTask", err)
	}
	status = TaskStatus{Phase: TaskPhaseFailed, ErrorClass: TaskErrorClassExecutionFailed}
	if err := status.Validate(); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Validate() error = %v, want ErrInvalidTask", err)
	}
}

func TestTaskJSONRoundTripPreservesEnvelope(t *testing.T) {
	in := validTask(t, TaskScopeNode, TaskOperationNoopNode)
	in.ResourceVersion = "42"
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("raw unmarshal: %v", err)
	}
	for _, key := range []string{"apiVersion", "kind", "metadata", "spec", "status"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("missing top-level key %q in %s", key, b)
		}
	}

	var out Task
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Kind != metav1.KindTask || out.Name != in.Name || out.UID != in.UID || out.ResourceVersion != "42" {
		t.Fatalf("identity mismatch: %+v", out)
	}
	if out.Spec.Scope != TaskScopeNode || out.Spec.Operation != TaskOperationNoopNode {
		t.Fatalf("spec mismatch: %+v", out.Spec)
	}
	if out.Status.Phase != TaskPhasePending {
		t.Fatalf("status mismatch: %+v", out.Status)
	}
}

func validTask(t *testing.T, scope TaskScope, operation TaskOperation) Task {
	t.Helper()
	input, err := json.Marshal(NoopInput{Marker: "phase-one"})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	meta := metav1.ObjectMeta{
		Name:     "task-phase-one",
		UID:      "task-phase-one-uid",
		NodeName: "node0",
	}
	if scope == TaskScopeCluster {
		meta.NodeName = ""
	}
	return Task{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindTask},
		ObjectMeta: meta,
		Spec: TaskSpec{
			Scope:     scope,
			OwnerKind: metav1.KindTask,
			OwnerName: "phase-one-owner",
			OwnerUID:  "phase-one-owner-uid",
			Operation: operation,
			Input:     input,
		},
		Status: TaskStatus{Phase: TaskPhasePending},
	}
}
