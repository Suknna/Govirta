# Hostnet Route Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/hostnet/route` as Govirta's IPv4 host route primitive layer with forwarding readiness checks, route add/replace/delete/list/get operations, unit tests, and Linux acceptance coverage.

**Architecture:** Follow the existing `internal/hostnet/link` shape: root contract package, `routeerr` sentinel package, `linux` implementation subpackage, fake handle unit tests, and Lima acceptance tests. The package uses strong Govirta types and maps them internally to `vishvananda/netlink` and `/proc/sys/net/ipv4/ip_forward`; upper layers never pass raw netlink flags or shell command strings.

**Tech Stack:** Go 1.26, `github.com/vishvananda/netlink`, `golang.org/x/sys/unix`, Linux `/proc/sys/net/ipv4/ip_forward`, Lima acceptance harness, QEMU-independent route acceptance.

---

## Execution result

Implemented in branch `hostnet-route` on 2026-06-01. The implementation added the root route contract, Linux netlink route manager, fake-backed Linux tests, real Linux acceptance coverage, and AGENTS knowledge-base updates. Final verification ran `scripts/verify.sh` and `scripts/acceptance.sh full`; the passing acceptance log is archived under `test/log/` as a gitignored run artifact.

The task checkboxes below are intentionally preserved as the original implementation plan rather than rewritten as a progress tracker. See git history for the executed commits.

## Source file structure and size plan

Expected handwritten source file sizes stay below the 500-line soft limit. If an implementation file approaches 500 lines during execution, do not split mid-task; finish the planned task and record the split as a follow-up plan correction.

Create:

- `internal/hostnet/route/constants.go` — strong types, constants, explicit constructor helpers.
- `internal/hostnet/route/forwarding.go` — forwarding state and observed info types.
- `internal/hostnet/route/route.go` — `Manager`, `RouteSpec`, `RouteFilter`, `RouteQuery`, `RouteInfo`.
- `internal/hostnet/route/noop.go` — no-op implementation that validates context and returns `ErrUnsupported` for live operations.
- `internal/hostnet/route/routeerr/errors.go` — stable route sentinel errors.
- `internal/hostnet/route/noop_test.go` — root package no-op and explicit metric tests.
- `internal/hostnet/route/linux/handle_linux.go` — private netlink handle abstraction and real handle.
- `internal/hostnet/route/linux/sysctl_linux.go` — forwarding reader abstraction and real `/proc` reader.
- `internal/hostnet/route/linux/errors_linux.go` — Linux/netlink/syscall error translation.
- `internal/hostnet/route/linux/validate_linux.go` — context, forwarding, route spec, filter, and query validation.
- `internal/hostnet/route/linux/info_linux.go` — conversion between Govirta route types and netlink route types.
- `internal/hostnet/route/linux/manager_linux.go` — Linux `Manager` implementation.
- `internal/hostnet/route/linux/fake_handle_test.go` — fake netlink/sysctl test harness.
- `internal/hostnet/route/linux/validation_test.go` — explicit validation matrix.
- `internal/hostnet/route/linux/forwarding_test.go` — forwarding read/check behavior.
- `internal/hostnet/route/linux/route_test.go` — add/replace/delete behavior.
- `internal/hostnet/route/linux/list_get_test.go` — list/get behavior.
- `internal/hostnet/route/linux/errors_test.go` — error translation behavior.
- `test/acceptance/hostnet_route_test.go` — Linux route primitive acceptance.

Modify:

- `scripts/acceptance.sh` — set `net.ipv4.ip_forward=1` during acceptance setup.
- `test/acceptance/harness.go` — add route diagnostics helper if needed by the acceptance test.
- `test/acceptance/doc.go` — document the route acceptance precondition.
- `AGENTS.md` — update code map, flows, and current hostnet status after implementation.

Do not modify:

- `internal/hostnet/link` behavior.
- `internal/network/bridge` skeleton.
- NAT/firewall code; none exists in this plan.

---

### Task 1: Root route contract, errors, and no-op manager

**Files:**
- Create: `internal/hostnet/route/constants.go`
- Create: `internal/hostnet/route/forwarding.go`
- Create: `internal/hostnet/route/route.go`
- Create: `internal/hostnet/route/noop.go`
- Create: `internal/hostnet/route/routeerr/errors.go`
- Create: `internal/hostnet/route/noop_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: define the platform-neutral API and no-op behavior exactly as the approved spec states.

Acceptance evidence:

- `go test -count=1 ./internal/hostnet/route` passes.
- `go test -count=1 ./internal/hostnet/...` still passes.
- Root API exposes no raw netlink constants, no shell command strings, and no implicit zero-value defaults for behavior-affecting fields.

- [ ] **Step 2: Add stable route errors**

Create `internal/hostnet/route/routeerr/errors.go`:

```go
package routeerr

import "errors"

var (
    ErrInvalidRequest       = errors.New("invalid host route request")
    ErrInvalidObservedState = errors.New("invalid observed host route state")
    ErrNotReady             = errors.New("host route prerequisite not ready")
    ErrNotFound             = errors.New("host route not found")
    ErrAlreadyExists        = errors.New("host route already exists")
    ErrConflict             = errors.New("host route conflict")
    ErrPermission           = errors.New("host route permission denied")
    ErrIncompleteList       = errors.New("host route list incomplete")
    ErrUnsupported          = errors.New("host route operation unsupported")
)
```

- [ ] **Step 3: Add strong constants and explicit helper constructors**

Create `internal/hostnet/route/constants.go`:

```go
package route

type Family string
type RouteTable string
type RouteType string
type RouteScope string
type RouteProtocol string
type DestinationMode string
type GatewayMode string
type LinkFilterMode string
type MetricFilterMode string

const (
    FamilyIPv4 Family = "ipv4"

    RouteTableMain RouteTable = "main"

    RouteTypeUnicast RouteType = "unicast"

    RouteScopeGlobal RouteScope = "global"
    RouteScopeLink   RouteScope = "link"
    RouteScopeHost   RouteScope = "host"

    RouteProtocolStatic      RouteProtocol = "static"
    RouteProtocolKernel      RouteProtocol = "kernel"
    RouteProtocolBoot        RouteProtocol = "boot"
    RouteProtocolDHCP        RouteProtocol = "dhcp"
    RouteProtocolUnspecified RouteProtocol = "unspecified"

    DestinationCIDR    DestinationMode = "cidr"
    DestinationDefault DestinationMode = "default"
    DestinationAny     DestinationMode = "any"

    GatewayNone GatewayMode = "none"
    GatewayIPv4 GatewayMode = "ipv4"
    GatewayAny  GatewayMode = "any"

    LinkAny  LinkFilterMode = "any"
    LinkName LinkFilterMode = "name"

    MetricAny   MetricFilterMode = "any"
    MetricValue MetricFilterMode = "value"
)

type Metric struct {
    Value uint32
    Set   bool
}

type MetricFilter struct {
    Mode  MetricFilterMode
    Value uint32
}

func ExplicitMetric(value uint32) Metric {
    return Metric{Value: value, Set: true}
}

func AnyMetric() MetricFilter {
    return MetricFilter{Mode: MetricAny}
}

func FilterMetric(value uint32) MetricFilter {
    return MetricFilter{Mode: MetricValue, Value: value}
}
```

- [ ] **Step 4: Add forwarding types**

Create `internal/hostnet/route/forwarding.go`:

```go
package route

type IPv4ForwardingState string

const (
    IPv4ForwardingEnabled  IPv4ForwardingState = "enabled"
    IPv4ForwardingDisabled IPv4ForwardingState = "disabled"
)

type IPv4ForwardingInfo struct {
    State IPv4ForwardingState
    Path  string
}
```

- [ ] **Step 5: Add Manager and request/result types**

Create `internal/hostnet/route/route.go`:

```go
package route

import (
    "context"
    "net/netip"

    "github.com/suknna/govirta/internal/hostnet/link"
)

type Manager interface {
    GetIPv4Forwarding(ctx context.Context) (IPv4ForwardingInfo, error)
    CheckIPv4Forwarding(ctx context.Context, expected IPv4ForwardingState) (IPv4ForwardingInfo, error)

    AddRoute(ctx context.Context, spec RouteSpec) (RouteInfo, error)
    ReplaceRoute(ctx context.Context, spec RouteSpec) (RouteInfo, error)
    DeleteRoute(ctx context.Context, spec RouteSpec) error
    ListRoutes(ctx context.Context, filter RouteFilter) ([]RouteInfo, error)
    GetRoute(ctx context.Context, query RouteQuery) (RouteInfo, error)
}

type Destination struct {
    Mode DestinationMode
    CIDR netip.Prefix
}

type Gateway struct {
    Mode GatewayMode
    Addr netip.Addr
}

type LinkFilter struct {
    Mode LinkFilterMode
    Name link.Name
}

type RouteSpec struct {
    Family      Family
    Destination Destination
    LinkName    link.Name
    Gateway     Gateway

    Table    RouteTable
    Type     RouteType
    Scope    RouteScope
    Protocol RouteProtocol
    Metric   Metric
}

type RouteFilter struct {
    Family      Family
    Table       RouteTable
    Link        LinkFilter
    Destination Destination
    Gateway     Gateway
    Metric      MetricFilter
}

type RouteQuery struct {
    Family      Family
    Destination netip.Addr
}

type RouteInfo struct {
    Family      Family
    Destination Destination
    LinkName    link.Name
    Gateway     Gateway

    Table    RouteTable
    Type     RouteType
    Scope    RouteScope
    Protocol RouteProtocol
    Metric   Metric
}
```

- [ ] **Step 6: Add NoopManager**

Create `internal/hostnet/route/noop.go`:

```go
package route

import (
    "context"
    "fmt"

    "github.com/suknna/govirta/internal/hostnet/route/routeerr"
)

type NoopManager struct{}

func NewNoopManager() NoopManager { return NoopManager{} }

func (NoopManager) GetIPv4Forwarding(ctx context.Context) (IPv4ForwardingInfo, error) {
    if err := noopRouteOperationError(ctx, "get IPv4 forwarding"); err != nil {
        return IPv4ForwardingInfo{}, err
    }
    return IPv4ForwardingInfo{}, nil
}

func (NoopManager) CheckIPv4Forwarding(ctx context.Context, _ IPv4ForwardingState) (IPv4ForwardingInfo, error) {
    if err := noopRouteOperationError(ctx, "check IPv4 forwarding"); err != nil {
        return IPv4ForwardingInfo{}, err
    }
    return IPv4ForwardingInfo{}, nil
}

func (NoopManager) AddRoute(ctx context.Context, _ RouteSpec) (RouteInfo, error) {
    if err := noopRouteOperationError(ctx, "add route"); err != nil {
        return RouteInfo{}, err
    }
    return RouteInfo{}, nil
}

func (NoopManager) ReplaceRoute(ctx context.Context, _ RouteSpec) (RouteInfo, error) {
    if err := noopRouteOperationError(ctx, "replace route"); err != nil {
        return RouteInfo{}, err
    }
    return RouteInfo{}, nil
}

func (NoopManager) DeleteRoute(ctx context.Context, _ RouteSpec) error {
    return noopRouteOperationError(ctx, "delete route")
}

func (NoopManager) ListRoutes(ctx context.Context, _ RouteFilter) ([]RouteInfo, error) {
    if err := noopRouteOperationError(ctx, "list routes"); err != nil {
        return nil, err
    }
    return nil, nil
}

func (NoopManager) GetRoute(ctx context.Context, _ RouteQuery) (RouteInfo, error) {
    if err := noopRouteOperationError(ctx, "get route"); err != nil {
        return RouteInfo{}, err
    }
    return RouteInfo{}, nil
}

func noopRouteOperationError(ctx context.Context, operation string) error {
    if ctx == nil {
        return fmt.Errorf("%s: %w", operation, routeerr.ErrInvalidRequest)
    }
    if err := ctx.Err(); err != nil {
        return err
    }
    return fmt.Errorf("%s: %w", operation, routeerr.ErrUnsupported)
}
```

- [ ] **Step 7: Add root tests**

Create `internal/hostnet/route/noop_test.go`:

```go
package route

import (
    "context"
    "errors"
    "testing"

    "github.com/suknna/govirta/internal/hostnet/route/routeerr"
)

func TestExplicitMetricAllowsZeroValue(t *testing.T) {
    metric := ExplicitMetric(0)
    if !metric.Set {
        t.Fatal("ExplicitMetric(0) must mark the metric as explicitly set")
    }
    if metric.Value != 0 {
        t.Fatalf("metric value = %d, want 0", metric.Value)
    }
}

func TestMetricFilterAllowsExplicitZeroValue(t *testing.T) {
    filter := FilterMetric(0)
    if filter.Mode != MetricValue {
        t.Fatalf("filter mode = %q, want %q", filter.Mode, MetricValue)
    }
    if filter.Value != 0 {
        t.Fatalf("filter value = %d, want 0", filter.Value)
    }
}

func TestNoopManagerRejectsNilContext(t *testing.T) {
    manager := NewNoopManager()
    _, err := manager.GetIPv4Forwarding(nil)
    if !errors.Is(err, routeerr.ErrInvalidRequest) {
        t.Fatalf("GetIPv4Forwarding(nil) error = %v, want ErrInvalidRequest", err)
    }
}

func TestNoopManagerReturnsCanceledContext(t *testing.T) {
    manager := NewNoopManager()
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    _, err := manager.GetRoute(ctx, RouteQuery{})
    if !errors.Is(err, context.Canceled) {
        t.Fatalf("GetRoute canceled error = %v, want context.Canceled", err)
    }
}

func TestNoopManagerReturnsUnsupportedForLiveOperations(t *testing.T) {
    manager := NewNoopManager()
    ctx := context.Background()
    operations := []struct {
        name string
        run  func() error
    }{
        {"GetIPv4Forwarding", func() error { _, err := manager.GetIPv4Forwarding(ctx); return err }},
        {"CheckIPv4Forwarding", func() error { _, err := manager.CheckIPv4Forwarding(ctx, IPv4ForwardingEnabled); return err }},
        {"AddRoute", func() error { _, err := manager.AddRoute(ctx, RouteSpec{}); return err }},
        {"ReplaceRoute", func() error { _, err := manager.ReplaceRoute(ctx, RouteSpec{}); return err }},
        {"DeleteRoute", func() error { return manager.DeleteRoute(ctx, RouteSpec{}) }},
        {"ListRoutes", func() error { _, err := manager.ListRoutes(ctx, RouteFilter{}); return err }},
        {"GetRoute", func() error { _, err := manager.GetRoute(ctx, RouteQuery{}); return err }},
    }
    for _, operation := range operations {
        t.Run(operation.name, func(t *testing.T) {
            if err := operation.run(); !errors.Is(err, routeerr.ErrUnsupported) {
                t.Fatalf("error = %v, want ErrUnsupported", err)
            }
        })
    }
}
```

- [ ] **Step 8: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/route
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/hostnet/route
git commit -m "feat(hostnet/route): add route Manager contract"
```

---

### Task 2: Linux forwarding reader, validation, and error translation

**Files:**
- Create: `internal/hostnet/route/linux/sysctl_linux.go`
- Create: `internal/hostnet/route/linux/errors_linux.go`
- Create: `internal/hostnet/route/linux/validate_linux.go`
- Create: `internal/hostnet/route/linux/forwarding_test.go`
- Create: `internal/hostnet/route/linux/validation_test.go`
- Create: `internal/hostnet/route/linux/errors_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: implement forwarding read/check and request validation with Linux error translation, without touching real routes.

Acceptance evidence:

- `go test -count=1 ./internal/hostnet/route/linux -run 'Test.*Forwarding|TestValidation|TestTranslate'` passes.
- Invalid values map to `routeerr.ErrInvalidRequest` or `routeerr.ErrUnsupported` exactly as the spec says.
- Forwarding reader never writes `/proc/sys/net/ipv4/ip_forward`.

- [ ] **Step 2: Add forwarding reader**

Create `internal/hostnet/route/linux/sysctl_linux.go`:

```go
//go:build linux

package linux

import (
    "context"
    "os"
)

const ipv4ForwardingPath = "/proc/sys/net/ipv4/ip_forward"

type forwardingReader interface {
    ReadIPv4Forwarding(ctx context.Context) (string, error)
}

type procForwardingReader struct{}

func (procForwardingReader) ReadIPv4Forwarding(ctx context.Context) (string, error) {
    if err := checkContext(ctx); err != nil {
        return "", err
    }
    data, err := os.ReadFile(ipv4ForwardingPath)
    if err != nil {
        return "", err
    }
    return string(data), nil
}
```

- [ ] **Step 3: Add Linux error translation**

Create `internal/hostnet/route/linux/errors_linux.go`:

```go
//go:build linux

package linux

import (
    "errors"
    "fmt"
    "os"
    "syscall"

    "github.com/suknna/govirta/internal/hostnet/route/routeerr"
    "github.com/vishvananda/netlink"
)

func translateError(operation string, err error) error {
    if err == nil {
        return nil
    }
    class := classifyError(err)
    if class == nil {
        return fmt.Errorf("%s: %w", operation, err)
    }
    if errors.Is(err, class) {
        return fmt.Errorf("%s: %w", operation, err)
    }
    return fmt.Errorf("%s: %w: %w", operation, class, err)
}

func classifyError(err error) error {
    switch {
    case errors.Is(err, routeerr.ErrInvalidRequest):
        return routeerr.ErrInvalidRequest
    case errors.Is(err, routeerr.ErrInvalidObservedState):
        return routeerr.ErrInvalidObservedState
    case errors.Is(err, routeerr.ErrNotReady):
        return routeerr.ErrNotReady
    case errors.Is(err, routeerr.ErrNotFound):
        return routeerr.ErrNotFound
    case errors.Is(err, routeerr.ErrAlreadyExists):
        return routeerr.ErrAlreadyExists
    case errors.Is(err, routeerr.ErrConflict):
        return routeerr.ErrConflict
    case errors.Is(err, routeerr.ErrPermission):
        return routeerr.ErrPermission
    case errors.Is(err, routeerr.ErrIncompleteList):
        return routeerr.ErrIncompleteList
    case errors.Is(err, routeerr.ErrUnsupported):
        return routeerr.ErrUnsupported
    case errors.As(err, new(netlink.LinkNotFoundError)):
        return routeerr.ErrNotFound
    case errors.Is(err, netlink.ErrDumpInterrupted):
        return routeerr.ErrIncompleteList
    case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
        return routeerr.ErrPermission
    case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ESRCH):
        return routeerr.ErrNotFound
    case errors.Is(err, syscall.EEXIST):
        return routeerr.ErrAlreadyExists
    case errors.Is(err, syscall.EINVAL):
        return routeerr.ErrInvalidRequest
    default:
        return nil
    }
}
```

- [ ] **Step 4: Add validation helpers**

Create `internal/hostnet/route/linux/validate_linux.go`:

```go
//go:build linux

package linux

import (
    "context"
    "fmt"
    "net/netip"

    "github.com/suknna/govirta/internal/hostnet/link"
    "github.com/suknna/govirta/internal/hostnet/route"
    "github.com/suknna/govirta/internal/hostnet/route/routeerr"
)

func checkContext(ctx context.Context) error {
    if ctx == nil {
        return routeerr.ErrInvalidRequest
    }
    return ctx.Err()
}

func validateForwardingState(state route.IPv4ForwardingState) error {
    switch state {
    case route.IPv4ForwardingEnabled, route.IPv4ForwardingDisabled:
        return nil
    default:
        return fmt.Errorf("IPv4 forwarding state %q: %w", state, routeerr.ErrInvalidRequest)
    }
}

func validateRouteSpec(spec route.RouteSpec) error {
    if err := validateFamily(spec.Family); err != nil {
        return err
    }
    if err := validateMainTable(spec.Table); err != nil {
        return err
    }
    if spec.Type == "" || spec.Protocol == "" || spec.Scope == "" {
        return routeerr.ErrInvalidRequest
    }
    if spec.Type != route.RouteTypeUnicast || spec.Protocol != route.RouteProtocolStatic {
        return routeerr.ErrUnsupported
    }
    if !spec.Metric.Set {
        return routeerr.ErrInvalidRequest
    }
    if err := validateLinkName(spec.LinkName); err != nil {
        return err
    }
    if err := validateSpecDestination(spec.Destination); err != nil {
        return err
    }
    if err := validateSpecGateway(spec.Gateway); err != nil {
        return err
    }
    return validateRouteShape(spec)
}

func validateRouteFilter(filter route.RouteFilter) error {
    if err := validateFamily(filter.Family); err != nil {
        return err
    }
    if err := validateMainTable(filter.Table); err != nil {
        return err
    }
    if err := validateFilterDestination(filter.Destination); err != nil {
        return err
    }
    if err := validateFilterGateway(filter.Gateway); err != nil {
        return err
    }
    if err := validateLinkFilter(filter.Link); err != nil {
        return err
    }
    switch filter.Metric.Mode {
    case route.MetricAny, route.MetricValue:
        return nil
    default:
        return routeerr.ErrInvalidRequest
    }
}

func validateRouteQuery(query route.RouteQuery) error {
    if err := validateFamily(query.Family); err != nil {
        return err
    }
    if !query.Destination.IsValid() || !query.Destination.Is4() {
        return routeerr.ErrInvalidRequest
    }
    return nil
}

func validateFamily(family route.Family) error {
    switch family {
    case route.FamilyIPv4:
        return nil
    case "":
        return routeerr.ErrInvalidRequest
    default:
        return routeerr.ErrUnsupported
    }
}

func validateMainTable(table route.RouteTable) error {
    switch table {
    case route.RouteTableMain:
        return nil
    case "":
        return routeerr.ErrInvalidRequest
    default:
        return routeerr.ErrUnsupported
    }
}

func validateLinkName(name link.Name) error {
    if name == "" || len(string(name)) > link.MaxInterfaceNameLength {
        return routeerr.ErrInvalidRequest
    }
    return nil
}

func validateLinkFilter(filter route.LinkFilter) error {
    switch filter.Mode {
    case route.LinkAny:
        if filter.Name != "" {
            return routeerr.ErrInvalidRequest
        }
        return nil
    case route.LinkName:
        return validateLinkName(filter.Name)
    default:
        return routeerr.ErrInvalidRequest
    }
}

func validateSpecDestination(destination route.Destination) error {
    switch destination.Mode {
    case route.DestinationCIDR:
        return validateIPv4Prefix(destination.CIDR)
    case route.DestinationDefault:
        if destination.CIDR != (netip.Prefix{}) {
            return routeerr.ErrInvalidRequest
        }
        return nil
    case route.DestinationAny:
        return routeerr.ErrInvalidRequest
    default:
        return routeerr.ErrInvalidRequest
    }
}

func validateFilterDestination(destination route.Destination) error {
    switch destination.Mode {
    case route.DestinationAny:
        if destination.CIDR != (netip.Prefix{}) {
            return routeerr.ErrInvalidRequest
        }
        return nil
    case route.DestinationCIDR, route.DestinationDefault:
        return validateSpecDestination(destination)
    default:
        return routeerr.ErrInvalidRequest
    }
}

func validateIPv4Prefix(prefix netip.Prefix) error {
    if !prefix.IsValid() || !prefix.Addr().Is4() {
        return routeerr.ErrInvalidRequest
    }
    return nil
}

func validateSpecGateway(gateway route.Gateway) error {
    switch gateway.Mode {
    case route.GatewayNone:
        if gateway.Addr != (netip.Addr{}) {
            return routeerr.ErrInvalidRequest
        }
        return nil
    case route.GatewayIPv4:
        if !gateway.Addr.IsValid() || !gateway.Addr.Is4() || gateway.Addr.IsUnspecified() || gateway.Addr.IsMulticast() {
            return routeerr.ErrInvalidRequest
        }
        return nil
    case route.GatewayAny:
        return routeerr.ErrInvalidRequest
    default:
        return routeerr.ErrInvalidRequest
    }
}

func validateFilterGateway(gateway route.Gateway) error {
    switch gateway.Mode {
    case route.GatewayAny:
        if gateway.Addr != (netip.Addr{}) {
            return routeerr.ErrInvalidRequest
        }
        return nil
    case route.GatewayNone, route.GatewayIPv4:
        return validateSpecGateway(gateway)
    default:
        return routeerr.ErrInvalidRequest
    }
}

func validateRouteShape(spec route.RouteSpec) error {
    switch {
    case spec.Destination.Mode == route.DestinationCIDR && spec.Gateway.Mode == route.GatewayNone && spec.Scope == route.RouteScopeLink:
        return nil
    case spec.Destination.Mode == route.DestinationCIDR && spec.Gateway.Mode == route.GatewayIPv4 && spec.Scope == route.RouteScopeGlobal:
        return nil
    case spec.Destination.Mode == route.DestinationDefault && spec.Gateway.Mode == route.GatewayIPv4 && spec.Scope == route.RouteScopeGlobal:
        return nil
    case spec.Scope == route.RouteScopeHost:
        return routeerr.ErrUnsupported
    default:
        return routeerr.ErrInvalidRequest
    }
}
```

- [ ] **Step 5: Add focused tests**

Create tests that assert these exact cases:

```go
// validation_test.go cases:
// - nil ctx returns ErrInvalidRequest through manager methods in Task 3.
// - Family "" -> ErrInvalidRequest.
// - Family "ipv6" -> ErrUnsupported.
// - RouteTable "custom" -> ErrUnsupported.
// - Metric{Set:false} -> ErrInvalidRequest.
// - ExplicitMetric(0) passes validateRouteSpec.
// - DestinationAny in RouteSpec -> ErrInvalidRequest.
// - GatewayAny in RouteSpec -> ErrInvalidRequest.
// - DestinationDefault + GatewayNone -> ErrInvalidRequest.
// - DestinationCIDR + GatewayNone + RouteScopeLink -> valid.
// - DestinationCIDR + GatewayIPv4 + RouteScopeGlobal -> valid.
// - DestinationDefault + GatewayIPv4 + RouteScopeGlobal -> valid.
// - GatewayIPv4 with 0.0.0.0 -> ErrInvalidRequest.
// - GatewayIPv4 with multicast 224.0.0.1 -> ErrInvalidRequest.
```

```go
// forwarding_test.go cases:
// - fake reader value "1\n" -> IPv4ForwardingEnabled.
// - fake reader value "0\n" -> IPv4ForwardingDisabled.
// - fake reader value "2\n" -> ErrInvalidObservedState.
// - fake reader missing proc path error -> ErrUnsupported through GetIPv4Forwarding.
// - Check expected enabled with observed disabled -> ErrNotReady.
```

```go
// errors_test.go cases:
// - syscall.EPERM -> ErrPermission.
// - syscall.EEXIST -> ErrAlreadyExists.
// - syscall.ESRCH -> ErrNotFound.
// - netlink.ErrDumpInterrupted -> ErrIncompleteList.
```

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/route/linux -run 'Test.*Forwarding|TestValidation|TestTranslate'
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/hostnet/route/linux internal/hostnet/route
git commit -m "feat(hostnet/route/linux): validate route requests and forwarding checks"
```

---

### Task 3: Linux route manager implementation and fake route tests

**Files:**
- Create: `internal/hostnet/route/linux/handle_linux.go`
- Create: `internal/hostnet/route/linux/info_linux.go`
- Create: `internal/hostnet/route/linux/manager_linux.go`
- Create: `internal/hostnet/route/linux/fake_handle_test.go`
- Create: `internal/hostnet/route/linux/route_test.go`
- Create: `internal/hostnet/route/linux/list_get_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: implement `route.Manager` for Linux using `vishvananda/netlink` without shelling out or exposing raw flags.

Acceptance evidence:

- `go test -count=1 ./internal/hostnet/route/linux` passes.
- `go test -count=1 ./internal/hostnet/route/...` passes.
- Add, replace, delete, list, and get operations are covered by fake netlink tests.

- [ ] **Step 2: Add netlink handle abstraction**

Create `internal/hostnet/route/linux/handle_linux.go`:

```go
//go:build linux

package linux

import (
    "net"

    "github.com/vishvananda/netlink"
)

type handle interface {
    LinkByIndex(index int) (netlink.Link, error)
    LinkByName(name string) (netlink.Link, error)
    RouteAdd(route *netlink.Route) error
    RouteReplace(route *netlink.Route) error
    RouteDel(route *netlink.Route) error
    RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error)
    RouteGet(destination net.IP) ([]netlink.Route, error)
}

type realHandle struct{}

func (realHandle) LinkByIndex(index int) (netlink.Link, error) { return netlink.LinkByIndex(index) }
func (realHandle) LinkByName(name string) (netlink.Link, error) { return netlink.LinkByName(name) }
func (realHandle) RouteAdd(route *netlink.Route) error { return netlink.RouteAdd(route) }
func (realHandle) RouteReplace(route *netlink.Route) error { return netlink.RouteReplace(route) }
func (realHandle) RouteDel(route *netlink.Route) error { return netlink.RouteDel(route) }
func (realHandle) RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error) {
    return netlink.RouteListFiltered(family, filter, filterMask)
}
func (realHandle) RouteGet(destination net.IP) ([]netlink.Route, error) {
    return netlink.RouteGet(destination)
}
```

- [ ] **Step 3: Add manager implementation**

Create `internal/hostnet/route/linux/manager_linux.go` with this structure:

```go
//go:build linux

package linux

import (
    "context"
    "errors"
    "fmt"
    "os"
    "strings"

    "github.com/suknna/govirta/internal/hostnet/route"
    "github.com/suknna/govirta/internal/hostnet/route/routeerr"
)

type Manager struct {
    handle     handle
    forwarding forwardingReader
}

func NewManager() *Manager {
    return NewManagerWithHandle(realHandle{}, procForwardingReader{})
}

func NewManagerWithHandle(h handle, forwarding forwardingReader) *Manager {
    if h == nil {
        h = realHandle{}
    }
    if forwarding == nil {
        forwarding = procForwardingReader{}
    }
    return &Manager{handle: h, forwarding: forwarding}
}

func (m *Manager) GetIPv4Forwarding(ctx context.Context) (route.IPv4ForwardingInfo, error) {
    if err := checkContext(ctx); err != nil {
        return route.IPv4ForwardingInfo{}, translateError("get IPv4 forwarding", err)
    }
    value, err := m.forwarding.ReadIPv4Forwarding(ctx)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return route.IPv4ForwardingInfo{}, fmt.Errorf("get IPv4 forwarding: %w: %w", routeerr.ErrUnsupported, err)
        }
        return route.IPv4ForwardingInfo{}, translateError("get IPv4 forwarding", err)
    }
    switch strings.TrimSpace(value) {
    case "1":
        return route.IPv4ForwardingInfo{State: route.IPv4ForwardingEnabled, Path: ipv4ForwardingPath}, nil
    case "0":
        return route.IPv4ForwardingInfo{State: route.IPv4ForwardingDisabled, Path: ipv4ForwardingPath}, nil
    default:
        return route.IPv4ForwardingInfo{}, fmt.Errorf("get IPv4 forwarding: observed %q: %w", strings.TrimSpace(value), routeerr.ErrInvalidObservedState)
    }
}

func (m *Manager) CheckIPv4Forwarding(ctx context.Context, expected route.IPv4ForwardingState) (route.IPv4ForwardingInfo, error) {
    if err := checkContext(ctx); err != nil {
        return route.IPv4ForwardingInfo{}, translateError("check IPv4 forwarding", err)
    }
    if err := validateForwardingState(expected); err != nil {
        return route.IPv4ForwardingInfo{}, translateError("check IPv4 forwarding", err)
    }
    info, err := m.GetIPv4Forwarding(ctx)
    if err != nil {
        return route.IPv4ForwardingInfo{}, err
    }
    if info.State != expected {
        return info, fmt.Errorf("check IPv4 forwarding: expected %s observed %s: %w", expected, info.State, routeerr.ErrNotReady)
    }
    return info, nil
}
```

Then implement route methods in the same file:

```go
func (m *Manager) AddRoute(ctx context.Context, spec route.RouteSpec) (route.RouteInfo, error) {
    return m.applyRoute(ctx, "add route", spec, m.handle.RouteAdd)
}

func (m *Manager) ReplaceRoute(ctx context.Context, spec route.RouteSpec) (route.RouteInfo, error) {
    return m.applyRoute(ctx, "replace route", spec, m.handle.RouteReplace)
}

func (m *Manager) DeleteRoute(ctx context.Context, spec route.RouteSpec) error {
    if err := checkContext(ctx); err != nil {
        return translateError("delete route", err)
    }
    if err := validateRouteSpec(spec); err != nil {
        return translateError("delete route", err)
    }
    nlRoute, err := m.netlinkRouteForSpec("delete route", spec)
    if err != nil {
        return err
    }
    if err := m.handle.RouteDel(&nlRoute); err != nil {
        if errors.Is(classifyError(err), routeerr.ErrNotFound) {
            return nil
        }
        return translateError("delete route", err)
    }
    return nil
}
```

Keep the imports exact: `errors` is used by forwarding missing-path handling and idempotent delete; `os` is used to map missing `/proc/sys/net/ipv4/ip_forward` to `routeerr.ErrUnsupported`. Do not ignore the `RouteDel` error.

- [ ] **Step 4: Add route conversion helpers**

Create `internal/hostnet/route/linux/info_linux.go` with helpers that perform these exact mappings:

```go
// routeSpec -> netlink.Route:
// FamilyIPv4 -> netlink.FAMILY_V4
// RouteTableMain -> unix.RT_TABLE_MAIN
// RouteTypeUnicast -> unix.RTN_UNICAST
// RouteScopeGlobal -> netlink.SCOPE_UNIVERSE
// RouteScopeLink -> netlink.SCOPE_LINK
// RouteProtocolStatic -> unix.RTPROT_STATIC
// RouteProtocolUnspecified is observed-only for Linux protocol 0 / RTPROT_UNSPEC path-selection results.
// DestinationDefault -> 0.0.0.0/0
// GatewayNone -> nil Gw
// GatewayIPv4 -> net.IP(gateway.AsSlice())
// ExplicitMetric(value) -> Priority=int(value)
```

Functions to implement:

```go
func (m *Manager) netlinkRouteForSpec(operation string, spec route.RouteSpec) (netlink.Route, error)
func (m *Manager) applyRoute(ctx context.Context, operation string, spec route.RouteSpec, mutate func(*netlink.Route) error) (route.RouteInfo, error)
func (m *Manager) observedRouteInfo(operation string, spec route.RouteSpec) (route.RouteInfo, error)
func (m *Manager) netlinkFilterForRouteFilter(filter route.RouteFilter) (netlink.Route, uint64, error)
func destinationIPNet(destination route.Destination) *net.IPNet
func netlinkRouteInfo(h handle, nlRoute netlink.Route) (route.RouteInfo, error)
func routeProtocolFromNetlink(protocol netlink.RouteProtocol) (route.RouteProtocol, error)
func routeScopeFromNetlink(scope netlink.Scope) (route.RouteScope, error)
func routeTypeFromNetlink(routeType int) (route.RouteType, error)
func exactRouteMatch(want route.RouteSpec, got route.RouteInfo) bool
func routeInfoMatchesFilter(info route.RouteInfo, filter route.RouteFilter) bool
func sortRouteInfos(infos []route.RouteInfo)
```

`netlinkRouteInfo` must call `LinkByIndex` to fill `RouteInfo.LinkName`. If `LinkByIndex` fails, return an error.

Implementation contract:

| Helper | Required behavior |
| --- | --- |
| `applyRoute` | Validate ctx/spec, build `netlink.Route`, call `mutate`, translate mutation error, then call `observedRouteInfo`; never return the request spec as success. |
| `observedRouteInfo` | Build a filter from the full spec, call `RouteListFiltered`, convert results with `netlinkRouteInfo`, and return only a route that passes `exactRouteMatch`. Missing observed match returns `routeerr.ErrNotFound`. |
| `netlinkFilterForRouteFilter` | Always sets main-table filter. Adds `RT_FILTER_OIF`, `RT_FILTER_DST`, `RT_FILTER_GW`, and `RT_FILTER_PRIORITY` only when the corresponding explicit filter mode requests it. |
| `routeInfoMatchesFilter` | Performs Go-side exact filtering for values netlink cannot express directly, including `GatewayNone`, metric, destination mode, and link name. |
| `exactRouteMatch` | Compares destination, link name, gateway, table, type, scope, protocol, and metric. |

Observed mapping table:

| netlink field | Govirta value | Unsupported/invalid behavior |
| --- | --- | --- |
| `unix.RT_TABLE_MAIN` | `RouteTableMain` | Other tables return `ErrUnsupported` when observed through this first-version API. |
| `unix.RTN_UNICAST` | `RouteTypeUnicast` | Other route types return `ErrUnsupported` or `ErrInvalidObservedState` based on whether they are known but unsupported or malformed. |
| `netlink.SCOPE_UNIVERSE` | `RouteScopeGlobal` | Unknown scopes return `ErrInvalidObservedState`. |
| `netlink.SCOPE_LINK` | `RouteScopeLink` | Unknown scopes return `ErrInvalidObservedState`. |
| `netlink.SCOPE_HOST` | `RouteScopeHost` for observed routes only | Mutation specs using `RouteScopeHost` still return `ErrUnsupported`. |
| protocol `0` / `unix.RTPROT_UNSPEC` | `RouteProtocolUnspecified` | Observed only; can appear in Linux `RouteGet` path-selection results. Mutation specs using `RouteProtocolUnspecified` still return `ErrUnsupported`. |
| `unix.RTPROT_STATIC` | `RouteProtocolStatic` | Unknown protocols return `ErrInvalidObservedState`. |
| `unix.RTPROT_KERNEL` | `RouteProtocolKernel` | Observed only. |
| `unix.RTPROT_BOOT` | `RouteProtocolBoot` | Observed only. |
| `unix.RTPROT_DHCP` | `RouteProtocolDHCP` | Observed only. |

- [ ] **Step 5: Add ListRoutes and GetRoute**

In `manager_linux.go`, implement:

```go
func (m *Manager) ListRoutes(ctx context.Context, filter route.RouteFilter) ([]route.RouteInfo, error) {
    if err := checkContext(ctx); err != nil {
        return nil, translateError("list routes", err)
    }
    if err := validateRouteFilter(filter); err != nil {
        return nil, translateError("list routes", err)
    }
    nlFilter, mask, err := m.netlinkFilterForRouteFilter(filter)
    if err != nil {
        return nil, err
    }
    routes, err := m.handle.RouteListFiltered(netlink.FAMILY_V4, &nlFilter, mask)
    if err != nil {
        return nil, translateError("list routes", err)
    }
    infos := make([]route.RouteInfo, 0, len(routes))
    for _, nlRoute := range routes {
        info, err := netlinkRouteInfo(m.handle, nlRoute)
        if err != nil {
            return nil, translateError("list routes", err)
        }
        if routeInfoMatchesFilter(info, filter) {
            infos = append(infos, info)
        }
    }
    sortRouteInfos(infos)
    return infos, nil
}

func (m *Manager) GetRoute(ctx context.Context, query route.RouteQuery) (route.RouteInfo, error) {
    if err := checkContext(ctx); err != nil {
        return route.RouteInfo{}, translateError("get route", err)
    }
    if err := validateRouteQuery(query); err != nil {
        return route.RouteInfo{}, translateError("get route", err)
    }
    routes, err := m.handle.RouteGet(query.Destination.AsSlice())
    if err != nil {
        return route.RouteInfo{}, translateError("get route", err)
    }
    if len(routes) == 0 {
        return route.RouteInfo{}, fmt.Errorf("get route: %w", routeerr.ErrNotFound)
    }
    info, err := netlinkRouteInfo(m.handle, routes[0])
    if err != nil {
        return route.RouteInfo{}, translateError("get route", err)
    }
    return info, nil
}
```

- [ ] **Step 6: Add fake handle tests**

Create `internal/hostnet/route/linux/fake_handle_test.go` with a fake that:

- Stores links by name and index.
- Stores routes as `[]netlink.Route`.
- Records every call string.
- Allows injected per-operation failures.
- Implements exact route identity for add/delete/replace.
- Returns `routeerr.ErrNotFound` wrapped for fake not-found cases.

The fake must distinguish two routes with the same destination but different metric.

- [ ] **Step 7: Add route behavior tests**

Create `route_test.go` and `list_get_test.go` covering:

```go
// route_test.go:
// - AddRoute direct route returns observed RouteInfo.
// - AddRoute gateway route returns observed RouteInfo.
// - AddRoute default route returns DestinationDefault in RouteInfo.
// - AddRoute duplicate returns ErrAlreadyExists.
// - ReplaceRoute missing adds route.
// - ReplaceRoute existing replaces metric 100 with 200.
// - ReplaceRoute metric change leaves no observed route with metric 100.
// - DeleteRoute deletes exact route.
// - DeleteRoute missing returns nil.
// - DeleteRoute does not delete same destination with different metric.
// - LinkByName missing returns ErrNotFound.
```

```go
// list_get_test.go:
// - ListRoutes DestinationAny + GatewayAny + LinkAny lists routes sorted deterministically.
// - ListRoutes DestinationCIDR filters by prefix.
// - ListRoutes DestinationDefault filters default route.
// - ListRoutes GatewayNone excludes gateway routes.
// - ListRoutes GatewayIPv4 filters by gateway.
// - ListRoutes MetricValue filters explicit metric 0 and metric 100 separately.
// - netlink.Route with protocol 0/kernel/boot/dhcp maps to RouteProtocolUnspecified/RouteProtocolKernel/RouteProtocolBoot/RouteProtocolDHCP in observed RouteInfo.
// - netlink.Route with unknown protocol returns ErrInvalidObservedState.
// - netlink.Route with SCOPE_UNIVERSE/SCOPE_LINK/SCOPE_HOST maps to RouteScopeGlobal/RouteScopeLink/RouteScopeHost in observed RouteInfo.
// - netlink.Route with unknown scope returns ErrInvalidObservedState.
// - netlink.Route with RTN_UNICAST maps to RouteTypeUnicast in observed RouteInfo.
// - netlink.Route with unsupported or unknown route type returns ErrUnsupported or ErrInvalidObservedState according to the observed mapping table.
// - RouteListFiltered ErrDumpInterrupted returns ErrIncompleteList.
// - GetRoute returns first route from RouteGet with LinkName resolved by LinkByIndex.
// - GetRoute no routes returns ErrNotFound.
// - GetRoute LinkByIndex failure returns an error.
```

- [ ] **Step 8: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/route/linux
go test -count=1 ./internal/hostnet/route/...
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/hostnet/route
git commit -m "feat(hostnet/route/linux): implement netlink route manager"
```

---

### Task 4: Linux acceptance for route primitives

**Files:**
- Modify: `scripts/acceptance.sh`
- Modify: `test/acceptance/doc.go`
- Modify: `test/acceptance/harness.go`
- Create: `test/acceptance/hostnet_route_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: verify real Linux route primitives in Lima without testing VM external-network access.

Acceptance evidence:

- `scripts/acceptance.sh full` passes.
- `test/log/YYYY-MM-DD-HHMMSS-acceptance-full.log` contains `PASS: TestHostnetRoutePrimitives`.
- The route acceptance creates only a test dummy link and a test route, then removes both.

- [ ] **Step 2: Configure forwarding in acceptance setup**

Modify `scripts/acceptance.sh` inside the guest command before `go test`:

```sh
sudo sysctl -w net.ipv4.ip_forward=1
sudo -E env \
    PATH="$HOME/.local/go/bin:$PATH" \
    GOCACHE=/govirta-cache/gocache \
    GOMODCACHE=/govirta-cache/gomodcache \
    GOVIRTA_ACCEPTANCE=1 \
    GOVIRTA_ACCEPTANCE_LIMA_GUEST=1 \
    GOVIRTA_ACCEPTANCE_QEMU=/usr/bin/qemu-system-aarch64 \
    GOVIRTA_ACCEPTANCE_QEMU_IMG=/usr/bin/qemu-img \
    GOVIRTA_ACCEPTANCE_FIRMWARE=/usr/share/AAVMF/AAVMF_CODE.fd \
    GOVIRTA_ACCEPTANCE_CIRROS=/govirta-cache/images/cirros-aarch64.qcow2 \
    GOVIRTA_ACCEPTANCE_CIRROS_KERNEL=/govirta-cache/images/cirros-0.6.2-aarch64-kernel \
    GOVIRTA_ACCEPTANCE_CIRROS_INITRAMFS=/govirta-cache/images/cirros-0.6.2-aarch64-initramfs \
    go test -v -tags acceptance -count=1 ./test/acceptance/...
```

Do not move this responsibility into `lima/govirta.yaml` in this task.

- [ ] **Step 3: Add route diagnostics helper**

If `test/acceptance/harness.go` does not already expose enough diagnostics, add:

```go
func logRouteDiagnostics(t *testing.T, ctx context.Context, probe string, linkName string) {
    t.Helper()
    commands := [][]string{
        {"ip", "route", "show", "table", "main"},
        {"ip", "route", "get", probe},
        {"sysctl", "net.ipv4.ip_forward"},
        {"ip", "link", "show", linkName},
    }
    for _, args := range commands {
        stdout, stderr, err := runCommand(ctx, args[0], args[1:]...)
        if err != nil {
            t.Logf("%s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
            continue
        }
        t.Logf("%s stdout:\n%s\nstderr:\n%s", strings.Join(args, " "), stdout, stderr)
    }
}
```

Add `strings` to imports only if the file does not already import it.

- [ ] **Step 4: Add acceptance test**

Create `test/acceptance/hostnet_route_test.go`:

```go
//go:build acceptance && linux

package acceptance

import (
    "context"
    "errors"
    "net/netip"
    "testing"
    "time"

    "github.com/suknna/govirta/internal/hostnet/link"
    hostroute "github.com/suknna/govirta/internal/hostnet/route"
    routelinux "github.com/suknna/govirta/internal/hostnet/route/linux"
    "github.com/vishvananda/netlink"
)

func TestHostnetRoutePrimitives(t *testing.T) {
    requireHostnetAcceptanceEnv(t)

    ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
    defer cancel()

    const linkName = "gvrt0"
    probe := netip.MustParseAddr("198.51.100.10")
    destination := hostroute.Destination{Mode: hostroute.DestinationCIDR, CIDR: netip.MustParsePrefix("198.51.100.0/24")}

    cleanupDummyLink(t, linkName)
    dummy := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: linkName}}
    if err := netlink.LinkAdd(dummy); err != nil {
        t.Fatalf("create dummy link %s: %v", linkName, err)
    }
    t.Cleanup(func() { cleanupDummyLink(t, linkName) })
    if err := netlink.LinkSetUp(dummy); err != nil {
        t.Fatalf("set dummy link %s up: %v", linkName, err)
    }

    manager := routelinux.NewManager()
    if _, err := manager.CheckIPv4Forwarding(ctx, hostroute.IPv4ForwardingEnabled); err != nil {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("check IPv4 forwarding: %v", err)
    }

    spec100 := hostroute.RouteSpec{
        Family:      hostroute.FamilyIPv4,
        Destination: destination,
        LinkName:    linkName,
        Gateway:     hostroute.Gateway{Mode: hostroute.GatewayNone},
        Table:       hostroute.RouteTableMain,
        Type:        hostroute.RouteTypeUnicast,
        Scope:       hostroute.RouteScopeLink,
        Protocol:    hostroute.RouteProtocolStatic,
        Metric:      hostroute.ExplicitMetric(100),
    }
    spec200 := spec100
    spec200.Metric = hostroute.ExplicitMetric(200)

    if _, err := manager.AddRoute(ctx, spec100); err != nil {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("add route: %v", err)
    }
    t.Cleanup(func() {
        if err := manager.DeleteRoute(context.Background(), spec100); err != nil {
            t.Logf("cleanup route metric 100: %v", err)
        }
        if err := manager.DeleteRoute(context.Background(), spec200); err != nil {
            t.Logf("cleanup route metric 200: %v", err)
        }
    })

    routes, err := manager.ListRoutes(ctx, routeFilterForTest(destination, hostroute.FilterMetric(100), linkName))
    if err != nil {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("list route metric 100: %v", err)
    }
    if len(routes) != 1 {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("listed %d routes for metric 100, want 1: %#v", len(routes), routes)
    }

    got, err := manager.GetRoute(ctx, hostroute.RouteQuery{Family: hostroute.FamilyIPv4, Destination: probe})
    if err != nil {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("get route: %v", err)
    }
    if got.LinkName != linkName {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("get route link = %q, want %q", got.LinkName, linkName)
    }

    if _, err := manager.ReplaceRoute(ctx, spec200); err != nil {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("replace route: %v", err)
    }
    routes, err = manager.ListRoutes(ctx, routeFilterForTest(destination, hostroute.FilterMetric(100), linkName))
    if err != nil {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("list old route metric 100 after replace: %v", err)
    }
    if len(routes) != 0 {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("old metric 100 routes after replace = %#v, want none", routes)
    }
    routes, err = manager.ListRoutes(ctx, routeFilterForTest(destination, hostroute.FilterMetric(200), linkName))
    if err != nil {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("list route metric 200: %v", err)
    }
    if len(routes) != 1 {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("listed %d routes for metric 200, want 1: %#v", len(routes), routes)
    }

    if err := manager.DeleteRoute(ctx, spec200); err != nil {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("delete route: %v", err)
    }
    if err := manager.DeleteRoute(ctx, spec200); err != nil {
        t.Fatalf("delete missing route must be idempotent: %v", err)
    }
    routes, err = manager.ListRoutes(ctx, routeFilterForTest(destination, hostroute.AnyMetric(), linkName))
    if err != nil {
        t.Fatalf("list absent route: %v", err)
    }
    if len(routes) != 0 {
        logRouteDiagnostics(t, ctx, probe.String(), linkName)
        t.Fatalf("routes after delete = %#v, want none", routes)
    }
}

func routeFilterForTest(destination hostroute.Destination, metric hostroute.MetricFilter, name string) hostroute.RouteFilter {
    return hostroute.RouteFilter{
        Family:      hostroute.FamilyIPv4,
        Table:       hostroute.RouteTableMain,
        Link:        hostroute.LinkFilter{Mode: hostroute.LinkName, Name: link.Name(name)},
        Destination: destination,
        Gateway:     hostroute.Gateway{Mode: hostroute.GatewayNone},
        Metric:      metric,
    }
}

func cleanupDummyLink(t *testing.T, name string) {
    t.Helper()
    link, err := netlink.LinkByName(name)
    if err != nil {
        var notFound netlink.LinkNotFoundError
        if errors.As(err, &notFound) {
            return
        }
        t.Fatalf("lookup dummy link %s for cleanup: %v", name, err)
    }
    if err := netlink.LinkDel(link); err != nil {
        t.Fatalf("delete dummy link %s: %v", name, err)
    }
}
```

The test imports `internal/hostnet/link` only to explicitly convert the fixed test link name into `link.Name` for `RouteFilter`.

- [ ] **Step 5: Update acceptance docs**

Modify `test/acceptance/doc.go` to mention:

```go
// Host route acceptance verifies real Linux route primitives in the Lima guest.
// scripts/acceptance.sh enables net.ipv4.ip_forward before running tests so
// route.Manager can check the node precondition without mutating it.
```

- [ ] **Step 6: Run targeted Linux acceptance**

Run:

```bash
scripts/acceptance.sh full
```

Expected:

- `TestHostnetRoutePrimitives` passes.
- Existing `TestHostnetLinkBridgeTapEndToEnd` still passes.
- A complete log appears under `test/log/YYYY-MM-DD-HHMMSS-acceptance-full.log`.

- [ ] **Step 7: Commit**

```bash
git add scripts/acceptance.sh test/acceptance
git commit -m "test(acceptance): add host route primitive coverage"
```

---

### Task 5: Documentation, knowledge base, and full verification

**Files:**
- Modify: `AGENTS.md`
- Modify: `docs/superpowers/specs/2026-06-01-hostnet-route-design.md` only if implementation discovered a necessary design correction.

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: update project navigation and run final verification after implementation.

Acceptance evidence:

- `scripts/verify.sh` passes.
- `scripts/acceptance.sh full` passes after all changes.
- `git status --short` contains only intended tracked changes before commit.

- [ ] **Step 2: Update AGENTS.md knowledge base**

Add `internal/hostnet/route` to the relevant sections:

- `Verified-against.files`
- `OVERVIEW` or `CURRENT PHASE` if route state affects hostnet status.
- `STRUCTURE` under `internal/hostnet`.
- `WHERE TO LOOK` with a row like:
  ```text
  host route primitives | internal/hostnet/route -> internal/hostnet/route/linux | IPv4 forwarding checks and main-table unicast route management
  ```
- `CODE MAP` symbols:
  ```text
  route.Manager
  routelinux.Manager
  routelinux.Manager.GetIPv4Forwarding
  routelinux.Manager.CheckIPv4Forwarding
  routelinux.Manager.AddRoute
  routelinux.Manager.ReplaceRoute
  routelinux.Manager.DeleteRoute
  routelinux.Manager.ListRoutes
  routelinux.Manager.GetRoute
  ```
- Add a new flow section `flow-hostnet-route` describing:
  ```text
  caller -> route.Manager -> route/linux.Manager -> validate -> netlink Route* or /proc read -> observed RouteInfo/ForwardingInfo
  ```
- `CONVENTIONS` note: route does not mutate IPv4 forwarding; node installation or acceptance setup owns forwarding configuration.
- `ACCEPTANCE TESTS` note: full acceptance includes `TestHostnetRoutePrimitives`.
- In `NOTES` or a clearly named existing status section, keep the statement that VM external network still requires NAT/firewall/DNS/default-route orchestration beyond route primitives. Do not invent an unrelated structure if `AGENTS.md` does not already have a `KNOWN_ISSUES` section.

- [ ] **Step 3: Run local verification**

Run:

```bash
scripts/verify.sh
```

Expected: PASS.

- [ ] **Step 4: Run full acceptance**

Run:

```bash
scripts/acceptance.sh full
```

Expected: PASS and complete log under `test/log/`.

- [ ] **Step 5: Inspect final diff and status**

Run:

```bash
git status --short
git diff --stat
git diff -- AGENTS.md scripts/acceptance.sh test/acceptance internal/hostnet/route docs/superpowers/specs/2026-06-01-hostnet-route-design.md
```

Expected: only route implementation, acceptance, scripts, AGENTS, and approved spec/plan files changed.

- [ ] **Step 6: Commit**

```bash
git add AGENTS.md
git commit -m "docs: document host route primitives"
```

Stage `docs/superpowers/specs/2026-06-01-hostnet-route-design.md` or `docs/superpowers/plans/2026-06-01-hostnet-route.md` only if they are intentionally part of this implementation branch and still uncommitted. Do not stage incidental plan checkbox edits.

---

## Plan self-review checklist

- Spec coverage:
  - IPv4 only: Task 1 constants and Task 2 validation.
  - Forwarding read/check only: Task 2 and Task 4 acceptance setup.
  - No forwarding mutation inside route package: Task 2 sysctl reader only.
  - Add/Replace/Delete/List/Get: Task 3.
  - Interface pre-reserved but implementation-limited: Task 1 types and Task 2 validation.
  - Explicit metric: Task 1 and Task 2 validation.
  - Gateway/Destination modes: Task 1 and Task 2 validation.
  - `GetRoute` as `ip route get`: Task 3 and Task 4 acceptance.
  - Missing `DeleteRoute` idempotent success: Task 3 and Task 4 acceptance.
  - Acceptance route primitives only: Task 4.
- Placeholder scan: no placeholder markers or vague error-handling instructions are present.
- Type consistency:
  - `Metric` and `MetricFilter` are distinct.
  - `GatewayAny` is part of `GatewayMode` and filter-only.
  - `DestinationAny` is part of `DestinationMode` and filter-only.
  - `LinkByIndex` is included in the handle used by `GetRoute`.
  - `scripts/acceptance.sh` owns forwarding setup.
