package admission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/suknna/govirta/internal/controlplane/store"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// ReferenceValidator rejects apply requests whose upstream object references are
// absent or already deletion-marked. It reads the store at admission time so the
// control plane does not maintain a second, drift-prone reference index.
type ReferenceValidator struct {
	Store StoreReader
}

func (ReferenceValidator) Name() string { return "ReferenceValidator" }

func (v ReferenceValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationCreate && req.Operation != OperationUpdate {
		return nil
	}
	if v.Store == nil {
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("store reader is required"))
	}

	obj, err := normalizeObject(req.NewObject)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, err)
	}

	switch o := obj.(type) {
	case imagev1.Image:
		return v.requireByName(ctx, metav1.KindStoragePool, o.Spec.FilePoolRef)
	case volumev1.Volume:
		if err := v.requireByName(ctx, metav1.KindStoragePool, o.Spec.PoolRef); err != nil {
			return err
		}
		if err := v.rejectDeletingVMByUID(ctx, o.Spec.VMRef); err != nil {
			return err
		}
		if o.Spec.ImageRef != "" {
			if err := v.requireByName(ctx, metav1.KindImage, o.Spec.ImageRef); err != nil {
				return err
			}
		}
		if o.Spec.ImageFilePoolRef != "" {
			if err := v.requireByName(ctx, metav1.KindStoragePool, o.Spec.ImageFilePoolRef); err != nil {
				return err
			}
		}
		return nil
	case nicv1.NIC:
		if err := v.requireByName(ctx, metav1.KindNetwork, o.Spec.NetworkRef); err != nil {
			return err
		}
		return v.rejectDeletingVMByUID(ctx, o.Spec.VMRef)
	case vmv1.VM:
		for _, name := range o.Spec.VolumeRefs {
			if err := v.requireByName(ctx, metav1.KindVolume, name); err != nil {
				return err
			}
		}
		for _, name := range o.Spec.NICRefs {
			if err := v.requireByName(ctx, metav1.KindNIC, name); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func (v ReferenceValidator) requireByName(ctx context.Context, kind metav1.Kind, name string) error {
	raw, err := v.Store.Get(ctx, StoreKey(kind, name))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("referenced %s %q does not exist", kind, name))
		}
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("read referenced %s %q: %w", kind, name, err))
	}
	meta, err := decodeStoredMetadata(raw.Value)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("decode referenced %s %q metadata: %w", kind, name, err))
	}
	if meta.DeletionTimestamp != "" {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("referenced %s %q is deleting", kind, name))
	}
	return nil
}

func (v ReferenceValidator) rejectDeletingVMByUID(ctx context.Context, uid string) error {
	raws, err := v.Store.List(ctx, ListPrefix(metav1.KindVM))
	if err != nil {
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("list referenced VMs: %w", err))
	}
	for _, raw := range raws {
		meta, err := decodeStoredMetadata(raw.Value)
		if err != nil {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("decode referenced VM metadata from %q: %w", raw.Key, err))
		}
		if meta.UID != uid {
			continue
		}
		if meta.DeletionTimestamp != "" {
			return Reject(v.Name(), ReasonConflict, fmt.Errorf("referenced VM uid %q is deleting", uid))
		}
		return nil
	}
	return nil
}

func decodeStoredMetadata(raw []byte) (metav1.ObjectMeta, error) {
	var envelope struct {
		Metadata metav1.ObjectMeta `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return metav1.ObjectMeta{}, err
	}
	return envelope.Metadata, nil
}
