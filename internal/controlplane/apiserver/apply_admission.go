package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
	"github.com/suknna/govirta/internal/controlplane/store"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// classifyApply inspects the target key without changing the eventual Put
// semantics. Callers use the result for admission decisions, not CAS.
func (s *Server) classifyApply(ctx context.Context, kind metav1.Kind, key string) (admission.Operation, store.RawObject, any, error) {
	raw, err := s.store.Get(ctx, key)
	if err == nil {
		obj, err := decodeObjectByKind(kind, raw.Value)
		if err != nil {
			return "", store.RawObject{}, nil, fmt.Errorf("apiserver: decode existing %s %q: %w", kind, key, err)
		}
		return admission.OperationUpdate, raw, obj, nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return admission.OperationCreate, store.RawObject{}, nil, nil
	}
	return "", store.RawObject{}, nil, fmt.Errorf("apiserver: classify apply %q: %w", key, err)
}

func decodeObjectByKind(kind metav1.Kind, raw []byte) (any, error) {
	switch kind {
	case metav1.KindStoragePool:
		var obj storagepoolv1.StoragePool
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("decode StoragePool: %w", err)
		}
		return obj, nil
	case metav1.KindImage:
		var obj imagev1.Image
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("decode Image: %w", err)
		}
		return obj, nil
	case metav1.KindVolume:
		var obj volumev1.Volume
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("decode Volume: %w", err)
		}
		return obj, nil
	case metav1.KindNetwork:
		var obj networkv1.Network
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("decode Network: %w", err)
		}
		return obj, nil
	case metav1.KindNIC:
		var obj nicv1.NIC
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("decode NIC: %w", err)
		}
		return obj, nil
	case metav1.KindVM:
		var obj vmv1.VM
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("decode VM: %w", err)
		}
		return obj, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
}

// applyVM applies VM-specific admission. Creates still use bindVM placement;
// updates preserve the existing node binding so a power update cannot become an
// implicit migration or accidental unschedule.
func (s *Server) applyVM(ctx context.Context, key string, vm *vmv1.VM, req admission.Request) (store.RawObject, *apiError) {
	switch req.Operation {
	case admission.OperationCreate:
		if err := s.bindVM(ctx, vm); err != nil {
			return store.RawObject{}, err
		}
	case admission.OperationUpdate:
		if aerr := preserveVMUpdateMetadata(req.OldObject, vm); aerr != nil {
			return store.RawObject{}, aerr
		}
	}

	return s.putWithPostAdmission(ctx, key, *vm, req)
}

func preserveVMUpdateMetadata(oldObject any, vm *vmv1.VM) *apiError {
	existing, ok := oldObject.(vmv1.VM)
	if !ok {
		return internalErr(fmt.Errorf("apiserver: existing object for VM %q has type %T", vm.Name, oldObject))
	}

	if vm.NodeName == "" {
		vm.NodeName = existing.NodeName
	}

	vm.ResourceVersion = existing.ResourceVersion
	vm.DeletionTimestamp = existing.DeletionTimestamp
	if len(existing.Finalizers) > 0 {
		vm.Finalizers = append(vm.Finalizers[:0], existing.Finalizers...)
	}

	return nil
}
