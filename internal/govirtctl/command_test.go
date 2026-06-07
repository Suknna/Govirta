package govirtctl

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRunApplyPostsManifest(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"kind":"StoragePool","metadata":{"name":"pool-a"}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	manifest := filepath.Join(dir, "pool.json")
	if err := os.WriteFile(manifest, []byte(`{"kind":"StoragePool","metadata":{"name":"pool-a"}}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"apply", "--server", srv.URL, "-f", manifest}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run apply code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if gotPath != "POST /apis/StoragePool/pool-a" {
		t.Fatalf("master saw %q, want POST /apis/StoragePool/pool-a", gotPath)
	}
}

func TestRunApplyRejectsManifestWithoutKind(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(manifest, []byte(`{"metadata":{"name":"x"}}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"apply", "--server", "http://unused", "-f", manifest}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("Run apply code = 0, want non-zero for manifest with no kind")
	}
}

func TestRunGetPrintsPhaseForVM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kind":"VM","metadata":{"name":"vm-a"},"status":{"phase":"running"}}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"get", "--server", srv.URL, "VM", "vm-a"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run get code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("phase: running")) {
		t.Fatalf("get stdout = %q, want it to contain 'phase: running'", stdout.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"frobnicate"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("Run code = 0, want non-zero for unknown command")
	}
}
