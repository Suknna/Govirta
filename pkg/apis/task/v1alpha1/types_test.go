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

func TestTaskValidateAcceptsCacheImageNode(t *testing.T) {
	task := validImageTask(t, TaskOperationCacheImageNode, validCacheImageInput())
	if err := task.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestTaskValidateRejectsCacheImageWithoutNodeName(t *testing.T) {
	task := validImageTask(t, TaskOperationCacheImageNode, validCacheImageInput())
	task.NodeName = ""
	err := task.Validate()
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Validate() error = %v, want ErrInvalidTask", err)
	}
}

func TestTaskValidateRejectsCacheImageBadChecksum(t *testing.T) {
	input := validCacheImageInput()
	input.SHA256 = "ABCDEF"
	task := validImageTask(t, TaskOperationCacheImageNode, input)
	err := task.Validate()
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Validate() error = %v, want ErrInvalidTask", err)
	}
}

func TestTaskValidateRejectsDeleteCachedImageBadScope(t *testing.T) {
	task := validImageTask(t, TaskOperationDeleteCachedImageNode, validDeleteCachedImageInput())
	task.Spec.Scope = TaskScopeCluster
	task.NodeName = ""
	err := task.Validate()
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Validate() error = %v, want ErrInvalidTask", err)
	}
}

func TestDecodeCacheImageObservedAcceptsValidPayload(t *testing.T) {
	raw := mustMarshal(t, CacheImageObserved{
		NodeName:   "node0",
		ImageName:  "alpine",
		Version:    "v1",
		Format:     "qcow2",
		CachedPath: "/var/lib/govirta/cache/alpine.qcow2",
		SizeBytes:  1024,
		SHA256:     validSHA256(),
	})
	observed, err := DecodeCacheImageObserved(raw)
	if err != nil {
		t.Fatalf("DecodeCacheImageObserved() error = %v, want nil", err)
	}
	if observed.NodeName != "node0" || observed.ImageName != "alpine" {
		t.Fatalf("observed mismatch: %+v", observed)
	}
}

func TestDecodeCacheImageObservedRejectsMalformedPayload(t *testing.T) {
	raw := json.RawMessage(`{"nodeName":"node0","imageName":"alpine","version":"v1","format":"qcow2","cachedPath":"/cache/alpine.qcow2","sizeBytes":1024,"sha256":"not-hex"}`)
	_, err := DecodeCacheImageObserved(raw)
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("DecodeCacheImageObserved() error = %v, want ErrInvalidTask", err)
	}
}

func TestDecodeDeleteCachedImageObservedAcceptsValidPayload(t *testing.T) {
	raw := mustMarshal(t, DeleteCachedImageObserved{
		NodeName:  "node0",
		ImageName: "alpine",
		Version:   "v1",
		Deleted:   true,
	})
	observed, err := DecodeDeleteCachedImageObserved(raw)
	if err != nil {
		t.Fatalf("DecodeDeleteCachedImageObserved() error = %v, want nil", err)
	}
	if observed.NodeName != "node0" || !observed.Deleted {
		t.Fatalf("observed mismatch: %+v", observed)
	}
}

func TestDecodeDeleteCachedImageObservedRejectsDeletedFalse(t *testing.T) {
	raw := mustMarshal(t, DeleteCachedImageObserved{
		NodeName:  "node0",
		ImageName: "alpine",
		Version:   "v1",
		Deleted:   false,
	})
	_, err := DecodeDeleteCachedImageObserved(raw)
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("DecodeDeleteCachedImageObserved() error = %v, want ErrInvalidTask", err)
	}
}

func TestDecodeDeleteCachedImageObservedRejectsMalformedPayload(t *testing.T) {
	raw := json.RawMessage(`{"nodeName":"node0","imageName":"alpine","deleted":true}`)
	_, err := DecodeDeleteCachedImageObserved(raw)
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("DecodeDeleteCachedImageObserved() error = %v, want ErrInvalidTask", err)
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

func TestTaskStatusValidateAcceptsTypedFailureClasses(t *testing.T) {
	for _, class := range []TaskErrorClass{TaskErrorClassInvalidInput, TaskErrorClassUnsupportedOperation, TaskErrorClassExecutionFailed, TaskErrorClassChecksumMismatch, TaskErrorClassTransientIO} {
		status := TaskStatus{Phase: TaskPhaseFailed, ErrorClass: class, Message: "failed"}
		if err := status.Validate(); err != nil {
			t.Fatalf("Validate(%s) error = %v, want nil", class, err)
		}
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
	input := mustMarshal(t, NoopInput{Marker: "phase-one"})
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

func validImageTask(t *testing.T, operation TaskOperation, input any) Task {
	t.Helper()
	task := validTask(t, TaskScopeNode, TaskOperationNoopNode)
	task.Name = "task-image-cache"
	task.UID = "task-image-cache-uid"
	task.Spec.OwnerKind = metav1.KindImage
	task.Spec.OwnerName = "alpine"
	task.Spec.OwnerUID = "image-uid"
	task.Spec.Operation = operation
	task.Spec.Input = mustMarshal(t, input)
	return task
}

func validCacheImageInput() CacheImageInput {
	return CacheImageInput{
		ImageName: "alpine",
		ImageUID:  "image-uid",
		Version:   "v1",
		Format:    "qcow2",
		Source: ImageTaskSource{
			Type:     ImageTaskSourceHTTP,
			Location: "https://images.example/alpine.qcow2",
		},
		DeclaredSizeBytes: 1024,
		SHA256:            validSHA256(),
		CacheRoot:         "/var/lib/govirta/image-cache",
	}
}

func validDeleteCachedImageInput() DeleteCachedImageInput {
	return DeleteCachedImageInput{
		ImageName: "alpine",
		ImageUID:  "image-uid",
		Version:   "v1",
		SHA256:    validSHA256(),
		CacheRoot: "/var/lib/govirta/image-cache",
	}
}

func validSHA256() string {
	return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}

func mustMarshal(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return b
}
