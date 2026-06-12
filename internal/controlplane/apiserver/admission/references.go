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
	snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// ReferenceValidator validates an apply request's object references against the
// live store at admission time, so the control plane does not maintain a second,
// drift-prone reference index. Two reference kinds have different absence rules:
//   - by-name upstream refs (StoragePool/Image/Network): absent -> reject 400.
//     These targets must already exist before the referring object is applied.
//   - Volume/NIC vmRef (a VM uid): absent -> allow. Volume/NIC are independent
//     resources that may be applied before their owning VM exists, binding a
//     future VM uid. Only an already-existing but deletion-marked VM is rejected.
//
// In all cases, referencing an object that already carries a deletionTimestamp is
// rejected 409, closing the Knife 1 stamp-to-finalize window from the apply side.
type ReferenceValidator struct {
	Store StoreReader
}

func (ReferenceValidator) Name() string { return "ReferenceValidator" }

func (v ReferenceValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationCreate && req.Operation != OperationUpdate && req.Operation != OperationReplace {
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
	case snapshotv1.Snapshot:
		// A Snapshot names its target VM by NAME (not the uid backpointer that
		// Volume/NIC use). The VM must already exist and not be deleting, so a
		// by-name upstream check is the correct rule: missing -> 400, deleting ->
		// 409.
		return v.requireByName(ctx, metav1.KindVM, o.Spec.VMRef)
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
