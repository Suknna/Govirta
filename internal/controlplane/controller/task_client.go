// Package controller hosts control-plane-owned reconciliation loops. Phase one
// only owns internal Task lifecycle proof-of-work and deliberately does not move
// existing govirtlet business controllers.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
	"github.com/suknna/govirta/internal/controlplane/store"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

// TaskClient is the typed store facade used by control-plane Task controllers.
type TaskClient struct {
	store store.Store
}

// NewTaskClient constructs a TaskClient over the required raw store boundary.
func NewTaskClient(st store.Store) *TaskClient {
	return &TaskClient{store: st}
}

// CreateOrGetTask writes task if absent and returns the stored object. If the
// task already exists, the existing Task is returned unchanged.
func (c *TaskClient) CreateOrGetTask(ctx context.Context, task taskv1.Task) (taskv1.Task, error) {
	if c == nil || c.store == nil {
		return taskv1.Task{}, fmt.Errorf("controlplane controller: task store is required")
	}
	if err := task.Validate(); err != nil {
		return taskv1.Task{}, err
	}
	key := taskKey(task.Name)
	raw, err := c.store.Get(ctx, key)
	if err == nil {
		existing, err := decodeStoredTask(raw)
		if err != nil {
			return taskv1.Task{}, err
		}
		if !sameTaskSpec(existing, task) {
			return taskv1.Task{}, fmt.Errorf("controlplane controller: existing task %q does not match explicit desired spec", task.Name)
		}
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return taskv1.Task{}, fmt.Errorf("controlplane controller: get task %q: %w", task.Name, err)
	}

	task.ResourceVersion = ""
	data, err := json.Marshal(task)
	if err != nil {
		return taskv1.Task{}, fmt.Errorf("controlplane controller: marshal task %q: %w", task.Name, err)
	}
	raw, err = c.store.Put(ctx, key, data, "")
	if err != nil {
		return taskv1.Task{}, fmt.Errorf("controlplane controller: put task %q: %w", task.Name, err)
	}
	return decodeStoredTask(raw)
}

// PatchStatus replaces only the Task status field using a store CAS.
func (c *TaskClient) PatchStatus(ctx context.Context, name string, status taskv1.TaskStatus) (taskv1.Task, error) {
	if c == nil || c.store == nil {
		return taskv1.Task{}, fmt.Errorf("controlplane controller: task store is required")
	}
	if err := status.Validate(); err != nil {
		return taskv1.Task{}, err
	}
	key := taskKey(name)
	for attempt := 0; attempt < 3; attempt++ {
		raw, err := c.store.Get(ctx, key)
		if err != nil {
			return taskv1.Task{}, fmt.Errorf("controlplane controller: get task %q: %w", name, err)
		}
		task, err := decodeStoredTask(raw)
		if err != nil {
			return taskv1.Task{}, err
		}
		task.Status = status
		task.ResourceVersion = ""
		data, err := json.Marshal(task)
		if err != nil {
			return taskv1.Task{}, fmt.Errorf("controlplane controller: marshal task %q: %w", name, err)
		}
		written, err := c.store.Put(ctx, key, data, raw.ResourceVersion)
		if err == nil {
			return decodeStoredTask(written)
		}
		if errors.Is(err, store.ErrRevisionConflict) {
			continue
		}
		return taskv1.Task{}, fmt.Errorf("controlplane controller: patch task status %q: %w", name, err)
	}
	return taskv1.Task{}, fmt.Errorf("controlplane controller: patch task status %q: %w", name, store.ErrRevisionConflict)
}

func decodeStoredTask(raw store.RawObject) (taskv1.Task, error) {
	var task taskv1.Task
	if err := json.Unmarshal(raw.Value, &task); err != nil {
		return taskv1.Task{}, fmt.Errorf("controlplane controller: decode task %q: %w", raw.Key, err)
	}
	task.ResourceVersion = raw.ResourceVersion
	if err := task.Validate(); err != nil {
		return taskv1.Task{}, fmt.Errorf("controlplane controller: stored task %q: %w", raw.Key, err)
	}
	return task, nil
}

func taskKey(name string) string {
	return admission.StoreKey(metav1.KindTask, name)
}

func sameTaskSpec(a, b taskv1.Task) bool {
	return a.APIVersion == b.APIVersion &&
		a.Kind == b.Kind &&
		a.Name == b.Name &&
		a.UID == b.UID &&
		a.NodeName == b.NodeName &&
		a.Spec.Scope == b.Spec.Scope &&
		a.Spec.OwnerKind == b.Spec.OwnerKind &&
		a.Spec.OwnerName == b.Spec.OwnerName &&
		a.Spec.OwnerUID == b.Spec.OwnerUID &&
		a.Spec.Operation == b.Spec.Operation &&
		string(a.Spec.Input) == string(b.Spec.Input)
}
