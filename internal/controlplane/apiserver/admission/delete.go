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
// downstream object that still references the deletion target, rejecting the
// delete with Conflict (409) until the dependent is removed first. It is the
// inverse of apply's ReferenceValidator: apply rejects a referrer that points at
// a missing/deleting target; this rejects deleting a target that is still
// pointed at. The reference graph (by object NAME unless noted):
//
//	StoragePool <- Volume.poolRef, Volume.imageFilePoolRef, Image.filePoolRef
//	Image       <- Volume.imageRef
//	Network     <- NIC.networkRef
//	Volume      <- VM.volumeRefs[]
//	NIC         <- VM.nicRefs[]
//	VM          <- Volume.vmRef, NIC.vmRef  (by VM UID, not name)
//
// The VM edges close the gap Knife 1 left open: Volume/NIC carry a vmRef holding
// the owning VM's UID, so deleting a VM that still owns a Volume or NIC must be
// rejected. DELETE only carries kind+name, so the VM's UID is read from the
// request's OldObject (the metadata the handler already decoded for the stamp
// write-back); OldRaw is the fallback source. See targetUID.
//
// Each scan decodes a minimal projection (metadata.name plus the spec ref fields
// it needs), never a whole typed object. A store list or projection decode
// failure is reported as Internal (500); errors never surface as a false
// "unreferenced" pass.
type ReverseReferenceValidator struct {
	Store StoreReader
}

func (ReverseReferenceValidator) Name() string { return "ReverseReferenceValidator" }

// volumeDeleteRefProjection decodes the StoragePool/Image/VM-bearing ref fields a
// Volume can hold: poolRef (block pool) and imageFilePoolRef (root volume's image
// file pool) both name a StoragePool; imageRef names an Image; vmRef holds the
// owning VM's UID.
type volumeDeleteRefProjection struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		PoolRef          string `json:"poolRef"`
		ImageRef         string `json:"imageRef"`
		ImageFilePoolRef string `json:"imageFilePoolRef"`
		VMRef            string `json:"vmRef"`
	} `json:"spec"`
}

// imageDeleteRefProjection decodes an Image's filePoolRef, which names the file
// StoragePool the image bytes live in.
type imageDeleteRefProjection struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		FilePoolRef string `json:"filePoolRef"`
	} `json:"spec"`
}

// nicDeleteRefProjection decodes a NIC's networkRef (names a Network) and vmRef
// (holds the owning VM's UID).
type nicDeleteRefProjection struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		NetworkRef string `json:"networkRef"`
		VMRef      string `json:"vmRef"`
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

func (v ReverseReferenceValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationDelete {
		return nil
	}
	if v.Store == nil {
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("store reader is required"))
	}

	switch req.Kind {
	case metav1.KindStoragePool:
		// Three edges name a StoragePool: Volume.poolRef, Volume.imageFilePoolRef,
		// and Image.filePoolRef. Scan Volume first, then Image — a file pool that
		// holds only an Image (no Volume) is still referenced via Image.filePoolRef,
		// and missing it would orphan the image.
		vols, err := v.list(ctx, metav1.KindVolume, req.Kind)
		if err != nil {
			return err
		}
		for _, raw := range vols {
			var proj volumeDeleteRefProjection
			if derr := json.Unmarshal(raw.Value, &proj); derr != nil {
				return v.decodeReject(metav1.KindVolume, raw.Key, derr)
			}
			if proj.Spec.PoolRef == req.Name || proj.Spec.ImageFilePoolRef == req.Name {
				return v.reject(req.Kind, req.Name, metav1.KindVolume, proj.Metadata.Name)
			}
		}
		imgs, err := v.list(ctx, metav1.KindImage, req.Kind)
		if err != nil {
			return err
		}
		for _, raw := range imgs {
			var proj imageDeleteRefProjection
			if derr := json.Unmarshal(raw.Value, &proj); derr != nil {
				return v.decodeReject(metav1.KindImage, raw.Key, derr)
			}
			if proj.Spec.FilePoolRef == req.Name {
				return v.reject(req.Kind, req.Name, metav1.KindImage, proj.Metadata.Name)
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
		// A VM is referenced by Volume.vmRef and NIC.vmRef, both holding the VM's
		// UID (not its name). Knife 1 skipped this edge; closing it requires the
		// VM's own UID, which DELETE does not carry by name — it is resolved from
		// the request's decoded target metadata.
		uid, err := v.targetUID(req)
		if err != nil {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("resolve deleting VM uid: %w", err))
		}
		if uid == "" {
			// A VM with no UID cannot be the target of any vmRef, so it is
			// reference-clear by construction; nothing downstream can point at it.
			return nil
		}
		vols, err := v.list(ctx, metav1.KindVolume, req.Kind)
		if err != nil {
			return err
		}
		for _, raw := range vols {
			var proj volumeDeleteRefProjection
			if derr := json.Unmarshal(raw.Value, &proj); derr != nil {
				return v.decodeReject(metav1.KindVolume, raw.Key, derr)
			}
			if proj.Spec.VMRef == uid {
				return v.reject(req.Kind, req.Name, metav1.KindVolume, proj.Metadata.Name)
			}
		}
		nics, err := v.list(ctx, metav1.KindNIC, req.Kind)
		if err != nil {
			return err
		}
		for _, raw := range nics {
			var proj nicDeleteRefProjection
			if derr := json.Unmarshal(raw.Value, &proj); derr != nil {
				return v.decodeReject(metav1.KindNIC, raw.Key, derr)
			}
			if proj.Spec.VMRef == uid {
				return v.reject(req.Kind, req.Name, metav1.KindNIC, proj.Metadata.Name)
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

// targetUID resolves the deletion target's UID for the VM reverse-reference scan.
// The DELETE handler decodes the target's metadata for the stamp write-back and
// passes it as Request.OldObject (a metav1.ObjectMeta); OldRaw (the full stored
// bytes) is the fallback. Only the VM branch needs this — other kinds match by
// name from Request.Name.
func (v ReverseReferenceValidator) targetUID(req Request) (string, error) {
	if meta, ok := req.OldObject.(metav1.ObjectMeta); ok {
		return meta.UID, nil
	}
	if len(req.OldRaw) > 0 {
		meta, err := decodeStoredMetadata(req.OldRaw)
		if err != nil {
			return "", err
		}
		return meta.UID, nil
	}
	return "", fmt.Errorf("delete target metadata unavailable (OldObject type %T, OldRaw empty)", req.OldObject)
}
