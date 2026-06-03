# CoreDHCP-backed DHCP Wrapper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an in-process compute-node DHCP wrapper under `internal/hostnet/dhcp` that uses CoreDHCP internally, accepts explicit MAC-to-IPv4 bindings, and proves with Lima that CirrOS obtains the bound address.

**Architecture:** Add a Govirta-owned root DHCP contract package plus `dhcperr` sentinels, then implement a CoreDHCP-backed manager in `internal/hostnet/dhcp/coredhcp`. CoreDHCP stays hidden behind root interfaces; QEMU/TAP/bridge/route/firewall/guest state remains outside DHCP ownership. Bindings are process-memory only and replay-safe: `ApplyBinding` rebuilds the responder table without touching existing VM dataplane state.

**Tech Stack:** Go 1.26, `github.com/coredhcp/coredhcp v0.0.0-20260217182248-a0841cb3038f`, `github.com/insomniacslk/dhcp/dhcpv4`, existing hostnet link acceptance harness, Lima nested-KVM acceptance, CirrOS aarch64.

---

## Source file structure and size plan

Original size estimates expected all handwritten source files to stay under the 500-line soft limit and no file to approach the 800-line hard limit. Implementation showed that estimate was too low for `internal/hostnet/dhcp/coredhcp/manager_test.go`: it is 552 lines, above the 500-line soft limit but below the 800-line hard limit. Because this is a test file and the covered lifecycle, plugin-registration, and concurrency scenarios remain cohesive around `coredhcp.Manager`, keep it unsplit for this task; if it continues to grow, split it later by scenario group such as lifecycle, plugin registry, and concurrency behavior.

Create:

- `internal/hostnet/dhcp/constants.go` — strong types and explicit option wrappers; expected 120-180 lines.
- `internal/hostnet/dhcp/dhcp.go` — `Manager`, request/result structs, and package comments; expected 120-180 lines.
- `internal/hostnet/dhcp/noop.go` — unsupported no-op manager for composition tests; expected 70-110 lines.
- `internal/hostnet/dhcp/noop_test.go` — root no-op and explicit type behavior tests; expected 120-180 lines.
- `internal/hostnet/dhcp/dhcperr/errors.go` — stable sentinel errors; expected 20-40 lines.
- `internal/hostnet/dhcp/coredhcp/manager.go` — manager construction, start/stop, CoreDHCP starter seam, plugin registration bridge; expected 220-320 lines.
- `internal/hostnet/dhcp/coredhcp/runtime.go` — runtime structs, binding indexes, query helpers; expected 180-260 lines.
- `internal/hostnet/dhcp/coredhcp/validate.go` — context and request validation; expected 180-260 lines.
- `internal/hostnet/dhcp/coredhcp/info.go` — observed `ServerInfo`/`LeaseInfo` conversion and stable sorting; expected 100-160 lines.
- `internal/hostnet/dhcp/coredhcp/errors.go` — CoreDHCP/socket error classification; expected 80-140 lines.
- `internal/hostnet/dhcp/coredhcp/handler.go` — DHCPv4 handler and option rendering; expected 180-280 lines.
- `internal/hostnet/dhcp/coredhcp/manager_test.go` — lifecycle, plugin-registration, and concurrency tests with fake CoreDHCP starter; actual 552 lines, accepted above the 500-line soft limit because the scenarios remain cohesive and below the 800-line hard limit.
- `internal/hostnet/dhcp/coredhcp/binding_test.go` — binding/query conflict tests; expected 180-260 lines.
- `internal/hostnet/dhcp/coredhcp/handler_test.go` — DHCPv4 packet behavior tests; expected 220-340 lines.
- `test/acceptance/hostnet_dhcp_test.go` — real Linux bridge/TAP/QEMU/CirrOS DHCP acceptance; expected 180-300 lines.

Modify:

- `go.mod` / `go.sum` — add the pinned CoreDHCP dependency and transitive sums.
- `test/acceptance/harness.go` — add a serial command helper only if the DHCP acceptance test cannot reuse existing helpers cleanly; keep changes focused.
- `test/acceptance/doc.go` — mention DHCP acceptance coverage if helpful.
- `AGENTS.md` — after implementation, update code map, flow, commands, and acceptance notes for DHCP.

Do not modify:

- `internal/virt/qemu` lifecycle or argv semantics.
- `internal/hostnet/link` bridge/TAP behavior except by using its public API in acceptance.
- `internal/hostnet/route` IPv4 forwarding behavior.
- Firewall/NAT behavior.
- Control-plane persistence or etcd design.

---

### Task 1: Add root DHCP contract

**Files:**
- Create: `internal/hostnet/dhcp/dhcperr/errors.go`
- Create: `internal/hostnet/dhcp/constants.go`
- Create: `internal/hostnet/dhcp/dhcp.go`
- Create: `internal/hostnet/dhcp/noop.go`
- Create: `internal/hostnet/dhcp/noop_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: define the platform-neutral DHCP API and no-op implementation without importing CoreDHCP into the root package or changing module dependencies.

Acceptance evidence:

- `go test -count=1 ./internal/hostnet/dhcp` passes.
- `go test -count=1 ./internal/hostnet/...` passes.
- `go list -deps ./internal/hostnet/dhcp` does not include `github.com/coredhcp/coredhcp`.
- `git diff -- go.mod go.sum` has no output for this task.

- [ ] **Step 2: Keep dependencies unchanged in the root-contract task**

Run:

```bash
git diff -- go.mod go.sum
```

Expected:

- No output. CoreDHCP is introduced in Task 2 when the `coredhcp` implementation package first imports it.

- [ ] **Step 3: Add stable DHCP errors**

Create `internal/hostnet/dhcp/dhcperr/errors.go`:

```go
package dhcperr

import "errors"

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

- [ ] **Step 4: Add strong constants and explicit wrappers**

Create `internal/hostnet/dhcp/constants.go`:

```go
package dhcp

import (
    "net/netip"
)

type ServerID string
type Port uint16
type BindMode string
type DHCPOptionMode string
type ServerState string
type LeaseState string

const (
    BindModeInterfaceZone BindMode = "interface-zone"

    DHCPOptionDisabled DHCPOptionMode = "disabled"
    DHCPOptionEnabled  DHCPOptionMode = "enabled"

    ServerStateStarting ServerState = "starting"
    ServerStateReady    ServerState = "ready"
    ServerStateStopping ServerState = "stopping"
    ServerStateStopped  ServerState = "stopped"

    LeaseStateReserved LeaseState = "reserved"
    LeaseStateBound    LeaseState = "bound"
)

type DHCPOptionAddrs struct {
    Mode  DHCPOptionMode
    Addrs []netip.Addr
}

type AddressRange struct {
    Start netip.Addr
    End   netip.Addr
}

type BindingHostname struct {
    Value string
    Set   bool
}
```

- [ ] **Step 5: Add root Manager and request/result types**

Create `internal/hostnet/dhcp/dhcp.go`:

```go
package dhcp

import (
    "context"
    "net"
    "net/netip"
    "time"

    "github.com/suknna/govirta/internal/hostnet/link"
)

type Manager interface {
    Start(ctx context.Context, spec ServerSpec) (ServerInfo, error)
    Stop(ctx context.Context, id ServerID) error

    ApplyBinding(ctx context.Context, req BindingRequest) (LeaseInfo, error)
    RemoveBinding(ctx context.Context, query BindingQuery) error

    GetServer(ctx context.Context, id ServerID) (ServerInfo, error)
    GetLease(ctx context.Context, query BindingQuery) (LeaseInfo, error)
    ListLeases(ctx context.Context, filter LeaseFilter) ([]LeaseInfo, error)
}

type ServerSpec struct {
    ID            ServerID
    InterfaceName link.Name
    ListenAddr    netip.Addr
    ListenPort    Port
    ServerAddr    netip.Addr
    Subnet        netip.Prefix
    Pool          AddressRange
    LeaseDuration time.Duration
    Router        DHCPOptionAddrs
    DNS           DHCPOptionAddrs
    BindMode      BindMode
}

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

type LeaseFilter struct {
    ServerID ServerID
}

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

- [ ] **Step 6: Add no-op manager**

Create `internal/hostnet/dhcp/noop.go`:

```go
package dhcp

import (
    "context"
    "fmt"

    "github.com/suknna/govirta/internal/hostnet/dhcp/dhcperr"
)

type NoopManager struct{}

func NewNoopManager() NoopManager { return NoopManager{} }

func checkNoopContext(ctx context.Context) error {
    if ctx == nil {
        return fmt.Errorf("%w: context is nil", dhcperr.ErrInvalidRequest)
    }
    return ctx.Err()
}

func (NoopManager) Start(ctx context.Context, _ ServerSpec) (ServerInfo, error) {
    if err := checkNoopContext(ctx); err != nil { return ServerInfo{}, err }
    return ServerInfo{}, dhcperr.ErrUnsupported
}

func (NoopManager) Stop(ctx context.Context, _ ServerID) error {
    if err := checkNoopContext(ctx); err != nil { return err }
    return dhcperr.ErrUnsupported
}

func (NoopManager) ApplyBinding(ctx context.Context, _ BindingRequest) (LeaseInfo, error) {
    if err := checkNoopContext(ctx); err != nil { return LeaseInfo{}, err }
    return LeaseInfo{}, dhcperr.ErrUnsupported
}

func (NoopManager) RemoveBinding(ctx context.Context, _ BindingQuery) error {
    if err := checkNoopContext(ctx); err != nil { return err }
    return dhcperr.ErrUnsupported
}

func (NoopManager) GetServer(ctx context.Context, _ ServerID) (ServerInfo, error) {
    if err := checkNoopContext(ctx); err != nil { return ServerInfo{}, err }
    return ServerInfo{}, dhcperr.ErrUnsupported
}

func (NoopManager) GetLease(ctx context.Context, _ BindingQuery) (LeaseInfo, error) {
    if err := checkNoopContext(ctx); err != nil { return LeaseInfo{}, err }
    return LeaseInfo{}, dhcperr.ErrUnsupported
}

func (NoopManager) ListLeases(ctx context.Context, _ LeaseFilter) ([]LeaseInfo, error) {
    if err := checkNoopContext(ctx); err != nil { return nil, err }
    return nil, dhcperr.ErrUnsupported
}
```

- [ ] **Step 7: Add root tests**

Create `internal/hostnet/dhcp/noop_test.go` with tests that assert:

```go
func TestNoopManagerRejectsNilContext(t *testing.T) {
    manager := dhcp.NewNoopManager()
    _, err := manager.Start(nil, dhcp.ServerSpec{})
    if !errors.Is(err, dhcperr.ErrInvalidRequest) {
        t.Fatalf("expected ErrInvalidRequest, got %v", err)
    }
}

func TestNoopManagerReturnsUnsupported(t *testing.T) {
    manager := dhcp.NewNoopManager()
    _, err := manager.Start(context.Background(), dhcp.ServerSpec{})
    if !errors.Is(err, dhcperr.ErrUnsupported) {
        t.Fatalf("expected ErrUnsupported, got %v", err)
    }
}

func TestExplicitDHCPOptionModes(t *testing.T) {
    enabled := dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionEnabled, Addrs: []netip.Addr{netip.MustParseAddr("192.168.100.1")}}
    disabled := dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionDisabled}
    if enabled.Mode == disabled.Mode {
        t.Fatalf("expected distinct option modes")
    }
}
```

- [ ] **Step 8: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/dhcp
go test -count=1 ./internal/hostnet/...
```

Expected: both commands pass.

- [ ] **Step 9: Commit**

```bash
git add internal/hostnet/dhcp
git commit -m "feat(hostnet/dhcp): add DHCP contract"
```

---

### Task 2: Add CoreDHCP manager validation and lifecycle seams

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/hostnet/dhcp/coredhcp/manager.go`
- Create: `internal/hostnet/dhcp/coredhcp/runtime.go`
- Create: `internal/hostnet/dhcp/coredhcp/validate.go`
- Create: `internal/hostnet/dhcp/coredhcp/info.go`
- Create: `internal/hostnet/dhcp/coredhcp/errors.go`
- Create: `internal/hostnet/dhcp/coredhcp/manager_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: create a CoreDHCP-backed manager that validates explicit specs, starts/stops through an injectable starter, and exposes observed server state.

Acceptance evidence:

- `go test -count=1 ./internal/hostnet/dhcp/coredhcp -run 'TestStart|TestStop|TestValidate'` passes.
- Tests do not bind UDP 67 or require root.
- Start rejects unsupported/implicit bind modes instead of falling back.

- [ ] **Step 2: Add pinned CoreDHCP dependency**

Run:

```bash
go get github.com/coredhcp/coredhcp@v0.0.0-20260217182248-a0841cb3038f
go mod tidy
```

Expected:

- `go.mod` includes `github.com/coredhcp/coredhcp v0.0.0-20260217182248-a0841cb3038f` because Task 2 adds real imports under `internal/hostnet/dhcp/coredhcp`.
- `go.sum` includes CoreDHCP and required transitive module sums.

- [ ] **Step 3: Add runtime and starter types**

Create `runtime.go` with:

```go
type coreServerStarter interface {
    Start(config *config.Config) (coreServers, error)
}

type coreServers interface {
    Close()
    Wait() error
}

type realStarter struct{}

func (realStarter) Start(cfg *config.Config) (coreServers, error) {
    return server.Start(cfg)
}

type leaseRecord struct {
    serverID  dhcp.ServerID
    mac       net.HardwareAddr
    ip        netip.Addr
    hostname  dhcp.BindingHostname
    state     dhcp.LeaseState
    expiresAt time.Time
}

type serverRuntime struct {
    mu sync.RWMutex
    spec dhcp.ServerSpec
    state dhcp.ServerState
    bindingsByMAC map[string]*leaseRecord
    bindingsByIP map[netip.Addr]*leaseRecord
    coreServers coreServers
}
```

- [ ] **Step 4: Add manager construction**

Create `manager.go` with:

```go
type Manager struct {
    mu sync.RWMutex
    starter coreServerStarter
    servers map[dhcp.ServerID]*serverRuntime
}

func NewManager() *Manager {
    return newManager(realStarter{})
}

func newManager(starter coreServerStarter) *Manager {
    return &Manager{starter: starter, servers: make(map[dhcp.ServerID]*serverRuntime)}
}
```

- [ ] **Step 5: Add validation helpers**

Create `validate.go` with functions:

```go
func checkContext(ctx context.Context) error
func validateServerSpec(spec dhcp.ServerSpec) error
func validateOptionAddrs(name string, opt dhcp.DHCPOptionAddrs) error
func validateAddressRange(subnet netip.Prefix, pool dhcp.AddressRange) error
func validateServerID(id dhcp.ServerID) error
func validateMAC(mac net.HardwareAddr) error
```

Required validation outcomes:

- nil context wraps `dhcperr.ErrInvalidRequest`.
- canceled context returns `ctx.Err()`.
- empty `ServerID`, empty `InterfaceName`, invalid/non-IPv4 `ListenAddr`, zero `ListenPort`, invalid/non-IPv4 `ServerAddr`, invalid/non-IPv4 subnet, invalid pool, zero `LeaseDuration`, empty `BindMode`, empty Router/DNS option mode all wrap `ErrInvalidRequest`.
- only `BindModeInterfaceZone` is supported in the first implementation; other non-empty modes wrap `dhcperr.ErrUnsupported`.

- [ ] **Step 6: Add info conversion**

Create `info.go` with:

```go
func serverInfo(rt *serverRuntime) dhcp.ServerInfo
func leaseInfo(record *leaseRecord) dhcp.LeaseInfo
func sortedLeaseInfos(records []*leaseRecord) []dhcp.LeaseInfo
```

Sort leases by `ServerID`, then MAC string, then IP string.

- [ ] **Step 7: Add error classification skeleton**

Create `errors.go` with:

```go
func classifyStartError(err error) error {
    if err == nil { return nil }
    if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
        return fmt.Errorf("%w: %w", dhcperr.ErrPermission, err)
    }
    if errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.EADDRNOTAVAIL) {
        return fmt.Errorf("%w: %w", dhcperr.ErrConflict, err)
    }
    return err
}
```

If Go returns `*net.OpError`, unwrap with `errors.As` and classify the wrapped syscall error.

- [ ] **Step 8: Implement Start/Stop/GetServer lifecycle**

Implement:

```go
func (m *Manager) Start(ctx context.Context, spec dhcp.ServerSpec) (dhcp.ServerInfo, error)
func (m *Manager) Stop(ctx context.Context, id dhcp.ServerID) error
func (m *Manager) GetServer(ctx context.Context, id dhcp.ServerID) (dhcp.ServerInfo, error)
```

CoreDHCP config must use explicit interface zone:

```go
addr := net.UDPAddr{IP: net.IP(spec.ListenAddr.AsSlice()), Port: int(spec.ListenPort), Zone: string(spec.InterfaceName)}
cfg := &config.Config{Server4: &config.ServerConfig{Addresses: []net.UDPAddr{addr}, Plugins: []config.PluginConfig{{Name: govirtaPluginName, Args: []string{string(spec.ID)}}}}}
```

Do not implement handler logic in this task beyond registering a placeholder plugin that returns a handler which drops all packets. Handler behavior is Task 4.

- [ ] **Step 9: Add lifecycle tests**

`manager_test.go` should include fakes:

```go
type fakeStarter struct { started []*config.Config; servers coreServers; err error }
func (f *fakeStarter) Start(cfg *config.Config) (coreServers, error) { f.started = append(f.started, cfg); return f.servers, f.err }

type fakeServers struct { closed bool; waitErr error }
func (f *fakeServers) Close() { f.closed = true }
func (f *fakeServers) Wait() error { return f.waitErr }
```

Add tests for valid start, duplicate start conflict, invalid specs, unsupported bind mode, stop missing server, stop closes fake server, and get server observed state.

- [ ] **Step 10: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/dhcp/coredhcp -run 'Test(Start|Stop|Validate|GetServer)'
go test -count=1 ./internal/hostnet/dhcp/...
```

Expected: both commands pass.

- [ ] **Step 11: Commit**

```bash
git add go.mod go.sum internal/hostnet/dhcp/coredhcp
git commit -m "feat(hostnet/dhcp): add CoreDHCP lifecycle"
```

---

### Task 3: Implement binding and lease queries

**Files:**
- Modify: `internal/hostnet/dhcp/coredhcp/runtime.go`
- Modify: `internal/hostnet/dhcp/coredhcp/manager.go`
- Modify: `internal/hostnet/dhcp/coredhcp/validate.go`
- Create: `internal/hostnet/dhcp/coredhcp/binding_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: implement replay-safe process-memory binding operations without touching QEMU/TAP/bridge/guest state.

Acceptance evidence:

- `go test -count=1 ./internal/hostnet/dhcp/coredhcp -run 'TestApplyBinding|TestRemoveBinding|TestGetLease|TestListLeases'` passes.
- Same MAC/IP replay is idempotent.
- Conflicting MAC/IP changes return `dhcperr.ErrConflict` and leave old state unchanged.

- [ ] **Step 2: Add binding helpers**

Add to `runtime.go`:

```go
func macKey(mac net.HardwareAddr) string {
    return strings.ToLower(mac.String())
}

func (rt *serverRuntime) applyBinding(req dhcp.BindingRequest, now time.Time) (dhcp.LeaseInfo, error) {
    rt.mu.Lock()
    defer rt.mu.Unlock()
    key := macKey(req.MAC)
    if existing := rt.bindingsByMAC[key]; existing != nil {
        if existing.ip != req.IP {
            return dhcp.LeaseInfo{}, fmt.Errorf("%w: MAC %s already bound to %s", dhcperr.ErrConflict, req.MAC, existing.ip)
        }
        return leaseInfo(existing), nil
    }
    if existing := rt.bindingsByIP[req.IP]; existing != nil {
        return dhcp.LeaseInfo{}, fmt.Errorf("%w: IP %s already bound to %s", dhcperr.ErrConflict, req.IP, existing.mac)
    }
    record := &leaseRecord{serverID: req.ServerID, mac: append(net.HardwareAddr(nil), req.MAC...), ip: req.IP, hostname: req.Hostname, state: dhcp.LeaseStateReserved}
    rt.bindingsByMAC[key] = record
    rt.bindingsByIP[req.IP] = record
    _ = now
    return leaseInfo(record), nil
}
```

- [ ] **Step 3: Implement public binding methods**

Implement:

```go
func (m *Manager) ApplyBinding(ctx context.Context, req dhcp.BindingRequest) (dhcp.LeaseInfo, error)
func (m *Manager) RemoveBinding(ctx context.Context, query dhcp.BindingQuery) error
func (m *Manager) GetLease(ctx context.Context, query dhcp.BindingQuery) (dhcp.LeaseInfo, error)
func (m *Manager) ListLeases(ctx context.Context, filter dhcp.LeaseFilter) ([]dhcp.LeaseInfo, error)
```

Rules:

- validate context first;
- validate server ID and MAC;
- validate `BindingRequest.IP` is IPv4 and inside the server pool;
- do not allocate IPs;
- missing server wraps `ErrNotFound`;
- missing binding wraps `ErrNotFound` for `GetLease`/`RemoveBinding`;
- `ListLeases` requires explicit `filter.ServerID` in the first version.

- [ ] **Step 4: Add binding tests**

`binding_test.go` must include:

```go
func TestApplyBindingCreatesReservedLease(t *testing.T)
func TestApplyBindingIsIdempotentForSameMACAndIP(t *testing.T)
func TestApplyBindingRejectsSameMACDifferentIP(t *testing.T)
func TestApplyBindingRejectsDifferentMACSameIP(t *testing.T)
func TestApplyBindingRejectsIPOutsidePool(t *testing.T)
func TestRemoveBindingDeletesLease(t *testing.T)
func TestListLeasesSortsByMAC(t *testing.T)
```

Each conflict test must verify the old binding remains unchanged via `GetLease`.

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/dhcp/coredhcp -run 'TestApplyBinding|TestRemoveBinding|TestGetLease|TestListLeases'
go test -count=1 ./internal/hostnet/dhcp/...
```

Expected: both commands pass.

- [ ] **Step 6: Commit**

```bash
git add internal/hostnet/dhcp/coredhcp
git commit -m "feat(hostnet/dhcp): add static bindings"
```

---

### Task 4: Implement Govirta DHCPv4 handler

**Files:**
- Create: `internal/hostnet/dhcp/coredhcp/handler.go`
- Modify: `internal/hostnet/dhcp/coredhcp/manager.go`
- Create: `internal/hostnet/dhcp/coredhcp/handler_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: make CoreDHCP requests for known MACs produce OFFER/ACK responses with explicit Govirta options, and make unknown/conflicting requests silent.

Acceptance evidence:

- `go test -count=1 ./internal/hostnet/dhcp/coredhcp -run 'TestHandler'` passes.
- Unknown MAC produces `nil, true` and no NAK.
- Bound lease state is observed after ACK.

- [ ] **Step 2: Add controlled CoreDHCP plugin bridge**

CoreDHCP loads handlers through a package-global plugin registry. Implement a controlled adapter in `manager.go`:

```go
const govirtaPluginName = "govirta_static"

var registerPluginOnce sync.Once
var registerPluginErr error
var runtimeRegistry sync.Map // map[string]*serverRuntime keyed by ServerID

func ensurePluginRegistered() error {
    registerPluginOnce.Do(func() {
        registerPluginErr = plugins.RegisterPlugin(&plugins.Plugin{Name: govirtaPluginName, Setup4: setupHandler4})
    })
    return registerPluginErr
}

func setupHandler4(args ...string) (handler.Handler4, error) {
    if len(args) != 1 || args[0] == "" {
        return nil, fmt.Errorf("%w: govirta DHCP plugin requires ServerID", dhcperr.ErrInvalidRequest)
    }
    value, ok := runtimeRegistry.Load(args[0])
    if !ok {
        return nil, fmt.Errorf("%w: DHCP runtime %s not registered", dhcperr.ErrNotFound, args[0])
    }
    rt, ok := value.(*serverRuntime)
    if !ok {
        return nil, fmt.Errorf("%w: DHCP runtime %s has invalid type", dhcperr.ErrInvalidObservedState, args[0])
    }
    return newHandler4(rt), nil
}
```

Register the runtime in `runtimeRegistry` immediately before calling CoreDHCP `Start`; delete it on start failure and stop.

- [ ] **Step 3: Add handler implementation**

Create `handler.go` with:

```go
func newHandler4(rt *serverRuntime) handler.Handler4 {
    return func(req, resp *dhcpv4.DHCPv4) (*dhcpv4.DHCPv4, bool) {
        record := rt.lookupBindingForRequest(req)
        if record == nil {
            return nil, true
        }
        if req.MessageType() == dhcpv4.MessageTypeRequest && !requestedIPMatches(req, record.ip) {
            return nil, true
        }
        applyOptions(rt, record, req, resp)
        if resp.MessageType() == dhcpv4.MessageTypeAck {
            rt.markBound(record, time.Now())
        }
        return resp, true
    }
}
```

`applyOptions` must set:

- `resp.YourIPAddr = net.IP(record.ip.AsSlice())`;
- `dhcpv4.OptServerIdentifier(net.IP(spec.ServerAddr.AsSlice()))`;
- `dhcpv4.OptIPAddressLeaseTime(spec.LeaseDuration)`;
- `dhcpv4.OptSubnetMask(net.IPMask(net.CIDRMask(spec.Subnet.Bits(), 32)))`;
- `dhcpv4.OptRouter(...)` only when Router is enabled;
- `dhcpv4.OptDNS(...)` only when DNS is enabled.

- [ ] **Step 4: Add handler tests**

Create tests that construct packets with `github.com/insomniacslk/dhcp/dhcpv4`:

```go
discover, _ := dhcpv4.New(dhcpv4.WithHwAddr(mac), dhcpv4.WithOption(dhcpv4.OptMessageType(dhcpv4.MessageTypeDiscover)))
resp, _ := dhcpv4.NewReplyFromRequest(discover)
resp.UpdateOption(dhcpv4.OptMessageType(dhcpv4.MessageTypeOffer))
out, stop := newHandler4(rt)(discover, resp)
```

Required tests:

- `TestHandlerUnknownMACDropsRequest`;
- `TestHandlerDiscoverKnownMACOffersBoundIP`;
- `TestHandlerRequestKnownMACAcksBoundIP`;
- `TestHandlerRequestDifferentIPDropsWithoutNAK`;
- `TestHandlerSetsRouterAndDNSOnlyWhenEnabled`;
- `TestHandlerAckMarksLeaseBoundAndSetsExpiry`.

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/dhcp/coredhcp -run 'TestHandler'
go test -count=1 ./internal/hostnet/dhcp/...
```

Expected: both commands pass.

- [ ] **Step 6: Commit**

```bash
git add internal/hostnet/dhcp/coredhcp
git commit -m "feat(hostnet/dhcp): answer static DHCP leases"
```

---

### Task 5: Add Lima DHCP acceptance

**Files:**
- Create: `test/acceptance/hostnet_dhcp_test.go`
- Modify: `test/acceptance/harness.go` if command/serial helper is required
- Modify: `test/acceptance/doc.go` if documenting DHCP acceptance separately is useful

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: prove in Lima that a real CirrOS guest gets `192.168.100.10` from the Govirta DHCP wrapper over bridge/TAP.

Acceptance evidence:

- `go test -v -tags acceptance -count=1 ./test/acceptance/... -run TestHostnetDHCPBindingEndToEnd` passes inside the Lima acceptance flow.
- `scripts/acceptance.sh full` passes before final handoff.

- [ ] **Step 2: Add acceptance test skeleton**

Create `test/acceptance/hostnet_dhcp_test.go` with:

```go
//go:build acceptance && linux

package acceptance

func TestHostnetDHCPBindingEndToEnd(t *testing.T) {
    t.Parallel()
    ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
    defer cancel()
    env := requireHostnetAcceptanceEnv(t)
    // create bridge/TAP using linklinux.NewManager as in hostnet_link_test.go
    // start DHCP manager, apply static binding, then start QEMU
}
```

Use the same bridge/TAP identity as current hostnet acceptance unless parallel acceptance requires unique names:

- bridge `gvbr0`, gateway `192.168.100.1/24`, MAC `02:00:00:00:01:01`;
- TAP `gvtap0`, MAC `02:00:00:00:01:02`;
- guest IP `192.168.100.10`.

- [ ] **Step 3: Start DHCP before QEMU**

In the test, create:

```go
dhcpManager := coredhcp.NewManager()
serverInfo, err := dhcpManager.Start(ctx, dhcp.ServerSpec{
    ID: dhcp.ServerID("acceptance-gvbr0-192.168.100.0-24"),
    InterfaceName: link.Name("gvbr0"),
    ListenAddr: netip.MustParseAddr("0.0.0.0"),
    ListenPort: dhcp.Port(dhcpv4.ServerPort),
    ServerAddr: netip.MustParseAddr("192.168.100.1"),
    Subnet: netip.MustParsePrefix("192.168.100.0/24"),
    Pool: dhcp.AddressRange{Start: netip.MustParseAddr("192.168.100.10"), End: netip.MustParseAddr("192.168.100.10")},
    LeaseDuration: time.Hour,
    Router: dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionDisabled},
    DNS: dhcp.DHCPOptionAddrs{Mode: dhcp.DHCPOptionDisabled},
    BindMode: dhcp.BindModeInterfaceZone,
})
```

Then call `ApplyBinding` for the guest MAC/IP. Cleanup must call `dhcpManager.Stop(ctx, serverInfo.ID)` and log stop errors without hiding primary QEMU cleanup errors. Task 5 intentionally disables the DHCP Router option because CirrOS 0.6.2 waits on metadata probing when DHCP installs a default route; Router option rendering remains covered by unit tests and is not part of this acceptance gate.

- [ ] **Step 4: Boot CirrOS with TAP and no static IP commands**

Reuse the QEMU direct-kernel pattern from `hostnet_link_test.go`, but do not run the serial static IP commands. Keep `console=ttyAMA0 ds=none` and the virtio-net MAC `02:00:00:00:01:02`.

- [ ] **Step 5: Assert DHCP, guest, and dataplane evidence**

Acceptance must check:

```go
lease, err := dhcpManager.GetLease(ctx, dhcp.BindingQuery{ServerID: serverInfo.ID, MAC: guestMAC})
if err != nil { t.Fatalf("get DHCP lease: %v", err) }
if lease.State != dhcp.LeaseStateBound || lease.IP != guestIP { t.Fatalf("unexpected lease: %+v", lease) }
```

Then verify guest state through serial commands. If current helpers cannot read command output, add a focused helper that writes a command and waits for expected marker text. Finally run `pingUntilSuccess(t, ctx, guestIP.String())`.

- [ ] **Step 6: Add failure diagnostics**

On failure, log:

- `ServerInfo`;
- `ListLeases`;
- QEMU argv;
- QMP status;
- serial tail;
- QEMU stderr;
- `logNetworkDiagnostics` output.

- [ ] **Step 7: Run targeted acceptance**

Run through the project acceptance harness:

```bash
scripts/acceptance.sh full
```

Expected: full acceptance passes and archives a log under `test/log/`.

- [ ] **Step 8: Commit**

```bash
git add test/acceptance/hostnet_dhcp_test.go test/acceptance/harness.go test/acceptance/doc.go
git commit -m "test(acceptance): verify DHCP lease to CirrOS"
```

---

### Task 6: Documentation and knowledge-base update

**Files:**
- Modify: `AGENTS.md`
- Optional modify: `docs/superpowers/specs/2026-06-02-dhcp-coredhcp-wrapper-design.md` only if implementation discovers a verified API detail that changes the design.

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: make future agents able to find the DHCP package, flow, constraints, and acceptance command without rediscovering them.

Acceptance evidence:

- `AGENTS.md` contains a `hostnet DHCP` entry in WHERE TO LOOK or equivalent.
- `AGENTS.md` documents the non-destructive replay model.
- `AGENTS.md` documents the Lima DHCP acceptance test.

- [ ] **Step 2: Update AGENTS.md**

Add or update:

- `Verified-against` file list entries for `internal/hostnet/dhcp/...` and `test/acceptance/hostnet_dhcp_test.go`.
- `WHERE TO LOOK`: DHCP wrapper under `internal/hostnet/dhcp`.
- `CODE MAP`: `dhcp.Manager`, `coredhcp.Manager`, `ApplyBinding`, `newHandler4`.
- `CALL GRAPHS & DATA FLOW`: flow for DHCP start/binding/request.
- `CONVENTIONS`: DHCP bindings are explicit, process-memory only, and replay-safe.
- `ANTI-PATTERNS`: no DHCPNAK for unknown/conflicting requests in first version, no QEMU/TAP/bridge mutation from DHCP.
- `ACCEPTANCE TESTS`: hostnet DHCP CirrOS lease acceptance.

- [ ] **Step 3: Run docs and local verification**

Run:

```bash
scripts/verify.sh
go test -race -count=1 ./internal/hostnet/dhcp/...
```

Expected: both commands pass.

- [ ] **Step 4: Commit**

```bash
git add AGENTS.md docs/superpowers/specs/2026-06-02-dhcp-coredhcp-wrapper-design.md
git commit -m "docs: document DHCP wrapper"
```

---

### Task 7: Final verification and review readiness

**Files:**
- No new files unless verification exposes a defect.

- [ ] **Step 1: Confirm final acceptance criteria**

Final evidence must include:

- `scripts/verify.sh` passes.
- `go test -race -count=1 ./internal/hostnet/dhcp/...` passes.
- `scripts/acceptance.sh full` passes.
- `git status --short` shows only intended changes before final commit/PR handoff.

- [ ] **Step 2: Run local CI**

Run:

```bash
scripts/verify.sh
```

Expected: gofmt check, Go tests, and main service builds pass.

- [ ] **Step 3: Run DHCP race tests**

Run:

```bash
go test -race -count=1 ./internal/hostnet/dhcp/...
```

Expected: pass with no data races.

- [ ] **Step 4: Run Lima acceptance**

Run:

```bash
scripts/acceptance.sh full
```

Expected: acceptance passes, including `TestHostnetDHCPBindingEndToEnd`. Task 5 intentionally keeps the Router option disabled in acceptance to avoid CirrOS metadata-delay behavior; Router option rendering remains covered by unit tests.

- [ ] **Step 5: Inspect final diff**

Run:

```bash
git status --short
git diff --stat
git diff
```

Expected: diff contains only DHCP contract, CoreDHCP implementation, DHCP acceptance, and DHCP docs/knowledge-base updates.

- [ ] **Step 6: Commit final fixes if needed**

If verification required final fixes:

```bash
git add <fixed files>
git commit -m "fix(hostnet/dhcp): address verification findings"
```

If no fixes were required, do not create an empty commit.

---

## Plan self-review checklist

- Spec coverage: root contract, CoreDHCP wrapping, process-memory bindings, no auto allocation, no DHCPNAK, restart replay safety, unit tests, Lima acceptance, and AGENTS update are covered by Tasks 1-7.
- Placeholder scan: no task uses unresolved placeholder wording or vague edge-case instructions.
- Type consistency: the plan consistently uses `ServerID`, `ServerSpec`, `BindingRequest`, `BindingQuery`, `LeaseFilter`, `ServerInfo`, `LeaseInfo`, `BindModeInterfaceZone`, `DHCPOptionAddrs`, `LeaseStateReserved`, and `LeaseStateBound`.
- File size discipline: `internal/hostnet/dhcp/coredhcp/manager_test.go` is a known 500-line soft-limit deviation at 552 lines, but remains below the 800-line hard limit and is accepted for now because its lifecycle, plugin, and concurrency test scenarios are cohesive. Other planned files do not exceed the hard limit.
- Third-party API evidence: CoreDHCP version `v0.0.0-20260217182248-a0841cb3038f` was resolved with `go list -m -json`, and source inspection verified `server.Start`, `Servers.Close`, `Servers.Wait`, `handler.Handler4`, `config.Config`, and plugin registration shape.
