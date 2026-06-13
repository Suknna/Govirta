package govirtctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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

func TestRunReplacePutsManifest(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kind":"VM","metadata":{"name":"vm-a","resourceVersion":"2"}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	manifest := filepath.Join(dir, "vm.json")
	if err := os.WriteFile(manifest, []byte(`{"kind":"VM","metadata":{"name":"vm-a","resourceVersion":"1"}}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"replace", "--server", srv.URL, "-f", manifest}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run replace code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if gotPath != "PUT /apis/VM/vm-a" {
		t.Fatalf("master saw %q, want PUT /apis/VM/vm-a", gotPath)
	}
	if got := stdout.String(); got != "VM/vm-a replaced\n" {
		t.Fatalf("Run replace stdout = %q, want replaced message", got)
	}
}

func TestRunReplaceRequiresResourceVersion(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "vm.json")
	if err := os.WriteFile(manifest, []byte(`{"kind":"VM","metadata":{"name":"vm-a"}}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"replace", "--server", "http://127.0.0.1:1", "-f", manifest}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run replace code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("Run replace stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !contains(got, "metadata.resourceVersion") || !contains(got, "govirtctl get first") {
		t.Fatalf("Run replace stderr = %q, want resourceVersion guidance", got)
	}
}

func TestRunReplaceConflictExitsOneAndSurfacesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/apis/VM/vm-a" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"resource version conflict"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	manifest := filepath.Join(dir, "vm.json")
	if err := os.WriteFile(manifest, []byte(`{"kind":"VM","metadata":{"name":"vm-a","resourceVersion":"1"}}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"replace", "--server", srv.URL, "-f", manifest}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("Run replace code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("Run replace stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !contains(got, "master returned 409") || !contains(got, "resource version conflict") {
		t.Fatalf("Run replace stderr = %q, want 409 and conflict message", got)
	}
}

func TestRunGetPrintsEditableJSON(t *testing.T) {
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
	if bytes.Contains(stdout.Bytes(), []byte("phase:")) {
		t.Fatalf("get stdout = %q, want pure JSON without human phase suffix", stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("get stdout must be directly usable as JSON for replace: %v\noutput:\n%s", err, stdout.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"frobnicate"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("Run code = 0, want non-zero for unknown command")
	}
}

func TestRunImageUploadComputesSizeAndSHA256(t *testing.T) {
	content := []byte("image-bytes")
	wantSum := sha256.Sum256(content)
	wantSHA := hex.EncodeToString(wantSum[:])
	var gotPath, gotUID, gotFormat, gotSHA, gotSize, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		gotUID = r.URL.Query().Get("uid")
		gotFormat = r.URL.Query().Get("format")
		gotSHA = r.URL.Query().Get("sha256")
		gotSize = r.URL.Query().Get("declaredSizeBytes")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upload body: %v", err)
		}
		gotBody = string(body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"kind":"Image","metadata":{"name":"img-a"}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "image.qcow2")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatalf("write image file: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"image", "upload", "--server", srv.URL, "--name", "img-a", "--uid", "uid-img-a", "--version", "v1", "--format", "qcow2", "--file", file}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run image upload code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if gotPath != "PUT /apis/Image/img-a/store/v1" {
		t.Fatalf("master saw %q, want PUT /apis/Image/img-a/store/v1", gotPath)
	}
	if gotUID != "uid-img-a" || gotFormat != "qcow2" || gotSHA != wantSHA || gotSize != "11" || gotBody != string(content) {
		t.Fatalf("upload fields uid=%q format=%q sha=%q size=%q body=%q, want computed fields", gotUID, gotFormat, gotSHA, gotSize, gotBody)
	}
}

func TestRunImageUploadRequiresUID(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "image.qcow2")
	if err := os.WriteFile(file, []byte("image-bytes"), 0o600); err != nil {
		t.Fatalf("write image file: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"image", "upload", "--server", "http://unused", "--name", "img-a", "--version", "v1", "--format", "qcow2", "--file", file}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run image upload code = %d, want 2", code)
	}
	if got := stderr.String(); !contains(got, "--uid") {
		t.Fatalf("stderr = %q, want --uid guidance", got)
	}
}

// runDelete exit-code contract: 0 on acceptance, 1 on any client error
// (not-found / referenced / server), 2 on usage errors (missing --server or
// wrong positional arg count). Each case drives the real runDelete against an
// httptest server so we exercise the full client→HTTP→exit-code path.

func TestRunDeleteAcceptedExitsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/apis/VM/vm-a" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runDelete(context.Background(), []string{"--server", srv.URL, "VM", "vm-a"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDelete exit = %d, want 0 (stderr=%q)", code, stderr.String())
	}
	if got := stdout.String(); got != "VM/vm-a deleting\n" {
		t.Fatalf("runDelete stdout = %q, want %q", got, "VM/vm-a deleting\n")
	}
}

func TestRunDeleteNotFoundExitsOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runDelete(context.Background(), []string{"--server", srv.URL, "VM", "missing"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("runDelete exit = %d, want 1", code)
	}
}

func TestRunDeleteReferencedExitsOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"still referenced by VM/vm-a"}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := runDelete(context.Background(), []string{"--server", srv.URL, "StoragePool", "pool-a"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("runDelete exit = %d, want 1", code)
	}
	if got := stderr.String(); !contains(got, "still referenced by VM/vm-a") {
		t.Fatalf("runDelete stderr = %q, want it to surface the referenced-by text", got)
	}
}

func TestRunDeleteMissingServerExitsTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDelete(context.Background(), []string{"VM", "vm-a"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("runDelete exit = %d, want 2", code)
	}
}

func TestRunDeleteWrongArgCountExitsTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDelete(context.Background(), []string{"--server", "http://127.0.0.1:0", "VM"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("runDelete exit = %d, want 2", code)
	}
}
