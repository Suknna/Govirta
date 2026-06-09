package admission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/suknna/govirta/internal/controlplane/store"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// statusEnvelopeKeys are the top-level keys that only ever appear on a full API
// object envelope, never on a bare status. A status PATCH body that carries any
// of these is a caller mistake (submitting a whole object, or nesting status
// under a "status" key) and is rejected before the handler merges it into the
// stored object's .status field.
var statusEnvelopeKeys = []string{"apiVersion", "kind", "metadata", "spec", "status"}

// PatchShapeValidator enforces that a status PATCH body is a bare status JSON
// object — not a full object envelope and not a JSON array/scalar. The handler's
// mergeStatus splices the body verbatim into the stored object's .status field,
// so a body that smuggled spec/metadata/apiVersion/kind (or nested status) would
// either be silently dropped into .status or, worse, mislead a caller into
// thinking they patched spec. We reject those shapes up front.
type PatchShapeValidator struct{}

func (PatchShapeValidator) Name() string { return "PatchShapeValidator" }

func (v PatchShapeValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationStatusPatch {
		return nil
	}
	// A bare status must be a JSON object. Unmarshalling into a map fails for
	// arrays and scalars, which is exactly the shape rejection we want.
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(req.NewRaw, &fields); err != nil {
		return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("status body must be a bare status JSON object: %w", err))
	}
	for _, key := range statusEnvelopeKeys {
		if _, ok := fields[key]; ok {
			return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("status body must be a bare status object, not a full object envelope: unexpected key %q", key))
		}
	}
	return nil
}

// StatusTypeValidator decodes the status PATCH body into the concrete Status type
// for the request kind and runs its Validate contract. This rejects malformed
// status bytes and out-of-range enum values (phase, and for VM the observed power
// state and power transition) before the handler merges the body into the stored
// object. message is free-form and intentionally not checked.
type StatusTypeValidator struct{}

func (StatusTypeValidator) Name() string { return "StatusTypeValidator" }

func (v StatusTypeValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationStatusPatch {
		return nil
	}
	status, err := decodeStatus(req.Kind, req.NewRaw)
	if err != nil {
		return Reject(v.Name(), ReasonBadRequest, err)
	}
	if err := status.Validate(); err != nil {
		return Reject(v.Name(), ReasonBadRequest, err)
	}
	return nil
}

// decodeStatus decodes raw status bytes into the concrete Status value for kind.
// The returned value exposes the per-kind Validate contract added in the API
// contract layer. A decode failure or an unknown kind is a bad request: the kind
// comes from the request URL and the bytes from the caller.
func decodeStatus(kind metav1.Kind, raw []byte) (statusValidator, error) {
	switch kind {
	case metav1.KindStoragePool:
		var s storagepoolv1.StoragePoolStatus
		if err := decodeStrictStatus(raw, &s); err != nil {
			return nil, fmt.Errorf("decode %s status: %w", kind, err)
		}
		return s, nil
	case metav1.KindImage:
		var s imagev1.ImageStatus
		if err := decodeStrictStatus(raw, &s); err != nil {
			return nil, fmt.Errorf("decode %s status: %w", kind, err)
		}
		return s, nil
	case metav1.KindVolume:
		var s volumev1.VolumeStatus
		if err := decodeStrictStatus(raw, &s); err != nil {
			return nil, fmt.Errorf("decode %s status: %w", kind, err)
		}
		return s, nil
	case metav1.KindNetwork:
		var s networkv1.NetworkStatus
		if err := decodeStrictStatus(raw, &s); err != nil {
			return nil, fmt.Errorf("decode %s status: %w", kind, err)
		}
		return s, nil
	case metav1.KindNIC:
		var s nicv1.NICStatus
		if err := decodeStrictStatus(raw, &s); err != nil {
			return nil, fmt.Errorf("decode %s status: %w", kind, err)
		}
		return s, nil
	case metav1.KindVM:
		var s vmv1.VMStatus
		if err := decodeStrictStatus(raw, &s); err != nil {
			return nil, fmt.Errorf("decode %s status: %w", kind, err)
		}
		return s, nil
	default:
		return nil, fmt.Errorf("unsupported status kind %q", kind)
	}
}

func decodeStrictStatus(raw []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("status body contains trailing JSON values")
	}
	return nil
}

// TargetObjectValidator gates a status PATCH on the lifecycle state of the target
// object. It deliberately does not re-implement the handler's 404: when the object
// is absent it returns nil and lets the handler's read-modify-write loop surface
// the not-found. Its job is the one thing the handler cannot see — refusing to
// write status onto a zombie object (deletion-marked with no finalizers left,
// about to be hard-deleted) while still allowing progress/failure reporting during
// an in-flight node teardown (deletion-marked but finalizers still present).
type TargetObjectValidator struct {
	Store StoreReader
}

func (TargetObjectValidator) Name() string { return "TargetObjectValidator" }

func (v TargetObjectValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationStatusPatch {
		return nil
	}
	if len(req.OldRaw) != 0 {
		return v.validateRaw(req.Kind, req.Name, req.OldRaw)
	}
	if v.Store == nil {
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("store reader is required"))
	}

	raw, err := v.Store.Get(ctx, StoreKey(req.Kind, req.Name))
	if err != nil {
		// Absent object: defer to the handler's existing 404 path rather than
		// inventing a competing rejection semantics here.
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("read target %s %q: %w", req.Kind, req.Name, err))
	}

	return v.validateRaw(req.Kind, req.Name, raw.Value)
}

func (v TargetObjectValidator) validateRaw(kind metav1.Kind, name string, raw []byte) error {
	meta, err := decodeStoredMetadata(raw)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("decode target %s %q metadata: %w", kind, name, err))
	}

	// Not deleting: a normal live object always accepts status reports.
	if meta.DeletionTimestamp == "" {
		return nil
	}
	// Deleting with finalizers still present: node teardown is in progress and the
	// node must keep reporting status (progress/failure), so allow it.
	if len(meta.Finalizers) != 0 {
		return nil
	}
	// Deleting with no finalizers left: the object is about to be hard-deleted.
	// Writing status onto this zombie is a conflict with its terminal lifecycle.
	return Reject(v.Name(), ReasonConflict, fmt.Errorf("cannot patch status of %s %q: object is being deleted", kind, name))
}
