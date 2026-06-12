package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

// TaskExecutor executes a Task and returns the observed status to persist.
type TaskExecutor interface {
	Execute(ctx context.Context, task taskv1.Task) (taskv1.TaskStatus, error)
}

// NoopClusterExecutor is the phase-one control-plane ClusterTask executor.
type NoopClusterExecutor struct {
	ExecutorID string
	NoopMarker string
}

// Execute completes only the explicit NoopCluster operation.
func (e NoopClusterExecutor) Execute(ctx context.Context, task taskv1.Task) (taskv1.TaskStatus, error) {
	if err := ctx.Err(); err != nil {
		return taskv1.TaskStatus{}, err
	}
	if task.Spec.Scope != taskv1.TaskScopeCluster || task.Spec.Operation != taskv1.TaskOperationNoopCluster {
		return failedStatus(taskv1.TaskErrorClassUnsupportedOperation, "unsupported cluster task"), nil
	}
	observed, err := json.Marshal(taskv1.NoopObserved{Executor: e.ExecutorID, Marker: e.NoopMarker})
	if err != nil {
		return taskv1.TaskStatus{}, fmt.Errorf("controlplane controller: marshal noop observed: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return taskv1.TaskStatus{
		Phase:       taskv1.TaskPhaseSucceeded,
		Observed:    observed,
		ErrorClass:  taskv1.TaskErrorClassNone,
		StartedAt:   now,
		CompletedAt: now,
	}, nil
}

func failedStatus(class taskv1.TaskErrorClass, message string) taskv1.TaskStatus {
	now := time.Now().UTC().Format(time.RFC3339)
	return taskv1.TaskStatus{Phase: taskv1.TaskPhaseFailed, ErrorClass: class, Message: message, StartedAt: now, CompletedAt: now}
}
