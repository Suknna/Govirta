# internal/virt/qmp Knowledge Base

**Generated:** 2026-05-24

## OVERVIEW

Project-owned QMP boundary. Callers import only `internal/virt/qmp`; raw protocol transport, command JSON, and event conversion stay under `internal/virt/qmp/internal/*`.

The direct socket monitor is vendored under `internal/goqemu` from `github.com/digitalocean/go-qemu/qmp` at `v0.0.0-20250212194115-ee9b0668d242`. Do not import the upstream `qmp` package directly: its package directory also compiles libvirt RPC code and would introduce `github.com/digitalocean/go-libvirt`, which Govirta permanently forbids.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Public QMP API | `client.go` | `Client`, `SocketClient`, lifecycle and operation facade |
| Public value types | `types.go` | `Config`, `Status`, `State`, `EventName`, `Event` |
| Error classes | `errors.go` | `ErrInvalidConfig`, `ErrNotConnected`, `ErrEventsAlreadyStarted` |
| QMP protocol errors | `internal/protocol/` + root alias | `ResponseError{Class, Description}` preserves QMP error class and desc |
| Transport boundary | `internal/monitor/` | Project-owned `Monitor`/`Factory` and adapter to vendored socket monitor |
| Vendored go-qemu subset | `internal/goqemu/socket.go` | Direct socket QMP greeting, capabilities handshake, command run, event stream |
| Status command | `internal/status/` | `query-status` command construction and response parsing |
| Power commands | `internal/power/` | `system_powerdown` and `quit` helpers |
| Event filtering | `internal/events/` | monitor event conversion and filter stream |
| Gated live QMP test | `integration_test.go` | Requires `GOVIRTA_QMP_INTEGRATION=1` and `GOVIRTA_QMP_SOCKET` |

## CONVENTIONS

- Upper layers must depend on the root `qmp.Client` interface, not internal subpackages.
- `SocketClient.Connect(ctx)` means the QMP unix socket was dialed and the capabilities handshake completed. Duplicate connects return `ErrAlreadyConnected`; failed connects close the created monitor before returning.
- `Disconnect(ctx)` always attempts to release the monitor before returning context cancellation.
- `WaitReady(ctx)` confirms QMP monitor readiness, not guest OS boot completion.
- `Events(ctx, ...)` is single-use per connected socket because the underlying go-qemu socket monitor documents event streams as single-use.
- Internal monitor `Run(ctx, command)` is context-aware; command callers must pass caller context rather than blocking indefinitely on a live socket.
- QMP command names are typed constants; do not expose a raw `RunCommand` API to callers.
- Preserve unknown `query-status` states as `State(raw)` instead of rejecting future QEMU values.

## ANTI-PATTERNS

- Do not import `github.com/digitalocean/go-qemu/qmp` directly in production code.
- Do not import `github.com/digitalocean/go-libvirt` or any libvirt-derived package.
- Do not expose `internal/goqemu` or `internal/monitor` types in root `qmp` public APIs.
- Do not call `Events` more than once for the same connected socket.
- Do not make unit tests depend on a real QEMU binary, TAP device, or live QMP socket.

## CALL GRAPHS & DATA FLOW

### Flow: socket QMP readiness {#flow-qmp-ready}

- Entry: `qmp.NewSocketClient(Config{SocketPath, Timeout})`
- Local chain:
  1. `SocketClient.Connect(ctx)` checks context cancellation.
  2. `internal/monitor.GoQEMUFactory.New("unix", socketPath, timeout)` creates the vendored socket monitor.
  3. `internal/goqemu.NewSocketMonitor` dials the unix socket.
  4. `SocketMonitor.Connect(ctx)` checks cancellation, reads the QMP greeting, sends `qmp_capabilities`, validates the response, and starts the listener goroutine.
- Exit: successful return means QMP can accept commands.

### Flow: status and power commands {#flow-qmp-commands}

- `SocketClient.QueryStatus(ctx)` -> `internal/status.Query(ctx, monitor)` -> JSON `query-status` -> `monitor.Run(ctx, ...)` -> typed `Status`.
- `SocketClient.SystemPowerdown(ctx)` -> `internal/power.SystemPowerdown(ctx, monitor)` -> JSON `system_powerdown` -> `monitor.Run(ctx, ...)`.
- `SocketClient.Quit(ctx)` -> `internal/power.Quit(ctx, monitor)` -> JSON `quit` -> `monitor.Run(ctx, ...)`.

### Flow: event stream {#flow-qmp-events}

- `SocketClient.Events(ctx, names...)` checks connection and duplicate-start state.
- `monitor.Events(ctx)` starts the underlying event stream.
- `internal/events.Stream` converts timestamps, applies optional filters, and returns root `qmp.Event` values.

## NOTES

- The upstream go-qemu source is Apache-2.0, matching this repository license. Vendored files retain attribution comments.
- `go test ./internal/virt/qmp/...` covers unit behavior without live QEMU. The live QMP test is opt-in through environment variables.
