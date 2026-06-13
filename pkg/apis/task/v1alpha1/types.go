// Package v1alpha1 defines the internal Task API object. Tasks are the explicit
// execution contract between the control plane and task executors; they use the
// same API envelope as user-facing resources but are not user-applyable.
package v1alpha1

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// ErrInvalidTask is returned when a Task, TaskSpec, or TaskStatus is invalid.
var ErrInvalidTask = errors.New("task api: invalid task")

// TaskScope names where a task is executed.
type TaskScope string

const (
	// TaskScopeNode is executed by a govirtlet selected via metadata.nodeName.
	TaskScopeNode TaskScope = "Node"
	// TaskScopeCluster is executed by the control-plane task executor.
	TaskScopeCluster TaskScope = "Cluster"
)

// Valid reports whether s is a known task scope.
func (s TaskScope) Valid() bool {
	return s == TaskScopeNode || s == TaskScopeCluster
}

// TaskOperation names the explicit operation a task executor must run.
type TaskOperation string

const (
	// TaskOperationNoopCluster is the phase-one no-op operation for the control plane.
	TaskOperationNoopCluster TaskOperation = "NoopCluster"
	// TaskOperationNoopNode is the phase-one no-op operation for node executors.
	TaskOperationNoopNode TaskOperation = "NoopNode"
	// TaskOperationCacheImageNode instructs a node executor to cache an image locally.
	TaskOperationCacheImageNode TaskOperation = "CacheImageNode"
	// TaskOperationDeleteCachedImageNode instructs a node executor to delete a local cached image.
	TaskOperationDeleteCachedImageNode TaskOperation = "DeleteCachedImageNode"
)

// Valid reports whether o is a known task operation.
func (o TaskOperation) Valid() bool {
	return o == TaskOperationNoopCluster ||
		o == TaskOperationNoopNode ||
		o == TaskOperationCacheImageNode ||
		o == TaskOperationDeleteCachedImageNode
}

// TaskPhase is the observed lifecycle phase of a task.
type TaskPhase string

const (
	// TaskPhasePending means the task is persisted and waiting for an executor.
	TaskPhasePending TaskPhase = "Pending"
	// TaskPhaseRunning means an executor has started the task.
	TaskPhaseRunning TaskPhase = "Running"
	// TaskPhaseSucceeded means execution completed successfully.
	TaskPhaseSucceeded TaskPhase = "Succeeded"
	// TaskPhaseFailed means execution failed and status carries classification.
	TaskPhaseFailed TaskPhase = "Failed"
)

// Valid reports whether p is a known task phase. Empty is invalid: task writers
// must explicitly choose Pending, Running, Succeeded, or Failed.
func (p TaskPhase) Valid() bool {
	return p == TaskPhasePending || p == TaskPhaseRunning || p == TaskPhaseSucceeded || p == TaskPhaseFailed
}

// Terminal reports whether p is a terminal task phase.
func (p TaskPhase) Terminal() bool {
	return p == TaskPhaseSucceeded || p == TaskPhaseFailed
}

// TaskErrorClass is a stable, bounded task failure classification.
type TaskErrorClass string

const (
	// TaskErrorClassNone means no error class is present.
	TaskErrorClassNone TaskErrorClass = "None"
	// TaskErrorClassInvalidInput means the task input could not be decoded or validated.
	TaskErrorClassInvalidInput TaskErrorClass = "InvalidInput"
	// TaskErrorClassUnsupportedOperation means the executor does not support the operation.
	TaskErrorClassUnsupportedOperation TaskErrorClass = "UnsupportedOperation"
	// TaskErrorClassExecutionFailed means the supported operation failed while running.
	TaskErrorClassExecutionFailed TaskErrorClass = "ExecutionFailed"
	// TaskErrorClassChecksumMismatch means observed bytes did not match declared content identity.
	TaskErrorClassChecksumMismatch TaskErrorClass = "ChecksumMismatch"
	// TaskErrorClassTransientIO means the executor hit a retryable I/O boundary failure.
	TaskErrorClassTransientIO TaskErrorClass = "TransientIO"
)

// Valid reports whether c is a known bounded task error class.
func (c TaskErrorClass) Valid() bool {
	return c == "" || c == TaskErrorClassNone || c == TaskErrorClassInvalidInput || c == TaskErrorClassUnsupportedOperation || c == TaskErrorClassExecutionFailed || c == TaskErrorClassChecksumMismatch || c == TaskErrorClassTransientIO
}

// Task is an internal execution object persisted in the control-plane store.
type Task struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              TaskSpec   `json:"spec"`
	Status            TaskStatus `json:"status"`
}

// Validate reports whether the Task envelope, desired operation, and observed
// status are internally consistent.
func (t Task) Validate() error {
	if t.APIVersion != metav1.APIGroupVersion {
		return fmt.Errorf("%w: apiVersion %q must be %q", ErrInvalidTask, t.APIVersion, metav1.APIGroupVersion)
	}
	if t.Kind != metav1.KindTask {
		return fmt.Errorf("%w: kind %q must be %q", ErrInvalidTask, t.Kind, metav1.KindTask)
	}
	if err := t.ObjectMeta.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTask, err)
	}
	if err := t.Spec.ValidateWithMetadata(t.ObjectMeta); err != nil {
		return err
	}
	if err := t.Status.Validate(); err != nil {
		return err
	}
	return nil
}

// TaskSpec is the complete desired execution instruction.
type TaskSpec struct {
	Scope     TaskScope       `json:"scope"`
	OwnerKind metav1.Kind     `json:"ownerKind"`
	OwnerName string          `json:"ownerName"`
	OwnerUID  string          `json:"ownerUID"`
	Operation TaskOperation   `json:"operation"`
	Input     json.RawMessage `json:"input"`
}

// Validate reports whether the task spec carries every behavior-affecting field
// explicitly. Envelope-level validation calls ValidateWithMetadata so scope and
// metadata.nodeName stay consistent.
func (s TaskSpec) Validate() error {
	if !s.Scope.Valid() {
		return fmt.Errorf("%w: spec.scope %q is invalid", ErrInvalidTask, s.Scope)
	}
	if s.OwnerKind == "" || s.OwnerName == "" || s.OwnerUID == "" {
		return fmt.Errorf("%w: ownerKind, ownerName, and ownerUID are required", ErrInvalidTask)
	}
	if !s.Operation.Valid() {
		return fmt.Errorf("%w: spec.operation %q is invalid", ErrInvalidTask, s.Operation)
	}
	if len(s.Input) == 0 || string(s.Input) == "null" {
		return fmt.Errorf("%w: spec.input is required", ErrInvalidTask)
	}
	if s.Scope == TaskScopeNode && s.Operation != TaskOperationNoopNode {
		if s.Operation != TaskOperationCacheImageNode && s.Operation != TaskOperationDeleteCachedImageNode {
			return fmt.Errorf("%w: node task operation %q is invalid", ErrInvalidTask, s.Operation)
		}
	}
	if s.Scope == TaskScopeCluster && s.Operation != TaskOperationNoopCluster {
		return fmt.Errorf("%w: cluster task operation must be %q", ErrInvalidTask, TaskOperationNoopCluster)
	}
	if err := s.validateInput(); err != nil {
		return err
	}
	return nil
}

func (s TaskSpec) validateInput() error {
	switch s.Operation {
	case TaskOperationNoopCluster, TaskOperationNoopNode:
		var input NoopInput
		if err := json.Unmarshal(s.Input, &input); err != nil {
			return fmt.Errorf("%w: decode noop input: %v", ErrInvalidTask, err)
		}
		if input.Marker == "" {
			return fmt.Errorf("%w: noop input marker is required", ErrInvalidTask)
		}
	case TaskOperationCacheImageNode:
		var input CacheImageInput
		if err := json.Unmarshal(s.Input, &input); err != nil {
			return fmt.Errorf("%w: decode cache image input: %v", ErrInvalidTask, err)
		}
		if err := input.Validate(); err != nil {
			return err
		}
	case TaskOperationDeleteCachedImageNode:
		var input DeleteCachedImageInput
		if err := json.Unmarshal(s.Input, &input); err != nil {
			return fmt.Errorf("%w: decode delete cached image input: %v", ErrInvalidTask, err)
		}
		if err := input.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: spec.operation %q is invalid", ErrInvalidTask, s.Operation)
	}
	return nil
}

// ValidateWithMetadata reports whether the spec is valid and consistent with
// object metadata routing fields.
func (s TaskSpec) ValidateWithMetadata(meta metav1.ObjectMeta) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Scope == TaskScopeNode && meta.NodeName == "" {
		return fmt.Errorf("%w: node-scoped task requires metadata.nodeName", ErrInvalidTask)
	}
	if s.Scope == TaskScopeCluster && meta.NodeName != "" {
		return fmt.Errorf("%w: cluster-scoped task must not carry metadata.nodeName", ErrInvalidTask)
	}
	return nil
}

// NoopInput is the phase-one explicit input for no-op task execution.
type NoopInput struct {
	Marker string `json:"marker"`
}

// NoopObserved is the phase-one explicit observed result for no-op tasks.
type NoopObserved struct {
	Executor string `json:"executor"`
	Marker   string `json:"marker"`
}

// TaskStatus is the observed execution state reported by task executors.
type TaskStatus struct {
	Phase       TaskPhase       `json:"phase"`
	Observed    json.RawMessage `json:"observed,omitempty"`
	ErrorClass  TaskErrorClass  `json:"errorClass,omitempty"`
	Message     string          `json:"message,omitempty"`
	StartedAt   string          `json:"startedAt,omitempty"`
	CompletedAt string          `json:"completedAt,omitempty"`
}

// Validate reports whether the status carries a known phase, bounded error
// class, and parseable timestamps when present.
func (s TaskStatus) Validate() error {
	if !s.Phase.Valid() {
		return fmt.Errorf("%w: status.phase %q is invalid", ErrInvalidTask, s.Phase)
	}
	if !s.ErrorClass.Valid() {
		return fmt.Errorf("%w: status.errorClass %q is invalid", ErrInvalidTask, s.ErrorClass)
	}
	if s.Phase == TaskPhaseSucceeded {
		if len(s.Observed) == 0 || string(s.Observed) == "null" {
			return fmt.Errorf("%w: succeeded task status requires observed", ErrInvalidTask)
		}
		if s.ErrorClass != TaskErrorClassNone {
			return fmt.Errorf("%w: succeeded task status requires errorClass %q", ErrInvalidTask, TaskErrorClassNone)
		}
	}
	if s.Phase == TaskPhaseFailed {
		if s.ErrorClass == "" || s.ErrorClass == TaskErrorClassNone {
			return fmt.Errorf("%w: failed task status requires a non-none errorClass", ErrInvalidTask)
		}
		if s.Message == "" {
			return fmt.Errorf("%w: failed task status requires message", ErrInvalidTask)
		}
	}
	if s.StartedAt != "" {
		if _, err := time.Parse(time.RFC3339, s.StartedAt); err != nil {
			return fmt.Errorf("%w: startedAt must be RFC3339: %v", ErrInvalidTask, err)
		}
	}
	if s.CompletedAt != "" {
		if _, err := time.Parse(time.RFC3339, s.CompletedAt); err != nil {
			return fmt.Errorf("%w: completedAt must be RFC3339: %v", ErrInvalidTask, err)
		}
	}
	return nil
}
