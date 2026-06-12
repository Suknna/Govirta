package apiserver

import (
	"encoding/json"
	"fmt"
)

func withBodyResourceVersion(raw []byte, resourceVersion string) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("decode object for resourceVersion injection: %w", err)
	}

	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("object metadata is missing or not an object")
	}
	meta["resourceVersion"] = resourceVersion

	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("encode object with resourceVersion: %w", err)
	}
	return out, nil
}
