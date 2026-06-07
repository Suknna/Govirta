# Vertical Slice Plan 4: End-to-End Acceptance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Light up the full Govirta distributed spine end-to-end: `govirtctl` submits seven resource manifests (the six first-class kinds, with StoragePool instantiated twice — a block pool and a file pool) to the master, the node's controller-manager reconciles each onto the real Linux execution plane, a real daemonized QEMU guest boots, and `govirtctl get vm` observes `Running` — proven on the spec's three-node topology (etcd container + host `govirtad` + Lima-guest `govirtlet`).

**Architecture:** This is the fourth and final slice of the create-only walking skeleton. Plans 1–3 built the API contract layer, the master side (etcd Store + apiserver + scheduler + MAC allocator), and the node side (controller-manager framework + six first-class controllers + master watch client). Plan 4 adds the missing human entrypoint — `govirtctl` as a kind-agnostic manifest CLI — and proves the whole chain on real hardware. It is the first time the six controllers drive the **real** Linux netlink/nftables/CoreDHCP/QEMU execution plane via watch from a **real** master (Plan 3's smoke used darwin no-op host managers), so integration defects surface here one kind at a time.

**Tech Stack:** Go 1.26, the existing apiserver HTTP contract (`POST/PUT /apis/{kind}/{name}`, `GET /apis/{kind}/{name}`, `GET /apis/{kind}?watch=true&nodeName=&resourceVersion=`, `PATCH /apis/{kind}/{name}/status`), etcd v3.6.12 (OrbStack Docker), Lima (vz + nestedVirtualization) for the KVM compute node, CirrOS aarch64 guest.

---

## Confirmed real interfaces (grounding — do not re-derive)

These were read from the merged Plan 1–3 code at plan-writing time. Use them verbatim; do not invent endpoints or constants.

- **apiVersion**: `govirta.io/v1alpha1` (`pkg/apis/meta/v1alpha1`, const `APIGroupVersion`).
- **Kind constants** (`metav1.Kind`): `StoragePool`, `Image`, `Volume`, `Network`, `NIC`, `VM`.
- **Routes** (all under the master apiserver root):
  - Apply: `POST /apis/{kind}/{name}` and `PUT /apis/{kind}/{name}` — body is the full object JSON; path name must equal `metadata.name` (apiserver returns 400 `ErrNameMismatch` otherwise).
  - Get one: `GET /apis/{kind}/{name}` — returns the stored object JSON; response header `X-Resource-Version` carries its resourceVersion. 404 if absent.
  - List/Watch: `GET /apis/{kind}` lists; `GET /apis/{kind}?watch=true&nodeName=<n>&resourceVersion=<rv>` streams (govirtctl only needs list/get, not watch).
  - Status: `PATCH /apis/{kind}/{name}/status` — node-only; govirtctl never patches status.
- **Error envelope**: failures return `{"error": "..."}` with the HTTP status code; success bodies are the raw object JSON.
- **govirtad flags** (`cmd/govirtad/main.go`): `--etcd-endpoint`, `--node-name`, `--listen-addr`, `--mac-prefix` (3-byte OUI `x:xx:xx`), `--mac-suffix-start`, `--mac-suffix-end`.
- **govirtlet flags** (`cmd/govirtlet/main.go`): `--master-url`, `--node-name`, `--runtime-root`, `--image-source-root`, `--owner-uid`, `--owner-gid`, `--guest-cpu`.
- **M-2 target**: `internal/storage/local/driver.go:24` has unexported `pathKey = "path"`; the volume controller (`internal/node/controllers/volume.go`) re-hardcodes `volumePathContextKey = "path"`. Exporting the storage-side constant removes the silent cross-layer duplication.

---

## File Structure

**M-2 prerequisite fix** (storage layer, the one cross-plan touch this slice owns):
- Modify `internal/storage/local/driver.go` — rename unexported `pathKey` → exported `PathKey` (doc the cross-layer contract), update in-file uses.
- Modify `internal/node/controllers/volume.go` — replace local `volumePathContextKey` constant with a reference to `local.PathKey`.

**govirtctl manifest CLI** (new testable package + thin entry):
- Create `internal/govirtctl/client.go` — minimal master HTTP client: `Apply(ctx, kind, name, body)` and `Get(ctx, kind, name)`.
- Create `internal/govirtctl/client_test.go` — httptest-backed client behavior tests.
- Create `internal/govirtctl/command.go` — manifest decode (kind+name from object), `apply -f`, `get <kind> <name>` dispatch + flag parsing.
- Create `internal/govirtctl/command_test.go` — command parsing + dispatch tests against an httptest master.
- Modify `cmd/govirtctl/main.go` — thin entry: parse args, dispatch, exit code (replaces the version-only stub; version moves to a `version` subcommand).

**e2e acceptance** (new build-tagged suite + orchestration):
- Create `test/e2e/doc.go` — `//go:build e2e` package doc.
- Create `test/e2e/closure_test.go` — the host-driven closure test (applies all seven manifests).
- Create `test/e2e/manifests/*.json` — seven dependency-ordered resource manifests.
- Create `scripts/e2e.sh` — three-node orchestration (etcd container + host govirtad + Lima-guest govirtlet + drive govirtctl).

---

### Task 1: M-2 prerequisite — export the storage path-context key

**Files:**
- Modify: `internal/storage/local/driver.go:21-25`
- Modify: `internal/node/controllers/volume.go` (the `volumePathContextKey` constant and its uses)
- Test: `internal/storage/local` and `internal/node/controllers` existing suites

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the host-path context key has exactly one definition. `internal/storage/local` exports `PathKey = "path"`; the volume controller references `local.PathKey` instead of re-declaring the literal.
Acceptance evidence:
- `go build ./...` succeeds.
- `grep -rn '"path"' internal/node/controllers/volume.go` returns no standalone key literal (only `local.PathKey` reference remains).
- `go test ./internal/storage/local/... ./internal/node/controllers/...` passes.

- [ ] **Step 2: Export the constant in storage/local**

In `internal/storage/local/driver.go`, change the const block:

```go
const (
	driverName = "local-qcow2"
	formatKey  = "format"
	// PathKey is the volume.Context map key under which this driver records the
	// host filesystem path of a created volume. It is exported because the node
	// VM/volume controller reads the published volume's host path from
	// Context[PathKey]; sharing one constant removes a silent cross-layer string
	// duplication (a drifted key would otherwise degrade the controller to a
	// permanent not-ready without a compile error).
	PathKey = "path"
)
```

Then update every in-file use of `pathKey` to `PathKey` (the literal occurrences at the driver's volume-creation and publish/delete sites).

- [ ] **Step 3: Reference the exported constant in the volume controller**

In `internal/node/controllers/volume.go`, remove the local `volumePathContextKey = "path"` declaration and replace its uses with `local.PathKey`. Add the import `"github.com/suknna/govirta/internal/storage/local"` if not already present (use aft_import). Keep the existing `path == ""` guard that maps a missing path to failed+requeue.

- [ ] **Step 4: Run targeted verification**

Run: `go build ./... && go test ./internal/storage/local/... ./internal/node/controllers/...`
Expected: build OK, all tests PASS.

- [ ] **Step 5: If verification fails, fix implementation or stale tests**

If a storage-local test asserted the old unexported name via reflection (unlikely), update it. If the volume controller test referenced `volumePathContextKey`, point it at `local.PathKey`.

- [ ] **Step 6: Run broader verification**

Run: `gofmt -l internal/storage/local internal/node/controllers && go vet ./internal/storage/local/... ./internal/node/controllers/...`
Expected: no gofmt output, vet clean.

- [ ] **Step 7: Commit**

```bash
git add internal/storage/local/driver.go internal/node/controllers/volume.go
git commit -m "refactor(storage): export local.PathKey to remove cross-layer key duplication"
```

---

### Task 2: govirtctl master HTTP client

**Files:**
- Create: `internal/govirtctl/client.go`
- Test: `internal/govirtctl/client_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: a minimal HTTP client that applies and gets objects against the apiserver contract. `Apply(ctx, "StoragePool", "pool-a", body)` issues `POST /apis/StoragePool/pool-a` and returns the response object bytes; `Get(ctx, "VM", "vm-a")` issues `GET /apis/VM/vm-a` and returns object bytes + resourceVersion, classifying 404 as `ErrNotFound`.
Acceptance evidence:
- `go test ./internal/govirtctl/ -run TestClient -v` passes (httptest-backed).
- Apply surfaces the apiserver `{"error":...}` envelope as a Go error on non-2xx.
- Get on 404 returns a sentinel `ErrNotFound`.

- [ ] **Step 2: Write the client**

```go
// Package govirtctl implements the Govirta control-plane CLI: a kind-agnostic
// manifest tool that submits full resource objects to the master apiserver and
// reads them back. It deliberately holds no schema knowledge beyond locating the
// kind and name inside an object's envelope — every behavior-affecting field is
// supplied by the operator in the manifest (显式优于隐式), never inferred here.
package govirtctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrNotFound is returned by Get when the master has no object of that kind/name.
var ErrNotFound = errors.New("govirtctl: object not found")

// resourceVersionHeader mirrors the apiserver's X-Resource-Version response
// header (internal/controlplane/apiserver/handler_get.go).
const resourceVersionHeader = "X-Resource-Version"

// Client talks to one master apiserver root over HTTP. It is the only place
// govirtctl encodes the apiserver route contract.
type Client struct {
	baseURL string
	hc      *http.Client
}

// NewClient builds a Client for baseURL (scheme://host[:port]). hc may be nil,
// in which case http.DefaultClient is used.
func NewClient(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: baseURL, hc: hc}
}

// Apply submits body as the full object for kind/name via POST
// /apis/{kind}/{name}. It returns the master's stored object bytes on success,
// or an error carrying the apiserver {"error":...} envelope on a non-2xx reply.
func (c *Client) Apply(ctx context.Context, kind, name string, body []byte) (_ []byte, err error) {
	url := fmt.Sprintf("%s/apis/%s/%s", c.baseURL, kind, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("govirtctl: build apply request for %s/%s: %w", kind, name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("govirtctl: apply %s/%s: %w", kind, name, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("govirtctl: close apply response body: %w", cerr)
		}
	}()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("govirtctl: read apply response for %s/%s: %w", kind, name, readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("govirtctl: apply %s/%s: master returned %d: %s", kind, name, resp.StatusCode, errorMessage(respBody))
	}
	return respBody, nil
}

// Get fetches kind/name via GET /apis/{kind}/{name}. It returns the object bytes
// and the X-Resource-Version header value, or ErrNotFound on a 404.
func (c *Client) Get(ctx context.Context, kind, name string) (_ []byte, _ string, err error) {
	url := fmt.Sprintf("%s/apis/%s/%s", c.baseURL, kind, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("govirtctl: build get request for %s/%s: %w", kind, name, err)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("govirtctl: get %s/%s: %w", kind, name, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("govirtctl: close get response body: %w", cerr)
		}
	}()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, "", fmt.Errorf("govirtctl: read get response for %s/%s: %w", kind, name, readErr)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", fmt.Errorf("govirtctl: get %s/%s: %w", kind, name, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("govirtctl: get %s/%s: master returned %d: %s", kind, name, resp.StatusCode, errorMessage(respBody))
	}
	return respBody, resp.Header.Get(resourceVersionHeader), nil
}

// errorMessage extracts the apiserver {"error":"..."} message, falling back to
// the raw body when it is not the expected envelope.
func errorMessage(body []byte) string {
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error != "" {
		return env.Error
	}
	return string(body)
}
```

- [ ] **Step 3: Write the tests**

```go
package govirtctl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientApplyReturnsStoredObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/apis/StoragePool/pool-a" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"kind":"StoragePool","metadata":{"name":"pool-a"},"status":{"phase":"pending"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	got, err := c.Apply(context.Background(), "StoragePool", "pool-a", []byte(`{"kind":"StoragePool"}`))
	if err != nil {
		t.Fatalf("Apply error = %v, want nil", err)
	}
	if len(got) == 0 {
		t.Fatal("Apply returned empty body, want stored object")
	}
}

func TestClientApplySurfacesErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"name mismatch"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	_, err := c.Apply(context.Background(), "VM", "vm-a", []byte(`{}`))
	if err == nil {
		t.Fatal("Apply error = nil, want non-2xx error")
	}
	if got := err.Error(); !contains(got, "name mismatch") {
		t.Fatalf("Apply error = %q, want it to carry the envelope message", got)
	}
}

func TestClientGetReturnsObjectAndResourceVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(resourceVersionHeader, "42")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kind":"VM","status":{"phase":"running"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	body, rv, err := c.Get(context.Background(), "VM", "vm-a")
	if err != nil {
		t.Fatalf("Get error = %v, want nil", err)
	}
	if rv != "42" {
		t.Fatalf("Get resourceVersion = %q, want 42", rv)
	}
	if len(body) == 0 {
		t.Fatal("Get returned empty body")
	}
}

func TestClientGetNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	_, _, err := c.Get(context.Background(), "VM", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 4: Run targeted verification**

Run: `go test ./internal/govirtctl/ -run TestClient -v`
Expected: all four tests PASS.

- [ ] **Step 5: If verification fails, fix implementation or stale tests**

If close-error propagation trips a test (named-return shadow), confirm the `defer func(){...}()` writes the named `err` and the success path returns the named values.

- [ ] **Step 6: Run broader verification**

Run: `gofmt -l internal/govirtctl && go vet ./internal/govirtctl/...`
Expected: no output, vet clean.

- [ ] **Step 7: Commit**

```bash
git add internal/govirtctl/client.go internal/govirtctl/client_test.go
git commit -m "feat(govirtctl): add master apiserver HTTP client (apply/get)"
```

---

### Task 3: govirtctl command dispatch (apply -f / get)

**Files:**
- Create: `internal/govirtctl/command.go`
- Test: `internal/govirtctl/command_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `Run(ctx, args, stdout, stderr)` dispatches `apply -f <file>` (reads the manifest, locates kind+name from the object envelope, applies it) and `get <kind> <name>` (fetches and prints the object; for VM, also prints `status.phase`). The manifest's `kind` and `metadata.name` are authoritative; the CLI infers nothing.
Acceptance evidence:
- `go test ./internal/govirtctl/ -run TestRun -v` passes against an httptest master.
- `apply -f` with a manifest missing `kind` or `metadata.name` returns a clear error (no silent default).
- Unknown subcommand returns a non-zero result with usage text.

- [ ] **Step 2: Write the command dispatch**

```go
package govirtctl

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

// objectEnvelope is the minimal projection the CLI decodes from a manifest to
// route it: the kind and name are the operator's authoritative choice, never
// inferred. Everything else in the manifest is forwarded verbatim to the master.
type objectEnvelope struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

// Run is the govirtctl entrypoint. args excludes the program name. It writes
// command output to stdout and diagnostics to stderr, returning a process exit
// code (0 success, non-zero failure).
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}

	switch args[0] {
	case "apply":
		return runApply(ctx, args[1:], stdout, stderr)
	case "get":
		return runGet(ctx, args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, versionString())
		return 0
	default:
		fmt.Fprintf(stderr, "govirtctl: unknown command %q\n\n%s\n", args[0], usage)
		return 2
	}
}

const usage = `usage:
  govirtctl --server <url> apply -f <manifest.json>
  govirtctl --server <url> get <kind> <name>
  govirtctl version`

// runApply reads a manifest file, locates its kind/name, and applies it.
func runApply(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "master apiserver root, e.g. http://127.0.0.1:8080 (required)")
	file := fs.String("f", "", "path to the resource manifest JSON file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" {
		fmt.Fprintln(stderr, "govirtctl apply: --server is required")
		return 2
	}
	if *file == "" {
		fmt.Fprintln(stderr, "govirtctl apply: -f <manifest> is required")
		return 2
	}

	body, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(stderr, "govirtctl apply: read manifest %q: %v\n", *file, err)
		return 1
	}

	var env objectEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		fmt.Fprintf(stderr, "govirtctl apply: decode manifest %q: %v\n", *file, err)
		return 1
	}
	if env.Kind == "" {
		fmt.Fprintf(stderr, "govirtctl apply: manifest %q has no kind\n", *file)
		return 1
	}
	if env.Metadata.Name == "" {
		fmt.Fprintf(stderr, "govirtctl apply: manifest %q has no metadata.name\n", *file)
		return 1
	}

	c := NewClient(*server, nil)
	if _, err := c.Apply(ctx, env.Kind, env.Metadata.Name, body); err != nil {
		fmt.Fprintf(stderr, "govirtctl apply: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s/%s applied\n", env.Kind, env.Metadata.Name)
	return 0
}

// runGet fetches one object and prints it. For a VM it also extracts and prints
// status.phase on its own line so scripts can grep the lifecycle state.
func runGet(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "master apiserver root (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if *server == "" {
		fmt.Fprintln(stderr, "govirtctl get: --server is required")
		return 2
	}
	if len(rest) != 2 {
		fmt.Fprintln(stderr, "govirtctl get: expected <kind> <name>")
		return 2
	}
	kind, name := rest[0], rest[1]

	c := NewClient(*server, nil)
	body, _, err := c.Get(ctx, kind, name)
	if err != nil {
		fmt.Fprintf(stderr, "govirtctl get: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(append(body, '\n')); err != nil {
		fmt.Fprintf(stderr, "govirtctl get: write output: %v\n", err)
		return 1
	}

	var withStatus struct {
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &withStatus); err == nil && withStatus.Status.Phase != "" {
		fmt.Fprintf(stdout, "phase: %s\n", withStatus.Status.Phase)
	}
	return 0
}
```

- [ ] **Step 3: Add a version helper**

`govirtctl version` should print the same version string the binary used to. Add to `command.go`:

```go
// versionString returns the Govirta version line for the `version` subcommand.
// It mirrors the prior version-only behaviour of cmd/govirtctl.
func versionString() string {
	return version.String()
}
```

Add the import `"github.com/suknna/govirta/internal/version"` (use aft_import).

- [ ] **Step 4: Write the tests**

```go
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
```

- [ ] **Step 5: Run targeted verification**

Run: `go test ./internal/govirtctl/ -run TestRun -v`
Expected: all four tests PASS.

- [ ] **Step 6: Run broader verification**

Run: `go test ./internal/govirtctl/... && gofmt -l internal/govirtctl && go vet ./internal/govirtctl/...`
Expected: all PASS, no gofmt output, vet clean.

- [ ] **Step 7: Commit**

```bash
git add internal/govirtctl/command.go internal/govirtctl/command_test.go
git commit -m "feat(govirtctl): add apply -f and get command dispatch"
```

---

### Task 4: govirtctl thin entry

**Files:**
- Modify: `cmd/govirtctl/main.go`
- Test: covered by `internal/govirtctl` (entry is a thin shim)

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `cmd/govirtctl/main.go` becomes a thin shim over `govirtctl.Run`, exiting with its return code. `govirtctl version` still prints the version line (back-compat for any script depending on it).
Acceptance evidence:
- `go build ./cmd/govirtctl` succeeds.
- `go run ./cmd/govirtctl version` prints the version string and exits 0.
- `go run ./cmd/govirtctl` (no args) prints usage and exits 2.

- [ ] **Step 2: Rewrite the entry**

```go
// Command govirtctl is the Govirta control-plane CLI. It submits resource
// manifests to the master apiserver and reads objects back. All logic lives in
// internal/govirtctl; this entry only wires os.Args/stdio/exit code.
package main

import (
	"context"
	"os"

	"github.com/suknna/govirta/internal/govirtctl"
)

func main() {
	os.Exit(govirtctl.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
```

- [ ] **Step 3: Run targeted verification**

Run: `go build ./cmd/govirtctl && go run ./cmd/govirtctl version && go run ./cmd/govirtctl; echo "no-args exit: $?"`
Expected: build OK, version line printed, usage printed with `no-args exit: 2`.

- [ ] **Step 4: If verification fails, fix the shim**

The only likely failure is an import cycle or a leftover reference to the old `version` import — remove it; the entry imports only `internal/govirtctl`.

- [ ] **Step 5: Run broader verification**

Run: `gofmt -l cmd/govirtctl && go vet ./cmd/govirtctl/... && bash scripts/verify.sh`
Expected: no gofmt output, vet clean, verify.sh exit 0.

Note: `scripts/verify.sh` currently builds `cmd/govirtad cmd/govirtlet cmd/govirtctl`? Confirm it includes govirtctl; if not, this is out of scope for behavior but the build line should still pass for the three main services.

- [ ] **Step 6: Commit**

```bash
git add cmd/govirtctl/main.go
git commit -m "feat(govirtctl): wire thin entry over govirtctl.Run"
```

---

### Task 5: e2e resource manifests

**Files:**
- Create: `test/e2e/manifests/01-storagepool-block.json`
- Create: `test/e2e/manifests/02-storagepool-file.json`
- Create: `test/e2e/manifests/03-image.json`
- Create: `test/e2e/manifests/04-volume.json`
- Create: `test/e2e/manifests/05-network.json`
- Create: `test/e2e/manifests/06-nic.json`
- Create: `test/e2e/manifests/07-vm.json`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: seven dependency-ordered manifests (two pools: a block pool for the root volume + a file pool for the image; then image, volume, network, NIC, VM) that exercise the full chain. Each carries explicit `apiVersion`, `kind`, `metadata.name`, `metadata.nodeName` (the local-resource node binding), and a valid `spec` per the Plan 1 contracts. The VM manifest omits `nodeName` (scheduler binds it). The NIC manifest omits `spec.mac` (apiserver allocates it).
Acceptance evidence:
- Each file is valid JSON.
- Field names and enum values match the Plan 1 apis contracts exactly (verified against `pkg/apis/*/v1alpha1` during implementation — the implementer MUST read those types and reconcile field names before finalizing, since the manifests are the contract surface).

- [ ] **Step 2: Read the real apis specs before writing manifests**

Before writing any manifest, read every resource's `Spec` struct and its JSON tags:
- `pkg/apis/storagepool/v1alpha1` — backend/type enums, storageRoot, capacityBytes.
- `pkg/apis/image/v1alpha1` — poolRef, source (file://), format.
- `pkg/apis/volume/v1alpha1` — poolRef, imageRef, imageFilePoolRef, role, diskIndex, capacityBytes.
- `pkg/apis/network/v1alpha1` — bridge/gateway/cidr/dhcp range/egress interface fields.
- `pkg/apis/nic/v1alpha1` — networkRef, vmRef (no mac).
- `pkg/apis/vm/v1alpha1` — arch, vcpus, memoryMiB, volumeRefs, nicRefs.

Use the exact JSON field names from the struct tags. Do not guess.

- [ ] **Step 3: Write the manifests using confirmed field names**

Write each JSON file with `apiVersion: "govirta.io/v1alpha1"`, the correct `kind`, a `metadata.name` matching the filename's resource, and `metadata.nodeName: "node0"` for the five node-local kinds (pools, image, volume, network, NIC). The VM manifest (`07-vm.json`) omits `nodeName`. The NIC manifest (`06-nic.json`) omits `spec.mac`.

Example shape for `01-storagepool-block.json` (reconcile field names against the real spec):

```json
{
  "apiVersion": "govirta.io/v1alpha1",
  "kind": "StoragePool",
  "metadata": { "name": "pool-block", "nodeName": "node0" },
  "spec": {
    "backend": "local-block",
    "type": "block",
    "storageRoot": "/var/lib/govirta/block",
    "capacityBytes": 10737418240
  }
}
```

The block pool storageRoot and file pool storageRoot must be writable paths inside the Lima guest that `scripts/e2e.sh` pre-creates. The image source (file://) must point at a CirrOS image present in the guest's image-source-root.

- [ ] **Step 4: Validate JSON**

Run: `for f in test/e2e/manifests/*.json; do python3 -m json.tool "$f" >/dev/null && echo "OK $f"; done`
Expected: `OK` for every file.

- [ ] **Step 5: Commit**

```bash
git add test/e2e/manifests/
git commit -m "test(e2e): add seven dependency-ordered resource manifests"
```

---

### Task 6: e2e orchestration script

**Files:**
- Create: `scripts/e2e.sh`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `scripts/e2e.sh full` stands up the spec's three-node topology and runs the closure test:
1. start etcd v3.6.12 in an OrbStack Docker container (host-published localhost port);
2. build `govirtad` for the host, start it (background, pidfile in `.tmp/`), connecting to the etcd container and listening on a host port reachable from Lima as `host.lima.internal`;
3. prepare a Lima VM (vz + nestedVirtualization), cross-build `govirtlet` for linux/arm64, push it + the manifests' image dependency into the guest;
4. start `govirtlet` in the guest (background), dialing `--master-url http://host.lima.internal:<port>`;
5. run the e2e Go test (`go test -tags e2e ./test/e2e/...`) on the host, which drives `govirtctl` and asserts VM `Running`;
6. always clean up (kill govirtlet, govirtad, delete container, optionally delete Lima VM), archiving logs under `test/log/`.

Acceptance evidence:
- `scripts/e2e.sh check-tools` verifies docker/limactl/go without starting anything.
- `scripts/e2e.sh full` exits 0 only when the closure test passes.

- [ ] **Step 2: Write the script**

Model it on `scripts/acceptance.sh` (Lima short-home handling, cache dir, trap-based cleanup, logged run) but with the host-driven topology. Key differences:
- govirtad runs on the **host**, not in Lima.
- govirtlet runs **in Lima** and dials back to the host (node-initiated).
- etcd runs in a Docker container with a published port.
- The Go test runs on the **host** (darwin), reaching govirtad over localhost and reading VM status via govirtctl.

Pin etcd image to `quay.io/coreos/etcd:v3.6.12`. Write pidfiles/sockets/logs under `.tmp/e2e/` (never `/tmp`). Use a `trap` to tear down etcd container + govirtad process + govirtlet-in-Lima on EXIT/INT/TERM.

The script must export env vars the Go test reads (Task 7): `GOVIRTA_E2E=1`, `GOVIRTA_E2E_SERVER=http://127.0.0.1:<port>`, `GOVIRTA_E2E_GOVIRTCTL=<built binary path>`, `GOVIRTA_E2E_MANIFESTS=<repo>/test/e2e/manifests`, `GOVIRTA_E2E_NODE=node0`.

- [ ] **Step 3: Make the script executable and check-tools clean**

Run: `chmod +x scripts/e2e.sh && scripts/e2e.sh check-tools`
Expected: prints tool versions, exits 0 (or a clear "missing tool" message if docker/limactl absent).

- [ ] **Step 4: Commit**

```bash
git add scripts/e2e.sh
git commit -m "test(e2e): add three-node host-driven orchestration script"
```

---

### Task 7: e2e closure test

**Files:**
- Create: `test/e2e/doc.go`
- Create: `test/e2e/closure_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: a `//go:build e2e` Go test that, against a running govirtad (URL from env) and a running govirtlet (in Lima, started by the script), applies the seven manifests in dependency order via the `govirtctl` binary, then polls `govirtctl get VM <name>` until `phase: running` within a deadline, asserting the full closure.
Acceptance evidence:
- `go vet -tags e2e ./test/e2e/...` is clean (compiles under the e2e tag).
- When run by `scripts/e2e.sh full` on the real topology, the test reaches `phase: running` and passes.
- The test skips (not fails) when `GOVIRTA_E2E != 1`.

- [ ] **Step 2: Write the package doc**

```go
//go:build e2e

// Package e2e holds Govirta's end-to-end distributed-spine acceptance test. It
// is host-driven: scripts/e2e.sh starts etcd (container), govirtad (host), and
// govirtlet (Lima guest dialing back to the host), then runs this test, which
// drives the govirtctl binary to apply the seven manifests (six first-class
// kinds) and waits
// for the VM to reach Running. It exercises the real Linux execution plane
// (netlink/nftables/CoreDHCP/QEMU) reconciled from a real master over watch —
// the proof the unit and per-package acceptance tests cannot give.
package e2e
```

- [ ] **Step 3: Write the closure test**

```go
//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	e2eEnabledEnv  = "GOVIRTA_E2E"
	e2eServerEnv   = "GOVIRTA_E2E_SERVER"
	e2eCtlEnv      = "GOVIRTA_E2E_GOVIRTCTL"
	e2eManifestEnv = "GOVIRTA_E2E_MANIFESTS"
)

// applyOrder is the dependency order the controllers gate on: pools first, then
// image (needs file pool), volume (needs block pool + image), network, NIC
// (needs network), VM (needs volume + NIC).
var applyOrder = []string{
	"01-storagepool-block.json",
	"02-storagepool-file.json",
	"03-image.json",
	"04-volume.json",
	"05-network.json",
	"06-nic.json",
	"07-vm.json",
}

func TestDistributedSpineClosure(t *testing.T) {
	if os.Getenv(e2eEnabledEnv) != "1" {
		t.Skipf("set %s=1 (via scripts/e2e.sh) to run the e2e closure test", e2eEnabledEnv)
	}
	server := requireEnv(t, e2eServerEnv)
	ctl := requireEnv(t, e2eCtlEnv)
	manifests := requireEnv(t, e2eManifestEnv)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	for _, name := range applyOrder {
		path := filepath.Join(manifests, name)
		out, err := runCtl(ctx, ctl, "--server", server, "apply", "-f", path)
		if err != nil {
			t.Fatalf("apply %s failed: %v\noutput:\n%s", name, err, out)
		}
		t.Logf("applied %s: %s", name, strings.TrimSpace(out))
	}

	// Poll the VM until it reports Running. The VM name must match 07-vm.json's
	// metadata.name.
	const vmName = "vm-e2e"
	deadline := time.Now().Add(5 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context ended before VM reached Running: %v\nlast get:\n%s", err, last)
		}
		out, err := runCtl(ctx, ctl, "--server", server, "get", "VM", vmName)
		last = out
		if err == nil && strings.Contains(out, "phase: running") {
			t.Logf("VM %s reached Running:\n%s", vmName, out)
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("VM %s did not reach Running before deadline\nlast get:\n%s", vmName, last)
}

func runCtl(ctx context.Context, ctl string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, ctl, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is required when %s=1", name, e2eEnabledEnv)
	}
	return v
}
```

- [ ] **Step 4: Run targeted verification (compile-only on host)**

Run: `go vet -tags e2e ./test/e2e/...`
Expected: clean (the test compiles under the e2e tag; it skips without `GOVIRTA_E2E=1`).

- [ ] **Step 5: Commit**

```bash
git add test/e2e/doc.go test/e2e/closure_test.go
git commit -m "test(e2e): add distributed-spine closure test"
```

---

### Task 8: Full verification + real three-node closure run

**Files:** none (verification task)

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the whole repo builds/tests/vets cleanly, and the real three-node closure passes on actual hardware (the spine kernel gate, per memory 799 — only the real-environment run catches "self-consistent but wrong").
Acceptance evidence:
- `scripts/verify.sh` exit 0.
- `go vet -tags e2e ./test/e2e/...` clean.
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` exit 0 (govirtlet cross-builds for the Lima guest).
- `scripts/e2e.sh full` exit 0 with the closure test reaching `phase: running`.

- [ ] **Step 2: Run the host-side full verification**

Run:
```bash
bash scripts/verify.sh
go vet -tags e2e ./test/e2e/...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
```
Expected: all exit 0.

- [ ] **Step 3: Run the real three-node closure (the spine kernel gate)**

Run: `scripts/e2e.sh full`
Expected: etcd container starts, govirtad runs on host, govirtlet runs in Lima dialing back, the seven manifests apply, and the VM reaches `phase: running`. Archive the log under `test/log/`.

This is the verification that must be done by hand (memory 822: the implementer does not delegate the real-environment proof, and per memory 799 only this gate catches a self-consistent-but-wrong integration). Capture the real command output (apply lines + the final `phase: running`) as the closure evidence. Expect integration defects to surface one kind at a time here — fix them at their root (in the relevant controller or manifest), not by patching the test.

- [ ] **Step 4: If the closure fails, diagnose at the failing kind**

The controllers gate in dependency order, so a stuck VM means an upstream kind never reached Ready. Use `govirtctl get <kind> <name>` for each kind to find the first non-Ready resource, read its status message, and fix the root cause (missing guest path, driver wiring, MAC allocation, DHCP binding, etc.). Re-run `scripts/e2e.sh full` after each fix.

- [ ] **Step 5: Commit any fixes**

Commit each integration fix as its own logical change (per the small-commit discipline), referencing the kind it unblocks. Do not bundle unrelated fixes.

- [ ] **Step 6: Final confirmation**

Run: `scripts/e2e.sh full` once more from a clean state.
Expected: green closure, log archived. The distributed spine is lit end-to-end.

---

## Self-Review (completed at plan-writing time)

**1. Spec coverage:** The spec's section 5 data flow (govirtctl apply → master → node controllers reconcile in dependency order → real QEMU → status reconcile → govirtctl get VM = Running) is covered by Tasks 2–4 (govirtctl), Task 5 (manifests), Tasks 6–7 (orchestration + closure test), Task 8 (real run). The spec's section 7 three-node acceptance topology (etcd container + host govirtad + Lima-guest govirtlet, node-initiated dial) is covered by Task 6. The M-2 backlog item (note #28) is covered by Task 1 as a prerequisite. The spec's section 8 non-goals (stop/delete, backoff, Lease, reconnect, informer, real scheduler) are correctly untouched.

**2. Placeholder scan:** No TBD/TODO/"handle edge cases". Task 5 deliberately instructs the implementer to read the real apis specs before writing manifests (the field names are the contract surface and must not be guessed) — this is a read-then-write instruction, not a placeholder. The example manifest shows the exact shape.

**3. Type consistency:** `Client.Apply`/`Client.Get` signatures in Task 2 match their uses in Task 3 (`runApply`/`runGet`). `Run(ctx, args, stdout, stderr) int` in Task 3 matches the entry shim in Task 4. The env var names in Task 6 (`GOVIRTA_E2E_*`) match those read in Task 7. `local.PathKey` exported in Task 1 matches its reference in the same task's volume-controller edit. The VM name `vm-e2e` in Task 7 must match `07-vm.json`'s `metadata.name` (called out in Task 7 Step 3).

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-06-vertical-slice-plan4-e2e.md`. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, two-stage review between tasks, fast iteration. Tasks 1–7 are subagent-friendly; Task 8's real three-node run is done by the lead (not delegated) per memory 822.
2. **Inline Execution** — execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
