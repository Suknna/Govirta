//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func replaceCycle(ctx context.Context, t *testing.T, ctl, server, tmpDir string) {
	t.Helper()

	getOut, err := runCtl(ctx, ctl, "get", "--server", server, "VM", vmName)
	if err != nil {
		t.Fatalf("govirtctl get VM/%s: %v\noutput:\n%s", vmName, err, getOut)
	}

	var current map[string]any
	dec := json.NewDecoder(strings.NewReader(getOut))
	if err := dec.Decode(&current); err != nil {
		t.Fatalf("decode first JSON object from govirtctl get output: %v\noutput:\n%s", err, getOut)
	}
	oldObject := cloneJSONMap(t, current)

	meta, ok := current["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("govirtctl get VM/%s returned metadata=%T, want object", vmName, current["metadata"])
	}
	if rv, ok := meta["resourceVersion"].(string); !ok || rv == "" {
		t.Fatalf("govirtctl get VM/%s returned empty metadata.resourceVersion: %#v", vmName, meta["resourceVersion"])
	}

	spec, ok := current["spec"].(map[string]any)
	if !ok {
		t.Fatalf("govirtctl get VM/%s returned spec=%T, want object", vmName, current["spec"])
	}
	spec["powerState"] = "Off"
	spec["powerOffMode"] = "Acpi"

	replacePath := filepath.Join(tmpDir, "vm-replace-current.json")
	writeJSONManifest(t, replacePath, current)
	replaceOut, err := runCtl(ctx, ctl, "replace", "--server", server, "-f", replacePath)
	if err != nil {
		t.Fatalf("govirtctl replace VM/%s: %v\noutput:\n%s", vmName, err, replaceOut)
	}
	if !strings.Contains(replaceOut, "VM/"+vmName+" replaced") {
		t.Fatalf("govirtctl replace output = %q, want VM/%s replaced", replaceOut, vmName)
	}
	t.Logf("replace VM/%s with fresh resourceVersion succeeded: %s", vmName, strings.TrimSpace(replaceOut))

	stalePath := filepath.Join(tmpDir, "vm-replace-stale.json")
	writeJSONManifest(t, stalePath, oldObject)
	staleOut, err := runCtl(ctx, ctl, "replace", "--server", server, "-f", stalePath)
	if err == nil {
		t.Fatalf("stale govirtctl replace VM/%s unexpectedly succeeded\noutput:\n%s", vmName, staleOut)
	}
	if !strings.Contains(staleOut, "409") {
		t.Fatalf("stale govirtctl replace output = %q, want 409", staleOut)
	}
	t.Logf("stale replace VM/%s correctly rejected with 409: %s", vmName, strings.TrimSpace(staleOut))
}

func cloneJSONMap(t *testing.T, obj map[string]any) map[string]any {
	t.Helper()

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(obj); err != nil {
		t.Fatalf("clone JSON object: encode: %v", err)
	}
	var cloned map[string]any
	if err := json.NewDecoder(&buf).Decode(&cloned); err != nil {
		t.Fatalf("clone JSON object: decode: %v", err)
	}
	return cloned
}

func writeJSONManifest(t *testing.T, path string, obj map[string]any) {
	t.Helper()

	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
