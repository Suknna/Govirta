# CoreDHCP-backed DHCP Wrapper Design

## Summary

Govirta needs a compute-node-internal DHCP capability so a future control plane can assign an explicit IPv4 address to a VM NIC and have the node answer that guest's DHCP requests. The first version wraps `github.com/coredhcp/coredhcp` behind a Govirta-owned interface under `internal/hostnet/dhcp`; upper layers must not depend on CoreDHCP types.

The DHCP capability is not an external daemon and is not the lifecycle owner of QEMU, TAP, bridge, route, NAT, firewall, or guest operating system state. It is an in-process `govirtlet`/compute-node component: one DHCP instance per explicit address segment, with process-memory MAC-to-IP bindings supplied by the upper layer. Node restart clears the in-memory bindings; recovery is performed by replaying control-plane bindings into the restarted node. Replay only rebuilds the DHCP responder table and must not touch running QEMU processes or their network dataplane.

## Goals

- Add a stable Govirta DHCP boundary under `internal/hostnet/dhcp`.
- Use CoreDHCP as the internal DHCP engine while hiding CoreDHCP implementation details from upper layers.
- Support one DHCP instance per explicit IPv4 address segment.
- Support node-internal start/stop of a DHCP instance.
- Support explicit MAC-to-IPv4 binding application and removal.
- Keep lease/binding state in process memory only for the first version.
- Preserve already-running QEMU guests during node process restart and DHCP binding replay.
- Verify the implementation with Lima acceptance: CirrOS obtains the bound IP through the Govirta DHCP wrapper and host-to-guest ping succeeds.

## Non-goals

- No standalone DHCP daemon managed outside the compute node process.
- No automatic IP allocation from a pool in the first version.
- No DHCPv6.
- No disk lease file, metadata database, etcd integration, or cross-process persistence in the first version.
- No `ReplaceBinding` or implicit running-guest IP migration in the first version.
- No bridge/TAP creation in the DHCP package.
- No QEMU process lifecycle management in the DHCP package.
- No route, NAT, firewall, DNS forwarding, or IPv4 forwarding mutation.
- No guest OS command execution from the DHCP package.
- No CoreDHCP type exposure in root `internal/hostnet/dhcp` APIs.

## Architecture

The package layout should be:

```text
internal/hostnet/dhcp/
├── dhcp.go              # Manager interface and public request/result types
├── constants.go         # typed constants and explicit option wrappers
├── noop.go              # unsupported/no-op implementation for composition tests
├── noop_test.go
├── dhcperr/
│   └── errors.go        # stable DHCP sentinel errors
└── coredhcp/
    ├── manager.go       # CoreDHCP-backed Manager implementation
    ├── runtime.go       # per-server runtime state and binding indexes
    ├── handler.go       # Govirta-owned DHCPv4 handler
    ├── validate.go      # explicit request validation
    ├── info.go          # ServerInfo/LeaseInfo observation helpers
    ├── errors.go        # CoreDHCP/socket error classification
    └── *_test.go
```

The orchestration call order remains layered:

```text
node orchestration
  -> hostnet/link       # bridge/TAP/address primitives
  -> hostnet/route      # route lifecycle and IPv4 forwarding readiness checks
  -> hostnet/firewall   # future NAT/filtering primitives
  -> hostnet/dhcp       # in-process DHCP responders for explicit bindings
  -> virt/qemu          # argv builder consumes an already-created TAP
```

The DHCP package does not own any QEMU or host link lifecycle. It only answers DHCP requests when a guest DHCP client asks for an address.

## Restart and replay model

Govirta already requires QEMU process lifecycle to be decoupled from the orchestrator process lifecycle. DHCP follows the same principle:

1. A `govirtlet` crash or restart must not terminate, restart, or reconfigure an already-running QEMU process.
2. Existing TAP/bridge dataplane state remains independent of the DHCP manager process.
3. On node restart, the DHCP manager starts empty.
4. The upper layer replays explicit DHCP instances and MAC-to-IP bindings.
5. Replay updates only the in-memory DHCP responder table.
6. Replay must not mutate QEMU, TAP, bridge, route, NAT, firewall, or guest state.

`ApplyBinding` is therefore an idempotent create/confirm operation, not reconciliation of the guest network configuration. A replayed binding affects a running guest only when that guest later sends a DHCP renew/request and receives the same ACK it would have received before the node restart.

Unknown or conflicting DHCP requests must be handled conservatively: do not allocate a fallback address, do not infer old state, and do not send DHCPNAK in the first version. Silence is preferred over a destructive protocol response because DHCPNAK can cause a guest to discard an already-working address.

## Public API shape

The root package exposes a Govirta-owned interface:

```go
type Manager interface {
    Start(ctx context.Context, spec ServerSpec) (ServerInfo, error)
    Stop(ctx context.Context, id ServerID) error

    ApplyBinding(ctx context.Context, req BindingRequest) (LeaseInfo, error)
    RemoveBinding(ctx context.Context, query BindingQuery) error

    GetServer(ctx context.Context, id ServerID) (ServerInfo, error)
    GetLease(ctx context.Context, query BindingQuery) (LeaseInfo, error)
    ListLeases(ctx context.Context, filter LeaseFilter) ([]LeaseInfo, error)
}
```

The first version intentionally omits `ReplaceBinding`. Changing a MAC from one IP to another is behavior-affecting for the guest's next DHCP renew and should be introduced only as an explicit later operation.

## Explicit server identity

`ServerID` identifies one DHCP instance:

```go
type ServerID string
```

Rules:

- The caller must pass `ServerID` explicitly.
- Empty `ServerID` is invalid.
- The DHCP package must not generate IDs.
- The DHCP package must not infer an ID from bridge name or subnet.

Upper layers may choose names such as `gvbr0-192.168.100.0-24`, but that naming policy remains outside the DHCP package.

## Server configuration

`ServerSpec` carries every behavior-affecting value explicitly:

```go
type ServerSpec struct {
    ID            ServerID
    InterfaceName link.Name

    ListenAddr netip.Addr
    ListenPort Port

    ServerAddr netip.Addr
    Subnet     netip.Prefix
    Pool       AddressRange

    LeaseDuration time.Duration

    Router        DHCPOptionAddrs
    DNS           DHCPOptionAddrs
    BindMode      BindMode
}
```

Rules:

- `InterfaceName` is the host bridge/interface that receives guest DHCP traffic, for example `gvbr0`.
- `ListenAddr` is explicit. The acceptance test may choose `0.0.0.0` or the bridge address, but the DHCP package must not choose for the caller.
- `ListenPort` is explicit, including the standard DHCP server port `67`.
- `ServerAddr` is the DHCP server identifier address and commonly the bridge gateway address.
- `Subnet` and `Pool` are explicit IPv4 values.
- `LeaseDuration` must be positive.
- `Router` and `DNS` use explicit option modes so nil/empty values are never treated as defaults.
- `BindMode` is a typed explicit mode for future Linux binding choices. Empty mode is invalid.

`Port` and `BindMode` should use dedicated custom types and constants, not raw primitive state in API contracts.

The first implementation must make interface binding capability explicit. If CoreDHCP or the selected socket path cannot safely isolate multiple listeners on the same DHCP port by interface, the implementation must reject that bind mode with `dhcperr.ErrUnsupported` instead of silently falling back to process-wide `0.0.0.0:67` behavior. Multi-instance support must never depend on hidden socket defaults.

## Explicit DHCP options

Router and DNS options use an explicit wrapper:

```go
type DHCPOptionAddrs struct {
    Mode  DHCPOptionMode
    Addrs []netip.Addr
}

type DHCPOptionMode string

const (
    DHCPOptionDisabled DHCPOptionMode = "disabled"
    DHCPOptionEnabled  DHCPOptionMode = "enabled"
)
```

Rules:

- Empty `Mode` is invalid.
- `DHCPOptionDisabled` means do not send that option; `Addrs` must be empty.
- `DHCPOptionEnabled` means send that option; `Addrs` must contain at least one IPv4 address.
- The DHCP package must not infer router or DNS values from `ServerAddr`, `Subnet`, bridge state, or host resolver config.

## Address range

The first version supports IPv4 ranges only:

```go
type AddressRange struct {
    Start netip.Addr
    End   netip.Addr
}
```

Validation:

- `Start` and `End` must be IPv4 addresses.
- Both addresses must be inside `ServerSpec.Subnet`.
- `Start` must be less than or equal to `End`.
- `BindingRequest.IP` must be inside this range.

## Binding model

Bindings are explicitly supplied by the upper layer:

```go
type BindingRequest struct {
    ServerID ServerID
    MAC      net.HardwareAddr
    IP       netip.Addr
    Hostname BindingHostname
}

type BindingQuery struct {
    ServerID ServerID
    MAC      net.HardwareAddr
}

type BindingHostname struct {
    Value string
    Set   bool
}
```

Rules:

- `ServerID` must refer to a started DHCP instance.
- `MAC` must be a valid unicast hardware address.
- `IP` must be an IPv4 address inside the instance pool.
- `Hostname.Set=false` means no hostname was supplied.
- `Hostname.Set=true` requires a non-empty valid hostname value.
- The DHCP package must not auto-assign an IP from the pool.

`ApplyBinding` idempotency and conflict rules:

| Current state | Request | Result |
| --- | --- | --- |
| No binding | MAC A -> IP X | Create `LeaseStateReserved` |
| MAC A -> IP X exists | MAC A -> IP X | Idempotent success |
| MAC A -> IP X exists | MAC A -> IP Y | `dhcperr.ErrConflict`; old binding unchanged |
| MAC A -> IP X exists | MAC B -> IP X | `dhcperr.ErrConflict`; old binding unchanged |
| IP outside pool | Any binding | `dhcperr.ErrInvalidRequest` |
| Server missing | Any binding | `dhcperr.ErrNotFound` |

`RemoveBinding` deletes the in-memory binding. It must not send DHCPNAK, notify the guest, change QEMU, change TAP/bridge, or flush any address. If the guest already has the address configured, the immediate dataplane remains unchanged; the guest simply will not receive future ACKs for that binding.

## Observed state

`ServerInfo` and `LeaseInfo` describe current in-process runtime state:

```go
type ServerInfo struct {
    ID            ServerID
    InterfaceName link.Name
    ListenAddr    netip.Addr
    ListenPort    Port
    ServerAddr    netip.Addr
    Subnet        netip.Prefix
    Pool          AddressRange
    State         ServerState
    LeaseCount    int
}

type LeaseInfo struct {
    ServerID  ServerID
    MAC       net.HardwareAddr
    IP        netip.Addr
    Hostname  BindingHostname
    State     LeaseState
    ExpiresAt time.Time
}
```

State values use custom typed constants:

```go
type ServerState string

const (
    ServerStateStarting ServerState = "starting"
    ServerStateReady    ServerState = "ready"
    ServerStateStopping ServerState = "stopping"
    ServerStateStopped  ServerState = "stopped"
)

type LeaseState string

const (
    LeaseStateReserved LeaseState = "reserved"
    LeaseStateBound    LeaseState = "bound"
)
```

`LeaseStateReserved` means the upper layer has applied a binding but the DHCP handler has not yet ACKed a request for it. `LeaseStateBound` means the handler has ACKed a guest request. `ExpiresAt` remains the zero `time.Time{}` for reserved bindings and becomes `ackTime + LeaseDuration` for bound leases.

## CoreDHCP wrapping strategy

The CoreDHCP-backed implementation maintains multiple independent runtimes:

```go
type Manager struct {
    mu      sync.RWMutex
    servers map[dhcp.ServerID]*serverRuntime
}

type serverRuntime struct {
    mu sync.RWMutex

    spec  dhcp.ServerSpec
    state dhcp.ServerState

    bindingsByMAC map[string]*leaseRecord
    bindingsByIP  map[netip.Addr]*leaseRecord

    coreServers coreServers
}
```

The implementation should introduce a narrow internal seam so unit tests do not need real UDP listeners:

```go
type coreServerStarter interface {
    Start(config *config.Config) (coreServers, error)
}

type coreServers interface {
    Close()
    Wait() error
}
```

Production code delegates to `github.com/coredhcp/coredhcp/server.Start`. Unit tests can inject fakes for start, close, and wait behavior.

CoreDHCP's built-in `file` plugin is not the core of this implementation because package-level global plugin state is a poor fit for multiple per-segment Govirta DHCP instances. Govirta should provide its own DHCPv4 handler that closes over one `serverRuntime` instance.

## Start flow

`Start(ctx, spec)` performs:

```text
validate context
validate every explicit ServerSpec field
take Manager lock
reject duplicate ServerID
create serverRuntime in Starting state
construct CoreDHCP config
inject Govirta-owned DHCPv4 handler
call CoreDHCP starter
record runtime in Manager map
mark runtime Ready
return observed ServerInfo
```

Rules:

- The manager must not write a ready runtime into `servers` before the CoreDHCP listener has started successfully.
- If CoreDHCP partially starts and then returns an error, the implementation must close/wait the partial server and return `errors.Join(startErr, cleanupErr)` when cleanup also fails.
- Start does not apply bindings unless a future API explicitly adds initial bindings. The first version applies bindings through separate `ApplyBinding` calls.

## Stop flow

`Stop(ctx, id)` performs:

```text
validate context
find runtime
mark runtime Stopping
remove or detach runtime from Manager map
call CoreDHCP Close
wait for server goroutines to finish
mark Stopped
return errors, preserving all causes
```

Rules:

- Stop only affects DHCP listeners and in-memory DHCP runtime.
- Stop must not affect QEMU, TAP, bridge, route, NAT, firewall, or guest configuration.
- Missing `ServerID` returns `dhcperr.ErrNotFound`.
- The first version may remove the runtime from the map after stop; callers that require idempotent shutdown should tolerate `ErrNotFound` explicitly.

## DHCP request handling

For DHCPv4:

```text
DHCPDISCOVER / DHCPREQUEST
  -> read client MAC
  -> find binding in runtime.bindingsByMAC
  -> if no binding: do not respond
  -> if binding exists:
       set YourIPAddr to binding IP
       set lease time from ServerSpec.LeaseDuration
       set server identifier from ServerSpec.ServerAddr
       set subnet mask from ServerSpec.Subnet
       set router option only when Router.Mode is enabled
       set DNS option only when DNS.Mode is enabled
       update lease state to Bound on ACK
       update ExpiresAt to now + LeaseDuration on ACK
       return response
```

For unknown MAC or conflicting requested IP, the first version should not send DHCPNAK. It should not respond. This conservative strategy avoids actively disrupting a guest that may already have a working address during node restart or binding replay.

## Concurrency model

The implementation must account for concurrent control-plane replay, guest DHCP requests, and shutdown:

- `Manager.mu` protects the `servers` map.
- Each `serverRuntime.mu` protects bindings and lease state.
- DHCP handler code must read/update runtime state through `serverRuntime.mu`.
- `Stop` marks a runtime stopping before closing the listener.
- Handler calls that observe `ServerStateStopping` or `ServerStateStopped` should not respond.
- The implementation must not hold `Manager.mu` while waiting for CoreDHCP `Wait()`, because that would block unrelated DHCP instances.

## Error model

`internal/hostnet/dhcp/dhcperr/errors.go` defines stable sentinels:

```go
var (
    ErrInvalidRequest       = errors.New("invalid DHCP request")
    ErrNotFound             = errors.New("DHCP resource not found")
    ErrAlreadyExists        = errors.New("DHCP resource already exists")
    ErrAlreadyRunning       = errors.New("DHCP server already running")
    ErrNotRunning           = errors.New("DHCP server not running")
    ErrConflict             = errors.New("DHCP resource conflict")
    ErrPermission           = errors.New("DHCP permission denied")
    ErrUnsupported          = errors.New("DHCP operation unsupported")
    ErrInvalidObservedState = errors.New("invalid observed DHCP state")
)
```

Rules:

- Validation errors wrap `ErrInvalidRequest`.
- Missing servers or bindings wrap `ErrNotFound`.
- Duplicate start wraps `ErrAlreadyRunning` or `ErrAlreadyExists`.
- Binding identity conflicts wrap `ErrConflict`.
- UDP bind permission failures wrap `ErrPermission` while preserving the original system error.
- Port/interface conflicts wrap `ErrConflict` while preserving the original system error.
- Cleanup errors must not be discarded; use `errors.Join` when an operation has both primary and cleanup failures.
- Callers must be able to classify with `errors.Is`.

## Context handling

Every public method accepts caller-provided `context.Context`.

Rules:

- `ctx == nil` returns `dhcperr.ErrInvalidRequest`.
- Already-canceled contexts return `ctx.Err()` before live socket/CoreDHCP/state mutation work.
- Long-running close/wait paths must respect the caller's context where possible.
- Production code must not create orphan `context.Background()` or `context.TODO()` inside the DHCP package.

## Unit testing strategy

Unit tests should not require real QEMU, real TAP, UDP 67, root privileges, or Lima.

Core test groups:

- `Start` validation: nil/canceled context, missing ID, missing interface, missing explicit port, invalid subnet/pool, zero lease duration, invalid Router/DNS mode, duplicate `ServerID`.
- `Stop` behavior: missing server, successful shutdown, no calls to unrelated QEMU/hostnet code.
- `ApplyBinding`: idempotent same MAC/IP, conflict for same MAC/different IP, conflict for different MAC/same IP, IP outside pool, reserved lease state.
- Handler behavior: unknown MAC silent, DISCOVER known MAC offers binding IP, REQUEST known MAC ACKs binding IP, REQUEST conflicting IP silent, ACK transitions lease to bound and sets expiry.
- Query behavior: `GetServer`, `GetLease`, and stable sorted `ListLeases` results.
- Error classification: `errors.Is` matches `dhcperr` sentinels and preserves original causes.
- Cleanup behavior: start/stop cleanup errors are not swallowed.

Focused verification commands after implementation:

```bash
go test -count=1 ./internal/hostnet/dhcp/...
go test -race -count=1 ./internal/hostnet/dhcp/...
scripts/verify.sh
```

## Lima acceptance strategy

Add a Linux acceptance test:

```text
test/acceptance/hostnet_dhcp_test.go
```

with build tag:

```go
//go:build acceptance && linux
```

Acceptance flow:

```text
requireHostnetAcceptanceEnv
  -> create bridge gvbr0 with 192.168.100.1/24
  -> create TAP gvtap0 attached to gvbr0
  -> start Govirta DHCP manager instance
  -> ApplyBinding MAC 02:00:00:00:01:02 -> 192.168.100.10
  -> start CirrOS QEMU with virtio-net MAC 02:00:00:00:01:02 on gvtap0
  -> wait for QMP running
  -> wait for serial login or DHCP-related guest evidence
  -> assert DHCP GetLease/ListLeases reports Bound for MAC/IP
  -> use serial to inspect guest ip addr and route
  -> host ping 192.168.100.10 succeeds
  -> stop DHCP
  -> stop QEMU through QMP
  -> delete TAP and bridge
```

Acceptance proof requires three evidence classes:

1. DHCP state evidence: `LeaseInfo{MAC=02:00:00:00:01:02, IP=192.168.100.10, State=Bound}`.
2. Guest state evidence: serial `ip -4 addr show dev eth0` shows `192.168.100.10/24`; if Router is enabled, `ip route show` shows `default via 192.168.100.1`.
3. Dataplane evidence: host `ping 192.168.100.10` succeeds.

Stability rules:

- Start DHCP and apply binding before starting QEMU.
- Use fixed guest MAC and fixed IP.
- Reuse existing QMP, serial, ping, network diagnostic helpers.
- If CirrOS DHCP timing is flaky, the test may use serial to explicitly restart the guest DHCP client as a logged fallback.
- Failure logs should include `ServerInfo`, `ListLeases`, QEMU argv, QMP status, serial tail, QEMU stderr, `ip addr`, `ip route`, and bridge/TAP state.

The acceptance test does not verify guest internet access, NAT, firewall, DNS resolution, multi-VM allocation, control-plane persistence, or automatic QEMU reattach after node restart.

## Official documentation and source references

This design uses CoreDHCP as a third-party library.

Context7 was attempted first, per project policy:

```bash
ctx7 library github.com/coredhcp/coredhcp "How to embed CoreDHCP as a Go library and implement a DHCPv4 handler with static MAC to IP leases"
```

The query did not resolve a CoreDHCP-specific Context7 library ID in this environment. This design therefore uses a `[降级查询]` to official project sources:

- CoreDHCP official repository: <https://github.com/coredhcp/coredhcp>
- CoreDHCP official site: <https://coredhcp.io/>
- CoreDHCP license: <https://github.com/coredhcp/coredhcp/blob/master/LICENSE>
- CoreDHCP README: <https://github.com/coredhcp/coredhcp/blob/master/README.md>
- CoreDHCP server package source: <https://github.com/coredhcp/coredhcp/tree/master/server>
- CoreDHCP handler package source: <https://github.com/coredhcp/coredhcp/tree/master/handler>
- CoreDHCP plugin package source: <https://github.com/coredhcp/coredhcp/tree/master/plugins>

The implementation plan should re-check the exact CoreDHCP API against the selected module version before coding and should pin the dependency version or pseudo-version explicitly rather than relying on an unpinned moving branch.

## Open questions resolved during design

- Lease persistence: first version uses process memory only.
- DHCP lifecycle: it is an internal compute-node function, not an independent daemon.
- Restart replay: replay only rebuilds the DHCP responder table and does not touch existing QEMU/TAP/bridge/guest state.
- Existing guest impact: replaying the same MAC/IP is non-disruptive; conflicting MAC/IP changes are rejected by `ApplyBinding` rather than silently applied.
- Unknown or conflicting DHCP requests: first version does not send DHCPNAK; it silently does not respond.
