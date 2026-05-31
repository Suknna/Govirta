# Lima Acceptance Framework Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a local Lima-backed acceptance gate that runs Linux-only Govirta validation in an ephemeral nested-KVM VM before `main` can be pushed.

**Architecture:** Keep the existing fast macOS verification path unchanged, and add a separate `acceptance` build-tag test suite under `test/acceptance/`. `scripts/acceptance.sh` owns the Lima VM lifecycle with a short generated `LIMA_HOME` under parent `.l/<repo_key>` to avoid Lima socket path limits, while preserving persistent repo cache under `.lima/cache/`. A versioned `pre-push` hook decides when to run the expensive Lima path. Acceptance tests validate Govirta's existing QEMU/qemu-img/QMP abstractions against real Linux binaries, starting with no-network cirros boot and qemu-img lifecycle coverage.

**Tech Stack:** Go 1.26 stdlib `testing` / `os/exec` / `context`, existing `internal/virt/qemu`, `internal/virt/qemuimg`, and `internal/virt/qmp` packages, Lima 2.1+ with `vmType: vz`, Ubuntu 24.04 arm64, `qemu-system-aarch64`, `qemu-img`, EDK2 firmware, POSIX shell scripts, git `pre-push` hook.

---

## File Structure

Create and modify these files:

- Create: `lima/govirta.yaml` — Lima VM definition using Apple Virtualization.framework (`vz`) with `nestedVirtualization: true`, read-only source mount, writable cache mount, and provisioning for QEMU/firmware/Go.
- Create: `scripts/acceptance.sh` — orchestration script; prepares `.lima/cache`, starts an ephemeral Lima VM, runs acceptance tests inside it, and deletes the VM instance.
- Create: `.githooks/pre-push` — versioned git hook; always runs `scripts/verify.sh`, runs acceptance unconditionally for pushes to `main`, and runs acceptance on feature branches only when Linux-relevant paths changed.
- Modify: `.gitignore` — add `.lima/` so cached images, toolchains, and Lima disks never enter git.
- Create: `test/acceptance/doc.go` — acceptance package documentation, build tag, and environment contract.
- Create: `test/acceptance/harness.go` — shared environment parsing, command execution helpers, QMP wait helper, and QEMU process cleanup helpers.
- Create: `test/acceptance/qemuimg_test.go` — real `qemu-img` lifecycle test through `internal/virt/qemuimg`.
- Modify: `internal/virt/qemu/vm.go` — add a typed `NoNIC()` builder method that renders explicit `-nic none` for no-network acceptance.
- Modify: `internal/virt/qemu/vm_test.go` — cover `NoNIC()` argv rendering and prove it does not use the generic argument escape hatch.
- Create: `test/acceptance/boot_test.go` — real no-network cirros boot via `internal/virt/qemu` and QMP query-status via `internal/virt/qmp`.
- Modify: `AGENTS.md` — document the new Lima acceptance workflow, remove the obsolete remote `192.168.139.206` acceptance workflow, and add the `--no-verify` prohibition for `main` pushes.

Expected source file sizes for AGENTS.md 第十八章 planning discipline:

| File | Expected lines | Boundary |
| --- | ---: | --- |
| `scripts/acceptance.sh` | 170-230 | One shell harness; below soft limit. |
| `.githooks/pre-push` | 80-130 | Thin decision layer; below soft limit. |
| `test/acceptance/harness.go` | 220-320 | Shared helpers only; below soft limit. |
| `test/acceptance/boot_test.go` | 120-180 | One boot acceptance scenario; below soft limit. |
| `test/acceptance/qemuimg_test.go` | 80-130 | One qemu-img lifecycle scenario; below soft limit. |
| `test/acceptance/doc.go` | 30-60 | Package docs; below soft limit. |
| `internal/virt/qemu/vm.go` | Existing ~510 + ~8 | Small typed API addition; still below soft limit. |

Dependency order:

```text
.gitignore + Lima config -> acceptance shell harness -> Go acceptance helpers -> qemu-img acceptance test -> typed QEMU NoNIC support -> cirros boot acceptance test -> pre-push hook -> AGENTS.md documentation -> verification
```

---

### Task 1: Cache boundary and Lima VM definition

**Files:**
- Modify: `.gitignore`
- Create: `lima/govirta.yaml`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the repository defines a reproducible Lima guest that can expose nested KVM and keeps all large local VM artifacts under ignored `.lima/`.

Acceptance evidence:
- `git check-ignore .lima/cache/images/cirros-aarch64.qcow2` prints `.lima/cache/images/cirros-aarch64.qcow2`.
- `scripts/acceptance.sh check-tools` exits 0 and `scripts/acceptance.sh full` uses generated short Lima home under parent `.l/<repo_key>` while keeping persistent gitignored repo cache under `.lima/cache/`.
- `grep -n "nestedVirtualization: true" lima/govirta.yaml` prints the matching line.

- [ ] **Step 2: Ignore the project-local Lima home**

Add this exact entry under the local environment / agent state block in `.gitignore`:

```gitignore
.lima/
```

Run:

```bash
git check-ignore .lima/cache/images/cirros-aarch64.qcow2
```

Expected output:

```text
.lima/cache/images/cirros-aarch64.qcow2
```

- [ ] **Step 3: Create `lima/govirta.yaml`**

Create `lima/govirta.yaml`:

```yaml
# Govirta Linux-only acceptance VM.
# This instance is ephemeral. Persistent artifacts belong under .lima/cache,
# not inside the guest disk.
vmType: "vz"
nestedVirtualization: true

images:
  - location: "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-arm64.img"
    arch: "aarch64"

cpus: 2
memory: "2GiB"
disk: "12GiB"
mountType: "virtiofs"

mounts:
  - location: "."
    mountPoint: "/govirta-src"
    writable: false
  - location: ".lima/cache"
    mountPoint: "/govirta-cache"
    writable: true

provision:
  - mode: system
    script: |
      set -eu
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        qemu-system-arm \
        qemu-utils \
        qemu-efi-aarch64
      if getent group kvm >/dev/null 2>&1; then
        usermod -aG kvm {{.User}}
      fi
  - mode: user
    script: |
      set -eu
      mkdir -p /govirta-cache/toolchain /govirta-cache/gocache /govirta-cache/gomodcache /govirta-cache/images
      GO_VERSION="1.26.0"
      GO_TARBALL="go${GO_VERSION}.linux-arm64.tar.gz"
      GO_URL="https://go.dev/dl/${GO_TARBALL}"
      if [ ! -x "$HOME/.local/go/bin/go" ]; then
        if [ ! -f "/govirta-cache/toolchain/${GO_TARBALL}" ]; then
          curl -fL "$GO_URL" -o "/govirta-cache/toolchain/${GO_TARBALL}"
        fi
        rm -rf "$HOME/.local/go"
        mkdir -p "$HOME/.local"
        tar -C "$HOME/.local" -xzf "/govirta-cache/toolchain/${GO_TARBALL}"
      fi
      "$HOME/.local/go/bin/go" version
```

Important implementation notes:
- The Go version is intentionally explicit. If Go 1.26.0 is not yet available when implementing, stop and ask the user whether to use the installed host Go version instead; do not silently switch versions.
- The VM source mount is read-only. QMP sockets, pidfiles, serial logs, and qcow2 scratch files must be created with Go `t.TempDir()` inside the guest.

- [ ] **Step 4: Validate the Lima config**

Run:

```bash
scripts/acceptance.sh check-tools
```

Expected: exit code 0 and the installed `limactl` version is printed.

- [ ] **Step 5: Commit this slice if commits are approved for the execution session**

If the user explicitly allowed commits for the execution session, run:

```bash
git add .gitignore lima/govirta.yaml
git commit -m "test: add Lima acceptance VM config"
```

If commits are not approved, skip the commit and record the changed files in the execution summary.

---

### Task 2: Acceptance orchestration script

**Files:**
- Create: `scripts/acceptance.sh`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: one script prepares cache directories, downloads the explicit cirros image, starts a fresh Lima VM, runs acceptance tests inside the VM with explicit environment variables, and deletes the VM afterward.

Acceptance evidence:
- `sh -n scripts/acceptance.sh` passes.
- `scripts/acceptance.sh help` prints usage and exits 0.
- `scripts/acceptance.sh check-tools` verifies `limactl` exists and prints the detected version.

- [ ] **Step 2: Create the shell harness skeleton**

Create `scripts/acceptance.sh` and make it executable (`chmod +x scripts/acceptance.sh`):

```sh
#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
instance_name="govirta-acceptance"
lima_home="${GOVIRTA_LIMA_HOME:-$(dirname -- "$repo_root")/.l/$(printf '%s' "$repo_root" | cksum | cut -d ' ' -f 1)}"
cache_dir="$repo_root/.lima/cache"
image_dir="$cache_dir/images"
cirros_image="$image_dir/cirros-aarch64.qcow2"
cirros_url="https://download.cirros-cloud.net/0.6.2/cirros-0.6.2-aarch64-disk.img"

usage() {
  cat <<'USAGE'
Usage: scripts/acceptance.sh <full|linux|check-tools|help>

Modes:
  full         Run the full Lima acceptance suite.
  linux        Run the current Linux-only acceptance suite. Today this is the same as full.
  check-tools  Verify required host tools without starting Lima.
  help         Print this message.
USAGE
}

log() {
  printf '[acceptance] %s\n' "$*" >&2
}

require_tool() {
  command -v "$1" >/dev/null 2>&1 || {
    printf 'required tool not found: %s\n' "$1" >&2
    exit 127
  }
}

check_tools() {
  require_tool limactl
  require_tool curl
  limactl --version
}

prepare_cache() {
  mkdir -p "$image_dir" "$cache_dir/toolchain" "$cache_dir/gocache" "$cache_dir/gomodcache"
  if [ ! -f "$cirros_image" ]; then
    log "downloading cirros aarch64 image"
    curl -fL "$cirros_url" -o "$cirros_image"
  fi
}

cleanup_instance() {
  LIMA_HOME="$lima_home" limactl delete -f "$instance_name" >/dev/null 2>&1 || true
}

run_acceptance() {
  mode=$1
  case "$mode" in
    full|linux) ;;
    *)
      usage >&2
      exit 2
      ;;
  esac

  check_tools
  prepare_cache
  cleanup_instance
  trap cleanup_instance EXIT INT TERM

  log "starting Lima instance $instance_name"
  LIMA_HOME="$lima_home" limactl start --name="$instance_name" --yes "$repo_root/lima/govirta.yaml"

  log "running acceptance tests inside Lima"
  LIMA_HOME="$lima_home" limactl shell --workdir /govirta-src "$instance_name" -- sh -eu -c '
    sudo -E env \
      PATH="$HOME/.local/go/bin:$PATH" \
      GOCACHE=/govirta-cache/gocache \
      GOMODCACHE=/govirta-cache/gomodcache \
      GOVIRTA_ACCEPTANCE=1 \
      GOVIRTA_ACCEPTANCE_QEMU=/usr/bin/qemu-system-aarch64 \
      GOVIRTA_ACCEPTANCE_QEMU_IMG=/usr/bin/qemu-img \
      GOVIRTA_ACCEPTANCE_FIRMWARE=/usr/share/AAVMF/AAVMF_CODE.fd \
      GOVIRTA_ACCEPTANCE_CIRROS=/govirta-cache/images/cirros-aarch64.qcow2 \
      go test -v -tags acceptance -count=1 ./test/acceptance/...
  '
}

mode=${1:-help}
case "$mode" in
  help|-h|--help)
    usage
    ;;
  check-tools)
    check_tools
    ;;
  full|linux)
    run_acceptance "$mode"
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
```

- [ ] **Step 3: Run static verification**

Run:

```bash
chmod +x scripts/acceptance.sh
sh -n scripts/acceptance.sh
scripts/acceptance.sh help
scripts/acceptance.sh check-tools
```

Expected:
- `sh -n` produces no output.
- `help` prints the usage block.
- `check-tools` prints the installed `limactl` version.

- [ ] **Step 4: Commit this slice if commits are approved for the execution session**

```bash
git add scripts/acceptance.sh
git commit -m "test: add Lima acceptance harness"
```

Only run the commit command if the execution session has explicit commit approval.

---

### Task 3: Acceptance package docs and shared harness

**Files:**
- Create: `test/acceptance/doc.go`
- Create: `test/acceptance/harness.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the acceptance package has a documented environment contract and shared helpers for explicit configuration, command execution, QMP polling, and process cleanup.

Acceptance evidence:
- `go test -tags acceptance ./test/acceptance -run TestNonExistent` compiles and exits without build errors.
- Running without `GOVIRTA_ACCEPTANCE=1` skips acceptance tests rather than failing.

- [ ] **Step 2: Create `doc.go`**

Create `test/acceptance/doc.go`:

```go
//go:build acceptance

// Package acceptance contains Linux-only Govirta acceptance tests.
//
// These tests run inside the Lima VM created by scripts/acceptance.sh. They are
// excluded from normal macOS unit tests and require explicit environment
// variables for every behavior-affecting external dependency.
//
// Required environment when GOVIRTA_ACCEPTANCE=1:
//   - GOVIRTA_ACCEPTANCE_QEMU: qemu-system-aarch64 path.
//   - GOVIRTA_ACCEPTANCE_QEMU_IMG: qemu-img path.
//   - GOVIRTA_ACCEPTANCE_FIRMWARE: AArch64 EDK2 firmware path.
//   - GOVIRTA_ACCEPTANCE_CIRROS: cirros aarch64 qcow2 image path.
package acceptance
```

- [ ] **Step 3: Create `harness.go`**

Create `test/acceptance/harness.go`:

```go
//go:build acceptance

package acceptance

import (
    "context"
    "errors"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/suknna/govirta/internal/virt/qmp"
)

type acceptanceEnv struct {
    QEMU     string
    QEMUImg  string
    Firmware string
    Cirros   string
}

func requireAcceptanceEnv(t *testing.T) acceptanceEnv {
    t.Helper()
    if os.Getenv("GOVIRTA_ACCEPTANCE") != "1" {
        t.Skip("set GOVIRTA_ACCEPTANCE=1 to run Lima acceptance tests")
    }
    env := acceptanceEnv{
        QEMU:     os.Getenv("GOVIRTA_ACCEPTANCE_QEMU"),
        QEMUImg:  os.Getenv("GOVIRTA_ACCEPTANCE_QEMU_IMG"),
        Firmware: os.Getenv("GOVIRTA_ACCEPTANCE_FIRMWARE"),
        Cirros:   os.Getenv("GOVIRTA_ACCEPTANCE_CIRROS"),
    }
    missing := make([]string, 0, 4)
    if env.QEMU == "" {
        missing = append(missing, "GOVIRTA_ACCEPTANCE_QEMU")
    }
    if env.QEMUImg == "" {
        missing = append(missing, "GOVIRTA_ACCEPTANCE_QEMU_IMG")
    }
    if env.Firmware == "" {
        missing = append(missing, "GOVIRTA_ACCEPTANCE_FIRMWARE")
    }
    if env.Cirros == "" {
        missing = append(missing, "GOVIRTA_ACCEPTANCE_CIRROS")
    }
    if len(missing) > 0 {
        t.Fatalf("missing acceptance environment variables: %s", strings.Join(missing, ", "))
    }
    requireRegularFile(t, env.QEMU)
    requireRegularFile(t, env.QEMUImg)
    requireRegularFile(t, env.Firmware)
    requireRegularFile(t, env.Cirros)
    return env
}

func requireRegularFile(t *testing.T, path string) {
    t.Helper()
    info, err := os.Stat(path)
    if err != nil {
        t.Fatalf("stat %s: %v", path, err)
    }
    if info.IsDir() {
        t.Fatalf("%s is a directory, want regular file", path)
    }
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
    cmd := exec.CommandContext(ctx, name, args...)
    var stdout, stderr strings.Builder
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr
    err := cmd.Run()
    return []byte(stdout.String()), []byte(stderr.String()), err
}

func waitForQMPStatus(t *testing.T, ctx context.Context, socketPath string, want qmp.State) qmp.Status {
    t.Helper()
    deadline := time.Now().Add(45 * time.Second)
    var lastErr error
    for time.Now().Before(deadline) {
        client, err := qmp.NewSocketClient(qmp.Config{SocketPath: socketPath, Timeout: 2 * time.Second})
        if err != nil {
            t.Fatalf("NewSocketClient() error = %v", err)
        }
        if err := client.Connect(ctx); err != nil {
            lastErr = err
            time.Sleep(500 * time.Millisecond)
            continue
        }
        status, err := client.QueryStatus(ctx)
        disconnectErr := client.Disconnect(ctx)
        if err == nil && status.State == want {
            if disconnectErr != nil {
                t.Fatalf("Disconnect() after desired status error = %v", disconnectErr)
            }
            return status
        }
        if err != nil {
            lastErr = err
        }
        if disconnectErr != nil {
            lastErr = errors.Join(lastErr, disconnectErr)
        }
        time.Sleep(500 * time.Millisecond)
    }
    t.Fatalf("QMP status did not reach %q before deadline; last error: %v", want, lastErr)
    return qmp.Status{}
}

func stopQEMU(ctx context.Context, socketPath string, process *os.Process) error {
    var errs error
    client, err := qmp.NewSocketClient(qmp.Config{SocketPath: socketPath, Timeout: 2 * time.Second})
    if err == nil {
        if connectErr := client.Connect(ctx); connectErr == nil {
            if quitErr := client.Quit(ctx); quitErr != nil {
                errs = errors.Join(errs, quitErr)
            }
            if disconnectErr := client.Disconnect(ctx); disconnectErr != nil {
                errs = errors.Join(errs, disconnectErr)
            }
        } else {
            errs = errors.Join(errs, connectErr)
        }
    } else {
        errs = errors.Join(errs, err)
    }
    if process != nil {
        done := make(chan error, 1)
        go func() { _, waitErr := process.Wait(); done <- waitErr }()
        select {
        case waitErr := <-done:
            errs = errors.Join(errs, waitErr)
        case <-time.After(10 * time.Second):
            errs = errors.Join(errs, process.Kill())
        }
    }
    return errs
}

func shortSocketPath(t *testing.T, dir string, name string) string {
    t.Helper()
    path := filepath.Join(dir, name)
    if len(path) >= 100 {
        t.Fatalf("unix socket path too long for QEMU: %s", path)
    }
    return path
}

func commandError(name string, args []string, stdout []byte, stderr []byte, err error) error {
    if err == nil {
        return nil
    }
    return fmt.Errorf("%s %s failed: %w\nstdout:\n%s\nstderr:\n%s", name, strings.Join(args, " "), err, stdout, stderr)
}
```

- [ ] **Step 4: Run compile-only verification**

Run:

```bash
go test -tags acceptance ./test/acceptance -run TestNonExistent
```

Expected: package builds successfully and reports no tests to run.

- [ ] **Step 5: Commit this slice if commits are approved for the execution session**

```bash
git add test/acceptance/doc.go test/acceptance/harness.go
git commit -m "test: add acceptance test harness"
```

Only run the commit command if the execution session has explicit commit approval.

---

### Task 4: Real qemu-img lifecycle acceptance test

**Files:**
- Create: `test/acceptance/qemuimg_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the acceptance suite validates the existing `internal/virt/qemuimg` client against a real `qemu-img` binary for the operations Govirta depends on.

Acceptance evidence:
- `GOVIRTA_ACCEPTANCE=1 ... go test -tags acceptance ./test/acceptance -run TestQEMUImgLifecycle -count=1` passes inside Lima.
- The test asserts `Info().Do(ctx)` reports `Format == "qcow2"` and the expected virtual size after create and resize.
- The test asserts `Check().Do(ctx)` reports zero corruptions and zero check errors.

- [ ] **Step 2: Create `qemuimg_test.go`**

Create `test/acceptance/qemuimg_test.go`:

```go
//go:build acceptance

package acceptance

import (
    "context"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/suknna/govirta/internal/virt/qemuimg"
)

func TestQEMUImgLifecycle(t *testing.T) {
    env := requireAcceptanceEnv(t)
    ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
    defer cancel()

    workDir := t.TempDir()
    rawPath := filepath.Join(workDir, "source.raw")
    createdPath := filepath.Join(workDir, "created.qcow2")
    convertedPath := filepath.Join(workDir, "converted.qcow2")

    if err := os.WriteFile(rawPath, make([]byte, 1024*1024), 0o600); err != nil {
        t.Fatalf("write raw image: %v", err)
    }

    qcow2 := qemuimg.NewClient(qemuimg.Config{Binary: env.QEMUImg}).QCOW2()

    if err := qcow2.Create().Target(createdPath).SizeBytes(8 * 1024 * 1024).Do(ctx); err != nil {
        t.Fatalf("Create().Do() error = %v", err)
    }
    info, err := qcow2.Info().Path(createdPath).Do(ctx)
    if err != nil {
        t.Fatalf("Info(created).Do() error = %v", err)
    }
    if info.Format != "qcow2" {
        t.Fatalf("created format = %q, want qcow2", info.Format)
    }
    if info.VirtualSize != 8*1024*1024 {
        t.Fatalf("created virtual size = %d, want %d", info.VirtualSize, 8*1024*1024)
    }

    if err := qcow2.Convert().Source(rawPath).SourceFormat("raw").Target(convertedPath).Do(ctx); err != nil {
        t.Fatalf("Convert(raw).Do() error = %v", err)
    }
    convertedInfo, err := qcow2.Info().Path(convertedPath).Do(ctx)
    if err != nil {
        t.Fatalf("Info(converted).Do() error = %v", err)
    }
    if convertedInfo.Format != "qcow2" {
        t.Fatalf("converted format = %q, want qcow2", convertedInfo.Format)
    }

    if err := qcow2.Resize().Path(convertedPath).SizeBytes(2 * 1024 * 1024).Do(ctx); err != nil {
        t.Fatalf("Resize().Do() error = %v", err)
    }
    resizedInfo, err := qcow2.Info().Path(convertedPath).Do(ctx)
    if err != nil {
        t.Fatalf("Info(resized).Do() error = %v", err)
    }
    if resizedInfo.VirtualSize != 2*1024*1024 {
        t.Fatalf("resized virtual size = %d, want %d", resizedInfo.VirtualSize, 2*1024*1024)
    }

    if err := qcow2.Snapshot().Path(convertedPath).Name("acceptance-snap").Do(ctx); err != nil {
        t.Fatalf("Snapshot().Do() error = %v", err)
    }
    check, err := qcow2.Check().Path(convertedPath).Do(ctx)
    if err != nil {
        t.Fatalf("Check().Do() error = %v", err)
    }
    if check.CheckErrors != 0 || check.Corruptions != 0 {
        t.Fatalf("check errors = %d corruptions = %d raw = %s", check.CheckErrors, check.Corruptions, check.RawOutput)
    }

    if err := qcow2.Remove().Path(convertedPath).Do(ctx); err != nil {
        t.Fatalf("Remove(converted).Do() error = %v", err)
    }
    if _, err := os.Stat(convertedPath); !os.IsNotExist(err) {
        t.Fatalf("converted path still exists or stat failed with unexpected error: %v", err)
    }
}
```

- [ ] **Step 3: Run compile verification on macOS**

Run:

```bash
go test -tags acceptance ./test/acceptance -run TestQEMUImgLifecycle -count=1
```

Expected on macOS without `GOVIRTA_ACCEPTANCE=1`: test is skipped, package compiles.

- [ ] **Step 4: Run real verification inside Lima after Task 2 exists**

Run:

```bash
scripts/acceptance.sh linux
```

Expected: `TestQEMUImgLifecycle` passes inside Lima.

- [ ] **Step 5: Commit this slice if commits are approved for the execution session**

```bash
git add test/acceptance/qemuimg_test.go
git commit -m "test: add qemu-img acceptance coverage"
```

Only run the commit command if the execution session has explicit commit approval.

---

### Task 5: Typed QEMU no-network support

**Files:**
- Modify: `internal/virt/qemu/vm.go`
- Modify: `internal/virt/qemu/vm_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: Govirta can explicitly render `-nic none` through a typed builder method, so no-network acceptance does not rely on QEMU defaults or a generic argument escape hatch.

Acceptance evidence:
- `go test ./internal/virt/qemu -run TestVMArgvRendersExplicitNoNIC -count=1` passes.
- `grep -n "func (b \*Builder) NoNIC" internal/virt/qemu/vm.go` prints the typed method.
- Boot acceptance in Task 6 calls `.NoNIC()` rather than `AddArgument(qemu.Arg("-nic", "none"))`.

- [ ] **Step 2: Add `NoNIC` state and builder method**

Modify `internal/virt/qemu/vm.go`:

```go
type Builder struct {
    binary string
    // existing fields stay unchanged
    noNIC bool
    err   error
}

// NoNIC disables QEMU's default network device explicitly with `-nic none`.
// Use this for no-network acceptance boots instead of relying on QEMU defaults.
func (b *Builder) NoNIC() *Builder {
    b.noNIC = true
    return b
}
```

Place `noNIC bool` next to the existing boolean fields (`noReboot`, `noShutdown`) and place `NoNIC()` next to those builder methods. Do not add `-nic` to `AddArgument`'s generic allowlist; no-network boot must remain a typed API.

- [ ] **Step 3: Render `-nic none` in `VM.Argv`**

Modify `VM.Argv()` after ordered devices are rendered and before display/no-reboot flags:

```go
if b.noNIC {
    argv = append(argv, "-nic", "none")
}
```

This order keeps explicit block/QMP/serial devices before the no-network flag and before global display/shutdown flags.

- [ ] **Step 4: Add a focused argv test**

Add this test to `internal/virt/qemu/vm_test.go`:

```go
func TestVMArgvRendersExplicitNoNIC(t *testing.T) {
    vm, err := qemu.NewVM(qemu.ArchAArch64).
        Machine(machine.ProfileAArch64VirtKVM).
        CPU(cpu.ModelHost).
        SMP(qemu.SMP{CPUs: 1, Cores: 1, Threads: 1, Sockets: 1}).
        Memory(qemu.MiB(512)).
        NoNIC().
        Display(display.None).
        Build()
    if err != nil {
        t.Fatalf("Build() error = %v", err)
    }

    argv := strings.Join(vm.Argv(), " ")
    if !strings.Contains(argv, " -nic none ") && !strings.HasSuffix(argv, " -nic none") {
        t.Fatalf("Argv() = %q, want explicit -nic none", argv)
    }
}
```

This test uses existing imports already present in `vm_test.go` (`strings`, `qemu`, `machine`, `cpu`, `display`).

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test ./internal/virt/qemu -run TestVMArgvRendersExplicitNoNIC -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit this slice if commits are approved for the execution session**

```bash
git add internal/virt/qemu/vm.go internal/virt/qemu/vm_test.go
git commit -m "test: add explicit no-network QEMU option"
```

Only run the commit command if the execution session has explicit commit approval.

---

### Task 6: No-network cirros boot acceptance test

**Files:**
- Create: `test/acceptance/boot_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the acceptance suite boots cirros under nested KVM without guest networking, using Govirta's typed QEMU builder and QMP client.

Acceptance evidence:
- `scripts/acceptance.sh full` passes inside Lima.
- The test starts `qemu-system-aarch64` with `-machine type=virt,accel=kvm`, `-cpu host`, `-nic none`, EDK2 firmware, qcow2 blockdev, QMP socket, and no display.
- The test observes QMP `query-status` state `running` through `internal/virt/qmp`.

- [ ] **Step 2: Create `boot_test.go`**

Create `test/acceptance/boot_test.go`:

```go
//go:build acceptance

package acceptance

import (
    "context"
    "os"
    "os/exec"
    "path/filepath"
    "testing"
    "time"

    "github.com/suknna/govirta/internal/virt/qemu"
    "github.com/suknna/govirta/internal/virt/qemu/blockdev"
    "github.com/suknna/govirta/internal/virt/qemu/chardev"
    "github.com/suknna/govirta/internal/virt/qemu/cpu"
    "github.com/suknna/govirta/internal/virt/qemu/device"
    "github.com/suknna/govirta/internal/virt/qemu/display"
    "github.com/suknna/govirta/internal/virt/qemu/firmware"
    "github.com/suknna/govirta/internal/virt/qemu/machine"
    "github.com/suknna/govirta/internal/virt/qemu/monitor"
    "github.com/suknna/govirta/internal/virt/qemu/qflag"
    "github.com/suknna/govirta/internal/virt/qemu/serial"
    "github.com/suknna/govirta/internal/virt/qmp"
)

func TestBootCirrosNoNetworkWithNestedKVM(t *testing.T) {
    env := requireAcceptanceEnv(t)
    ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
    defer cancel()

    workDir := t.TempDir()
    rootPath := filepath.Join(workDir, "cirros-root.qcow2")
    qmpSocket := shortSocketPath(t, workDir, "qmp.sock")
    serialSocket := shortSocketPath(t, workDir, "serial.sock")
    pidFile := filepath.Join(workDir, "qemu.pid")

    copyArgs := []string{"convert", "-f", "qcow2", "-O", "qcow2", env.Cirros, rootPath}
    copyOut, copyErrOut, copyErr := runCommand(ctx, env.QEMUImg, copyArgs...)
    if err := commandError(env.QEMUImg, copyArgs, copyOut, copyErrOut, copyErr); err != nil {
        t.Fatalf("copy cirros root: %v", err)
    }

    vm, err := qemu.NewVM(qemu.ArchAArch64).
        Binary(env.QEMU).
        Name("govirta-acceptance-cirros", qemu.NameDebugThreads(qemu.On)).
        Machine(machine.ProfileAArch64VirtKVM).
        CPU(cpu.ModelHost).
        SMP(qemu.SMP{CPUs: 1, Cores: 1, Threads: 1, Sockets: 1}).
        Memory(qemu.MiB(512)).
        BIOS(firmware.BIOS{Path: env.Firmware}).
        AddBlockdev(blockdev.Qcow2{
            NodeName: "root",
            File:     blockdev.FileProtocol{Filename: rootPath},
            Cache:    blockdev.Cache{Direct: qflag.Off},
            AIO:      blockdev.AIOThreads,
        }).
        AddDevice(device.VirtioBlkPCI{ID: "rootdev", Drive: blockdev.Ref("root"), BootIndex: qemu.Int(1)}).
        AddChardev(chardev.Socket{ID: "qmp0", Path: qmpSocket, Server: qemu.On, Wait: qemu.Off}).
        Monitor(monitor.Monitor{Chardev: chardev.Ref("qmp0"), Mode: monitor.ModeControl}).
        AddChardev(chardev.Socket{ID: "serial0", Path: serialSocket, Server: qemu.On, Wait: qemu.Off}).
        Serial(serial.Chardev("serial0")).
        NoNIC().
        Display(display.None).
        NoReboot().NoShutdown().
        Msg(qemu.Msg{Timestamp: qemu.On, GuestName: qemu.On}).
        PidFile(pidFile).
        Build()
    if err != nil {
        t.Fatalf("Build() error = %v", err)
    }

    argv := vm.Argv()
    cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    if err := cmd.Start(); err != nil {
        t.Fatalf("start QEMU: %v", err)
    }
    t.Cleanup(func() {
        cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
        defer cleanupCancel()
        if err := stopQEMU(cleanupCtx, qmpSocket, cmd.Process); err != nil {
            t.Logf("QEMU cleanup error: %v", err)
        }
    })

    status := waitForQMPStatus(t, ctx, qmpSocket, qmp.StateRunning)
    if !status.Running {
        t.Fatalf("QMP running = false for state %q", status.State)
    }
}
```

- [ ] **Step 3: Run compile verification on macOS**

Run:

```bash
go test -tags acceptance ./test/acceptance -run TestBootCirrosNoNetworkWithNestedKVM -count=1
```

Expected on macOS without `GOVIRTA_ACCEPTANCE=1`: test is skipped, package compiles.

- [ ] **Step 4: Run real verification inside Lima**

Run:

```bash
scripts/acceptance.sh full
```

Expected: `TestBootCirrosNoNetworkWithNestedKVM` reaches QMP state `running` and exits cleanly.

- [ ] **Step 5: Commit this slice if commits are approved for the execution session**

```bash
git add test/acceptance/boot_test.go
git commit -m "test: add nested KVM cirros acceptance"
```

Only run the commit command if the execution session has explicit commit approval.

---

### Task 7: Versioned pre-push gate

**Files:**
- Create: `.githooks/pre-push`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: pushes to `main` always run fast verification plus full Lima acceptance, while feature branches only run Lima when Linux-relevant paths changed.

Acceptance evidence:
- `sh -n .githooks/pre-push` passes.
- A dry-run push to `refs/heads/main` against a throwaway local bare remote prints the main-gate message and runs `scripts/acceptance.sh full`.
- A simulated feature push with only docs changes runs `scripts/verify.sh` and does not invoke Lima.
- A dry-run feature push touching `internal/virt/qemu/vm.go` against a throwaway local bare remote prints the Linux-relevant message and runs `scripts/acceptance.sh linux`.

- [ ] **Step 2: Create `.githooks/pre-push`**

Create `.githooks/pre-push` and make it executable (`chmod +x .githooks/pre-push`):

```sh
#!/bin/sh
set -eu

repo_root=$(git rev-parse --show-toplevel)
remote_name=${1:-origin}
zero_sha=0000000000000000000000000000000000000000
run_acceptance=0
acceptance_mode=linux
pushes_main=0

linux_path_pattern='^(internal/network/bridge/|internal/virt/qemu/|internal/virt/qmp/|internal/virt/qemuimg/|internal/storage/local/|internal/storage/localfile/|test/acceptance/|lima/|scripts/acceptance\.sh)$'

changed_linux_paths() {
  local_sha=$1
  remote_sha=$2
  if [ "$remote_sha" = "$zero_sha" ]; then
    if git rev-parse --verify "$remote_name/main" >/dev/null 2>&1; then
      base="$remote_name/main"
    else
      base="HEAD~1"
    fi
  else
    base="$remote_sha"
  fi
  git diff --name-only "$base" "$local_sha" | grep -Eq "$linux_path_pattern"
}

while read local_ref local_sha remote_ref remote_sha; do
  [ -n "${local_ref:-}" ] || continue
  if [ "$remote_ref" = "refs/heads/main" ]; then
    pushes_main=1
    run_acceptance=1
    acceptance_mode=full
    continue
  fi
  if [ "$local_sha" != "$zero_sha" ] && changed_linux_paths "$local_sha" "$remote_sha"; then
    run_acceptance=1
  fi
done

cd "$repo_root"
scripts/verify.sh

if [ "$run_acceptance" -eq 1 ]; then
  if [ "$pushes_main" -eq 1 ]; then
    printf '[pre-push] pushing main: running full Lima acceptance gate\n' >&2
  else
    printf '[pre-push] Linux-relevant changes detected: running Lima acceptance\n' >&2
  fi
  scripts/acceptance.sh "$acceptance_mode"
else
  printf '[pre-push] no Linux-only acceptance trigger detected\n' >&2
fi
```

- [ ] **Step 3: Run static verification**

Run:

```bash
chmod +x .githooks/pre-push
sh -n .githooks/pre-push
```

Expected: no output from `sh -n`.

- [ ] **Step 4: Verify hook behavior against a local bare remote**

Create a throwaway bare remote under `.tmp/` and push dry-run refs to exercise the real hook without contacting GitHub. This intentionally uses the real `scripts/acceptance.sh` path when acceptance is selected; do not add a second bypass environment variable for hook testing.

```bash
rm -rf .tmp/pre-push-remote.git
git init --bare .tmp/pre-push-remote.git
git remote remove pre-push-test 2>/dev/null || true
git remote add pre-push-test .tmp/pre-push-remote.git
git push --dry-run pre-push-test HEAD:refs/heads/main
```

Expected: the hook prints `[pre-push] pushing main: running full Lima acceptance gate`, runs `scripts/verify.sh`, then runs `scripts/acceptance.sh full`. A failure here is a real gate failure, not a simulation artifact.

- [ ] **Step 5: Enable the versioned hook for this checkout**

Run:

```bash
git config core.hooksPath .githooks
git config --get core.hooksPath
```

Expected output:

```text
.githooks
```

- [ ] **Step 6: Commit this slice if commits are approved for the execution session**

```bash
git add .githooks/pre-push
git commit -m "test: add local pre-push acceptance gate"
```

Only run the commit command if the execution session has explicit commit approval.

---

### Task 8: AGENTS.md workflow documentation

**Files:**
- Modify: `AGENTS.md`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: future AI agents see the Lima acceptance workflow before touching code, and obsolete remote-host acceptance instructions are removed.

Acceptance evidence:
- `grep -n "Lima 验收" AGENTS.md` prints the new acceptance section.
- `grep -n "192.168.139.206\|govirta-qemu-test" AGENTS.md` prints no obsolete remote acceptance workflow lines.
- `grep -n "--no-verify" AGENTS.md` prints the new prohibition.

- [ ] **Step 2: Add an acceptance testing section after COMMANDS**

Insert this section after the existing `## COMMANDS` block:

````markdown
## ACCEPTANCE TESTS

Govirta uses a two-layer verification model:

```bash
# Fast macOS verification; runs on every push via .githooks/pre-push.
scripts/verify.sh

# Linux-only acceptance verification; runs inside ephemeral Lima VM.
scripts/acceptance.sh full
```

- macOS unit tests (`go test ./...`) cover pure logic, typed argv rendering, fake qemu-img runners, and no-op boundaries.
- Lima acceptance tests (`go test -tags acceptance ./test/acceptance/...`) cover behavior that physically requires Linux, real QEMU/KVM, real qemu-img, or future real netlink.
- The Lima VM is ephemeral (`limactl delete` after each run). `scripts/acceptance.sh` uses a short generated `LIMA_HOME` under parent `.l/<repo_key>` to avoid Lima socket path limits; persistent cache lives under project `.lima/cache/` and is gitignored.
- `lima/govirta.yaml` must use `vmType: "vz"` and `nestedVirtualization: true`; this path is verified on Apple M3 + macOS 26.5 + Lima 2.1.1.
- Initial acceptance is intentionally no-network: cirros boots with `-nic none`; bridge/TAP acceptance is added only after a real netlink bridge manager exists.
- Enable the versioned git hook once per checkout: `git config core.hooksPath .githooks`.
- Pushing `main` must pass full Lima acceptance. Do not use `git push --no-verify` to bypass the main gate.
````

- [ ] **Step 3: Replace the obsolete remote host notes**

Remove the existing NOTES bullets that describe:
- `root@192.168.139.206`
- cross-compiling `govirta-qemu.test`
- copying/running it on `/root/govirta-qemu-test/`

Replace them with:

```markdown
- Linux-only acceptance now runs locally through Lima rather than the old remote Rocky host. Use `scripts/acceptance.sh full`; the script uses a short generated `LIMA_HOME` under parent `.l/<repo_key>` to avoid Lima socket path limits, boots an ephemeral Ubuntu arm64 VM with nested KVM, runs `go test -tags acceptance ./test/acceptance/...`, then deletes the VM while preserving project `.lima/cache/`.
- Apple M3 + macOS 26.5 + Lima 2.1.1 has been verified to expose nested KVM through `vmType: "vz"` + `nestedVirtualization: true`; a cirros aarch64 guest booted with `qemu-system-aarch64 -machine virt -accel kvm -cpu host`, and its kernel logged `smccc: KVM: hypervisor services detected`.
```

- [ ] **Step 4: Add the local gate prohibition to ANTI-PATTERNS**

Add this bullet to `## ANTI-PATTERNS (THIS PROJECT)`:

```markdown
- Do not bypass the local main-branch acceptance gate with `git push --no-verify`. Pushing `main` must run `.githooks/pre-push`, which runs `scripts/verify.sh` plus full Lima acceptance.
```

- [ ] **Step 5: Run documentation checks**

Run:

```bash
grep -n "Lima 验收\|Lima acceptance\|scripts/acceptance.sh" AGENTS.md
grep -n "192.168.139.206\|govirta-qemu-test" AGENTS.md || true
grep -n -- "--no-verify" AGENTS.md
```

Expected:
- First command prints the new acceptance instructions.
- Second command prints no lines.
- Third command prints the anti-pattern and acceptance section lines.

- [ ] **Step 6: Commit this slice if commits are approved for the execution session**

```bash
git add AGENTS.md
git commit -m "docs: document Lima acceptance workflow"
```

Only run the commit command if the execution session has explicit commit approval.

---

### Task 9: End-to-end verification and final cleanup

**Files:**
- Verify: all files changed by Tasks 1-8

- [ ] **Step 1: Run fast local verification**

Run:

```bash
scripts/verify.sh
```

Expected:
- `gofmt -l .` reports no files.
- `go test ./...` passes.
- `go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl` passes.

- [ ] **Step 2: Run acceptance compile verification on macOS**

Run:

```bash
go test -tags acceptance ./test/acceptance/... -count=1
```

Expected: tests compile and skip when `GOVIRTA_ACCEPTANCE` is unset.

- [ ] **Step 3: Run Lima acceptance verification**

Run:

```bash
scripts/acceptance.sh full
```

Expected:
- Lima starts `govirta-acceptance` with nested virtualization.
- `TestQEMUImgLifecycle` passes.
- `TestBootCirrosNoNetworkWithNestedKVM` passes and observes QMP state `running`.
- The Lima instance is deleted after the run.
- `.lima/cache/` remains on disk and is ignored by git.

- [ ] **Step 4: Verify hook install state**

Run:

```bash
git config --get core.hooksPath
```

Expected output:

```text
.githooks
```

- [ ] **Step 5: Inspect git status and diff**

Run:

```bash
git status --short
```

Expected: only intended tracked files are modified/added; `.lima/` does not appear as an untracked directory.

- [ ] **Step 6: Commit or report pending changes according to execution-session instructions**

If the user explicitly approved commits, commit the final verification fixes with a focused message:

```bash
git add .gitignore lima/govirta.yaml scripts/acceptance.sh .githooks/pre-push test/acceptance AGENTS.md
git commit -m "test: add Lima acceptance gate"
```

If commits are not approved, do not commit. Report the final changed file list and verification evidence.
