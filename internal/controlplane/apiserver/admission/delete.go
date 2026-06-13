package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/suknna/govirta/internal/controlplane/store"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// ReverseReferenceValidator protects a DELETE by scanning the live store for any
// downstream object that still requires the deletion target as a prerequisite,
// rejecting the delete with Conflict (409) until the dependent is removed first.
// It is the inverse of apply's ReferenceValidator: apply rejects a referrer that
// points at a missing/deleting target; this rejects deleting a target that is
// still depended on. The dependency graph (by object NAME):
//
//	StoragePool <- Volume.poolRef
//	Image       <- Volume.imageRef
//	Network     <- NIC.networkRef
//	Volume      <- VM.volumeRefs[]
//	NIC         <- VM.nicRefs[]
//	VM          <- Snapshot.spec.vmRef (by name)
//
// A VM's only reverse delete edge is Snapshot.spec.vmRef: a Snapshot names its
// target VM by NAME, so the VM cannot be deleted while a Snapshot still points at
// it. VM.volumeRefs / VM.nicRefs make Volume/NIC prerequisites of the VM (so they
// cannot be deleted while the VM names them), but the Volume.vmRef / NIC.vmRef
// backpointers are ownership, not a dependency the VM has — they must NOT block
// deleting the VM. Reverse teardown deletes the VM first (its object disappears
// once its finalizer drains), which removes the volumeRefs/nicRefs edges and
// unblocks the owned Volume/NIC. Blocking VM deletion on those backpointers would
// deadlock teardown: the Volume cannot go (VM still names it) and the VM cannot go
// (Volume points back), so nothing is ever removed. The vmRef backpointer is
// enforced only on the apply side (ReferenceValidator rejects attaching a
// Volume/NIC to a deleting VM); delete protection deliberately ignores it.
//
// Each scan decodes a minimal projection (metadata.name plus the spec ref fields
// it needs), never a whole typed object. A store list or projection decode
// failure is reported as Internal (500); errors never surface as a false
// "unreferenced" pass.
type ReverseReferenceValidator struct {
	Store StoreReader
}

func (ReverseReferenceValidator) Name() string { return "ReverseReferenceValidator" }

// volumeDeleteRefProjection decodes the StoragePool/Image-bearing ref fields a
// Volume can hold: poolRef names a block StoragePool; imageRef names an Image.
// The vmRef ownership backpointer is intentionally not decoded here — it is not
// a delete dependency (see ReverseReferenceValidator doc).
type volumeDeleteRefProjection struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		PoolRef  string `json:"poolRef"`
		ImageRef string `json:"imageRef"`
	} `json:"spec"`
}

// nicDeleteRefProjection decodes a NIC's networkRef (names a Network). The vmRef
// ownership backpointer is intentionally not decoded — it is not a delete
// dependency (see ReverseReferenceValidator doc).
type nicDeleteRefProjection struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		NetworkRef string `json:"networkRef"`
	} `json:"spec"`
}

// vmDeleteRefProjection decodes a VM's volumeRefs/nicRefs, both string arrays of
// downstream object names.
type vmDeleteRefProjection struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		VolumeRefs []string `json:"volumeRefs"`
		NICRefs    []string `json:"nicRefs"`
	} `json:"spec"`
}

// snapshotDeleteRefProjection decodes a Snapshot's vmRef, which names the target
// VM (by NAME, not the uid backpointer Volume/NIC use).
type snapshotDeleteRefProjection struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		VMRef string `json:"vmRef"`
	} `json:"spec"`
}

func (v ReverseReferenceValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationDelete {
		return nil
	}
	if v.Store == nil {
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("store reader is required"))
	}

	switch req.Kind {
	case metav1.KindStoragePool:
		// Only Volume.poolRef names a StoragePool. Legacy imageFilePoolRef is ignored.
		vols, err := v.list(ctx, metav1.KindVolume, req.Kind)
		if err != nil {
			return err
		}
		for _, raw := range vols {
			var proj volumeDeleteRefProjection
			if derr := json.Unmarshal(raw.Value, &proj); derr != nil {
				return v.decodeReject(metav1.KindVolume, raw.Key, derr)
			}
			if proj.Spec.PoolRef == req.Name {
				return v.reject(req.Kind, req.Name, metav1.KindVolume, proj.Metadata.Name)
			}
		}
		return nil

	case metav1.KindImage:
		// An Image is referenced by a root Volume via its imageRef.
		vols, err := v.list(ctx, metav1.KindVolume, req.Kind)
		if err != nil {
			return err
		}
		for _, raw := range vols {
			var proj volumeDeleteRefProjection
			if derr := json.Unmarshal(raw.Value, &proj); derr != nil {
				return v.decodeReject(metav1.KindVolume, raw.Key, derr)
			}
			if proj.Spec.ImageRef == req.Name {
				return v.reject(req.Kind, req.Name, metav1.KindVolume, proj.Metadata.Name)
			}
		}
		return nil

	case metav1.KindNetwork:
		// A Network is referenced by a NIC via its networkRef.
		nics, err := v.list(ctx, metav1.KindNIC, req.Kind)
		if err != nil {
			return err
		}
		for _, raw := range nics {
			var proj nicDeleteRefProjection
			if derr := json.Unmarshal(raw.Value, &proj); derr != nil {
				return v.decodeReject(metav1.KindNIC, raw.Key, derr)
			}
			if proj.Spec.NetworkRef == req.Name {
				return v.reject(req.Kind, req.Name, metav1.KindNIC, proj.Metadata.Name)
			}
		}
		return nil

	case metav1.KindVolume:
		// A Volume is referenced by a VM when its name is in VM.spec.volumeRefs.
		vms, err := v.list(ctx, metav1.KindVM, req.Kind)
		if err != nil {
			return err
		}
		for _, raw := range vms {
			var proj vmDeleteRefProjection
			if derr := json.Unmarshal(raw.Value, &proj); derr != nil {
				return v.decodeReject(metav1.KindVM, raw.Key, derr)
			}
			if slices.Contains(proj.Spec.VolumeRefs, req.Name) {
				return v.reject(req.Kind, req.Name, metav1.KindVM, proj.Metadata.Name)
			}
		}
		return nil

	case metav1.KindNIC:
		// A NIC is referenced by a VM when its name is in VM.spec.nicRefs.
		vms, err := v.list(ctx, metav1.KindVM, req.Kind)
		if err != nil {
			return err
		}
		for _, raw := range vms {
			var proj vmDeleteRefProjection
			if derr := json.Unmarshal(raw.Value, &proj); derr != nil {
				return v.decodeReject(metav1.KindVM, raw.Key, derr)
			}
			if slices.Contains(proj.Spec.NICRefs, req.Name) {
				return v.reject(req.Kind, req.Name, metav1.KindVM, proj.Metadata.Name)
			}
		}
		return nil

	case metav1.KindVM:
		// A VM is referenced by Snapshot.spec.vmRef (by VM NAME). Knife 3 made VM
		// the apex with no reverse edge; Snapshot is the first legitimate VM
		// downstream reference (see knife4 spec §4.3). Volume.vmRef / NIC.vmRef are
		// ownership backpointers (uids), not dependencies the VM has, so they must
		// not block deleting the VM (see the type doc for why blocking on them
		// deadlocks reverse teardown). The apply-side ReferenceValidator still
		// prevents attaching a new Volume/NIC to a deleting VM, which is where that
		// backpointer is enforced.
		snaps, err := v.list(ctx, metav1.KindSnapshot, req.Kind)
		if err != nil {
			return err
		}
		for _, raw := range snaps {
			var proj snapshotDeleteRefProjection
			if derr := json.Unmarshal(raw.Value, &proj); derr != nil {
				return v.decodeReject(metav1.KindSnapshot, raw.Key, derr)
			}
			if proj.Spec.VMRef == req.Name {
				return v.reject(req.Kind, req.Name, metav1.KindSnapshot, proj.Metadata.Name)
			}
		}
		return nil

	default:
		// A kind with no downstream reverse edge is reference-clear. An unknown
		// kind never reaches here in practice: the DELETE handler reads the object
		// first, and an unknown kind's key resolves to a 404 before admission runs.
		return nil
	}
}

// list reads every stored object of listKind, wrapping a store failure as an
// Internal admission error annotated with the target kind being deleted so the
// 500 says which delete the scan was protecting.
func (v ReverseReferenceValidator) list(ctx context.Context, listKind, targetKind metav1.Kind) ([]store.RawObject, error) {
	raws, err := v.Store.List(ctx, ListPrefix(listKind))
	if err != nil {
		return nil, Reject(v.Name(), ReasonInternal, fmt.Errorf("list %s for %s reverse-reference scan: %w", listKind, targetKind, err))
	}
	return raws, nil
}

// decodeReject wraps a projection decode failure as an Internal admission error.
func (v ReverseReferenceValidator) decodeReject(listKind metav1.Kind, key string, err error) error {
	return Reject(v.Name(), ReasonInternal, fmt.Errorf("decode %s %q reverse-reference projection: %w", listKind, key, err))
}

// reject renders the protective Conflict. The message preserves the historical
// "still referenced by <Kind>/<name>" contract callers (and tests) depend on to
// learn what must be removed before the delete can proceed.
func (v ReverseReferenceValidator) reject(targetKind metav1.Kind, targetName string, refKind metav1.Kind, refName string) error {
	return Reject(v.Name(), ReasonConflict, fmt.Errorf("%s/%s still referenced by %s/%s", targetKind, targetName, refKind, refName))
}
