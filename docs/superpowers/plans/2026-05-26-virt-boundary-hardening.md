# Virt Boundary Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rework `internal/virt` so QEMU argv, qemu-img command operands, and QMP transport lifecycle are strongly typed, context-aware, and resistant to option-injection and resource leaks.

**Architecture:** QEMU argv generation moves from distributed string concatenation to a typed option renderer with centralized validation. qemu-img path operands move behind a shared operand validator and public error model. QMP monitor interfaces become context-aware and enforce deterministic connect/disconnect, command cancellation, single-use events, and typed QMP response errors.

**Tech Stack:** Go, `context`, `errors`, `encoding/json`, `net`, `sync/atomic`, table-driven unit tests, fake runners/monitors, local Unix-socket QMP test server, `go test`, `go test -race`.

---

## File Structure

- Create: `internal/virt/qemu/qopt/qopt.go` — typed QEMU option renderer and shared value validation.
- Create: `internal/virt/qemu/qopt/qopt_test.go` — renderer and value validation tests.
- Modify: `internal/virt/qemu/blockdev/blockdev.go` — make qcow2 blockdev validate and render through `qopt`.
- Modify: `internal/virt/qemu/chardev/chardev.go` — make socket chardev validate and render through `qopt`.
- Modify: `internal/virt/qemu/device/device.go` — make virtio devices validate and render through `qopt`.
- Modify: `internal/virt/qemu/monitor/monitor.go` — make QMP monitor option validate and render through `qopt`.
- Modify: `internal/virt/qemu/netdev/netdev.go` — make TAP netdev validate and render through `qopt`.
- Modify: `internal/virt/qemu/serial/serial.go` — make serial chardev reference validate and render through `qopt`.
- Modify: `internal/virt/qemu/qflag/qflag.go` — add enum validation for `OnOff`.
- Modify: `internal/virt/qemu/vm.go` — store renderable typed entries, validate in `Build()`, keep `VM.Argv()` immutable and error-free.
- Modify: `internal/virt/qemu/vm_test.go` — add invalid typed fields, injection, enum, nil option, and full aarch64 golden tests.
- Create: `internal/virt/qemuimg/internal/argv/argv.go` — shared qemu-img operand validation.
- Create: `internal/virt/qemuimg/internal/argv/argv_test.go` — operand validation tests.
- Modify: `internal/virt/qemuimg/client.go` — export `CommandError` alias.
- Modify: `internal/virt/qemuimg/create/create.go` — use shared operand validation.
- Modify: `internal/virt/qemuimg/convert/convert.go` — use shared operand validation.
- Modify: `internal/virt/qemuimg/info/info.go` — use shared operand validation.
- Modify: `internal/virt/qemuimg/check/check.go` — use shared operand validation.
- Modify: `internal/virt/qemuimg/snapshot/snapshot.go` — use shared operand validation.
- Modify: `internal/virt/qemuimg/remove/remove.go` — enforce ordinary `.qcow2` file deletion with `Lstat`, symlink rejection, and context recheck.
- Modify tests under `internal/virt/qemuimg/**` — update expected validation behavior and public command error checks.
- Modify: `internal/virt/qmp/errors.go` — add `ErrAlreadyConnected`, `ErrEventsAlreadyStarted` reuse, and public `ResponseError` if root-level exposure is needed.
- Modify: `internal/virt/qmp/internal/monitor/monitor.go` — make `Connect` and `Run` context-aware.
- Modify: `internal/virt/qmp/internal/monitor/goqemu.go` — propagate context and prevent event backpressure leaks.
- Modify: `internal/virt/qmp/internal/goqemu/socket.go` — context-aware connect/run/events, single-use event CAS, typed QMP errors.
- Modify: `internal/virt/qmp/internal/status/status.go` — parse QMP error class and propagate typed error.
- Modify: `internal/virt/qmp/internal/power/power.go` — call context-aware monitor `Run`.
- Modify: `internal/virt/qmp/internal/events/events.go` — make send path cancellation-aware.
- Modify: `internal/virt/qmp/client.go` — deterministic connect/disconnect lifecycle, duplicate connect rejection, context-aware event conversion.
- Modify tests under `internal/virt/qmp/**` — cover cleanup, cancellation, duplicate connect/events, event backpressure, and typed errors.

## Task 1: QEMU Option Renderer

**Files:**
- Create: `internal/virt/qemu/qopt/qopt.go`
- Create: `internal/virt/qemu/qopt/qopt_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: render QEMU comma-separated option strings from structured pairs while rejecting option injection characters and invalid enum values.

Acceptance evidence:
- `go test ./internal/virt/qemu/qopt` passes.
- Tests prove values containing `,`, `\x00`, `\n`, and empty required fields are rejected before argv rendering.

- [ ] **Step 2: Implement renderer**

Create `internal/virt/qemu/qopt/qopt.go` with this API:

```go
package qopt

import (
    "fmt"
    "strings"
    "unicode/utf8"
)

type Pair struct {
    Key      string
    Value    string
    Optional bool
}

type Enum interface {
    Valid() bool
}

func Required(key string, value string) Pair {
    return Pair{Key: key, Value: value}
}

func Optional(key string, value string) Pair {
    return Pair{Key: key, Value: value, Optional: true}
}

func Render(driver string, pairs ...Pair) (string, error) {
    if err := validateToken("driver", driver); err != nil {
        return "", err
    }
    parts := []string{driver}
    for _, pair := range pairs {
        if pair.Optional && pair.Value == "" {
            continue
        }
        if err := validateToken("key", pair.Key); err != nil {
            return "", err
        }
        if err := ValidateValue(pair.Key, pair.Value); err != nil {
            return "", err
        }
        parts = append(parts, pair.Key+"="+pair.Value)
    }
    return strings.Join(parts, ","), nil
}

func RenderPairs(pairs ...Pair) (string, error) {
    parts := make([]string, 0, len(pairs))
    for _, pair := range pairs {
        if pair.Optional && pair.Value == "" {
            continue
        }
        if err := validateToken("key", pair.Key); err != nil {
            return "", err
        }
        if err := ValidateValue(pair.Key, pair.Value); err != nil {
            return "", err
        }
        parts = append(parts, pair.Key+"="+pair.Value)
    }
    return strings.Join(parts, ","), nil
}

func ValidateValue(name string, value string) error {
    if value == "" {
        return fmt.Errorf("%s is required", name)
    }
    if !utf8.ValidString(value) {
        return fmt.Errorf("%s must be valid utf-8", name)
    }
    for _, r := range value {
        switch r {
        case ',', '\x00', '\n', '\r':
            return fmt.Errorf("%s contains invalid qemu option character %q", name, r)
        }
        if r < 0x20 {
            return fmt.Errorf("%s contains control character %q", name, r)
        }
    }
    return nil
}

func ValidateEnum(name string, value string, valid bool) error {
    if value == "" {
        return nil
    }
    if err := ValidateValue(name, value); err != nil {
        return err
    }
    if !valid {
        return fmt.Errorf("%s has unsupported value %q", name, value)
    }
    return nil
}

func validateToken(name string, value string) error {
    if value == "" {
        return fmt.Errorf("%s is required", name)
    }
    if strings.ContainsAny(value, ",=\x00\n\r") {
        return fmt.Errorf("%s contains invalid qemu option token character", name)
    }
    return nil
}
```

- [ ] **Step 3: Add renderer tests**

Create tests in `internal/virt/qemu/qopt/qopt_test.go`:

```go
package qopt

import "testing"

func TestRenderBuildsCommaSeparatedOptions(t *testing.T) {
    got, err := Render("tap", Required("id", "net0"), Required("ifname", "gv-tap0"), Optional("vhost", "on"))
    if err != nil {
        t.Fatalf("Render() error = %v", err)
    }
    want := "tap,id=net0,ifname=gv-tap0,vhost=on"
    if got != want {
        t.Fatalf("Render() = %q, want %q", got, want)
    }
}

func TestRenderOmitsEmptyOptionalValues(t *testing.T) {
    got, err := Render("socket", Required("id", "qmp0"), Optional("server", ""))
    if err != nil {
        t.Fatalf("Render() error = %v", err)
    }
    if got != "socket,id=qmp0" {
        t.Fatalf("Render() = %q", got)
    }
}

func TestRenderRejectsInjectionCharacters(t *testing.T) {
    tests := []string{"tap0,script=/bad", "tap0\nscript=/bad", "tap0\x00bad"}
    for _, value := range tests {
        t.Run(value, func(t *testing.T) {
            if _, err := Render("tap", Required("ifname", value)); err == nil {
                t.Fatalf("Render() error = nil, want error")
            }
        })
    }
}

func TestRenderRejectsEmptyRequiredValues(t *testing.T) {
    if _, err := Render("tap", Required("id", "")); err == nil {
        t.Fatalf("Render() error = nil, want error")
    }
}

func TestRenderPairsBuildsKeyFirstOptions(t *testing.T) {
    got, err := RenderPairs(Required("driver", "qcow2"), Required("node-name", "root"))
    if err != nil {
        t.Fatalf("RenderPairs() error = %v", err)
    }
    if got != "driver=qcow2,node-name=root" {
        t.Fatalf("RenderPairs() = %q", got)
    }
}
```

- [ ] **Step 4: Run targeted verification**

Run: `go test ./internal/virt/qemu/qopt`

Expected: package passes.

## Task 2: QEMU Typed Entry Validation and Rendering

**Files:**
- Modify: `internal/virt/qemu/blockdev/blockdev.go`
- Modify: `internal/virt/qemu/chardev/chardev.go`
- Modify: `internal/virt/qemu/device/device.go`
- Modify: `internal/virt/qemu/monitor/monitor.go`
- Modify: `internal/virt/qemu/netdev/netdev.go`
- Modify: `internal/virt/qemu/serial/serial.go`
- Modify: `internal/virt/qemu/qflag/qflag.go`
- Modify: `internal/virt/qemu/vm.go`
- Modify: `internal/virt/qemu/vm_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: every typed QEMU entry validates required fields and enum values before argv is exposed, and invalid typed config returns `ErrInvalidVM` from `Build()`.

Acceptance evidence:
- `go test ./internal/virt/qemu/...` passes.
- Tests prove invalid typed values cannot reach `VM.Argv()`.

- [ ] **Step 2: Add enum validation**

Update `internal/virt/qemu/qflag/qflag.go`:

```go
func (v OnOff) Valid() bool {
    return v == "" || v == On || v == Off
}
```

Add equivalent `Valid()` methods where enum types are defined:

```go
func (a AIO) Valid() bool { return a == "" || a == AIOThreads }
func (m Mode) Valid() bool { return m == ModeControl }
func (s Script) Valid() bool { return s == "" || s == ScriptNo }
```

- [ ] **Step 3: Change typed `Arg()` methods to `(string, error)` plus `Validate()`**

For each typed config, implement this pattern:

```go
func (d Qcow2) Arg() (string, error) {
    if err := d.Validate(); err != nil {
        return "", err
    }
    return qopt.RenderPairs(
        qopt.Required("driver", "qcow2"),
        qopt.Required("node-name", d.NodeName),
        qopt.Required("file.driver", "file"),
        qopt.Required("file.filename", d.File.Filename),
        qopt.Optional("cache.direct", string(d.Cache.Direct)),
        qopt.Optional("aio", string(d.AIO)),
    )
}
```

Use exact QEMU strings currently expected by golden tests. For `blockdev.Qcow2`, the rendered string must remain:

```text
driver=qcow2,node-name=root,file.driver=file,file.filename=/var/lib/vm/root.qcow2,cache.direct=off,aio=threads
```

If `qopt.Render` is driver-first and cannot express `driver=qcow2`, add `RenderPairs(pairs ...Pair)` to `qopt` so blockdev can render key-first options without special string concatenation.

- [ ] **Step 4: Update `Builder` to store renderable typed entries**

In `internal/virt/qemu/vm.go`, replace eager `Arg("-device", v.Arg())` calls with a renderable interface:

```go
type renderableArgument interface {
    appendArgv([]string) []string
    valid() bool
    name() string
}

type typedArgument struct {
    flag   string
    render func() (string, error)
    value  string
    err    error
}

func (a *typedArgument) prepare() {
    if a.err != nil || a.value != "" {
        return
    }
    a.value, a.err = a.render()
}

func (a *typedArgument) appendArgv(argv []string) []string {
    return append(argv, a.flag, a.value)
}

func (a *typedArgument) valid() bool {
    a.prepare()
    return a.err == nil && a.flag != "" && a.value != ""
}

func (a *typedArgument) name() string { return a.flag }
```

During `Build()`, call `prepare()` and wrap errors:

```go
if !validArgument(entry) {
    return VM{}, fmt.Errorf("%w: invalid qemu argument: %v", ErrInvalidVM, argumentError(entry))
}
```

- [ ] **Step 5: Make `Name(..., nil)` return `ErrInvalidVM` through `Build()`**

Add `invalid error` to `Builder` or `nameConfig` and record nil options:

```go
func (b *Builder) Name(name string, opts ...NameOption) *Builder {
    c := nameConfig{value: name}
    for _, opt := range opts {
        if opt == nil {
            b.err = errors.Join(b.err, errors.New("nil name option"))
            continue
        }
        opt(&c)
    }
    b.name = &c
    return b
}
```

In `Build()`, if `b.err != nil`, return `fmt.Errorf("%w: %v", ErrInvalidVM, b.err)`.

- [ ] **Step 6: Add invalid typed tests**

Add table cases to `TestBuildRejectsInvalidConfig` for:

```go
AddBlockdev(blockdev.Qcow2{})
AddNetdev(netdev.Tap{})
AddChardev(chardev.Socket{})
AddDevice(device.VirtioBlkPCI{})
AddDevice(device.VirtioNetPCI{})
Monitor(monitor.Monitor{})
Msg(qemu.Msg{Timestamp: qemu.OnOff("maybe")})
AddNetdev(netdev.Tap{ID: "net0", IfName: "tap0,script=/bad"})
Name("vm", nil)
```

Each must assert `errors.Is(err, qemu.ErrInvalidVM)`.

- [ ] **Step 7: Add full aarch64 golden test**

Add a test that renders:

```go
qemu.NewVM(qemu.ArchAArch64).
    Binary("/usr/libexec/qemu-kvm").
    Name("arm-vm").
    Machine(machine.ProfileAArch64VirtKVM).
    CPU(cpu.ModelCortexA57).
    SMP(qemu.SMP{CPUs: 1, Cores: 1, Threads: 1, Sockets: 1}).
    Memory(qemu.MiB(256)).
    AddArgument(qemu.Arg("-bios", "/usr/share/edk2/aarch64/QEMU_EFI.fd"))
```

Expected argv begins with `/usr/libexec/qemu-kvm` and includes `-machine type=virt,accel=kvm`, `-cpu cortex-a57`, and `-bios /usr/share/edk2/aarch64/QEMU_EFI.fd`.

- [ ] **Step 8: Run targeted verification**

Run: `go test ./internal/virt/qemu/...`

Expected: all qemu packages pass.

## Task 3: qemu-img Operand and Error Model

**Files:**
- Create: `internal/virt/qemuimg/internal/argv/argv.go`
- Create: `internal/virt/qemuimg/internal/argv/argv_test.go`
- Modify: `internal/virt/qemuimg/client.go`
- Modify: `internal/virt/qemuimg/create/create.go`
- Modify: `internal/virt/qemuimg/convert/convert.go`
- Modify: `internal/virt/qemuimg/info/info.go`
- Modify: `internal/virt/qemuimg/check/check.go`
- Modify: `internal/virt/qemuimg/snapshot/snapshot.go`
- Modify: `internal/virt/qemuimg/remove/remove.go`
- Modify tests under `internal/virt/qemuimg/**`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: all qemu-img path operands share one validation policy, process errors are publicly classifiable, and deletion only removes ordinary `.qcow2` files.

Acceptance evidence:
- `go test ./internal/virt/qemuimg/...` passes.
- Tests prove leading dash operands are rejected and `qemuimg.CommandError` is usable with `errors.As`.

- [ ] **Step 2: Add operand validator**

Create `internal/virt/qemuimg/internal/argv/argv.go`:

```go
package argv

import (
    "strings"

    imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

func PathOperand(name string, path string) (string, error) {
    if strings.TrimSpace(path) == "" {
        return "", imgexec.InvalidRequest("%s is required", name)
    }
    if strings.HasPrefix(path, "-") {
        return "", imgexec.InvalidRequest("%s must not start with '-'", name)
    }
    return path, nil
}
```

- [ ] **Step 3: Export `CommandError`**

Modify `internal/virt/qemuimg/client.go`:

```go
var ErrInvalidRequest = imgexec.ErrInvalidRequest

type CommandError = imgexec.CommandError
```

- [ ] **Step 4: Use validator in each builder**

Example for `info.Do`:

```go
path, err := argv.PathOperand("path", b.path)
if err != nil {
    return Result{}, err
}
result, err := b.runner.Run(ctx, b.binary, []string{"info", "--output=json", path})
```

Apply the same pattern to `create` (`base`, `target`), `convert` (`source`, `target`), `check` (`path`), and `snapshot` (`path`). Snapshot `name` remains non-path but must still reject blank strings.

- [ ] **Step 5: Harden remove**

Implement `Remove.Do` as:

```go
path, err := argv.PathOperand("path", b.path)
if err != nil {
    return err
}
if filepath.Ext(path) != ".qcow2" {
    return imgexec.InvalidRequest("path must be a .qcow2 file")
}
if err := ctx.Err(); err != nil {
    return err
}
info, err := os.Lstat(path)
if err != nil {
    return os.Remove(path)
}
if info.IsDir() {
    return imgexec.InvalidRequest("path must be a .qcow2 file, not a directory")
}
if info.Mode()&os.ModeSymlink != 0 {
    return imgexec.InvalidRequest("path must be a regular .qcow2 file, not a symlink")
}
if !info.Mode().IsRegular() {
    return imgexec.InvalidRequest("path must be a regular .qcow2 file")
}
if err := ctx.Err(); err != nil {
    return err
}
return os.Remove(path)
```

- [ ] **Step 6: Add tests**

Add tests for:

```go
func TestPathOperandRejectsLeadingDash(t *testing.T)
func TestPublicCommandErrorAliasSupportsErrorsAs(t *testing.T)
func TestDoRejectsLeadingDashPath(t *testing.T)
func TestDoRejectsSymlinkEvenWithQCOW2Suffix(t *testing.T)
```

The public command error test must use the root package API and assert:

```go
var commandErr *qemuimg.CommandError
if !errors.As(err, &commandErr) {
    t.Fatalf("errors.As(... CommandError) = false")
}
```

- [ ] **Step 7: Run targeted verification**

Run: `go test ./internal/virt/qemuimg/...`

Expected: all qemuimg packages pass.

## Task 4: QMP Context-Aware Monitor Interface

**Files:**
- Modify: `internal/virt/qmp/internal/monitor/monitor.go`
- Modify: `internal/virt/qmp/internal/monitor/goqemu.go`
- Modify: `internal/virt/qmp/internal/goqemu/socket.go`
- Modify: `internal/virt/qmp/internal/status/status.go`
- Modify: `internal/virt/qmp/internal/power/power.go`
- Modify tests under `internal/virt/qmp/internal/**`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: QMP connect and command execution obey context cancellation without leaving callers blocked on a live but unresponsive socket.

Acceptance evidence:
- `go test ./internal/virt/qmp/internal/...` passes.
- Tests prove `Run(ctx, ...)` returns when ctx is canceled while waiting for a response.

- [ ] **Step 2: Change monitor interface**

Update `internal/virt/qmp/internal/monitor/monitor.go`:

```go
type Monitor interface {
    Connect(ctx context.Context) error
    Disconnect() error
    Run(ctx context.Context, command []byte) ([]byte, error)
    Events(ctx context.Context) (<-chan Event, error)
}
```

- [ ] **Step 3: Update status and power packages**

In `status.Query`:

```go
raw, err := mon.Run(ctx, command)
```

In `power.run`:

```go
_, err = mon.Run(ctx, payload)
```

- [ ] **Step 4: Make socket monitor run context-aware**

In `internal/goqemu/socket.go`, change `Run`:

```go
func (m *SocketMonitor) Run(ctx context.Context, command []byte) ([]byte, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    m.mu.Lock()
    defer m.mu.Unlock()
    if m.stream == nil {
        return nil, errors.New("qmp monitor is not connected")
    }
    command = append(append([]byte(nil), command...), '\n')
    if _, err := m.c.Write(command); err != nil {
        return nil, err
    }
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    case result, ok := <-m.stream:
        if !ok {
            return nil, io.EOF
        }
        if result.err != nil {
            return nil, result.err
        }
        var response response
        if err := json.Unmarshal(result.buf, &response); err != nil {
            return nil, err
        }
        if err := response.Err(); err != nil {
            return nil, err
        }
        return result.buf, nil
    }
}
```

- [ ] **Step 5: Make events single-use at bottom layer**

In `SocketMonitor.Events`, use compare-and-swap:

```go
if !atomic.CompareAndSwapInt32(m.listeners, 0, 1) {
    return nil, errors.New("qmp events already started")
}
return m.events, nil
```

- [ ] **Step 6: Add in-flight cancellation test**

Add a fake server that accepts a command but never writes a response. Test:

```go
ctx, cancel := context.WithCancel(context.Background())
done := make(chan error, 1)
go func() { _, err := monitor.Run(ctx, []byte(`{"execute":"query-status"}`)); done <- err }()
cancel()
select {
case err := <-done:
    if !errors.Is(err, context.Canceled) { t.Fatalf(...) }
case <-time.After(time.Second):
    t.Fatalf("Run did not return after cancellation")
}
```

- [ ] **Step 7: Run targeted verification**

Run: `go test ./internal/virt/qmp/internal/...`

Expected: all internal qmp packages pass.

## Task 5: QMP Root Client Lifecycle and Events

**Files:**
- Modify: `internal/virt/qmp/errors.go`
- Modify: `internal/virt/qmp/client.go`
- Modify: `internal/virt/qmp/client_test.go`
- Modify: `internal/virt/qmp/internal/events/events.go`
- Modify: `internal/virt/qmp/internal/monitor/goqemu.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `SocketClient` rejects duplicate connects, always cleans up on disconnect, cleans up failed connects, and all event forwarding goroutines exit when context is canceled.

Acceptance evidence:
- `go test ./internal/virt/qmp` passes.
- `go test -race ./internal/virt/qmp/...` passes after Task 6.

- [ ] **Step 2: Add error sentinel**

In `errors.go`:

```go
var ErrAlreadyConnected = errors.New("qmp client already connected")
```

- [ ] **Step 3: Fix `Connect` lifecycle**

Implement the root `Connect` shape:

```go
func (c *SocketClient) Connect(ctx context.Context) error {
    if err := ctx.Err(); err != nil {
        return err
    }
    c.mu.Lock()
    if c.monitor != nil {
        c.mu.Unlock()
        return ErrAlreadyConnected
    }
    c.mu.Unlock()

    mon, err := c.factory.New("unix", c.socketPath, c.timeout)
    if err != nil {
        return err
    }
    installed := false
    defer func() {
        if !installed {
            _ = mon.Disconnect()
        }
    }()
    if err := ctx.Err(); err != nil {
        return err
    }
    if err := mon.Connect(ctx); err != nil {
        return err
    }
    c.mu.Lock()
    defer c.mu.Unlock()
    if c.monitor != nil {
        return ErrAlreadyConnected
    }
    c.monitor = mon
    c.eventsStarted = false
    installed = true
    return nil
}
```

- [ ] **Step 4: Fix `Disconnect` lifecycle**

Implement:

```go
func (c *SocketClient) Disconnect(ctx context.Context) error {
    c.mu.Lock()
    mon := c.monitor
    c.monitor = nil
    c.eventsStarted = false
    c.mu.Unlock()
    if mon == nil {
        return nil
    }
    err := mon.Disconnect()
    if err != nil {
        return err
    }
    return ctx.Err()
}
```

This returns canceled context only after cleanup succeeds.

- [ ] **Step 5: Make event forwarding cancellation-aware**

In `events.Stream`, `monitor.go`, and `client.convertEvents`, replace naked sends with:

```go
select {
case out <- value:
case <-ctx.Done():
    return
}
```

Change root call to:

```go
return convertEvents(ctx, qevents.Stream(ctx, stream, eventNameStrings(names)...)), nil
```

- [ ] **Step 6: Add lifecycle tests**

Add tests to `client_test.go`:

```go
func TestSocketClientDisconnectCanceledContextStillClosesMonitor(t *testing.T)
func TestSocketClientConnectFailureDisconnectsMonitor(t *testing.T)
func TestSocketClientConnectCanceledAfterFactoryDisconnectsMonitor(t *testing.T)
func TestSocketClientRejectsDuplicateConnect(t *testing.T)
func TestSocketClientEventsCancelWithoutConsumer(t *testing.T)
```

The fake monitor should count `Disconnect()` calls and support `connectErr`.

- [ ] **Step 7: Run targeted verification**

Run: `go test ./internal/virt/qmp`

Expected: root qmp package passes.

## Task 6: QMP Typed Response Errors

**Files:**
- Modify: `internal/virt/qmp/errors.go`
- Modify: `internal/virt/qmp/internal/goqemu/socket.go`
- Modify: `internal/virt/qmp/internal/status/status.go`
- Modify: `internal/virt/qmp/internal/status/status_test.go`
- Modify: `internal/virt/qmp/internal/goqemu/socket_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: QMP errors preserve `class` and `desc` so callers can use `errors.As` without string matching.

Acceptance evidence:
- `go test ./internal/virt/qmp/...` passes.
- Tests prove `ResponseError{Class, Description}` is returned for QMP error responses.

- [ ] **Step 2: Add typed error**

In `errors.go` or an internal shared package if avoiding import cycles:

```go
type ResponseError struct {
    Class       string
    Description string
}

func (e *ResponseError) Error() string {
    if e.Class == "" {
        return e.Description
    }
    return e.Class + ": " + e.Description
}
```

If root package import cycles appear, keep an internal `protocol.ResponseError` and type-alias it from root.

- [ ] **Step 3: Parse class in status and goqemu responses**

Parse:

```go
Error *struct {
    Class       string `json:"class"`
    Description string `json:"desc"`
} `json:"error,omitempty"`
```

Return `&ResponseError{Class: response.Error.Class, Description: response.Error.Description}` when desc is non-empty.

- [ ] **Step 4: Add tests**

Update status parse test with:

```go
raw := []byte(`{"error":{"class":"GenericError","desc":"bad command"}}`)
_, err := Parse(raw)
var responseErr *ResponseError
if !errors.As(err, &responseErr) { t.Fatalf(...) }
if responseErr.Class != "GenericError" { t.Fatalf(...) }
```

Add socket-level response error test for `Run(ctx, ...)`.

- [ ] **Step 5: Run targeted verification**

Run: `go test ./internal/virt/qmp/...`

Expected: all qmp packages pass.

## Task 7: Full Verification and Documentation Updates

**Files:**
- Modify: `internal/virt/AGENTS.md` if flows or interfaces materially change.
- Modify: `internal/virt/qemu/AGENTS.md` if QEMU builder contracts change.
- Modify: `internal/virt/qemuimg/AGENTS.md` if qemu-img operand/error contracts change.
- Modify: `internal/virt/qmp/AGENTS.md` if monitor interface, errors, or lifecycle contracts change.

- [ ] **Step 1: Confirm documentation changes needed**

Goal: AGENTS knowledge base reflects the new typed renderer, operand validator, and context-aware QMP monitor contracts.

Acceptance evidence:
- Each changed AGENTS section names the new boundary and constraints.
- No obsolete statements claim `Monitor.Run(command []byte)` or direct string rendering as the current contract.

- [ ] **Step 2: Update module docs**

Update docs with these facts:

```text
qemu: typed entries validate through qopt before VM.Argv is exposed.
qemuimg: path operands reject leading dash and CommandError is public via qemuimg.CommandError.
qmp: internal monitor Run is context-aware and Events are single-use at both root and socket layers.
```

- [ ] **Step 3: Run full internal virt tests**

Run: `go test ./internal/virt/...`

Expected: all packages pass.

- [ ] **Step 4: Run QMP race verification**

Run: `go test -race ./internal/virt/qmp/...`

Expected: all QMP packages pass under race detector.

- [ ] **Step 5: Run repository verification**

Run: `scripts/verify.sh`

Expected:
- gofmt check reports no files.
- `go test ./...` passes.
- configured service builds pass.

- [ ] **Step 6: Inspect final diff**

Run: `git diff -- internal/virt docs/superpowers/plans/2026-05-26-virt-boundary-hardening.md`

Expected: diff contains only the planned hardening and documentation changes.

## Self-Review Notes

- Spec coverage: QEMU option injection, typed required-field validation, enum validation, nil name option, qemu-img public `CommandError`, qemu-img option injection, remove TOCTOU/context, QMP disconnect cleanup, connect cleanup, duplicate connect, in-flight cancellation, event backpressure, typed QMP errors, bottom-layer single-use events, and verification are all covered by tasks.
- Placeholder scan: no task uses unspecified future work; every task lists files, exact behavior, and verification command.
- Type consistency: the plan consistently uses `Run(ctx, command []byte)`, `Connect(ctx)`, root `qemuimg.CommandError`, and QEMU `qopt` renderer contracts.
