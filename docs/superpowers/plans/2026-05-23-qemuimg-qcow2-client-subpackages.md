# qemu-img qcow2 Client Subpackages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `internal/virt/qemuimg` with a qcow2-only, client-go-style fluent client whose qemu-img subcommands live in independent subpackages.

**Architecture:** The root `qemuimg` package owns the public client entrypoint and qcow2 resource facade. Each qemu-img subcommand owns its own builder, validation, argv construction, result parsing, and unit tests in a dedicated subpackage. Shared command execution and invalid-request helpers live in `internal/virt/qemuimg/internal/exec` to avoid import cycles.

**Tech Stack:** Go, `os/exec`, `encoding/json`, `os.Remove`, table-driven unit tests, `go test`.

---

## File Structure

- Modify: `internal/virt/qemuimg/client.go` — public `Config`, `Client`, `ExecClient`, `QCOW2()` resource facade, and `ErrInvalidRequest` alias.
- Create: `internal/virt/qemuimg/internal/exec/exec.go` — shared `Runner`, `Result`, default `OSRunner`, and invalid request constructor.
- Create: `internal/virt/qemuimg/internal/exec/exec_test.go` — shared helper behavior tests.
- Replace: `internal/virt/qemuimg/client_test.go` — root client facade tests.
- Create: `internal/virt/qemuimg/create/create.go` and `create_test.go` — `qemu-img create` builder.
- Create: `internal/virt/qemuimg/info/info.go` and `info_test.go` — `qemu-img info` builder and JSON parsing.
- Create: `internal/virt/qemuimg/convert/convert.go` and `convert_test.go` — `qemu-img convert` builder.
- Create: `internal/virt/qemuimg/snapshot/snapshot.go` and `snapshot_test.go` — `qemu-img snapshot -c` builder.
- Create: `internal/virt/qemuimg/check/check.go` and `check_test.go` — `qemu-img check` builder and JSON parsing.
- Create: `internal/virt/qemuimg/remove/remove.go` and `remove_test.go` — disk file removal builder.

## Task 1: Root client facade and shared exec boundary

**Files:**
- Modify: `internal/virt/qemuimg/client.go`
- Replace: `internal/virt/qemuimg/client_test.go`
- Create: `internal/virt/qemuimg/internal/exec/exec.go`
- Create: `internal/virt/qemuimg/internal/exec/exec_test.go`

- [ ] **Step 1: Write failing root facade tests**

Replace `internal/virt/qemuimg/client_test.go` with tests that assert default binary, configured binary, and public invalid-request classification:

```go
package qemuimg

import (
    "errors"
    "testing"

    imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

func TestNewClientDefaultsBinary(t *testing.T) {
    client := NewClient(Config{})
    if got := client.QCOW2().Binary(); got != "qemu-img" {
        t.Fatalf("Binary() = %q, want qemu-img", got)
    }
}

func TestNewClientUsesConfiguredBinary(t *testing.T) {
    client := NewClient(Config{Binary: "/usr/bin/qemu-img"})
    if got := client.QCOW2().Binary(); got != "/usr/bin/qemu-img" {
        t.Fatalf("Binary() = %q, want /usr/bin/qemu-img", got)
    }
}

func TestErrInvalidRequestAliasesInternalExecError(t *testing.T) {
    err := imgexec.InvalidRequest("path is required")
    if !errors.Is(err, ErrInvalidRequest) {
        t.Fatalf("errors.Is(%v, ErrInvalidRequest) = false, want true", err)
    }
}
```

- [ ] **Step 2: Run root facade tests and verify failure**

Run:

```bash
go test ./internal/virt/qemuimg
```

Expected: FAIL because `Config`, `QCOW2`, `Binary`, and internal exec package do not exist yet.

- [ ] **Step 3: Implement shared exec boundary and root facade**

Create `internal/virt/qemuimg/internal/exec/exec.go`:

```go
package exec

import (
    "context"
    "errors"
    "fmt"
    osexec "os/exec"
)

var ErrInvalidRequest = errors.New("invalid qemu-img request")

type Result struct {
    Stdout string
    Stderr string
}

type Runner interface {
    Run(ctx context.Context, binary string, args []string) (Result, error)
}

type OSRunner struct{}

func (r OSRunner) Run(ctx context.Context, binary string, args []string) (Result, error) {
    cmd := osexec.CommandContext(ctx, binary, args...)
    stdout, err := cmd.Output()
    if err != nil {
        if exitErr, ok := err.(*osexec.ExitError); ok {
            return Result{Stdout: string(stdout), Stderr: string(exitErr.Stderr)}, err
        }
        return Result{Stdout: string(stdout)}, err
    }
    return Result{Stdout: string(stdout)}, nil
}

func InvalidRequest(format string, args ...any) error {
    return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}
```

Replace `internal/virt/qemuimg/client.go` with:

```go
package qemuimg

import (
    "github.com/suknna/govirta/internal/virt/qemuimg/check"
    "github.com/suknna/govirta/internal/virt/qemuimg/convert"
    "github.com/suknna/govirta/internal/virt/qemuimg/create"
    imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
    "github.com/suknna/govirta/internal/virt/qemuimg/info"
    "github.com/suknna/govirta/internal/virt/qemuimg/remove"
    "github.com/suknna/govirta/internal/virt/qemuimg/snapshot"
)

var ErrInvalidRequest = imgexec.ErrInvalidRequest

type Config struct {
    Binary string
    Runner imgexec.Runner
}

type Client interface {
    QCOW2() QCOW2Client
}

type ExecClient struct {
    binary string
    runner imgexec.Runner
}

type QCOW2Client struct {
    binary string
    runner imgexec.Runner
}

func NewClient(config Config) *ExecClient {
    binary := config.Binary
    if binary == "" {
        binary = "qemu-img"
    }
    runner := config.Runner
    if runner == nil {
        runner = imgexec.OSRunner{}
    }
    return &ExecClient{binary: binary, runner: runner}
}

func (c *ExecClient) QCOW2() QCOW2Client {
    return QCOW2Client{binary: c.binary, runner: c.runner}
}

func (c QCOW2Client) Binary() string { return c.binary }

func (c QCOW2Client) Create() *create.Builder { return create.New(c.binary, c.runner) }
func (c QCOW2Client) Info() *info.Builder { return info.New(c.binary, c.runner) }
func (c QCOW2Client) Convert() *convert.Builder { return convert.New(c.binary, c.runner) }
func (c QCOW2Client) Snapshot() *snapshot.Builder { return snapshot.New(c.binary, c.runner) }
func (c QCOW2Client) Check() *check.Builder { return check.New(c.binary, c.runner) }
func (c QCOW2Client) Remove() *remove.Builder { return remove.New() }
```

- [ ] **Step 4: Run root tests and expect compile errors for missing subpackages**

Run:

```bash
go test ./internal/virt/qemuimg
```

Expected: FAIL because the subcommand packages do not exist yet. This confirms the root facade depends on the planned subpackages.

## Task 2: Create subcommand package

**Files:**
- Create: `internal/virt/qemuimg/create/create.go`
- Create: `internal/virt/qemuimg/create/create_test.go`

- [ ] **Step 1: Write failing create tests**

Create `internal/virt/qemuimg/create/create_test.go`:

```go
package create

import (
    "context"
    "errors"
    "reflect"
    "testing"

    imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

type fakeRunner struct{ binary string; args []string }

func (r *fakeRunner) Run(ctx context.Context, binary string, args []string) (imgexec.Result, error) {
    r.binary = binary
    r.args = append([]string(nil), args...)
    return imgexec.Result{}, nil
}

func TestBuilderDoBuildsBackingCreateArgs(t *testing.T) {
    runner := &fakeRunner{}
    err := New("qemu-img", runner).Target("child.qcow2").FromBase("base.qcow2").SizeBytes(117440512).Do(context.Background())
    if err != nil { t.Fatalf("Do() error = %v, want nil", err) }
    want := []string{"create", "-f", "qcow2", "-F", "qcow2", "-b", "base.qcow2", "child.qcow2", "117440512"}
    if runner.binary != "qemu-img" || !reflect.DeepEqual(runner.args, want) {
        t.Fatalf("runner = %q %v, want qemu-img %v", runner.binary, runner.args, want)
    }
}

func TestBuilderDoRejectsMissingTarget(t *testing.T) {
    err := New("qemu-img", &fakeRunner{}).FromBase("base.qcow2").SizeBytes(1).Do(context.Background())
    if !errors.Is(err, imgexec.ErrInvalidRequest) { t.Fatalf("error = %v, want ErrInvalidRequest", err) }
}
```

- [ ] **Step 2: Run create tests and verify failure**

Run:

```bash
go test ./internal/virt/qemuimg/create
```

Expected: FAIL because `New`, `Builder`, `Target`, `FromBase`, `SizeBytes`, and `Do` do not exist.

- [ ] **Step 3: Implement create builder**

Create `internal/virt/qemuimg/create/create.go`:

```go
package create

import (
    "context"
    "strconv"
    "strings"

    imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

type Builder struct { binary string; runner imgexec.Runner; target string; base string; sizeBytes int64 }

func New(binary string, runner imgexec.Runner) *Builder { return &Builder{binary: binary, runner: runner} }
func (b *Builder) Target(path string) *Builder { b.target = path; return b }
func (b *Builder) FromBase(path string) *Builder { b.base = path; return b }
func (b *Builder) SizeBytes(size int64) *Builder { b.sizeBytes = size; return b }

func (b *Builder) Do(ctx context.Context) error {
    if strings.TrimSpace(b.target) == "" { return imgexec.InvalidRequest("target is required") }
    if strings.TrimSpace(b.base) == "" { return imgexec.InvalidRequest("base is required") }
    if b.sizeBytes <= 0 { return imgexec.InvalidRequest("size_bytes must be positive") }
    args := []string{"create", "-f", "qcow2", "-F", "qcow2", "-b", b.base, b.target, strconv.FormatInt(b.sizeBytes, 10)}
    _, err := b.runner.Run(ctx, b.binary, args)
    return err
}
```

- [ ] **Step 4: Run create tests and verify pass**

Run:

```bash
go test ./internal/virt/qemuimg/create
```

Expected: PASS.

## Task 3: Info and check subcommand packages

**Files:**
- Create: `internal/virt/qemuimg/info/info.go`
- Create: `internal/virt/qemuimg/info/info_test.go`
- Create: `internal/virt/qemuimg/check/check.go`
- Create: `internal/virt/qemuimg/check/check_test.go`

- [ ] **Step 1: Implement tests first**

Write tests that assert `info.New("qemu-img", runner).Path("disk.qcow2").Do(ctx)` runs `[]string{"info", "--output=json", "disk.qcow2"}` and parses `filename`, `format`, `virtual-size`, `actual-size`, `backing-filename`, and `backing-filename-format`. Write check tests that assert `[]string{"check", "--output=json", "disk.qcow2"}` and parse common check fields plus `RawOutput`.

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
go test ./internal/virt/qemuimg/info ./internal/virt/qemuimg/check
```

Expected: FAIL because the package files have not been created yet.

- [ ] **Step 3: Implement info and check builders**

Implement each package with `New(binary string, runner imgexec.Runner)`, `Path(string)`, `Do(ctx)`, `strings.TrimSpace` validation, qemu-img argv construction, `encoding/json` parsing, and `RawOutput` preservation for check.

- [ ] **Step 4: Run info and check tests and verify pass**

Run:

```bash
go test ./internal/virt/qemuimg/info ./internal/virt/qemuimg/check
```

Expected: PASS.

## Task 4: Convert, snapshot, and remove subcommand packages

**Files:**
- Create: `internal/virt/qemuimg/convert/convert.go`
- Create: `internal/virt/qemuimg/convert/convert_test.go`
- Create: `internal/virt/qemuimg/snapshot/snapshot.go`
- Create: `internal/virt/qemuimg/snapshot/snapshot_test.go`
- Create: `internal/virt/qemuimg/remove/remove.go`
- Create: `internal/virt/qemuimg/remove/remove_test.go`

- [ ] **Step 1: Write failing tests**

Write tests for these argv and file behaviors:

```text
convert:  qemu-img convert -O qcow2 src.qcow2 dst.qcow2
snapshot: qemu-img snapshot -c before-upgrade disk.qcow2
remove:   os.Remove(path) deletes the file and returns missing-file errors
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
go test ./internal/virt/qemuimg/convert ./internal/virt/qemuimg/snapshot ./internal/virt/qemuimg/remove
```

Expected: FAIL because the package files have not been created yet.

- [ ] **Step 3: Implement convert, snapshot, and remove builders**

Implement `Source`, `Target`, `Path`, `Name`, and `Do(ctx)` methods as required by each command. `remove.Builder.Do(ctx)` should check `ctx.Err()` before calling `os.Remove` so cancellation is respected even though deletion is local filesystem I/O.

- [ ] **Step 4: Run tests and verify pass**

Run:

```bash
go test ./internal/virt/qemuimg/convert ./internal/virt/qemuimg/snapshot ./internal/virt/qemuimg/remove
```

Expected: PASS.

## Task 5: Integrate root client facade with all subpackages

**Files:**
- Modify: `internal/virt/qemuimg/client.go`
- Modify: `internal/virt/qemuimg/client_test.go`

- [ ] **Step 1: Add root integration tests**

Extend root tests so `client.QCOW2().Create()`, `Info()`, `Convert()`, `Snapshot()`, `Check()`, and `Remove()` all return non-nil builders and carry the configured binary into command-backed builders.

- [ ] **Step 2: Run qemuimg package tests and verify failure if wiring is incomplete**

Run:

```bash
go test ./internal/virt/qemuimg/...
```

Expected: PASS if previous tasks already wired everything; otherwise fix root facade only.

- [ ] **Step 3: Remove legacy API and unused code**

Delete `CreateRequest`, `ResizeRequest`, old `Info(ctx, path)`, `Resize`, `FormatRaw`, and any tests that describe the removed API.

- [ ] **Step 4: Run qemuimg tests and verify pass**

Run:

```bash
go test ./internal/virt/qemuimg/...
```

Expected: PASS.

## Task 6: Full verification and commit

**Files:**
- All files under `internal/virt/qemuimg/**`

- [ ] **Step 1: Format code**

Run:

```bash
gofmt -w internal/virt/qemuimg
```

Expected: command exits 0.

- [ ] **Step 2: Run focused tests**

Run:

```bash
go test ./internal/virt/qemuimg/...
```

Expected: PASS.

- [ ] **Step 3: Run repository baseline tests**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Inspect final diff**

Run:

```bash
git status --short
git diff -- internal/virt/qemuimg docs/superpowers/plans/2026-05-23-qemuimg-qcow2-client-subpackages.md
```

Expected: only qemuimg implementation, qemuimg tests, and this plan are changed.

- [ ] **Step 5: Commit implementation**

Run:

```bash
git add internal/virt/qemuimg docs/superpowers/plans/2026-05-23-qemuimg-qcow2-client-subpackages.md
git commit -m "feat(qemuimg): add qcow2 client subcommands"
```

Expected: commit succeeds.

## Self-Review

- Spec coverage: all six requested abilities are represented by dedicated subpackages and tests.
- Scope check: no raw support, no resize compatibility layer, no qemu-nbd/qemu-io work.
- Type consistency: public root API uses `NewClient(Config).QCOW2().<Subcommand>().<Options>().Do(ctx)`; subcommand result types remain in their own packages.
- Verification: focused qemuimg tests and full `go test ./...` are required before reporting completion.
