package apiserver

import (
	"encoding/json"
	"fmt"
)

func withBodyResourceVersion(raw []byte, resourceVersion string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("decode object for resourceVersion injection: %w", err)
	}

	metadataRaw, ok := obj["metadata"]
	if !ok {
		return nil, fmt.Errorf("object metadata is missing or not an object")
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metadataRaw, &meta); err != nil {
		return nil, fmt.Errorf("object metadata is missing or not an object")
	}
	resourceVersionRaw, err := json.Marshal(resourceVersion)
	if err != nil {
		return nil, fmt.Errorf("encode resourceVersion: %w", err)
	}
	meta["resourceVersion"] = resourceVersionRaw

	metadataOut, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("encode metadata with resourceVersion: %w", err)
	}
	obj["metadata"] = metadataOut

	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("encode object with resourceVersion: %w", err)
	}
	return out, nil
}
