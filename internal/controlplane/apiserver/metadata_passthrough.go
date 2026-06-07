package apiserver

import (
	"encoding/json"
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// metadata_passthrough.go holds the kind-agnostic "decode metadata, pass through
// the rest" object model shared by the metadata-only mutating handlers (delete's
// deletionTimestamp/finalizer stamp and the finalizers sub-resource's finalizer
// removal). Both must mutate only metadata while leaving spec/status physically
// untouched, and neither should know any of the six concrete resource types —
// extracting the shared model here keeps that pass-through guarantee in one place
// and out of every handler that needs it.

// metadataPatchObject is the minimal, kind-agnostic decode of a stored object for
// a metadata-only patch. The whole object is parsed only as a top-level map of
// raw bytes (rest), and metadata is parsed into the typed ObjectMeta so a handler
// can read/mutate deletionTimestamp and finalizers without bare-string fiddling.
// On re-marshal, every non-metadata key (spec, status, apiVersion, kind, ...) is
// carried through as its original bytes, so a handler using this model physically
// cannot rewrite spec/status — mirroring the status patch pass-through design.
type metadataPatchObject struct {
	Metadata metav1.ObjectMeta
	rest     map[string]json.RawMessage
}

// decodeMetadataPatchObject parses raw stored bytes into a metadataPatchObject:
// the top-level map preserves spec/status as opaque bytes, while metadata is
// decoded into the typed ObjectMeta. ObjectMeta is the canonical, complete
// metadata model (no fields outside it), so this round-trip is lossless for
// metadata too.
func decodeMetadataPatchObject(value []byte) (metadataPatchObject, error) {
	rest := map[string]json.RawMessage{}
	if err := json.Unmarshal(value, &rest); err != nil {
		return metadataPatchObject{}, fmt.Errorf("decode stored object: %w", err)
	}
	var meta metav1.ObjectMeta
	if metaRaw, ok := rest["metadata"]; ok {
		if err := json.Unmarshal(metaRaw, &meta); err != nil {
			return metadataPatchObject{}, fmt.Errorf("decode object metadata: %w", err)
		}
	}
	return metadataPatchObject{Metadata: meta, rest: rest}, nil
}

// marshal re-assembles the object: the (possibly mutated) typed metadata is
// re-encoded and spliced back over the metadata key, and the whole map is
// marshalled. spec/status and all other keys pass through as their original
// bytes, untouched.
func (o metadataPatchObject) marshal() ([]byte, error) {
	metaBytes, err := json.Marshal(o.Metadata)
	if err != nil {
		return nil, fmt.Errorf("encode object metadata: %w", err)
	}
	o.rest["metadata"] = json.RawMessage(metaBytes)
	out, err := json.Marshal(o.rest)
	if err != nil {
		return nil, fmt.Errorf("encode merged object: %w", err)
	}
	return out, nil
}
