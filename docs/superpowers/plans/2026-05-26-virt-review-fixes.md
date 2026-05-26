# Virt Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix blocking and important `internal/virt` review findings by tightening QEMU argv escape hatches, qemu-img qcow2/error boundaries, and QMP lifecycle/cancellation behavior, with tests that lock each repaired contract.

**Architecture:** Keep the existing three-package boundary: `qemu` renders validated argv only, `qemuimg` builds validated offline qcow2 commands through `Runner.Run(ctx,binary,args)`, and `qmp` exposes only a project-owned root facade over internal transport. The changes are boundary hardening, not a feature expansion.

**Tech Stack:** Go 1.26 module, table-driven `testing`, `context`, `errors`, `encoding/json`, `net`, `sync`, `time`, fake runners/monitors, local Go test verification with `go test` and `go test -race`.

---

## File Structure

- Modify: `internal/virt/qemu/vm.go` — add generic argument allowlist/rejection policy and CPU/display validation in `Build()`.
- Modify: `internal/virt/qemu/cpu/cpu.go` — add `Model.Valid()`.
- Modify: `internal/virt/qemu/display/display.go` — add `Display.Valid()`.
- Modify: `internal/virt/qemu/vm_test.go` — add tests for generic typed flag rejection, allowlisted generic flags, invalid CPU, and invalid display.
- Modify: `internal/virt/qemuimg/internal/exec/exec.go` — add `DecodeError` that preserves `Result` and unwraps the JSON decode cause.
- Modify: `internal/virt/qemuimg/client.go` — export `DecodeError` alias and update error model documentation.
- Modify: `internal/virt/qemuimg/info/info.go` — render `-f qcow2`, return `DecodeError` for JSON parse failures.
- Modify: `internal/virt/qemuimg/check/check.go` — render `-f qcow2`, return `DecodeError` for JSON parse failures.
- Modify: `internal/virt/qemuimg/convert/convert.go` — render source format `-f qcow2`.
- Modify: `internal/virt/qemuimg/info/info_test.go` — update argv and add decode/context tests.
- Modify: `internal/virt/qemuimg/check/check_test.go` — update argv and add decode/context tests.
- Modify: `internal/virt/qemuimg/convert/convert_test.go` — update argv expectation.
- Modify: `internal/virt/qemuimg/client_test.go` — update root-level argv expectations and alias tests.
- Modify: `internal/virt/qemuimg/remove/remove.go` — document trusted-directory deletion contract and keep explicit regular-file guardrails.
- Modify: `internal/virt/qemuimg/remove/remove_test.go` — lock remove contract through regular-file, directory, symlink, and non-regular behavior tests.
- Modify: `internal/virt/qmp/client.go` — let `SocketClient` own event stream cancellation and release it on `Disconnect`.
- Modify: `internal/virt/qmp/client_test.go` — add event disconnect release, event-start failure retry, and root `ResponseError` preservation tests.
- Modify: `internal/virt/qmp/internal/goqemu/socket.go` — add context-aware write deadline handling for handshake and command writes.
- Modify: `internal/virt/qmp/internal/goqemu/socket_test.go` — add deterministic write-cancellation coverage.
- Modify: `internal/virt/qemu/AGENTS.md`, `internal/virt/qemuimg/AGENTS.md`, `internal/virt/qmp/AGENTS.md` — update only the boundary notes affected by the new contracts.

## Task 1: QEMU Generic Argument and Enum Hardening

**Files:**
- Modify: `internal/virt/qemu/vm.go`
- Modify: `internal/virt/qemu/cpu/cpu.go`
- Modify: `internal/virt/qemu/display/display.go`
- Modify: `internal/virt/qemu/vm_test.go`
- Modify: `internal/virt/qemu/AGENTS.md`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `Builder.Build()` rejects generic typed-flag bypasses and invalid CPU/display values while still allowing the known generic flags `-bios`, `-rtc`, and `-enable-kvm`.

Acceptance evidence:
- `go test ./internal/virt/qemu/...` passes.
- Tests prove `AddArgument(qemu.Arg("-netdev", ...))` and other typed flags fail with `ErrInvalidVM`.
- Tests prove `AddArgument(qemu.Arg("-bios", ...))`, `AddArgument(qemu.Arg("-rtc", ...))`, and `AddArgument(qemu.Flag("-enable-kvm"))` still render.

- [ ] **Step 2: Add enum validation helpers**

Update `internal/virt/qemu/cpu/cpu.go` to expose supported CPU models:

```go
package cpu

type Model string

const (
	Host      Model = "host"
	CortexA57 Model = "cortex-a57"
)

// Valid reports whether the CPU model is unset or explicitly supported by Govirta.
func (m Model) Valid() bool {
	switch m {
	case "", Host, CortexA57:
		return true
	default:
		return false
	}
}
```

Update `internal/virt/qemu/display/display.go`:

```go
package display

type Display string

const None Display = "none"

// Valid reports whether the display setting is unset or supported by Govirta.
func (d Display) Valid() bool { return d == "" || d == None }
```

- [ ] **Step 3: Add generic flag allowlist and validation in Build**

Update `internal/virt/qemu/vm.go` with helper functions near `isMachineArgument`:

```go
func isAllowedGenericArgument(flag string) bool {
	switch flag {
	case "-bios", "-rtc", "-enable-kvm":
		return true
	default:
		return false
	}
}

func isTypedArgument(flag string) bool {
	switch flag {
	case "-machine", "-M", "-name", "-cpu", "-smp", "-m",
		"-blockdev", "-device", "-netdev", "-chardev", "-mon", "-serial",
		"-display", "-msg", "-pidfile", "-no-reboot", "-no-shutdown":
		return true
	default:
		return false
	}
}
```

In `Builder.Build()`, after machine/msg validation and before returning `VM`, add CPU/display validation and generic flag policy:

```go
if b.cpu != "" && !b.cpu.Valid() {
	return VM{}, fmt.Errorf("%w: unsupported cpu model %q", ErrInvalidVM, b.cpu)
}
if b.display != "" && !b.display.Valid() {
	return VM{}, fmt.Errorf("%w: unsupported display %q", ErrInvalidVM, b.display)
}
for _, entry := range b.ordered {
	if !validArgument(entry) {
		if err := argumentError(entry); err != nil {
			return VM{}, fmt.Errorf("%w: invalid qemu argument: %v", ErrInvalidVM, err)
		}
		return VM{}, fmt.Errorf("%w: invalid qemu argument", ErrInvalidVM)
	}
	name := argumentName(entry)
	if isTypedArgument(name) {
		return VM{}, fmt.Errorf("%w: %s must use a typed builder", ErrInvalidVM, name)
	}
	if !isAllowedGenericArgument(name) {
		return VM{}, fmt.Errorf("%w: unsupported generic qemu argument %q", ErrInvalidVM, name)
	}
}
```

Remove the now-redundant `isMachineArgument` usage if no other code uses it.

- [ ] **Step 4: Update existing generic argument tests**

In `internal/virt/qemu/vm_test.go`, keep the existing test that accepts generic arguments but make it cover exactly the allowlist:

```go
func TestBuilderAcceptsAllowlistedGenericArguments(t *testing.T) {
	vm, err := qemu.NewVM(qemu.ArchAArch64).
		Machine(machine.ProfileAArch64VirtKVM).
		CPU(cpu.CortexA57).
		AddArgument(qemu.Arg("-bios", "/usr/share/edk2/aarch64/QEMU_EFI.fd")).
		AddArgument(qemu.Arg("-rtc", "base=utc")).
		AddArgument(qemu.Flag("-enable-kvm")).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	got := vm.Argv()
	wantSuffix := []string{"-machine", "type=virt,accel=kvm", "-cpu", "cortex-a57", "-bios", "/usr/share/edk2/aarch64/QEMU_EFI.fd", "-rtc", "base=utc", "-enable-kvm"}
	if !reflect.DeepEqual(got[1:], wantSuffix) {
		t.Fatalf("Argv()[1:] = %#v, want %#v", got[1:], wantSuffix)
	}
}
```

Ensure `reflect`, `machine`, and `cpu` imports are present only if used.

- [ ] **Step 5: Add rejection tests for typed generic flags and invalid enums**

Add table cases to `TestBuildRejectsInvalidConfig` or a new table-driven test:

```go
func TestBuildRejectsGenericTypedArgumentBypass(t *testing.T) {
	cases := []qemu.Argument{
		qemu.Arg("-netdev", "tap,id=n0,ifname=tap0,script=/bad"),
		qemu.Arg("-device", "virtio-net-pci,netdev=n0"),
		qemu.Arg("-blockdev", "driver=qcow2,node-name=root"),
		qemu.Arg("-chardev", "socket,id=qmp0,path=/run/qmp"),
		qemu.Arg("-mon", "chardev=qmp0,mode=control"),
		qemu.Arg("-serial", "chardev:serial0"),
		qemu.Arg("-name", "vm"),
		qemu.Arg("-cpu", "max"),
		qemu.Arg("-display", "gtk"),
		qemu.Arg("-msg", "timestamp=on"),
		qemu.Flag("-no-reboot"),
	}
	for _, arg := range cases {
		t.Run(fmt.Sprintf("%#v", arg), func(t *testing.T) {
			_, err := qemu.NewVM(qemu.ArchX86_64).AddArgument(arg).Build()
			if !errors.Is(err, qemu.ErrInvalidVM) {
				t.Fatalf("Build() error = %v, want ErrInvalidVM", err)
			}
		})
	}
}

func TestBuildRejectsInvalidCPUAndDisplay(t *testing.T) {
	cases := []struct {
		name  string
		build func() (qemu.VM, error)
	}{
		{name: "invalid_cpu", build: func() (qemu.VM, error) {
			return qemu.NewVM(qemu.ArchX86_64).CPU(cpu.Model("max,host-phys-bits=on")).Build()
		}},
		{name: "invalid_display", build: func() (qemu.VM, error) {
			return qemu.NewVM(qemu.ArchX86_64).Display(display.Display("gtk")).Build()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build()
			if !errors.Is(err, qemu.ErrInvalidVM) {
				t.Fatalf("Build() error = %v, want ErrInvalidVM", err)
			}
		})
	}
}
```

- [ ] **Step 6: Update qemu AGENTS note**

Update `internal/virt/qemu/AGENTS.md` conventions to state:

```markdown
- Generic `AddArgument` is allowlisted for flags without typed builders, currently `-bios`, `-rtc`, and `-enable-kvm`. Existing typed flags must not be reintroduced through generic arguments.
```

- [ ] **Step 7: Run targeted verification**

Run:

```bash
go test ./internal/virt/qemu/...
```

Expected: all qemu packages pass.

- [ ] **Step 8: Commit checkpoint only if explicitly authorized**

Before committing, run:

```bash
git status --short
git diff -- internal/virt/qemu internal/virt/qemu/AGENTS.md
```

If the user has explicitly authorized commits in the execution session, commit with:

```bash
git add internal/virt/qemu internal/virt/qemu/AGENTS.md
git commit -m "fix(qemu): restrict generic argv escape hatch"
```

If commits are not authorized, leave the changes unstaged and report the checkpoint status.

## Task 2: qemu-img qcow2 Format and Error Model

**Files:**
- Modify: `internal/virt/qemuimg/internal/exec/exec.go`
- Modify: `internal/virt/qemuimg/client.go`
- Modify: `internal/virt/qemuimg/info/info.go`
- Modify: `internal/virt/qemuimg/check/check.go`
- Modify: `internal/virt/qemuimg/convert/convert.go`
- Modify: `internal/virt/qemuimg/info/info_test.go`
- Modify: `internal/virt/qemuimg/check/check_test.go`
- Modify: `internal/virt/qemuimg/convert/convert_test.go`
- Modify: `internal/virt/qemuimg/client_test.go`
- Modify: `internal/virt/qemuimg/AGENTS.md`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: qcow2 input commands pass `-f qcow2`, and JSON decode failures are not reported as process-level `CommandError`.

Acceptance evidence:
- `go test ./internal/virt/qemuimg/...` passes.
- `info`, `check`, and `convert` argv tests include `-f qcow2`.
- Tests prove process errors use `CommandError`, while JSON parse errors use `DecodeError` and preserve stdout/stderr.

- [ ] **Step 2: Add DecodeError at the exec boundary**

In `internal/virt/qemuimg/internal/exec/exec.go`, add this type below `CommandError`:

```go
type DecodeError struct {
	Result Result
	Err    error
}

func (e *DecodeError) Error() string {
	if e.Err == nil {
		return "decode qemu-img output"
	}
	return fmt.Sprintf("decode qemu-img output: %v", e.Err)
}

func (e *DecodeError) Unwrap() error { return e.Err }

func WrapDecodeError(result Result, err error) error {
	if err == nil {
		return nil
	}
	return &DecodeError{Result: result, Err: err}
}
```

- [ ] **Step 3: Export DecodeError and update docs**

In `internal/virt/qemuimg/client.go`, add:

```go
// DecodeError wraps successful qemu-img execution whose stdout could not be
// decoded as the expected JSON shape. It preserves stdout/stderr for diagnosis.
type DecodeError = imgexec.DecodeError
```

Keep `CommandError` documented as process-level failure only.

- [ ] **Step 4: Update qemu-img argv and decode wrapping**

In `internal/virt/qemuimg/info/info.go`, change the runner args and parse error wrapping:

```go
result, err := b.runner.Run(ctx, b.binary, []string{"info", "-f", "qcow2", "--output=json", path})
if err != nil {
	return Result{}, imgexec.WrapError(result, err)
}

var info Result
if err := json.Unmarshal([]byte(result.Stdout), &info); err != nil {
	return Result{}, imgexec.WrapDecodeError(result, err)
}
return info, nil
```

In `internal/virt/qemuimg/check/check.go`, use:

```go
runResult, err := b.runner.Run(ctx, b.binary, []string{"check", "-f", "qcow2", "--output=json", path})
if err != nil {
	return Result{}, imgexec.WrapError(runResult, err)
}

var result Result
if err := json.Unmarshal([]byte(runResult.Stdout), &result); err != nil {
	return Result{}, imgexec.WrapDecodeError(runResult, err)
}
result.RawOutput = runResult.Stdout
return result, nil
```

In `internal/virt/qemuimg/convert/convert.go`, use:

```go
result, err := b.runner.Run(ctx, b.binary, []string{"convert", "-f", "qcow2", "-O", "qcow2", source, target})
return imgexec.WrapError(result, err)
```

- [ ] **Step 5: Update argv expectations**

Update tests that assert args:

```go
want := []string{"info", "-f", "qcow2", "--output=json", "disk.qcow2"}
want := []string{"check", "-f", "qcow2", "--output=json", "disk.qcow2"}
want := []string{"convert", "-f", "qcow2", "-O", "qcow2", "source.qcow2", "target.qcow2"}
```

Apply these expectations in both subpackage tests and root `client_test.go` where full argv is asserted.

- [ ] **Step 6: Add error classification tests**

In `info_test.go`, replace the parse error assertion with:

```go
func TestDoReturnsDecodeErrorWithOutput(t *testing.T) {
	runner := &fakeRunner{result: imgexec.Result{Stdout: "not-json", Stderr: "warning"}}
	_, err := New("qemu-img", runner).Path("disk.qcow2").Do(context.Background())
	var decodeErr *imgexec.DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("Do() error = %T %[1]v, want DecodeError", err)
	}
	if decodeErr.Result.Stdout != "not-json" || decodeErr.Result.Stderr != "warning" {
		t.Fatalf("DecodeError.Result = %#v", decodeErr.Result)
	}
	var commandErr *imgexec.CommandError
	if errors.As(err, &commandErr) {
		t.Fatalf("Do() error matched CommandError, want decode-only classification")
	}
}
```

Add the equivalent test to `check_test.go`.

- [ ] **Step 7: Add context cancellation tests for info and check**

Use a fake runner that returns `ctx.Err()` when canceled:

```go
func TestDoReturnsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &fakeRunner{run: func(ctx context.Context, binary string, args []string) (imgexec.Result, error) {
		return imgexec.Result{}, ctx.Err()
	}}
	_, err := New("qemu-img", runner).Path("disk.qcow2").Do(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context.Canceled", err)
	}
}
```

If the existing fake runner does not support a `run` function, extend it with an optional function field:

```go
type fakeRunner struct {
	result imgexec.Result
	err    error
	run    func(context.Context, string, []string) (imgexec.Result, error)
}

func (r *fakeRunner) Run(ctx context.Context, binary string, args []string) (imgexec.Result, error) {
	if r.run != nil {
		return r.run(ctx, binary, args)
	}
	return r.result, r.err
}
```

- [ ] **Step 8: Update qemuimg AGENTS note**

Update `internal/virt/qemuimg/AGENTS.md` with these facts:

```markdown
- `QCOW2Client` input commands pass `-f qcow2`; do not rely on qemu-img format probing under this entry.
- Process failures use `CommandError`; JSON decode failures use `DecodeError` and still preserve stdout/stderr.
```

- [ ] **Step 9: Run targeted verification**

Run:

```bash
go test ./internal/virt/qemuimg/...
```

Expected: all qemuimg packages pass.

- [ ] **Step 10: Commit checkpoint only if explicitly authorized**

Before committing, run:

```bash
git status --short
git diff -- internal/virt/qemuimg
```

If commits are authorized, commit with:

```bash
git add internal/virt/qemuimg
git commit -m "fix(qemuimg): make qcow2 boundaries explicit"
```

If commits are not authorized, leave the changes unstaged and report the checkpoint status.

## Task 3: qemu-img Remove Contract

**Files:**
- Modify: `internal/virt/qemuimg/remove/remove.go`
- Modify: `internal/virt/qemuimg/remove/remove_test.go`
- Modify: `internal/virt/qemuimg/AGENTS.md`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `Remove().Do(ctx)` honestly documents and tests its trusted-directory regular-file deletion contract, without claiming `Lstat` plus pathname `Remove` is a complete TOCTOU defense for hostile directories.

Acceptance evidence:
- `go test ./internal/virt/qemuimg/remove` passes.
- Tests still prove directories, symlinks, and non-regular files are not removed.
- Comments state the parent directory trust precondition.

- [ ] **Step 2: Update remove contract comments**

In `internal/virt/qemuimg/remove/remove.go`, add a package-level or `Do` comment:

```go
// Do removes a trusted Govirta-owned qcow2 image path.
//
// Security contract: callers must pass a path resolved by Govirta's trusted
// storage layer, or otherwise ensure the parent directory is not writable by
// untrusted users. The Lstat checks below reject accidental directory, symlink,
// and non-regular-file deletion, but pathname deletion cannot prove the inode is
// unchanged in a hostile parent directory.
func (b *Builder) Do(ctx context.Context) error {
```

Keep the existing `PathOperand`, `.qcow2`, context, `Lstat`, directory, symlink, non-regular, second context check, and `os.Remove` flow.

- [ ] **Step 3: Add non-regular file test if missing**

In `remove_test.go`, add a FIFO or other non-regular file test. On platforms where FIFO creation is available, use `syscall.Mkfifo`; if this repository targets Unix-like hosts for QEMU, this is acceptable.

```go
func TestDoRejectsNonRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	err := New("qemu-img", nil).Path(path).Do(context.Background())
	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want ErrInvalidRequest", err)
	}
	if _, statErr := os.Lstat(path); statErr != nil {
		t.Fatalf("fifo was removed: %v", statErr)
	}
}
```

Add imports only if not already present: `syscall` and the internal exec alias used by this package's tests.

- [ ] **Step 4: Add lowercase suffix contract test**

Because this task touches remove semantics, lock the current lowercase `.qcow2` requirement:

```go
func TestDoRejectsUppercaseQCOW2Suffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.QCOW2")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := New("qemu-img", nil).Path(path).Do(context.Background())
	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want ErrInvalidRequest", err)
	}
	if _, statErr := os.Lstat(path); statErr != nil {
		t.Fatalf("file was removed: %v", statErr)
	}
}
```

- [ ] **Step 5: Update qemuimg AGENTS remove note**

Add this statement to `internal/virt/qemuimg/AGENTS.md`:

```markdown
- `Remove` is local filesystem deletion for trusted Govirta-owned image paths. Its `Lstat` checks reject accidental directory/symlink/non-regular deletion but do not make untrusted parent directories safe.
```

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test ./internal/virt/qemuimg/remove
```

Expected: remove package passes.

- [ ] **Step 7: Commit checkpoint only if explicitly authorized**

Before committing, run:

```bash
git status --short
git diff -- internal/virt/qemuimg/remove internal/virt/qemuimg/AGENTS.md
```

If commits are authorized, commit with:

```bash
git add internal/virt/qemuimg/remove internal/virt/qemuimg/AGENTS.md
git commit -m "fix(qemuimg): clarify trusted remove semantics"
```

If commits are not authorized, leave the changes unstaged and report the checkpoint status.

## Task 4: QMP Event Stream Lifecycle

**Files:**
- Modify: `internal/virt/qmp/client.go`
- Modify: `internal/virt/qmp/client_test.go`
- Modify: `internal/virt/qmp/AGENTS.md`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `SocketClient.Disconnect(ctx)` releases event conversion goroutines even if the caller does not cancel the context passed to `Events`.

Acceptance evidence:
- `go test ./internal/virt/qmp` passes.
- A test proves an unconsumed event stream closes after `Disconnect` without caller cancel.
- A test proves failed `mon.Events(ctx)` does not consume the single-use event slot.

- [ ] **Step 2: Add event cancel fields to SocketClient**

In `internal/virt/qmp/client.go`, extend `SocketClient`:

```go
type SocketClient struct {
	socketPath string
	timeout    time.Duration
	factory    monitor.Factory

	lifecycleMu sync.Mutex

	mu            sync.Mutex
	monitor       monitor.Monitor
	eventsStarted bool
	eventsCancel  context.CancelFunc
}
```

- [ ] **Step 3: Reset and cancel event stream on lifecycle changes**

In `Connect`, when installing the monitor, reset the cancel function:

```go
c.monitor = mon
c.eventsStarted = false
c.eventsCancel = nil
installed = true
```

In `Disconnect`, extract and clear the cancel function while holding `mu`, then call it before disconnecting the monitor:

```go
c.mu.Lock()
mon := c.monitor
cancelEvents := c.eventsCancel
c.monitor = nil
c.eventsStarted = false
c.eventsCancel = nil
c.mu.Unlock()

if cancelEvents != nil {
	cancelEvents()
}
```

Keep the existing `mon == nil` and `mon.Disconnect(ctx)` behavior.

- [ ] **Step 4: Make Events use a child context**

Update `Events` so the client owns a child context:

```go
eventCtx, cancel := context.WithCancel(ctx)

c.mu.Lock()
if c.monitor == nil {
	c.mu.Unlock()
	cancel()
	return nil, ErrNotConnected
}
if c.eventsStarted {
	c.mu.Unlock()
	cancel()
	return nil, ErrEventsAlreadyStarted
}
c.eventsStarted = true
c.eventsCancel = cancel
mon := c.monitor
c.mu.Unlock()

stream, err := mon.Events(eventCtx)
if err != nil {
	cancel()
	c.mu.Lock()
	if c.eventsCancel == cancel {
		c.eventsCancel = nil
	}
	c.eventsStarted = false
	c.mu.Unlock()
	return nil, err
}
return convertEvents(eventCtx, qevents.Stream(eventCtx, stream, eventNameStrings(names)...)), nil
```

Do not reset `eventsStarted` when a successful stream naturally ends; the contract remains single-use for a connected socket.

- [ ] **Step 5: Add disconnect release test**

In `client_test.go`, add a test using the existing fake monitor. The fake monitor must expose its event channel so the test can push an event and then stop reading.

```go
func TestSocketClientDisconnectCancelsUnconsumedEvents(t *testing.T) {
	client, mon := connectedTestClient(t)
	events, err := client.Events(context.Background())
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	mon.events <- monitor.Event{Name: "SHUTDOWN"}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = client.Disconnect(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Disconnect() did not return")
	}

	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("events channel remained open")
		}
	case <-time.After(time.Second):
		t.Fatal("events channel did not close")
	}
}
```

Adjust channel buffering in the fake monitor only as needed for deterministic behavior.

- [ ] **Step 6: Add Events failure retry test**

Extend fake monitor with an optional `eventsErr error` field:

```go
func (m *fakeMonitor) Events(ctx context.Context) (<-chan monitor.Event, error) {
	if m.eventsErr != nil {
		return nil, m.eventsErr
	}
	return m.events, nil
}
```

Then add:

```go
func TestSocketClientEventsFailureCanRetry(t *testing.T) {
	client, mon := connectedTestClient(t)
	mon.eventsErr = errors.New("events failed")
	if _, err := client.Events(context.Background()); err == nil {
		t.Fatal("Events() error = nil, want error")
	}
	mon.eventsErr = nil
	if _, err := client.Events(context.Background()); err != nil {
		t.Fatalf("Events() retry error = %v", err)
	}
}
```

- [ ] **Step 7: Add root ResponseError preservation test**

Use the fake monitor's `Run` hook or extend it to return a typed error:

```go
func TestSocketClientPreservesResponseError(t *testing.T) {
	client, mon := connectedTestClient(t)
	mon.runErr = &qmp.ResponseError{Class: "GenericError", Description: "boom"}
	_, err := client.QueryStatus(context.Background())
	var responseErr *qmp.ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("QueryStatus() error = %T %[1]v, want ResponseError", err)
	}
	if responseErr.Class != "GenericError" || responseErr.Description != "boom" {
		t.Fatalf("ResponseError = %#v", responseErr)
	}
}
```

Use the package name style already present in `client_test.go`; if tests are in package `qmp`, omit the `qmp.` qualifier.

- [ ] **Step 8: Update qmp AGENTS note**

Update `internal/virt/qmp/AGENTS.md` with:

```markdown
- `SocketClient.Disconnect` cancels any event stream created by the connection so callers do not have to cancel the original `Events` context to release conversion goroutines.
```

- [ ] **Step 9: Run targeted verification**

Run:

```bash
go test ./internal/virt/qmp
```

Expected: root qmp package passes.

- [ ] **Step 10: Commit checkpoint only if explicitly authorized**

Before committing, run:

```bash
git status --short
git diff -- internal/virt/qmp/client.go internal/virt/qmp/client_test.go internal/virt/qmp/AGENTS.md
```

If commits are authorized, commit with:

```bash
git add internal/virt/qmp/client.go internal/virt/qmp/client_test.go internal/virt/qmp/AGENTS.md
git commit -m "fix(qmp): release event streams on disconnect"
```

If commits are not authorized, leave the changes unstaged and report the checkpoint status.

## Task 5: QMP Write Cancellation

**Files:**
- Modify: `internal/virt/qmp/internal/goqemu/socket.go`
- Modify: `internal/virt/qmp/internal/goqemu/socket_test.go`
- Modify: `internal/virt/qmp/AGENTS.md`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: QMP handshake and command writes observe caller cancellation through a temporary write deadline and return the context error when cancellation forces the write to unblock.

Acceptance evidence:
- `go test ./internal/virt/qmp/internal/goqemu` passes.
- Tests cover write cancellation using deterministic test doubles or a controlled connection.
- `go test -race ./internal/virt/qmp/...` passes after Task 4 and Task 5.

- [ ] **Step 2: Add context-aware write helper**

In `internal/virt/qmp/internal/goqemu/socket.go`, add a helper near `SocketMonitor.Run`:

```go
func writeWithContext(ctx context.Context, conn net.Conn, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetWriteDeadline(time.Now())
		case <-done:
		}
	}()
	err := func() error {
		defer close(done)
		_, writeErr := conn.Write(payload)
		return writeErr
	}()
	_ = conn.SetWriteDeadline(time.Time{})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}
```

This helper intentionally restores the write deadline after every attempt. If a future refactor adds a `sync.WaitGroup` like the read watchdog, keep the same externally observable behavior.

- [ ] **Step 3: Use helper in Connect handshake**

Replace the `enc.Encode(Command{Execute: "qmp_capabilities"})` call with marshaling plus `writeWithContext`:

```go
payload, err := json.Marshal(Command{Execute: "qmp_capabilities"})
if err != nil {
	return err
}
payload = append(payload, '\n')
if err := writeWithContext(ctx, m.c, payload); err != nil {
	return err
}
```

Remove the now-unused `enc := json.NewEncoder(m.c)` variable if nothing else uses it.

- [ ] **Step 4: Use helper in Run command writes**

Replace the synchronous write in `Run`:

```go
command = append(append([]byte(nil), command...), '\n')
if err := writeWithContext(ctx, m.c, command); err != nil {
	return nil, err
}
```

Keep the existing response wait `select` unchanged.

- [ ] **Step 5: Add deterministic write-cancellation test**

In `socket_test.go`, add a fake `net.Conn` that blocks in `Write` until a write deadline is set:

```go
type blockingWriteConn struct {
	net.Conn
	deadline chan time.Time
}

func (c *blockingWriteConn) Write(p []byte) (int, error) {
	<-c.deadline
	return 0, os.ErrDeadlineExceeded
}

func (c *blockingWriteConn) SetWriteDeadline(t time.Time) error {
	c.deadline <- t
	return nil
}
```

Then test the helper directly if it is package-private in the same package:

```go
func TestWriteWithContextReturnsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := &blockingWriteConn{deadline: make(chan time.Time, 2)}
	done := make(chan error, 1)
	go func() {
		done <- writeWithContext(ctx, conn, []byte("{}\n"))
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("writeWithContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writeWithContext() did not return")
	}
}
```

Add imports `os` and any missing packages. If direct helper testing is considered too implementation-specific, replace it with a `SocketMonitor.Run` test that installs the same fake conn and connected stream state.

- [ ] **Step 6: Update qmp AGENTS write-cancellation note**

Update `internal/virt/qmp/AGENTS.md` with:

```markdown
- The direct socket monitor applies context-aware write deadlines for handshake and command writes; tests cover cancellation of a blocked write path.
```

- [ ] **Step 7: Run targeted verification**

Run:

```bash
go test ./internal/virt/qmp/internal/goqemu
go test -race ./internal/virt/qmp/...
```

Expected: both commands pass.

- [ ] **Step 8: Commit checkpoint only if explicitly authorized**

Before committing, run:

```bash
git status --short
git diff -- internal/virt/qmp/internal/goqemu internal/virt/qmp/AGENTS.md
```

If commits are authorized, commit with:

```bash
git add internal/virt/qmp/internal/goqemu internal/virt/qmp/AGENTS.md
git commit -m "fix(qmp): make socket writes cancellation-aware"
```

If commits are not authorized, leave the changes unstaged and report the checkpoint status.

## Task 6: Final Integration Verification

**Files:**
- Review all modified files from Tasks 1-5.
- No new source files unless a task explicitly created one.

- [ ] **Step 1: Confirm final acceptance criteria**

Goal: all blocking and important findings in `docs/superpowers/specs/2026-05-26-virt-review-fixes-design.md` are addressed with tests.

Acceptance evidence:
- `go test ./internal/virt/...` passes.
- `go test -race ./internal/virt/qmp/...` passes.
- `scripts/verify.sh` passes.
- `git diff` shows only planned qemu/qemuimg/qmp/spec/plan/AGENTS changes.

- [ ] **Step 2: Run full virt verification**

Run:

```bash
go test ./internal/virt/...
```

Expected: every package under `internal/virt` passes.

- [ ] **Step 3: Run QMP race verification**

Run:

```bash
go test -race ./internal/virt/qmp/...
```

Expected: QMP packages pass under the race detector.

- [ ] **Step 4: Run repository verification script**

Run:

```bash
scripts/verify.sh
```

Expected:
- `gofmt -l .` reports no unformatted Go files.
- `go test ./...` passes.
- `go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl` passes.

- [ ] **Step 5: Inspect final diff**

Run:

```bash
git status --short
git diff --stat
git diff -- docs/superpowers/specs/2026-05-26-virt-review-fixes-design.md docs/superpowers/plans/2026-05-26-virt-review-fixes.md internal/virt
```

Expected: diff only contains the approved design, this plan, and planned `internal/virt` hardening changes.

- [ ] **Step 6: Final commit only if explicitly authorized**

If the user has explicitly authorized commits and previous task commits were not created, commit the full logical change with:

```bash
git add docs/superpowers/specs/2026-05-26-virt-review-fixes-design.md docs/superpowers/plans/2026-05-26-virt-review-fixes.md internal/virt
git commit -m "fix(virt): harden reviewed virtualization boundaries"
```

If commits are not authorized, do not commit. Report the verification evidence and changed files.

## Self-Review Notes

- Spec coverage: Task 1 covers QEMU generic allowlist and CPU/display validation; Tasks 2-3 cover qemu-img explicit qcow2 input, decode error separation, and remove contract; Tasks 4-5 cover QMP event lifecycle and write cancellation; Task 6 covers verification.
- Placeholder scan: this plan contains concrete file paths, code snippets, commands, and expected evidence. It intentionally avoids undefined future work.
- Type consistency: code snippets use existing package names and types (`qemu.ErrInvalidVM`, `imgexec.CommandError`, `imgexec.DecodeError`, `qmp.ResponseError`, `monitor.Event`) or define the new types before referencing them.
