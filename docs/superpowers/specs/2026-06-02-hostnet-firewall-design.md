# Hostnet Firewall Design

## Summary

Govirta needs a host firewall primitive layer to complete the current single-node networking path after `hostnet/link` and `hostnet/route`. The new layer will live under `internal/hostnet/firewall`, parallel to `link` and `route`, and will manage only Govirta-owned nftables rules. It will not create bridge/TAP devices, enable IPv4 forwarding, start QEMU, infer guest identity, or mutate non-Govirta firewall state.

The first implementation targets IPv4 nftables NAT and endpoint anti-spoofing:

1. IPv4 postrouting MASQUERADE for an explicit guest CIDR and explicit egress interface.
2. Per-VM/TAP IP spoofing protection with explicit `TapName + MAC + IPv4` identity.
3. A stable table/chain/rule model reserved for future upper-layer abstractions such as Kubernetes-style NetworkPolicy.

## Goals

- Add a dedicated firewall primitive boundary that follows the existing `hostnet/link` and `hostnet/route` package shape.
- Keep all behavior-affecting fields explicit: no default table, chain, hook, priority, CIDR, egress interface, TAP, MAC, or IP inference.
- Use nftables as the first Linux backend, with `google/nftables` hidden behind a narrow internal handle interface.
- Implement IPv4 NAT MASQUERADE in a NAT postrouting chain.
- Implement per-endpoint anti-spoofing in a bridge-family filter chain.
- Return observed firewall state after ensure operations, not request echoes.
- Preserve room for future NetworkPolicy-like orchestration without exposing a raw firewall DSL in the first version.

## Non-goals

- No iptables backend in the first version.
- No nft CLI production backend in the first version. CLI output may be used as acceptance evidence.
- No IPv6, inet-family NAT, DNAT, port forwarding, fixed SNAT, connection policy, or Kubernetes selector logic in the first version.
- No sysctl mutation. `net.ipv4.ip_forward` remains an installation or acceptance setup responsibility; `hostnet/route` continues to provide read/check semantics only.
- No bridge/TAP creation in the firewall package.
- No QEMU integration in the firewall package.
- No automatic discovery of VM IP, MAC, TAP, bridge, egress interface, or existing firewall backend.

## Architecture

The package layout mirrors the existing hostnet primitives:

```text
internal/hostnet/firewall/
├── firewall.go                 # Manager interface and public request/result structs
├── constants.go                # typed constants and explicit optional-value wrappers
├── noop.go                     # unsupported no-op manager for composition tests
├── noop_test.go
├── firewallerr/
│   └── errors.go               # stable firewall sentinel errors
└── linux/
    ├── manager_linux.go        # Linux nftables-backed Manager
    ├── handle_linux.go         # narrow handle interface plus real google/nftables adapter
    ├── validate_linux.go       # explicit request validation
    ├── info_linux.go           # nftables rule observation and RuleInfo translation
    ├── errors_linux.go         # Linux/nftables error classification
    ├── fake_handle_test.go     # fake ruleset, call recording, failure injection
    ├── validation_test.go
    ├── masquerade_test.go
    ├── anti_spoofing_test.go
    ├── list_get_test.go
    └── errors_test.go
```

The orchestration call order should remain layered:

```text
orchestration
  -> hostnet/link       # bridge/TAP/address primitives
  -> hostnet/route      # route lifecycle and IPv4 forwarding readiness checks
  -> hostnet/firewall   # NAT and filtering primitives
  -> virt/qemu          # argv builder consumes an already-created TAP
```

Upper layers depend on root `firewall.Manager` and request/result types. The Linux implementation may use `google/nftables`, but that dependency must not leak into the root package API.

## Public API shape

The first version exposes high-level operations for the two supported rule purposes plus observation methods:

```go
type Manager interface {
    EnsureMasquerade(ctx context.Context, spec MasqueradeSpec) (RuleInfo, error)
    DeleteMasquerade(ctx context.Context, ref RuleRef) error

    EnsureEndpointAntiSpoofing(ctx context.Context, spec EndpointAntiSpoofingSpec) (RuleInfo, error)
    DeleteEndpointAntiSpoofing(ctx context.Context, ref RuleRef) error

    GetRule(ctx context.Context, query RuleQuery) (RuleInfo, error)
    ListRules(ctx context.Context, filter RuleFilter) ([]RuleInfo, error)
}
```

The package will define generic table, chain, and rule types so future policy layers have a stable vocabulary, but the first version will not expose a raw arbitrary rule builder. This prevents the primitive from becoming a prematurely-general firewall DSL while still reserving the right abstractions.

Required typed concepts include:

- `TableFamily`: first version supports `TableFamilyIPv4` and `TableFamilyBridge`.
- `TableName`, `ChainName`, `InterfaceName`, `RuleOwner`, `RuleHandle`, and `RuleSummary` as dedicated types.
- `ChainType`: first version supports `ChainTypeNAT` and `ChainTypeFilter`.
- `Hook`: first version supports NAT `HookPostrouting` and bridge filter `HookForward`.
- `Priority`: explicit numeric or named-priority representation; callers must set it.
- `RulePurpose`: first version supports `RulePurposeMasquerade` and `RulePurposeEndpointAntiSpoofing`; future values may include NetworkPolicy ingress/egress purposes.

## NAT MASQUERADE semantics

`EnsureMasquerade` requires every behavior-affecting value explicitly:

```go
type MasqueradeSpec struct {
    TableName           TableName
    ChainName           ChainName
    RuleOwner           RuleOwner
    GuestCIDR           netip.Prefix
    EgressInterfaceName InterfaceName
    Priority            Priority
}
```

The Linux nftables backend ensures an equivalent rule:

```nft
table ip <TableName> {
  chain <ChainName> {
    type nat hook postrouting priority <Priority>; policy accept;
    ip saddr <GuestCIDR> oifname <EgressInterfaceName> masquerade
  }
}
```

Rules:

- `GuestCIDR` must be an IPv4 prefix.
- `EgressInterfaceName` must be explicit.
- `Priority` must be explicit; the recommended value is source NAT priority, but the API must not silently choose it.
- The operation must not enable IPv4 forwarding.
- The operation must not flush the global ruleset or delete non-Govirta rules.
- The operation only manages rules carrying Govirta-recognizable owner/purpose identity.

## Endpoint anti-spoofing semantics

`EnsureEndpointAntiSpoofing` binds one VM endpoint identity to one TAP:

```go
type EndpointAntiSpoofingSpec struct {
    TableName  TableName
    ChainName  ChainName
    RuleOwner  RuleOwner
    BridgeName InterfaceName
    TapName    InterfaceName
    MAC        net.HardwareAddr
    IPv4       netip.Addr
    Priority   Priority
}
```

The Linux nftables backend uses the bridge family so filtering happens on bridged guest traffic. The intended ruleset shape is:

```nft
table bridge <TableName> {
  chain <ChainName> {
    type filter hook forward priority <Priority>; policy accept;

    iifname <TapName> ether saddr != <MAC> drop
    iifname <TapName> ether type ip ip saddr != <IPv4> drop
    iifname <TapName> ether type arp arp saddr ether != <MAC> drop
    iifname <TapName> ether type arp arp saddr ip != <IPv4> drop
  }
}
```

Rules:

- The caller must pass `TapName`, `MAC`, and `IPv4`; the firewall layer must not probe them from the host.
- `MAC` must be a unicast hardware address.
- `IPv4` must be a unicast IPv4 address.
- The rule guards packets entering from the TAP. It does not define guest addressing policy on its own; upper orchestration owns endpoint assignment.
- The first version uses drop guards rather than broad accept rules so it does not change unrelated traffic in the same chain.
- `BridgeName` is explicit identity/context for the endpoint and future policy grouping. If the first Linux rule translation does not need it for an expression, it is still validated and preserved in `RuleInfo`/identity so upper-layer intent remains explicit.

## Rule identity, observation, and idempotency

Ensure operations must return observed state from nftables, not request echoes. `RuleInfo` should include at least:

```go
type RuleInfo struct {
    Ref       RuleRef
    Family    TableFamily
    TableName TableName
    ChainName ChainName
    Purpose   RulePurpose
    Owner     RuleOwner
    Handle    RuleHandle
    Summary   RuleSummary
}
```

Idempotency and conflict behavior:

- Repeating `EnsureMasquerade` with the same owner, purpose, guest CIDR, egress interface, table, chain, and priority is idempotent.
- Repeating `EnsureEndpointAntiSpoofing` with the same owner, purpose, TAP, MAC, IPv4, table, chain, and priority is idempotent.
- A matching owner/purpose/TAP with different MAC or IPv4 is a conflict and returns `firewallerr.ErrConflict`.
- A matching owner/purpose NAT rule with different guest CIDR or egress interface is a conflict and returns `firewallerr.ErrConflict`.
- If a kernel-assigned handle changes but the observed semantic rule is equivalent, the manager returns the current observed handle.
- Delete operations only remove rules matching the explicit `RuleRef` and expected owner/purpose identity.
- `RuleRef` is a compact owner-scoped identity: `Owner`, `Purpose`, `TableFamily`, `TableName`, `ChainName`, and kernel `Handle`. Endpoint identity such as TAP/MAC/IP remains in `RuleInfo.Summary`, not in `RuleRef`.
- `GetRule` and `ListRules` return Govirta-recognizable rules. The first version does not expose an API that observes every host firewall rule.

## Error model

`internal/hostnet/firewall/firewallerr/errors.go` defines stable sentinels:

```go
var (
    ErrInvalidRequest       = errors.New("invalid firewall request")
    ErrInvalidObservedState = errors.New("invalid observed firewall state")
    ErrNotFound             = errors.New("firewall rule not found")
    ErrAlreadyExists        = errors.New("firewall rule already exists")
    ErrConflict             = errors.New("firewall rule conflict")
    ErrPermission           = errors.New("firewall permission denied")
    ErrIncompleteList       = errors.New("incomplete firewall rule list")
    ErrUnsupported          = errors.New("unsupported firewall operation")
)
```

Linux error translation should follow the route/link pattern:

- Permission errors classify as `ErrPermission` while preserving the original cause.
- Missing table/chain/rule errors classify as `ErrNotFound`.
- Duplicate objects classify as `ErrAlreadyExists` or `ErrConflict` depending on whether the duplicate is semantically equivalent.
- Unparseable or incomplete observed nftables expressions classify as `ErrInvalidObservedState` or `ErrIncompleteList`.
- `ctx.Err()` is returned visibly and must not be hidden behind a generic firewall error.
- If rollback or cleanup fails after a primary failure, return `errors.Join(primaryErr, cleanupErr)`.

## Validation rules

Validation happens before touching nftables.

`MasqueradeSpec` validation:

- `ctx` is non-nil and not already canceled.
- `TableName`, `ChainName`, and `RuleOwner` are non-empty safe names using the same character class as existing hostnet link names: ASCII letter, digit, underscore, dot, or hyphen; names cannot be `.` or `..`.
- `GuestCIDR` is a valid IPv4 prefix.
- `EgressInterfaceName` is non-empty.
- `Priority` is explicitly set.
- Unsupported first-version features such as IPv6, inet family, DNAT, fixed SNAT, and port forwarding are rejected with `firewallerr.ErrInvalidRequest` or `firewallerr.ErrUnsupported` as appropriate.

`EndpointAntiSpoofingSpec` validation:

- `ctx` is non-nil and not already canceled.
- `TableName`, `ChainName`, and `RuleOwner` are non-empty safe names using the same character class as existing hostnet link names: ASCII letter, digit, underscore, dot, or hyphen; names cannot be `.` or `..`.
- `BridgeName` and `TapName` are non-empty interface names using the existing hostnet link-name validation style.
- `MAC` is a unicast hardware address.
- `IPv4` is a unicast IPv4 address.
- `Priority` is explicitly set.
- Missing MAC/IP identity is invalid; host probing is forbidden.

## Testing strategy

Unit tests use a fake handle that stores an in-memory ruleset, records calls, and supports failure injection. Tests should not require real nftables, root privileges, QEMU, TAP devices, or Linux-only host state.

Required unit coverage:

- `EnsureMasquerade` creates table, chain, and rule.
- `EnsureMasquerade` is idempotent for equivalent rules.
- `EnsureMasquerade` detects semantic conflicts.
- `EnsureEndpointAntiSpoofing` creates the four endpoint drop guards.
- `EnsureEndpointAntiSpoofing` is idempotent for equivalent endpoint identity.
- Same TAP with different MAC or IPv4 returns `firewallerr.ErrConflict`.
- Missing or unsupported fields return `firewallerr.ErrInvalidRequest` or `firewallerr.ErrUnsupported`.
- Already-canceled context does not touch the handle.
- Linux permission/not-found/observed-state failures map to firewall sentinels while preserving causes.
- Cleanup or rollback failures are preserved through `errors.Join`.

Acceptance coverage belongs under `test/acceptance/hostnet_firewall_test.go` and runs through `scripts/acceptance.sh full` in the Lima Linux guest.

Required first acceptance tests:

1. `TestHostnetFirewallMasqueradePrimitives`
   - Ensure a NAT masquerade rule with an explicit guest CIDR and egress interface.
   - Verify it is observable through both the manager and `nft list ruleset`.
   - Delete it and verify it disappears.
2. `TestHostnetFirewallAntiSpoofingPrimitives`
   - Create a bridge and TAP using the existing hostnet link primitive or test setup.
   - Ensure endpoint anti-spoofing for explicit TAP, MAC, and IPv4.
   - Verify the four drop guards are observable through both the manager and `nft list ruleset`.
   - Delete them and verify cleanup.

Packet-level spoofed-drop validation with namespaces/veth is not required for the first primitive acceptance test. It belongs in the later end-to-end network orchestration acceptance once guest addressing, default route, DNS, and external connectivity are part of the tested flow.

Full guest-to-external connectivity is a later end-to-end orchestration test because it combines link, route, firewall, QEMU, guest default route, DNS, and NAT behavior. The primitive layer acceptance should first prove rule lifecycle correctness.

## Official documentation and external sources

Actual documentation commands and sources used during design:

- `ctx7 library "google/nftables" "How to create nftables nat postrouting masquerade and bridge filtering rules in Go"`
- `ctx7 docs /google/nftables "How to create nftables NAT postrouting masquerade rules and bridge family anti spoofing filter rules in Go"`
- `ctx7 docs /websites/wiki_nftables_wiki-nftables "How to configure nftables bridge family anti spoofing rules matching ether source and IPv4 source plus NAT postrouting masquerade"`
- nftables NAT wiki: `https://wiki.nftables.org/wiki-nftables/index.php/Performing_Network_Address_Translation_(NAT)`
- nftables bridge filtering wiki: `https://wiki.nftables.org/wiki-nftables/index.php/Bridge_filtering`
- nftables netfilter hooks wiki: `https://wiki.nftables.org/wiki-nftables/index.php/Netfilter_hooks`
- nftables man page: `https://www.netfilter.org/projects/nftables/manpage.html`
- `google/nftables`: `https://github.com/google/nftables` and `https://pkg.go.dev/github.com/google/nftables`

Key verified points:

- nftables NAT masquerade belongs in a NAT postrouting chain and is a source NAT operation.
- nftables bridge family supports bridge-path filtering and can match Ethernet, IPv4, and ARP fields.
- `google/nftables` supports NAT chains, masquerade expressions, filtering expressions, sets/maps, rule listing, and rule deletion, but its upstream README describes it as early-stage; Govirta must isolate it behind its own API.

## Implementation planning decisions

- Use the existing hostnet link-name validation style for firewall table, chain, owner, and interface names: ASCII letter, digit, underscore, dot, or hyphen; reject empty names plus `.` and `..`.
- Prove exact `google/nftables` expression encoding for ARP sender MAC/IP and bridge-family payload offsets with focused Linux unit or acceptance coverage before relying on broader guest connectivity tests.
- Use compact `RuleRef` identity: owner, purpose, family, table, chain, and kernel handle. Keep semantic details in `RuleInfo.Summary`.
- First primitive acceptance verifies nftables lifecycle and observed ruleset state. Packet-level spoofed-drop validation is deferred to the later full network orchestration acceptance.
