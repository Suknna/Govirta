# QMP go-qemu Wrapper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use systematic debugging for unexpected test failures and verification-before-completion before claiming completion. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current no-op `internal/virt/qmp` boundary with a project-owned QMP client that vendors the direct-socket subset of `github.com/digitalocean/go-qemu/qmp` behind an internal adapter.

**Architecture:** The root `qmp` package is the only public project API for callers. It exposes typed operations for readiness, status, power control, quit, and event subscription. The vendored go-qemu direct-socket code is isolated in `internal/virt/qmp/internal/goqemu` and adapted through `internal/virt/qmp/internal/monitor`. Protocol-specific command construction and parsing live in internal single-purpose subpackages so upper layers cannot import or depend on raw QMP implementation details.

**Tech Stack:** Go, vendored `github.com/digitalocean/go-qemu/qmp` direct socket monitor subset, `encoding/json`, `context`, table-driven unit tests, fake monitor/factory tests, optional gated QMP integration tests.

**Official Documentation Evidence:**
- `ctx7 library github.com/digitalocean/go-qemu "How to use the QMP client to connect, negotiate capabilities, query status, send system_powerdown and quit commands, and receive events"` resolved `/digitalocean/go-qemu` with High source reputation.
- `ctx7 docs /digitalocean/go-qemu "How to use the QMP client to connect, negotiate capabilities, query status, send system_powerdown and quit commands, and receive events"` showed `NewSocketMonitor`, `Connect`, `Run`, `Events`, `query-status`, and event stream examples.
- Local isolated module under `.tmp/go-qemu-doc` ran `go doc github.com/digitalocean/go-qemu/qmp`, `go doc github.com/digitalocean/go-qemu/qmp.Monitor`, `go doc github.com/digitalocean/go-qemu/qmp.NewSocketMonitor`, `go doc github.com/digitalocean/go-qemu/qmp.SocketMonitor.Connect`, `go doc github.com/digitalocean/go-qemu/qmp.SocketMonitor.Run`, and `go doc github.com/digitalocean/go-qemu/qmp.SocketMonitor.Events` against `github.com/digitalocean/go-qemu v0.0.0-20250212194115-ee9b0668d242`.
- Verified API facts: `Monitor` has `Connect()`, `Disconnect()`, `Run([]byte)`, and `Events(context.Context)`; `NewSocketMonitor(network, addr, timeout)` creates a socket monitor; `SocketMonitor.Connect()` performs the QMP capabilities handshake; `SocketMonitor.Events()` should only be called once per socket.

---

## File Structure

- Do not keep `github.com/digitalocean/go-qemu` in `go.mod`; importing its `qmp` package also compiles libvirt RPC code and violates this project's no-libvirt rule.
- Replace: `internal/virt/qmp/client.go` — root client interface, `SocketClient`, constructor, lifecycle, and operation facade.
- Replace: `internal/virt/qmp/client_test.go` — root facade tests using fake monitor factory.
- Create: `internal/virt/qmp/types.go` — project-owned `Config`, `State`, `Status`, `EventName`, `Event`, and command constants.
- Create: `internal/virt/qmp/errors.go` — public sentinel errors for invalid config, not connected, and duplicate event subscription.
- Create: `internal/virt/qmp/internal/monitor/monitor.go` — project-owned internal `Monitor` and `Factory` interfaces.
- Create: `internal/virt/qmp/internal/goqemu/socket.go` — vendored direct-socket QMP monitor subset adapted from go-qemu.
- Create: `internal/virt/qmp/internal/monitor/goqemu.go` — adapter from the vendored socket monitor to Govirta's internal `monitor.Monitor`.
- Create: `internal/virt/qmp/internal/status/status.go` and `status_test.go` — `query-status` command and response parsing.
- Create: `internal/virt/qmp/internal/power/power.go` and `power_test.go` — `system_powerdown` and `quit` command execution.
- Create: `internal/virt/qmp/internal/events/events.go` and `events_test.go` — event conversion and filtering.
- Create: `internal/virt/qmp/integration_test.go` — optional gated live-QMP integration tests.
- Create: `internal/virt/qmp/AGENTS.md` — package knowledge base and flow documentation after implementation is stable.

## Public API Shape

Implement the root package as the only caller-facing QMP boundary:

```go
package qmp

import (
    "context"
    "time"
)

// Client defines the project-owned QMP protocol boundary.
type Client interface {
    Name() string
    Connect(ctx context.Context) error
    Disconnect(ctx context.Context) error
    QueryStatus(ctx context.Context) (Status, error)
    WaitReady(ctx context.Context) error
    SystemPowerdown(ctx context.Context) error
    Quit(ctx context.Context) error
    Events(ctx context.Context, names ...EventName) (<-chan Event, error)
}

// Config configures a socket-backed QMP client.
type Config struct {
    SocketPath string
    Timeout    time.Duration
}

// Status is the typed result of QMP query-status.
type Status struct {
    Running    bool
    Singlestep bool
    State      State
}

// State is the QEMU run-state returned by query-status.
type State string

// EventName is a QMP event name.
type EventName string

// Event is the project-owned QMP event representation.
type Event struct {
    Name      EventName
    Data      map[string]any
    Timestamp time.Time
}
```

Required constants:

```go
const (
    DefaultTimeout = 2 * time.Second

    StateRunning   State = "running"
    StatePaused    State = "paused"
    StateShutdown  State = "shutdown"
    StatePrelaunch State = "prelaunch"
    StateInMigrate State = "inmigrate"

    EventShutdown EventName = "SHUTDOWN"
    EventReset    EventName = "RESET"
    EventStop     EventName = "STOP"

    commandQueryStatus     commandName = "query-status"
    commandSystemPowerdown commandName = "system_powerdown"
    commandQuit            commandName = "quit"
)
```

Notes:
- Use an unexported `commandName` type unless tests require exported command constants. Do not expose a generic `RunCommand` API.
- Do not preserve the current `Connect(ctx, socketPath string)` shape. This is an internal fast-iteration package; move `SocketPath` to `Config`.
- `WaitReady(ctx)` means QMP socket connection plus capabilities handshake succeeded. This confirms QEMU's monitor is ready, not that the guest OS has booted.

## Task 1: Vendored socket monitor and root API tests

**Files:**
- Create: `internal/virt/qmp/internal/goqemu/socket.go`
- Create: `internal/virt/qmp/internal/goqemu/socket_test.go`
- Replace: `internal/virt/qmp/client_test.go`
- Create: `internal/virt/qmp/types.go`
- Create: `internal/virt/qmp/errors.go`

- [ ] **Step 1: Vendor direct socket QMP subset**

Create a small `internal/virt/qmp/internal/goqemu` package adapted from the direct socket monitor in `github.com/digitalocean/go-qemu/qmp` at `v0.0.0-20250212194115-ee9b0668d242`. Include source attribution and Apache-2.0 license note. Do not copy libvirt RPC files or import `github.com/digitalocean/go-qemu/qmp` directly; the upstream package compiles `rpc.go`, which imports `github.com/digitalocean/go-libvirt`.

- [ ] **Step 2: Write root facade tests first**

Replace `internal/virt/qmp/client_test.go` with tests that assert:
- `NewSocketClient(Config{})` returns `ErrInvalidConfig` for empty socket path.
- `NewSocketClient(Config{SocketPath: "vm.qmp"})` applies `DefaultTimeout`.
- `Name()` returns a stable constant such as `qmp-socket`.
- `Connect(ctx)` calls the injected factory with network `unix`, configured socket path, and timeout.
- `Connect(ctx)` stores a connected monitor after the factory and monitor handshake succeed.
- `WaitReady(ctx)` delegates to `Connect(ctx)` and returns handshake errors.
- `QueryStatus`, `SystemPowerdown`, `Quit`, and `Events` return `ErrNotConnected` before `Connect`.
- `Events` can be started only once; the second call returns `ErrEventsAlreadyStarted`.
- `Disconnect(ctx)` is idempotent or returns a documented sentinel. Prefer idempotent no-op on repeated disconnect for caller cleanup simplicity.

Use a package-private test helper to inject a fake factory into `SocketClient`. Keep the injection unexported so production callers cannot bypass the public constructor.

- [ ] **Step 3: Implement project-owned types and errors**

Create `types.go` and `errors.go` with Go doc comments on all exported identifiers. Keep comments concise and caller-oriented.

Required errors:

```go
var (
    ErrInvalidConfig        = errors.New("invalid qmp config")
    ErrNotConnected         = errors.New("qmp client is not connected")
    ErrEventsAlreadyStarted = errors.New("qmp events already started")
)
```

Validation failures should wrap `ErrInvalidConfig` with detail text so callers can use `errors.Is`.

## Task 2: Internal monitor adapter

**Files:**
- Create: `internal/virt/qmp/internal/monitor/monitor.go`
- Create: `internal/virt/qmp/internal/monitor/goqemu.go`
- Create: `internal/virt/qmp/internal/monitor/monitor_test.go`

- [ ] **Step 1: Define project-owned internal monitor interfaces**

`monitor.Monitor` should mirror only the operations needed by Govirta:

```go
type Monitor interface {
    Connect() error
    Disconnect() error
    Run(command []byte) ([]byte, error)
    Events(ctx context.Context) (<-chan Event, error)
}

type Event struct {
    Name         string
    Data         map[string]any
    Seconds      int64
    Microseconds int64
}

type Factory interface {
    New(network string, address string, timeout time.Duration) (Monitor, error)
}
```

Do not use `github.com/digitalocean/go-qemu/qmp.Event` or `qmp.Monitor` outside `goqemu.go`.

- [ ] **Step 2: Implement go-qemu adapter**

`goqemu.go` should:
- import `github.com/suknna/govirta/internal/virt/qmp/internal/goqemu` as an internal implementation detail;
- call `goqemu.NewSocketMonitor(network, address, timeout)`;
- adapt `go-qemu` events to `monitor.Event`;
- pass through `Connect`, `Disconnect`, and `Run` errors without string matching.

- [ ] **Step 3: Test adapter boundaries without a live QMP socket**

Unit tests should not dial a real socket. Use compile-time assertions and small adapter tests around event conversion helpers. Do not require QEMU binaries.

## Task 3: Status command subpackage

**Files:**
- Create: `internal/virt/qmp/internal/status/status.go`
- Create: `internal/virt/qmp/internal/status/status_test.go`

- [ ] **Step 1: Write failing command and parser tests**

Cover:
- command JSON for `query-status` uses the constant value, not a bare string;
- successful response parses `running`, `singlestep`, and `status`;
- unknown status strings are preserved as `State(raw)` instead of rejected;
- malformed JSON returns an error;
- QMP error responses return an error.

- [ ] **Step 2: Implement status execution**

Provide a package function with no dependency on root `qmp` to avoid import cycles. Prefer internal value types plus root aliases if necessary, or keep shared protocol types in one direction only.

Shape:

```go
func Query(ctx context.Context, mon monitor.Monitor) (Result, error)
```

Check `ctx.Err()` before calling `mon.Run`. Do not claim command-level cancellation after `Run` begins because `go-qemu` `Run` has no context parameter.

## Task 4: Power command subpackage

**Files:**
- Create: `internal/virt/qmp/internal/power/power.go`
- Create: `internal/virt/qmp/internal/power/power_test.go`

- [ ] **Step 1: Write failing tests for power commands**

Cover:
- `SystemPowerdown(ctx, mon)` sends `system_powerdown`;
- `Quit(ctx, mon)` sends `quit`;
- canceled context avoids calling `mon.Run`;
- monitor errors are returned to the caller.

- [ ] **Step 2: Implement command helpers**

Keep command names in typed constants. This package should not expose a generic command runner.

## Task 5: Events subpackage and lifecycle control

**Files:**
- Create: `internal/virt/qmp/internal/events/events.go`
- Create: `internal/virt/qmp/internal/events/events_test.go`
- Modify: `internal/virt/qmp/client.go`

- [ ] **Step 1: Write failing event conversion/filter tests**

Cover:
- `SHUTDOWN`, `RESET`, and `STOP` constants are preserved in uppercase QMP form;
- filter with selected names only emits matching events;
- no filter means all events are forwarded;
- empty or nil event data is handled consistently;
- QMP timestamp seconds/microseconds converts to `time.Time` without losing microsecond precision.

- [ ] **Step 2: Implement event conversion and filtering**

The internal events package may accept `monitor.Event` and return project-owned event values. Avoid exposing `go-qemu` event types.

- [ ] **Step 3: Enforce single event stream in `SocketClient`**

Because `go-qemu` documents `SocketMonitor.Events` as single-use per socket, `SocketClient.Events` must guard with a mutex/state flag or `sync.Once` plus explicit error state. Prefer a mutex/state flag so duplicate calls can return `ErrEventsAlreadyStarted` deterministically.

## Task 6: Root client implementation

**Files:**
- Replace: `internal/virt/qmp/client.go`
- Update: `internal/virt/qmp/client_test.go`

- [ ] **Step 1: Implement `NewSocketClient` and lifecycle methods**

Rules:
- `SocketPath` is required.
- empty `Timeout` uses `DefaultTimeout`.
- network is fixed to `unix` for this phase.
- `Connect(ctx)` checks `ctx.Err()` before dialing and before handshake if possible.
- `Connect(ctx)` calls factory then monitor `Connect()`; a successful `Connect()` means QMP capabilities negotiation completed.
- `Disconnect(ctx)` checks context before cleanup, then calls monitor `Disconnect()` if connected.
- `Disconnect` should clear the stored monitor so later operations return `ErrNotConnected`.

- [ ] **Step 2: Wire operation methods**

Implement:
- `WaitReady(ctx)` as `Connect(ctx)` unless already connected;
- `QueryStatus(ctx)` via internal status package;
- `SystemPowerdown(ctx)` via internal power package;
- `Quit(ctx)` via internal power package;
- `Events(ctx, names...)` via internal events package.

- [ ] **Step 3: Preserve no-op only if still useful**

If existing node skeleton tests require a no-op implementation, keep `NoopClient` updated to the expanded `Client` interface with documented no-op behavior. Otherwise remove it and update node construction in a later runtime integration plan. Do not keep stale methods solely for backward compatibility.

## Task 7: Optional gated integration test

**Files:**
- Create: `internal/virt/qmp/integration_test.go`

- [ ] **Step 1: Add environment-gated test skeleton**

Skip unless `GOVIRTA_QMP_INTEGRATION=1` is set.

Use env vars:
- `GOVIRTA_QMP_SOCKET` for an already-running QMP unix socket;
- optional `GOVIRTA_QMP_EXPECT_STATE` for expected state.

The test should connect, query status, optionally subscribe to events, and disconnect. Do not start QEMU in this test unless a later runtime package owns process lifecycle.

- [ ] **Step 2: Document remote acceptance path**

Add comments explaining that full VM acceptance still belongs to the QEMU runtime path: start CirrOS with a pre-created TAP, connect to QMP, query status, then use `SystemPowerdown` or `Quit`.

## Task 8: Package knowledge base

**Files:**
- Create: `internal/virt/qmp/AGENTS.md`
- Modify: `internal/virt/AGENTS.md`

- [ ] **Step 1: Create `internal/virt/qmp/AGENTS.md`**

Document:
- root `qmp` as the only public API;
- go-qemu isolation in `internal/monitor/goqemu.go`;
- call flow: `SocketClient.Connect -> monitor.Factory.New -> go-qemu NewSocketMonitor -> Connect capabilities handshake`;
- command flow: `QueryStatus/SystemPowerdown/Quit -> internal command packages -> monitor.Run`;
- event flow: `SocketClient.Events -> monitor.Events once -> internal/events filter -> qmp.Event`;
- anti-patterns: no libvirt, no raw go-qemu types in public API, no multiple event streams per socket.

- [ ] **Step 2: Update `internal/virt/AGENTS.md`**

Replace the no-op QMP description with a link to `qmp/AGENTS.md` and update the verified file list after implementation.

## Verification

Run after implementation:

```bash
gofmt -w internal/virt/qmp
go test ./internal/virt/qmp/...
go test ./internal/virt/...
go test ./...
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl
```

If QMP changes become concurrency-sensitive, also run:

```bash
go test -race ./internal/virt/qmp/...
```

Optional live QMP verification when a QEMU monitor socket exists:

```bash
GOVIRTA_QMP_INTEGRATION=1 GOVIRTA_QMP_SOCKET=.tmp/qemu/cirros/qmp.sock go test ./internal/virt/qmp -run TestIntegration
```

## Risks and Guardrails

- `go-qemu` command execution has no context parameter. Check `ctx.Err()` before calls, use dial timeout for connection, and avoid claiming mid-command cancellation.
- `Events` is single-use per socket. Enforce this in `SocketClient` and test duplicate calls.
- Do not expose `github.com/digitalocean/go-qemu/qmp` types in public root package APIs.
- Do not import or use go-qemu libvirt/RPC monitor types; Govirta permanently excludes libvirt.
- Do not make `CommandName` a caller-extensible raw QMP API. Keep typed methods for the current required operations.
- Unit tests must not require a real QEMU binary, TAP device, or live QMP socket.
