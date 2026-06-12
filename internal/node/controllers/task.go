package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/node/controller"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

// TaskStatusReporter is the narrow Task status patch boundary.
type TaskStatusReporter interface {
	PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error)
}

// TaskController executes node-scoped internal Tasks assigned to this govirtlet.
type TaskController struct {
	nodeName string
	client   TaskStatusReporter
}

var _ controller.Controller = (*TaskController)(nil)

// NewTaskController wires a TaskController for one explicit node identity.
func NewTaskController(nodeName string, client TaskStatusReporter) *TaskController {
	return &TaskController{nodeName: nodeName, client: client}
}

// Kind is the API kind this controller watches.
func (c *TaskController) Kind() string {
	return string(metav1.KindTask)
}

// Reconcile executes a node-scoped no-op Task and reports status. Cluster Tasks
// are control-plane owned and must not be executed by govirtlet.
func (c *TaskController) Reconcile(ctx context.Context, ev controller.Event) (controller.ReconcileResult, error) {
	if err := ctx.Err(); err != nil {
		return controller.Done(), fmt.Errorf("task controller: context done before reconcile: %w", err)
	}
	if ev.Type == controller.EventDeleted {
		return controller.Done(), nil
	}

	var task taskv1.Task
	if err := json.Unmarshal(ev.Object, &task); err != nil {
		return controller.Done(), fmt.Errorf("task controller: decode object %q: %w", ev.Key, err)
	}
	if task.Status.Phase == taskv1.TaskPhaseRunning || task.Status.Phase.Terminal() {
		return controller.Done(), nil
	}
	if task.Spec.Scope != taskv1.TaskScopeNode || task.NodeName != c.nodeName {
		return c.patchFailed(ctx, task.Name, taskv1.TaskErrorClassInvalidInput, "task is not assigned to this node")
	}
	if task.Spec.Operation != taskv1.TaskOperationNoopNode {
		return c.patchFailed(ctx, task.Name, taskv1.TaskErrorClassUnsupportedOperation, "unsupported node task operation")
	}
	var input taskv1.NoopInput
	if err := json.Unmarshal(task.Spec.Input, &input); err != nil || input.Marker == "" {
		return c.patchFailed(ctx, task.Name, taskv1.TaskErrorClassInvalidInput, "invalid noop input")
	}

	logger := zerolog.Ctx(ctx)
	logger.Info().Str("component", "task-executor").Str("operation", string(task.Spec.Operation)).Str("node_id", c.nodeName).Msg("node task started")
	start := time.Now().UTC().Format(time.RFC3339)
	if err := c.patchStatus(ctx, task.Name, taskv1.TaskStatus{Phase: taskv1.TaskPhaseRunning, ErrorClass: taskv1.TaskErrorClassNone, StartedAt: start}); err != nil {
		return controller.Requeue(), fmt.Errorf("task controller: patch running status for %q: %w", task.Name, err)
	}
	observed, err := json.Marshal(taskv1.NoopObserved{Executor: c.nodeName, Marker: input.Marker})
	if err != nil {
		return controller.Done(), fmt.Errorf("task controller: marshal observed for %q: %w", task.Name, err)
	}
	completed := time.Now().UTC().Format(time.RFC3339)
	if err := c.patchStatus(ctx, task.Name, taskv1.TaskStatus{Phase: taskv1.TaskPhaseSucceeded, Observed: observed, ErrorClass: taskv1.TaskErrorClassNone, StartedAt: start, CompletedAt: completed}); err != nil {
		return controller.Requeue(), fmt.Errorf("task controller: patch succeeded status for %q: %w", task.Name, err)
	}
	logger.Info().Str("component", "task-executor").Str("operation", string(task.Spec.Operation)).Str("node_id", c.nodeName).Str("outcome", "success").Msg("node task completed")
	return controller.Done(), nil
}

func (c *TaskController) patchFailed(ctx context.Context, name string, class taskv1.TaskErrorClass, message string) (controller.ReconcileResult, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if err := c.patchStatus(ctx, name, taskv1.TaskStatus{Phase: taskv1.TaskPhaseFailed, ErrorClass: class, Message: message, StartedAt: now, CompletedAt: now}); err != nil {
		return controller.Requeue(), fmt.Errorf("task controller: patch failed status for %q: %w", name, err)
	}
	return controller.Done(), nil
}

func (c *TaskController) patchStatus(ctx context.Context, name string, status taskv1.TaskStatus) error {
	body, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("task controller: marshal status for %q: %w", name, err)
	}
	if _, err := c.client.PatchStatus(ctx, c.Kind(), name, body); err != nil {
		return err
	}
	return nil
}
