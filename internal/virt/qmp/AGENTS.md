# internal/virt/qmp Knowledge Base

**Generated:** 2026-05-28

<!--
Verified-against:
  base_commit: 6c06c5f
  files:
    - internal/virt/qmp/client.go
    - internal/virt/qmp/types.go
    - internal/virt/qmp/errors.go
    - internal/virt/qmp/integration_test.go
    - internal/virt/qmp/internal/monitor/monitor.go
    - internal/virt/qmp/internal/monitor/goqemu.go
    - internal/virt/qmp/internal/goqemu/socket.go
    - internal/virt/qmp/internal/status/status.go
    - internal/virt/qmp/internal/power/power.go
    - internal/virt/qmp/internal/events/events.go
    - internal/virt/qmp/internal/protocol/error.go
  flows:
    - anchor: flow-qmp-ready
      sources:
        - internal/virt/qmp/client.go
        - internal/virt/qmp/internal/monitor/goqemu.go
        - internal/virt/qmp/internal/goqemu/socket.go
    - anchor: flow-qmp-commands
      sources:
        - internal/virt/qmp/client.go
        - internal/virt/qmp/internal/status/status.go
        - internal/virt/qmp/internal/power/power.go
        - internal/virt/qmp/internal/goqemu/socket.go
    - anchor: flow-qmp-events
      sources:
        - internal/virt/qmp/client.go
        - internal/virt/qmp/internal/events/events.go
        - internal/virt/qmp/internal/goqemu/socket.go
-->

## OVERVIEW

Project-owned QMP boundary. Callers import only `internal/virt/qmp`; raw protocol transport, command JSON, and event conversion stay under `internal/virt/qmp/internal/*`.

The direct socket monitor is vendored under `internal/goqemu` from `github.com/digitalocean/go-qemu/qmp` at `v0.0.0-20250212194115-ee9b0668d242`. Do not import the upstream `qmp` package directly: its package directory also compiles libvirt RPC code and would introduce `github.com/digitalocean/go-libvirt`, which Govirta permanently forbids.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Public QMP API | `client.go` | `Client`, `SocketClient`, lifecycle and operation facade |
| Public value types | `types.go` | `Config`, `Status`, `State`, `EventName`, `Event` |
| Error classes | `errors.go` | `ErrInvalidConfig`, `ErrAlreadyConnected`, `ErrNotConnected`, `ErrEventsAlreadyStarted` |
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
- `lifecycleMu` serializes `Connect`, `Disconnect`, and `WaitReady`; ordinary command/event methods use `mu` and do not take the lifecycle lock.
- `Disconnect(ctx)` cancels any connection-owned event stream before disconnecting the monitor. With no active monitor it returns `ctx.Err()` so caller cancellation remains visible.
- `WaitReady(ctx)` confirms QMP monitor readiness, not guest OS boot completion.
- `Events(ctx, ...)` is single-use per connected socket because the underlying go-qemu socket monitor documents event streams as single-use.
- Direct socket monitor command writes are context-aware and use temporary deadlines so caller cancellation can unblock stuck socket writes.
- QMP command names are typed constants; do not expose a raw `RunCommand` API to callers.
- Preserve unknown `query-status` states as `State(raw)` instead of rejecting future QEMU values.

## ANTI-PATTERNS

- Do not import `github.com/digitalocean/go-qemu/qmp` directly in production code.
- Do not import `github.com/digitalocean/go-libvirt` or any libvirt-derived package.
- Do not expose `internal/goqemu` or `internal/monitor` types in root `qmp` public APIs.
- Do not call `Events` more than once for the same connected socket.
- Do not make unit tests depend on a real QEMU binary, TAP device, or live QMP socket.

## CALL GRAPHS & DATA FLOW (LOCAL)

### Flow: socket QMP readiness {#flow-qmp-ready}

- Entry from root flow: `internal/virt/qmp/client.go:52 (NewSocketClient)` / `:76 (SocketClient.Connect)` — future node runtime path from root `#flow-govirtlet-boot`
- Local chain:
  1. `internal/virt/qmp/client.go:57 (newSocketClient)` — validate `Config.SocketPath`, monitor factory, default timeout
  2. `internal/virt/qmp/client.go:81 (SocketClient.Connect)` — check context, take `lifecycleMu`, reject duplicate connection
  3. `internal/virt/qmp/client.go:95 (SocketClient.Connect)` — call `factory.New("unix", socketPath, timeout)`
  4. `internal/virt/qmp/internal/monitor/goqemu.go:14 (GoQEMUFactory.New)` — wrap vendored socket monitor
  5. `internal/virt/qmp/internal/goqemu/socket.go:93 (NewSocketMonitor)` — dial QMP unix socket
  6. `internal/virt/qmp/internal/goqemu/socket.go:108 (SocketMonitor.Connect)` — read greeting, send `qmp_capabilities`, start listener
  7. `internal/virt/qmp/client.go:120 (SocketClient.Connect)` — install connected monitor and reset event state
- Data (within module): `qmp.Config{SocketPath,Timeout}` → `monitor.Monitor` → installed `SocketClient.monitor`
- Side effects (within module): opens unix socket; writes QMP capabilities command; starts monitor listener goroutine owned by monitor
- Exit / next hop: root `qmp.Client` is ready for `QueryStatus`, `SystemPowerdown`, `Quit`, or `Events`

### Flow: status and power commands {#flow-qmp-commands}

- Entry from root flow: `internal/virt/qmp/client.go:163 (SocketClient.QueryStatus)` / `:176 (SystemPowerdown)` / `:185 (Quit)`
- Local chain:
  1. `internal/virt/qmp/client.go:230 (connectedMonitor)` — read active monitor or return `ErrNotConnected`
  2. `internal/virt/qmp/internal/status/status.go:23 (Query)` — marshal `{"execute":"query-status"}` and call monitor
  3. `internal/virt/qmp/internal/power/power.go:18 (SystemPowerdown)` / `:23 (Quit)` — marshal power command and call monitor
  4. `internal/virt/qmp/internal/goqemu/socket.go:210 (SocketMonitor.Run)` — write command JSON, wait for response, decode protocol errors
  5. `internal/virt/qmp/client.go:172 (SocketClient.QueryStatus)` — convert internal status to public `Status`, preserving unknown state text
- Data (within module): typed root method → QMP JSON command → raw QMP response → `qmp.Status` or error
- Side effects (within module): writes commands to QMP socket; `system_powerdown` requests guest shutdown; `quit` asks QEMU process to terminate
- Exit / next hop: caller receives typed status/error or QEMU begins shutdown/exit

### Flow: event stream {#flow-qmp-events}

- Entry from root flow: `internal/virt/qmp/client.go:194 (SocketClient.Events)`
- Local chain:
  1. `internal/virt/qmp/client.go:194 (SocketClient.Events)` — require connected monitor and reject duplicate stream
  2. `internal/virt/qmp/client.go:204 (SocketClient.Events)` — create per-stream child context
  3. `internal/virt/qmp/internal/goqemu/socket.go:295 (SocketMonitor.Events)` — expose underlying QMP event channel
  4. `internal/virt/qmp/internal/events/events.go:18 (Stream)` — convert/filter raw monitor events by event name
  5. `internal/virt/qmp/client.go:250 (convertEvents)` — convert internal event to public `qmp.Event`
  6. `internal/virt/qmp/client.go:140 (Disconnect)` — cancels connection-owned event context on disconnect
- Data (within module): QMP raw event → internal `events.Event` → public `qmp.Event{Name,Data,Timestamp}`
- Side effects (within module): starts conversion goroutines bounded by caller/disconnect context
- Exit / next hop: receive-only public event channel returned to caller

## NOTES

- The upstream go-qemu source is Apache-2.0, matching this repository license. Vendored files retain attribution comments.
- `go test ./internal/virt/qmp/...` covers unit behavior without live QEMU. The live QMP test is opt-in through `GOVIRTA_QMP_INTEGRATION=1` and `GOVIRTA_QMP_SOCKET`.
- Evidence: direct source reads + read-only entry-flow subagent; LSP call hierarchy not used end-to-end. `[已验证]` / `[降级: LSP call hierarchy]`
