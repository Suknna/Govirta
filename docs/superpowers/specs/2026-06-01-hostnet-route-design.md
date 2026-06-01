# internal/hostnet/route Design

Date: 2026-06-01
Status: Implemented and verified in branch `hostnet-route`

## Summary

Add `internal/hostnet/route` as Govirta's host IPv4 route primitive layer. The package checks the current IPv4 forwarding state and manages Linux IPv4 route entries while hiding `vishvananda/netlink` and `/proc/sys/net/ipv4/ip_forward` details from upper layers.

The first version is intentionally narrow in implementation but explicit in API shape: it supports IPv4, main table, unicast, direct or single-gateway routes, explicit metrics, route listing, route lookup, and forwarding readiness checks. It does not mutate IPv4 forwarding, does not configure NAT/MASQUERADE/firewall rules, and does not attempt VM external-network end-to-end acceptance.

## Goals

- Provide a stable Govirta-owned route primitive contract under `internal/hostnet/route`.
- Check runtime IPv4 forwarding state without modifying it.
- Manage host IPv4 routes through `AddRoute`, `ReplaceRoute`, `DeleteRoute`, `ListRoutes`, and `GetRoute`.
- Keep all behavior-affecting values explicit: family, destination mode, gateway mode, table, type, scope, protocol, metric, and output link.
- Return observed kernel state after successful route mutation.
- Match existing `internal/hostnet/link` patterns: root contract package, Linux implementation subpackage, private handle abstraction, fake-based unit tests, stable error subpackage, and Linux acceptance coverage.
- Preserve future multi-node extension space without pretending to implement it in the first version.

## Non-goals

- Do not write `/proc/sys/net/ipv4/ip_forward`.
- Do not write persistent sysctl config such as `/etc/sysctl.d/*.conf`.
- Do not manage per-interface forwarding.
- Do not implement NAT, MASQUERADE, nftables, iptables, or firewall policy.
- Do not manage DHCP or DNS.
- Do not configure guest-internal routes.
- Do not create bridge/TAP/address resources; those remain in `internal/hostnet/link`.
- Do not implement policy routing rules, non-main route tables, ECMP/multipath, VRF, IPv6, or non-unicast route types in the first version.
- Do not make VM external-network reachability the acceptance target for this package.

## Package structure

```text
internal/hostnet/route/
├── route.go              # Manager, RouteSpec, RouteFilter, RouteQuery, RouteInfo
├── constants.go          # Strong typed constants
├── forwarding.go         # IPv4 forwarding read/check types
├── noop.go               # NoopManager
├── routeerr/
│   └── errors.go         # Stable route error classes
└── linux/
    ├── manager_linux.go  # Linux netlink + forwarding check implementation
    ├── handle_linux.go   # netlink handle abstraction
    ├── sysctl_linux.go   # /proc/sys/net/ipv4/ip_forward reader abstraction
    └── *_test.go
```

The root package is the only package upper layers should depend on. `internal/hostnet/route/linux` contains Linux-specific implementation details and must not leak `netlink.Route`, table IDs, scope integers, or `/proc` paths as caller responsibilities.

## API contract

### Manager

```go
type Manager interface {
    GetIPv4Forwarding(ctx context.Context) (IPv4ForwardingInfo, error)
    CheckIPv4Forwarding(ctx context.Context, expected IPv4ForwardingState) (IPv4ForwardingInfo, error)

    AddRoute(ctx context.Context, spec RouteSpec) (RouteInfo, error)
    ReplaceRoute(ctx context.Context, spec RouteSpec) (RouteInfo, error)
    DeleteRoute(ctx context.Context, spec RouteSpec) error
    ListRoutes(ctx context.Context, filter RouteFilter) ([]RouteInfo, error)
    GetRoute(ctx context.Context, query RouteQuery) (RouteInfo, error)
}
```

All methods require a caller-provided context. `ctx == nil` returns `routeerr.ErrInvalidRequest`. If `ctx.Err()` is already non-nil, the method returns that context error directly so `errors.Is(err, context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)` work.

### Strong types and constants

The first version supports only IPv4/main/unicast/static routes, but the API explicitly models route-shaping fields so future work can extend the allowed values without redesigning the contract.

```go
type Family string
type RouteTable string
type RouteType string
type RouteScope string
type RouteProtocol string

const (
    FamilyIPv4 Family = "ipv4"

    RouteTableMain RouteTable = "main"

    RouteTypeUnicast RouteType = "unicast"

    RouteScopeGlobal RouteScope = "global"
    RouteScopeLink   RouteScope = "link"
    RouteScopeHost   RouteScope = "host" // reserved for future observed-route support

    RouteProtocolStatic      RouteProtocol = "static"
    RouteProtocolKernel      RouteProtocol = "kernel"      // observed routes only
    RouteProtocolBoot        RouteProtocol = "boot"        // observed routes only
    RouteProtocolDHCP        RouteProtocol = "dhcp"        // observed routes only
    RouteProtocolUnspecified RouteProtocol = "unspecified" // observed route/path-selection results only
)
```

Unsupported future values return `routeerr.ErrUnsupported`. Empty values return `routeerr.ErrInvalidRequest`.

### Metric

Metric must be explicit because Linux metric `0` can be meaningful and cannot double as a Go zero-value default.

```go
type Metric struct {
    Value uint32
    Set   bool
}

func ExplicitMetric(value uint32) Metric {
    return Metric{Value: value, Set: true}
}
```

Validation:

- `Metric.Set == false` returns `routeerr.ErrInvalidRequest`.
- `Metric.Set == true && Value == 0` is valid and means explicit metric 0.
- `Metric.Set == true && Value > 0` is valid.

### Gateway

Gateway semantics must be explicit. Direct/link routes must say they have no gateway instead of relying on a zero `netip.Addr`.

```go
type GatewayMode string

const (
    GatewayNone GatewayMode = "none"
    GatewayIPv4 GatewayMode = "ipv4"
    GatewayAny  GatewayMode = "any" // filter only
)

type Gateway struct {
    Mode GatewayMode
    Addr netip.Addr
}
```

Validation:

- `GatewayNone` requires `Addr` to be zero.
- `GatewayIPv4` requires a valid IPv4 address that is not unspecified or multicast. The first version does not infer whether the gateway is reachable through `LinkName`; upper layers own that orchestration.
- `GatewayAny` is allowed only in `RouteFilter`, never in `RouteSpec`.
- Empty or unknown mode returns `routeerr.ErrInvalidRequest` or `routeerr.ErrUnsupported` depending on whether it is malformed or a known future value.

### Destination

Default route semantics must be explicit. Callers express “default route” directly instead of passing `0.0.0.0/0` as a magic value.

```go
type DestinationMode string

const (
    DestinationCIDR    DestinationMode = "cidr"
    DestinationDefault DestinationMode = "default"
    DestinationAny     DestinationMode = "any" // filter only
)

type Destination struct {
    Mode DestinationMode
    CIDR netip.Prefix
}
```

Validation:

- `DestinationCIDR` requires a valid IPv4 prefix.
- `DestinationDefault` requires `CIDR` to be zero and maps internally to `0.0.0.0/0`.
- `DestinationAny` is allowed only in `RouteFilter`, never in `RouteSpec`.
- Empty mode returns `routeerr.ErrInvalidRequest`.

Allowed first-version `RouteSpec` combinations:

| Route shape | Destination | Gateway | Scope | Notes |
| --- | --- | --- | --- | --- |
| Direct/link route | `DestinationCIDR` | `GatewayNone` | `RouteScopeLink` | Example: `198.51.100.0/24 dev gvrt0 scope link` |
| Gateway route | `DestinationCIDR` | `GatewayIPv4` | `RouteScopeGlobal` | Example: `10.0.0.0/8 via 192.168.100.1 dev gvbr0` |
| Default gateway route | `DestinationDefault` | `GatewayIPv4` | `RouteScopeGlobal` | Example: `default via 192.168.1.1 dev eth0` |

Other combinations return `routeerr.ErrInvalidRequest` when internally contradictory and `routeerr.ErrUnsupported` when they represent a known future capability. `RouteScopeHost` is modeled for future observed-route support but is not accepted in first-version `RouteSpec` mutations.

### RouteSpec

```go
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
```

Allowed first-version specs:

- IPv4 only.
- Main table only.
- Unicast type only.
- Static protocol only.
- Scope `global` or `link` according to the allowed combination matrix above.
- Direct route with `GatewayNone`.
- Gateway route with `GatewayIPv4`.
- Default route with `DestinationDefault`.

Rejected first-version specs:

- IPv6.
- Non-main table.
- Non-unicast type.
- Non-static protocol.
- ECMP/multipath.
- VRF.
- Policy routing rules.
- Blackhole, prohibit, unreachable, or NAT route types.

### Route filtering

Filters also avoid implicit empty values.

```go
type LinkFilterMode string

const (
    LinkAny  LinkFilterMode = "any"
    LinkName LinkFilterMode = "name"
)

type LinkFilter struct {
    Mode LinkFilterMode
    Name link.Name
}

type MetricFilterMode string

const (
    MetricAny   MetricFilterMode = "any"
    MetricValue MetricFilterMode = "value"
)

type MetricFilter struct {
    Mode  MetricFilterMode
    Value uint32
}

type RouteFilter struct {
    Family      Family
    Table       RouteTable
    Link        LinkFilter
    Destination Destination
    Gateway     Gateway
    Metric      MetricFilter
}
```

`RouteFilter.Gateway` reuses `GatewayNone` and `GatewayIPv4` for concrete filtering. To list both direct and gateway routes, callers pass `Gateway{Mode: GatewayAny}`. `GatewayAny` is filter-only and is invalid in `RouteSpec`.

`RouteFilter.Metric` uses `MetricAny` or `MetricValue` so metric 0 can still be explicitly filtered. It does not reuse `Metric{Set:false}` as a hidden “any” default.

`ListRoutes` returns routes sorted by table, destination, link name, gateway, and metric for stable tests and deterministic callers.

### Route query

`GetRoute` maps to `ip route get <destination-ip>` semantics. It asks the kernel how a packet to a destination would be routed; it does not perform exact route-entry existence checks.

```go
type RouteQuery struct {
    Family      Family
    Destination netip.Addr
}
```

Validation:

- `Family` must be `FamilyIPv4`.
- `Destination` must be a valid IPv4 address.

Exact route-entry checks belong to `ListRoutes` plus caller-side matching.

### RouteInfo

```go
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

`RouteInfo` is always based on observed kernel state after translation from netlink output. It must not simply echo the requested spec when a route operation succeeds.

For mutation observations, first-version implementation accepts observed `RouteProtocolStatic` for routes Govirta just created or replaced. `RouteSpec` mutation requests still accept only `RouteProtocolStatic`; `RouteProtocolUnspecified` is not a valid mutation protocol. `ListRoutes` and `GetRoute` may also map common observed Linux protocols such as `kernel`, `boot`, and `dhcp`. Linux `RouteGet` can return protocol `0` (`RTPROT_UNSPEC`) for path-selection results; that observed-only value maps to `RouteProtocolUnspecified`. Unknown protocol numbers still return `routeerr.ErrInvalidObservedState` rather than being hidden or echoed from the request.

Observed protocol mapping:

| Linux protocol | Govirta value | Mutation support |
| --- | --- | --- |
| `0` / `RTPROT_UNSPEC` | `RouteProtocolUnspecified` | Observed route/path-selection results only; `RouteSpec` mutation rejects it. |
| `RTPROT_STATIC` | `RouteProtocolStatic` | The only protocol accepted by first-version `RouteSpec` mutations. |
| `RTPROT_KERNEL` | `RouteProtocolKernel` | Observed only. |
| `RTPROT_BOOT` | `RouteProtocolBoot` | Observed only. |
| `RTPROT_DHCP` | `RouteProtocolDHCP` | Observed only. |
| Unknown protocol number | `routeerr.ErrInvalidObservedState` | Never hidden, guessed, or echoed from the request. |

## IPv4 forwarding check

Forwarding is a node installation prerequisite. The route package can read and check it, but never mutate it.

```go
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

`GetIPv4Forwarding` reads `/proc/sys/net/ipv4/ip_forward` and maps:

- `"1"` to `IPv4ForwardingEnabled`.
- `"0"` to `IPv4ForwardingDisabled`.
- Any other content to `routeerr.ErrInvalidObservedState`.

`CheckIPv4Forwarding(ctx, expected)` validates that `expected` is `IPv4ForwardingEnabled` or `IPv4ForwardingDisabled`, reads the current state, and returns `routeerr.ErrNotReady` when the observed state differs.

The error message should state that forwarding is configured by node installation or operations tooling, not by `internal/hostnet/route`.

## Linux implementation

### System interaction boundary

`internal/hostnet/route/linux.Manager` interacts only with:

- `github.com/vishvananda/netlink` for route and link-index operations.
- `/proc/sys/net/ipv4/ip_forward` for forwarding status reads.

It must not shell out to `ip`, `route`, `sysctl`, `iptables`, or `nft`.

### handle abstraction

```go
type handle interface {
    LinkByIndex(index int) (netlink.Link, error)
    LinkByName(name string) (netlink.Link, error)
    RouteAdd(route *netlink.Route) error
    RouteReplace(route *netlink.Route) error
    RouteDel(route *netlink.Route) error
    RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error)
    RouteGet(destination net.IP) ([]netlink.Route, error)
}
```

The real handle is a thin wrapper over `netlink` package functions. Tests use a fake handle that records calls, stores fake links and routes, and injects errors.

### forwarding reader abstraction

```go
type forwardingReader interface {
    ReadIPv4Forwarding(ctx context.Context) (string, error)
}
```

The real reader reads `/proc/sys/net/ipv4/ip_forward`. Tests inject a fake reader.

### AddRoute and ReplaceRoute

Both methods:

1. Validate context.
2. Validate `RouteSpec`.
3. Reject unsupported future values with `routeerr.ErrUnsupported`.
4. Resolve `spec.LinkName` with `LinkByName`.
5. Build `netlink.Route`:
   - `LinkIndex` from the resolved link.
   - `Dst` from `DestinationCIDR` or `DestinationDefault` mapped to `0.0.0.0/0`.
   - `Gw` from `GatewayIPv4`, nil for `GatewayNone`.
   - `Table = unix.RT_TABLE_MAIN`.
   - `Type = unix.RTN_UNICAST`.
   - `Scope = netlink.SCOPE_UNIVERSE` for `RouteScopeGlobal` or `netlink.SCOPE_LINK` for `RouteScopeLink`. `SCOPE_HOST` is reserved for observed route mapping and is not accepted in first-version mutations.
   - `Protocol = unix.RTPROT_STATIC`.
   - `Priority = int(spec.Metric.Value)`.
   - `Family = netlink.FAMILY_V4`.
6. Call `RouteAdd` or `RouteReplace`.
7. Re-read observed route state using `RouteListFiltered` and return matching `RouteInfo`.

The post-mutation observation must match the full requested route identity: destination, output link, gateway, table, type, scope, protocol, and metric. If `RouteListFiltered` cannot express part of that identity directly, the implementation filters the returned routes in Go before returning `RouteInfo`.

If the route cannot be observed immediately after a successful mutation, return `routeerr.ErrNotFound` or `routeerr.ErrInvalidObservedState` with operation context; do not fabricate success from the requested spec.

### DeleteRoute

`DeleteRoute` validates the same `RouteSpec` and constructs the same netlink route key. It deletes only the exact route described by the spec.

Behavior:

- Missing route returns nil for idempotent cleanup. This intentionally differs from strict `ip route del` behavior because Govirta host primitive cleanup must be safe to repeat.
- Permission errors return `routeerr.ErrPermission`.
- Other netlink errors are translated and wrapped with operation context.
- The method must not clear tables, delete by destination alone, or delete routes with different link, gateway, scope, protocol, or metric.

### ListRoutes

`ListRoutes` maps to filtered `ip route show` semantics.

Validation:

- `Family` must be `FamilyIPv4`.
- `Table` must be `RouteTableMain`.
- `Destination.Mode` must be explicit: `DestinationAny`, `DestinationCIDR`, or `DestinationDefault`.
- `Gateway.Mode` must be explicit: `GatewayAny`, `GatewayNone`, or `GatewayIPv4`.
- `Link.Mode` must be explicit: `LinkAny` or `LinkName`.
- `Metric.Mode` must be explicit: `MetricAny` or `MetricValue`.

Implementation:

- Always include `RT_FILTER_TABLE` with main table.
- Add `RT_FILTER_OIF` when `LinkName` is selected.
- Add `RT_FILTER_DST` for `DestinationCIDR` or `DestinationDefault`.
- Add `RT_FILTER_GW` for `GatewayIPv4`.
- For `GatewayNone`, filter returned routes to those with no gateway if netlink filtering cannot express “no gateway”.
- For `MetricValue`, use `RT_FILTER_PRIORITY` when available and still verify exact metric in Go before returning results.
- Map `netlink.ErrDumpInterrupted` to `routeerr.ErrIncompleteList` and return no partial success.

### GetRoute

`GetRoute` maps to `netlink.RouteGet(destination)` and returns the kernel's primary route-get result.

Behavior:

- No returned routes means `routeerr.ErrNotFound`.
- If multiple routes are returned, use the first route as the primary route-get result.
- Resolve `LinkIndex` back to `link.Name` before returning `RouteInfo`.
- If link-name resolution fails, return an error instead of silently returning incomplete route information.

## Error package

`internal/hostnet/route/routeerr` defines stable error classes:

```go
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

Linux translation maps:

- `EPERM` / `EACCES` to `ErrPermission`.
- `EEXIST` to `ErrAlreadyExists`.
- `ESRCH` / `ENOENT` / netlink not-found classes to `ErrNotFound`.
- `EINVAL` to `ErrInvalidRequest`.
- `netlink.ErrDumpInterrupted` to `ErrIncompleteList`.
- Missing `/proc/sys/net/ipv4/ip_forward` to `ErrUnsupported`.
- Unexpected forwarding content to `ErrInvalidObservedState`.

All translated errors preserve the original cause so callers can use `errors.Is` and `errors.As`.

## Noop manager

`NoopManager` mirrors `internal/hostnet/link.NoopManager`:

- Nil context returns `routeerr.ErrInvalidRequest`.
- Canceled/deadline context returns the context error.
- Live operations return `routeerr.ErrUnsupported`.

All `NoopManager` methods other than nil/canceled context handling return `routeerr.ErrUnsupported`, including forwarding reads/checks, route mutations, route listing, and route lookup.

## Testing strategy

### Unit tests

Regular `go test ./...` must not require root and must not mutate host routes. Linux implementation tests use fake handle and fake forwarding reader.

Files:

```text
internal/hostnet/route/noop_test.go
internal/hostnet/route/linux/validation_test.go
internal/hostnet/route/linux/forwarding_test.go
internal/hostnet/route/linux/route_test.go
internal/hostnet/route/linux/list_get_test.go
internal/hostnet/route/linux/errors_test.go
internal/hostnet/route/linux/fake_handle_test.go
```

Required coverage:

- Nil and canceled contexts for every manager method.
- All explicit validation rules for family, table, type, protocol, scope, metric, destination, gateway, and link name.
- `Metric.Set == true && Value == 0` is valid.
- IPv6 and future route capabilities return `ErrUnsupported`.
- Forwarding reader maps `0`, `1`, malformed content, permission errors, and missing proc path correctly.
- `AddRoute` supports direct, gateway, and default route specs.
- `AddRoute` maps existing route to `ErrAlreadyExists`.
- `ReplaceRoute` adds when missing and replaces when existing.
- `DeleteRoute` is idempotent when missing.
- `DeleteRoute` does not delete routes with different metric, gateway, link, scope, protocol, or destination.
- `ListRoutes` filters by destination, gateway, and link and returns stable order.
- `ListRoutes` filters by metric and does not conflate metric 0 with an unset/any filter.
- `ListRoutes` maps dump interruption to `ErrIncompleteList`.
- `GetRoute` returns the primary route-get result and fails when link-name resolution fails.
- Error translation preserves sentinel and original cause.

### Acceptance test

Add:

```text
test/acceptance/hostnet_route_test.go
```

Build tag:

```go
//go:build acceptance && linux
```

The test must call `requireHostnetAcceptanceEnv(t)` so it only runs in the configured Lima Linux acceptance environment.

Acceptance setup updates:

- `scripts/acceptance.sh` explicitly sets IPv4 forwarding during acceptance environment setup:
  ```sh
  sysctl -w net.ipv4.ip_forward=1
  ```
- This simulates node installation configuration. `internal/hostnet/route` still does not write forwarding. `lima/govirta.yaml` does not own this runtime precondition unless a later design explicitly moves node-install setup there.
- Logs continue to be written to `test/log/YYYY-MM-DD-HHMMSS-acceptance-full.log`.

Acceptance route path:

```text
dummy link: gvrt0
network:    198.51.100.0/24
probe IP:   198.51.100.10
metric:     100 then 200 after replace
```

Flow:

1. Create dummy link `gvrt0` and set it up.
2. Register cleanup to delete `gvrt0`.
3. Run `CheckIPv4Forwarding(ctx, IPv4ForwardingEnabled)` and require success.
4. Add direct route:
   ```text
   198.51.100.0/24 dev gvrt0 scope link metric 100
   ```
5. `ListRoutes` must observe the route.
6. `GetRoute(198.51.100.10)` must return `LinkName == gvrt0`.
7. `ReplaceRoute` changes metric to `200`.
8. `ListRoutes` must observe metric `200`.
9. `DeleteRoute` deletes the route.
10. A second `DeleteRoute` call succeeds idempotently.
11. `ListRoutes` confirms the route is absent.

Failure diagnostics:

- `ip route show table main`
- `ip route get 198.51.100.10`
- `sysctl net.ipv4.ip_forward`
- `ip link show gvrt0`
- RouteInfo values returned by Go APIs

Acceptance does not test VM external network access, NAT/MASQUERADE, firewall rules, policy routing, IPv6, ECMP, or multi-node behavior.

## Official documentation and source references

- Linux kernel IP sysctl documentation: `https://www.kernel.org/doc/html/latest/networking/ip-sysctl.html`
- Linux kernel raw `ip-sysctl.rst`: `https://raw.githubusercontent.com/torvalds/linux/master/Documentation/networking/ip-sysctl.rst`
- `/proc/sys/net` sysctl documentation: `https://docs.kernel.org/admin-guide/sysctl/net.html`
- `sysctl.d(5)` persistent sysctl configuration boundary: `https://man7.org/linux/man-pages/man5/sysctl.d.5.html`
- `ip-route(8)` route command semantics: `https://man7.org/linux/man-pages/man8/ip-route.8.html`
- `github.com/vishvananda/netlink` official repository: `https://github.com/vishvananda/netlink`
- Context7 library resolution command:
  ```text
  ctx7 library vishvananda/netlink "How to add replace delete list get Linux routes with RouteAdd RouteReplace RouteDel RouteListFiltered and configure IPv4 forwarding in Go"
  ```
- Context7 docs command:
  ```text
  ctx7 docs /vishvananda/netlink "How to add replace delete list get Linux routes with RouteAdd RouteReplace RouteDel RouteListFiltered RouteGet in Go"
  ```

Context7 had only bridge/address snippets for `/vishvananda/netlink`; route-specific behavior was verified against the official netlink repository source files `route.go`, `route_linux.go`, and `route_test.go`.

## Open questions deliberately deferred

- IPv6 forwarding and IPv6 routes.
- Per-interface forwarding checks.
- Non-main route tables and policy routing rules.
- ECMP/multipath.
- VRF.
- NAT/MASQUERADE/firewall backend design.
- VM guest external network end-to-end acceptance.
