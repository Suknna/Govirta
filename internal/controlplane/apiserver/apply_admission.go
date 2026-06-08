package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/suknna/govirta/internal/controlplane/store"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

// ApplyOperation describes whether an apply targets a missing or existing object.
// It is admission context only; store writes remain unconditional replacements.
type ApplyOperation string

const (
	// ApplyOperationCreate means no object currently exists at the target key.
	ApplyOperationCreate ApplyOperation = "Create"
	// ApplyOperationUpdate means the target key already has a stored object.
	ApplyOperationUpdate ApplyOperation = "Update"
)

// classifyApply inspects the target key without changing the eventual Put
// semantics. Callers use the result for admission decisions, not CAS.
func (s *Server) classifyApply(ctx context.Context, key string) (ApplyOperation, []byte, error) {
	raw, err := s.store.Get(ctx, key)
	if err == nil {
		return ApplyOperationUpdate, raw.Value, nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return ApplyOperationCreate, nil, nil
	}
	return "", nil, fmt.Errorf("apiserver: classify apply %q: %w", key, err)
}

// applyVM applies VM-specific admission. Creates still use bindVM placement;
// updates preserve the existing node binding so a power update cannot become an
// implicit migration or accidental unschedule.
func (s *Server) applyVM(ctx context.Context, key string, vm *vmv1.VM) (store.RawObject, *apiError) {
	op, existingRaw, err := s.classifyApply(ctx, key)
	if err != nil {
		return store.RawObject{}, internalErr(err)
	}

	switch op {
	case ApplyOperationCreate:
		if vm.Spec.PowerState == vmv1.PowerStateShutdown {
			return store.RawObject{}, badRequest(fmt.Errorf("%w: powerState Shutdown is only valid for VM updates", vmv1.ErrInvalidSpec))
		}
		if err := s.bindVM(ctx, vm); err != nil {
			return store.RawObject{}, err
		}
	case ApplyOperationUpdate:
		if aerr := preserveVMUpdateMetadata(existingRaw, vm); aerr != nil {
			return store.RawObject{}, aerr
		}
	}

	return s.put(ctx, key, *vm)
}

func preserveVMUpdateMetadata(existingRaw []byte, vm *vmv1.VM) *apiError {
	var existing vmv1.VM
	if err := json.Unmarshal(existingRaw, &existing); err != nil {
		return internalErr(fmt.Errorf("apiserver: decode existing VM %q: %w", vm.Name, err))
	}

	if vm.NodeName == "" {
		vm.NodeName = existing.NodeName
	} else if existing.NodeName != "" && vm.NodeName != existing.NodeName {
		return badRequest(fmt.Errorf("%w: nodeName is immutable for VM update: existing %q vs requested %q", vmv1.ErrInvalidSpec, existing.NodeName, vm.NodeName))
	}

	vm.ResourceVersion = existing.ResourceVersion
	vm.DeletionTimestamp = existing.DeletionTimestamp
	if len(existing.Finalizers) > 0 {
		vm.Finalizers = append(vm.Finalizers[:0], existing.Finalizers...)
	}

	return nil
}
