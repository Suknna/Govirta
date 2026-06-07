package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// reference_guard.go implements the reverse-reference scan that protects a
// delete: given the kind/name a caller wants to delete, it lists the downstream
// kind(s) that could reference it and reports the first referencing object. The
// DELETE handler (a later task) uses a true result to reject the delete with 409,
// forcing the caller to remove the dependent object first. The reference graph is
// the inverse of the apply-time refs: Volume points up at StoragePool/Image, NIC
// points up at Network, and VM points up at Volume/NIC. Every match is by object
// name (the *Ref fields holding a name), never by uid — Volume/NIC carry a vmRef
// uid, but that is not scanned here because a VM has no name-referencing
// downstream kind and is always reference-clear.
//
// Each scan uses a minimal projection (only metadata.name plus the spec ref
// fields it needs), mirroring the watch handler's nodeNameSelector pattern, so a
// scan never decodes a whole apis object. The store stays kind-agnostic: all kind
// dispatch and ref interpretation lives here.

// volumeRefProjection is the minimal projection decoded from a stored Volume to
// detect a reference to a StoragePool or an Image. PoolRef (block pool) and
// ImageFilePoolRef (the image-file pool of a root volume) both name a
// StoragePool, so a Pool delete must check both; ImageRef names an Image.
type volumeRefProjection struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		PoolRef          string `json:"poolRef"`
		ImageRef         string `json:"imageRef"`
		ImageFilePoolRef string `json:"imageFilePoolRef"`
	} `json:"spec"`
}

// nicRefProjection is the minimal projection decoded from a stored NIC to detect
// a reference to a Network (spec.networkRef names a Network object).
type nicRefProjection struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		NetworkRef string `json:"networkRef"`
	} `json:"spec"`
}

// vmRefProjection is the minimal projection decoded from a stored VM to detect a
// reference to a Volume or a NIC. VolumeRefs/NICRefs are string arrays of object
// names; a match is membership in the array.
type vmRefProjection struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		VolumeRefs []string `json:"volumeRefs"`
		NICRefs    []string `json:"nicRefs"`
	} `json:"spec"`
}

// referenceGuard reports whether any downstream object references the kind/name a
// caller wants to delete. On the first hit it returns referencedBy as
// "<DownstreamKind>/<name>" (the human-readable identity for a 409 message) and
// referenced=true. With no reference it returns ("", false, nil). A store list or
// projection decode failure is propagated (errors 向上传播, never swallowed) as
// ("", false, err). A VM has no name-referencing downstream kind, so it is always
// reference-clear and returns false without listing anything.
func (s *Server) referenceGuard(ctx context.Context, kind metav1.Kind, name string) (string, bool, error) {
	switch kind {
	case metav1.KindStoragePool:
		// A StoragePool is referenced by a Volume via either its block PoolRef or
		// its root-volume ImageFilePoolRef; either match counts.
		raws, err := s.store.List(ctx, listPrefix(metav1.KindVolume))
		if err != nil {
			return "", false, fmt.Errorf("apiserver: list Volume for StoragePool reference guard: %w", err)
		}
		for _, raw := range raws {
			var proj volumeRefProjection
			if err := json.Unmarshal(raw.Value, &proj); err != nil {
				return "", false, fmt.Errorf("apiserver: decode Volume %q reference projection: %w", raw.Key, err)
			}
			if proj.Spec.PoolRef == name || proj.Spec.ImageFilePoolRef == name {
				return refIdentity(metav1.KindVolume, proj.Metadata.Name), true, nil
			}
		}
		return "", false, nil

	case metav1.KindImage:
		// An Image is referenced by a root Volume via its ImageRef.
		raws, err := s.store.List(ctx, listPrefix(metav1.KindVolume))
		if err != nil {
			return "", false, fmt.Errorf("apiserver: list Volume for Image reference guard: %w", err)
		}
		for _, raw := range raws {
			var proj volumeRefProjection
			if err := json.Unmarshal(raw.Value, &proj); err != nil {
				return "", false, fmt.Errorf("apiserver: decode Volume %q reference projection: %w", raw.Key, err)
			}
			if proj.Spec.ImageRef == name {
				return refIdentity(metav1.KindVolume, proj.Metadata.Name), true, nil
			}
		}
		return "", false, nil

	case metav1.KindNetwork:
		// A Network is referenced by a NIC via its NetworkRef.
		raws, err := s.store.List(ctx, listPrefix(metav1.KindNIC))
		if err != nil {
			return "", false, fmt.Errorf("apiserver: list NIC for Network reference guard: %w", err)
		}
		for _, raw := range raws {
			var proj nicRefProjection
			if err := json.Unmarshal(raw.Value, &proj); err != nil {
				return "", false, fmt.Errorf("apiserver: decode NIC %q reference projection: %w", raw.Key, err)
			}
			if proj.Spec.NetworkRef == name {
				return refIdentity(metav1.KindNIC, proj.Metadata.Name), true, nil
			}
		}
		return "", false, nil

	case metav1.KindVolume:
		// A Volume is referenced by a VM when its name is in VM.spec.volumeRefs.
		raws, err := s.store.List(ctx, listPrefix(metav1.KindVM))
		if err != nil {
			return "", false, fmt.Errorf("apiserver: list VM for Volume reference guard: %w", err)
		}
		for _, raw := range raws {
			var proj vmRefProjection
			if err := json.Unmarshal(raw.Value, &proj); err != nil {
				return "", false, fmt.Errorf("apiserver: decode VM %q reference projection: %w", raw.Key, err)
			}
			if slices.Contains(proj.Spec.VolumeRefs, name) {
				return refIdentity(metav1.KindVM, proj.Metadata.Name), true, nil
			}
		}
		return "", false, nil

	case metav1.KindNIC:
		// A NIC is referenced by a VM when its name is in VM.spec.nicRefs.
		raws, err := s.store.List(ctx, listPrefix(metav1.KindVM))
		if err != nil {
			return "", false, fmt.Errorf("apiserver: list VM for NIC reference guard: %w", err)
		}
		for _, raw := range raws {
			var proj vmRefProjection
			if err := json.Unmarshal(raw.Value, &proj); err != nil {
				return "", false, fmt.Errorf("apiserver: decode VM %q reference projection: %w", raw.Key, err)
			}
			if slices.Contains(proj.Spec.NICRefs, name) {
				return refIdentity(metav1.KindVM, proj.Metadata.Name), true, nil
			}
		}
		return "", false, nil

	case metav1.KindVM:
		// A VM sits at the top of the reference graph: no first-class kind
		// references a VM by name (Volume/NIC carry a vmRef uid, deliberately not
		// scanned here), so a VM is always safe to delete from a reference
		// standpoint.
		return "", false, nil

	default:
		// An unrecognized kind is a caller/dispatch error, not a silent pass: the
		// DELETE handler dispatches on a validated kind, so reaching here means a
		// wiring mistake that must surface rather than be treated as unreferenced.
		return "", false, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
}

// listPrefix builds the trailing-slash store prefix /govirta/<kind>/ that scopes
// a List to exactly one kind's collection, matching the watch handler's prefix
// shape. The trailing slash prevents a kind whose name prefixes another from
// bleeding into the scan.
func listPrefix(kind metav1.Kind) string {
	return fmt.Sprintf("/govirta/%s/", kind)
}

// refIdentity renders a referencing object's identity as "<Kind>/<name>" for the
// 409 message the DELETE handler returns.
func refIdentity(kind metav1.Kind, name string) string {
	return fmt.Sprintf("%s/%s", kind, name)
}
