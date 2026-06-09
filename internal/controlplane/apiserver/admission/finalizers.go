package admission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// FinalizerPatch is the only accepted finalizers subresource body. The endpoint
// removes exactly one whitelisted finalizer; it is not a JSON Patch, merge patch,
// or replacement metadata.finalizers document.
type FinalizerPatch struct {
	Remove metav1.Finalizer `json:"remove"`
}

func DecodeFinalizerPatch(raw []byte) (FinalizerPatch, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var patch FinalizerPatch
	if err := dec.Decode(&patch); err != nil {
		return FinalizerPatch{}, fmt.Errorf("decode finalizers patch: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return FinalizerPatch{}, fmt.Errorf("finalizers patch contains trailing JSON values")
	}
	return patch, nil
}

type FinalizersPatchShapeValidator struct{}

func (FinalizersPatchShapeValidator) Name() string { return "FinalizersPatchShapeValidator" }

func (v FinalizersPatchShapeValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationFinalizersPatch {
		return nil
	}
	patch, err := DecodeFinalizerPatch(req.NewRaw)
	if err != nil {
		return Reject(v.Name(), ReasonBadRequest, err)
	}
	if patch.Remove == "" {
		return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("finalizers patch remove field is required"))
	}
	return nil
}

type WhitelistFinalizerValidator struct{}

func (WhitelistFinalizerValidator) Name() string { return "WhitelistFinalizerValidator" }

func (v WhitelistFinalizerValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationFinalizersPatch {
		return nil
	}
	patch, err := DecodeFinalizerPatch(req.NewRaw)
	if err != nil {
		return Reject(v.Name(), ReasonBadRequest, err)
	}
	if patch.Remove != metav1.FinalizerNodeTeardown {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("finalizer %q is not removable through this endpoint", patch.Remove))
	}
	return nil
}

type FinalizerDeletionPreconditionValidator struct{}

func (FinalizerDeletionPreconditionValidator) Name() string {
	return "FinalizerDeletionPreconditionValidator"
}

func (v FinalizerDeletionPreconditionValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationFinalizersPatch {
		return nil
	}
	if len(req.OldRaw) == 0 {
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("old raw object is required"))
	}
	meta, err := decodeStoredMetadata(req.OldRaw)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("decode target %s %q metadata: %w", req.Kind, req.Name, err))
	}
	if meta.DeletionTimestamp == "" {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("cannot remove finalizer from %s %q without deletionTimestamp", req.Kind, req.Name))
	}
	return nil
}
