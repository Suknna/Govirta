package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
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
	nodeName   string
	client     TaskStatusReporter
	cache      *ImageCache
	httpClient *http.Client
}

var _ controller.Controller = (*TaskController)(nil)

// NewTaskController wires a TaskController for one explicit node identity.
func NewTaskController(nodeName string, client TaskStatusReporter) *TaskController {
	return NewTaskControllerWithImageCache(nodeName, client, nil, nil)
}

// NewTaskControllerWithImageCache wires image-cache task support into the controller.
func NewTaskControllerWithImageCache(nodeName string, client TaskStatusReporter, cache *ImageCache, httpClient *http.Client) *TaskController {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &TaskController{nodeName: nodeName, client: client, cache: cache, httpClient: httpClient}
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
		return controller.Done(), nil
	}

	executor, err := c.executorFor(task.Spec.Operation)
	if err != nil {
		return c.patchFailed(ctx, task.Name, taskv1.TaskErrorClassUnsupportedOperation, "unsupported node task operation")
	}

	logger := zerolog.Ctx(ctx)
	logger.Info().Str("component", "task-executor").Str("operation", string(task.Spec.Operation)).Str("node_id", c.nodeName).Msg("node task started")
	start := time.Now().UTC().Format(time.RFC3339)
	if err := c.patchStatus(ctx, task.Name, taskv1.TaskStatus{Phase: taskv1.TaskPhaseRunning, ErrorClass: taskv1.TaskErrorClassNone, Message: "node task started", StartedAt: start}); err != nil {
		return controller.Requeue(), fmt.Errorf("task controller: patch running status for %q: %w", task.Name, err)
	}
	observed, err := executor(ctx, task)
	if err != nil {
		failedResult, failErr := c.patchFailed(ctx, task.Name, classifyTaskError(err), err.Error())
		if failErr != nil {
			return failedResult, failErr
		}
		return controller.Done(), nil
	}
	observedBody, err := json.Marshal(observed)
	if err != nil {
		return controller.Done(), fmt.Errorf("task controller: marshal observed for %q: %w", task.Name, err)
	}
	completed := time.Now().UTC().Format(time.RFC3339)
	if err := c.patchStatus(ctx, task.Name, taskv1.TaskStatus{Phase: taskv1.TaskPhaseSucceeded, Observed: observedBody, ErrorClass: taskv1.TaskErrorClassNone, StartedAt: start, CompletedAt: completed}); err != nil {
		return controller.Requeue(), fmt.Errorf("task controller: patch succeeded status for %q: %w", task.Name, err)
	}
	logger.Info().Str("component", "task-executor").Str("operation", string(task.Spec.Operation)).Str("node_id", c.nodeName).Str("outcome", "success").Msg("node task completed")
	return controller.Done(), nil
}

type taskExecutor func(context.Context, taskv1.Task) (any, error)

func (c *TaskController) executorFor(operation taskv1.TaskOperation) (taskExecutor, error) {
	switch operation {
	case taskv1.TaskOperationNoopNode:
		return c.executeNoop, nil
	case taskv1.TaskOperationCacheImageNode:
		if c.cache == nil {
			return nil, fmt.Errorf("task controller: image cache is not configured")
		}
		return c.executeCacheImage, nil
	case taskv1.TaskOperationDeleteCachedImageNode:
		if c.cache == nil {
			return nil, fmt.Errorf("task controller: image cache is not configured")
		}
		return c.executeDeleteCachedImage, nil
	default:
		return nil, fmt.Errorf("task controller: unsupported node task operation %q", operation)
	}
}

func (c *TaskController) executeNoop(_ context.Context, task taskv1.Task) (any, error) {
	var input taskv1.NoopInput
	if err := json.Unmarshal(task.Spec.Input, &input); err != nil {
		return nil, fmt.Errorf("%w: decode noop input: %v", errTaskInvalidInput, err)
	}
	if input.Marker == "" {
		return nil, fmt.Errorf("%w: noop marker is required", errTaskInvalidInput)
	}
	return taskv1.NoopObserved{Executor: c.nodeName, Marker: input.Marker}, nil
}

func (c *TaskController) executeCacheImage(ctx context.Context, task taskv1.Task) (any, error) {
	var input taskv1.CacheImageInput
	if err := json.Unmarshal(task.Spec.Input, &input); err != nil {
		return nil, fmt.Errorf("decode cache image input: %w", err)
	}
	if err := input.Validate(); err != nil {
		return nil, err
	}
	if err := c.requireLocalCacheRoot(input.CacheRoot); err != nil {
		return nil, err
	}
	if _, _, err := c.cache.cachePaths(input.ImageName, input.Version); err != nil {
		return nil, err
	}
	reader, err := c.openImageTaskSource(ctx, input.Source)
	if err != nil {
		return nil, err
	}
	observed, cacheErr := c.cache.Cache(ctx, c.nodeName, input, reader)
	closeErr := reader.Close()
	if cacheErr != nil || closeErr != nil {
		if closeErr != nil {
			closeErr = fmt.Errorf("%w: close image source body: %v", errTaskTransientIO, closeErr)
		}
		return nil, errors.Join(cacheErr, closeErr)
	}
	return observed, nil
}

func (c *TaskController) executeDeleteCachedImage(ctx context.Context, task taskv1.Task) (any, error) {
	var input taskv1.DeleteCachedImageInput
	if err := json.Unmarshal(task.Spec.Input, &input); err != nil {
		return nil, fmt.Errorf("decode delete cached image input: %w", err)
	}
	if err := input.Validate(); err != nil {
		return nil, err
	}
	if err := c.requireLocalCacheRoot(input.CacheRoot); err != nil {
		return nil, err
	}
	return c.cache.Delete(ctx, c.nodeName, input)
}

func classifyTaskError(err error) taskv1.TaskErrorClass {
	switch {
	case errors.Is(err, errTaskInvalidInput):
		return taskv1.TaskErrorClassInvalidInput
	case errors.Is(err, taskv1.ErrInvalidTask):
		return taskv1.TaskErrorClassInvalidInput
	case errors.Is(err, errImageCacheInvalidInput):
		return taskv1.TaskErrorClassInvalidInput
	case errors.Is(err, errImageCacheChecksumMismatch):
		return taskv1.TaskErrorClassChecksumMismatch
	case errors.Is(err, errImageCacheContentConflict):
		return taskv1.TaskErrorClassChecksumMismatch
	case errors.Is(err, errTaskTransientIO):
		return taskv1.TaskErrorClassTransientIO
	default:
		return taskv1.TaskErrorClassExecutionFailed
	}
}

func (c *TaskController) requireLocalCacheRoot(inputRoot string) error {
	if c.cache == nil {
		return fmt.Errorf("%w: image cache is not configured", errTaskInvalidInput)
	}
	if filepath.Clean(inputRoot) != c.cache.Root() {
		return fmt.Errorf("%w: cache root %q does not match local cache root %q", errTaskInvalidInput, inputRoot, c.cache.Root())
	}
	return nil
}

func (c *TaskController) openImageTaskSource(ctx context.Context, source taskv1.ImageTaskSource) (io.ReadCloser, error) {
	if !source.Type.Valid() || source.Location == "" {
		return nil, fmt.Errorf("%w: invalid image task source", errTaskInvalidInput)
	}
	u, err := url.Parse(source.Location)
	if err != nil {
		return nil, fmt.Errorf("%w: parse image source %q: %v", errTaskInvalidInput, source.Location, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: unsupported image source scheme %q", errTaskInvalidInput, u.Scheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.Location, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build image source request: %v", errTaskInvalidInput, err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch image source %q: %v", errTaskTransientIO, source.Location, err)
	}
	if resp.StatusCode != http.StatusOK {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("%w: fetch image source %q: status %d and close body: %v", errTaskTransientIO, source.Location, resp.StatusCode, closeErr)
		}
		return nil, fmt.Errorf("%w: fetch image source %q: status %d", errTaskTransientIO, source.Location, resp.StatusCode)
	}
	return resp.Body, nil
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
