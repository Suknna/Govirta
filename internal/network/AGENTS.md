# internal/network Knowledge Base

<!--
Verified-against:
  base_commit: e057cc0
  files:
    - internal/network/service.go
    - internal/network/nic_service.go
    - internal/network/netpool/network.go
    - internal/network/netpool/service.go
    - internal/network/netpool/orchestrate.go
    - internal/network/networker/errors.go
    - internal/hostnet/link/link.go
    - internal/hostnet/link/linux/manager_linux.go
    - internal/hostnet/route/route.go
    - internal/hostnet/route/linux/manager_linux.go
    - internal/hostnet/firewall/firewall.go
    - internal/hostnet/firewall/constants.go
    - internal/hostnet/firewall/linux/manager_linux.go
    - internal/hostnet/firewall/linux/forward_linux.go
    - internal/hostnet/firewall/linux/forward_expr_linux.go
    - internal/hostnet/dhcp/dhcp.go
    - internal/hostnet/dhcp/coredhcp/manager.go
    - test/acceptance/network_egress_test.go
  flows:
    - anchor: flow-network-ensure
      sources:
        - internal/network/service.go
        - internal/network/netpool/orchestrate.go
        - internal/hostnet/link/linux/manager_linux.go
        - internal/hostnet/route/linux/manager_linux.go
        - internal/hostnet/firewall/linux/manager_linux.go
        - internal/hostnet/dhcp/coredhcp/manager.go
    - anchor: flow-nic-ensure
      sources:
        - internal/network/nic_service.go
        - internal/network/netpool/orchestrate.go
        - internal/hostnet/link/linux/manager_linux.go
        - internal/hostnet/dhcp/coredhcp/manager.go
        - internal/hostnet/firewall/linux/manager_linux.go
    - anchor: flow-guest-egress
      sources:
        - internal/network/service.go
        - internal/network/nic_service.go
        - internal/network/netpool/orchestrate.go
        - test/acceptance/network_egress_test.go
-->

## OVERVIEW

VM-facing network orchestration layer mirroring the `internal/storage` layering: `NetworkService`/`NICService` are the VM-facing API, `netpool.Service` is the shared registration + orchestration core, and the `internal/hostnet/*` managers (link/route/firewall/dhcp) are the driver layer. The core stores declarative logical intent only; observed resource state is always read live from the primitives, never cached.

`EnsureNetwork`/`EnsureNIC` reconcile host primitives in a fixed dependency order and are idempotent; they never tear down already-created resources on partial failure. `DeleteNetwork`/`DeleteNIC` tear down in reverse order with `errors.Join` so every failure is preserved. The guest egress closure (bridge + IPv4 forwarding readiness + masquerade + forward-accept + static DHCP + endpoint anti-spoofing) is what turns the host primitives into real guest internet access.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| VM-facing network API | `service.go` | `NetworkService`; `RegisterNetwork`/`EnsureNetwork`/`DeleteNetwork`/`GetNetworkStatus`/`ListNetworks` over `*netpool.Service` |
| VM-facing NIC API | `nic_service.go` | `NICService`; `RegisterNIC`/`EnsureNIC`/`DeleteNIC`/`GetNICStatus`, shares one `*netpool.Service` with `NetworkService` |
| Registration core | `netpool/service.go` | `Service` registers/validates/clones logical definitions; `NewService(link, route, firewall, dhcp)` |
| Orchestration + live status | `netpool/orchestrate.go` | `EnsureNetwork`/`EnsureNIC`/`DeleteNetwork`/`DeleteNIC` order; `GetNetworkStatus`/`GetNICStatus` read live from primitives |
| Logical definitions | `netpool/network.go` | `NetworkName`, `VMID`, `NetworkDefinition`, `NICDefinition`, `networkRecord` + clone funcs |
| Error sentinels | `networker/errors.go` | `ErrInvalidRequest` / `ErrNotFound` / `ErrAlreadyExists` / `ErrConflict` / `ErrNotReady` |
| Host primitives (drivers) | `internal/hostnet/{link,route,firewall,dhcp}` | observed-truth managers injected into `netpool.Service`; see root `AGENTS.md` hostnet flows |
| End-to-end egress acceptance | `test/acceptance/network_egress_test.go` | `TestNetworkEgressEndToEnd`: boots CirrOS via orchestration API, guest pings `8.8.8.8` + `one.one.one.one` |

## CONVENTIONS

- The core stores declarative logical intent only. Observed state (`NetworkStatus`/`NICStatus`) is always read live from the primitives in `GetNetworkStatus`/`GetNICStatus`; the in-memory definition index is never returned as if it were observed truth.
- MAC is supplied by the control plane in `NICDefinition.MAC` and threaded unchanged to the TAP, the DHCP binding, and the endpoint anti-spoofing guard. The orchestration layer never generates a MAC.
- `Ensure*` is idempotent and never tears down already-created resources on partial failure; the caller decides whether to retry or `Delete*`.
- `Delete*` tears down in reverse dependency order and composes every failure with `errors.Join`. `DeleteNetwork` refuses (`networker.ErrConflict`) while NICs remain registered.
- Lower-layer primitive errors (`linkerr`/`routeerr`/`firewallerr`/`dhcperr`) are wrapped with `%w`; `classifyNotReady` maps `routeerr.ErrNotReady` into `networker.ErrNotReady` so callers branch on stable classes with `errors.Is`.
- Registration returns service-owned deep copies (`cloneNetworkDefinition`/`cloneNICDefinition`); external pointers cannot mutate the index, and callers must not mutate returned clones.
- After restart the control plane must replay `RegisterNetwork`/`RegisterNIC` then `EnsureNetwork`/`EnsureNIC`; the core holds no persistent state.

## ANTI-PATTERNS

- Do not cache drift-prone observed resource state in the core; always re-read live through the injected managers (single source of truth).
- Do not generate, infer, or default network names, addresses, MACs, or firewall identities in the orchestration layer; every behavior-affecting field is explicit and caller-provided.
- Do not enable, disable, or persist IPv4 forwarding from this layer. `EnsureNetwork` only *checks* readiness via `route.CheckIPv4Forwarding`; node install / acceptance setup own `net.ipv4.ip_forward`.
- Forward-accept adds only Govirta-owned accept rules for the guest CIDR across the egress interface. Do not change the host `FORWARD` default policy and do not flush non-Govirta rules.
- Do not tear down resources inside `Ensure*` on partial failure; reconciliation is forward-only and idempotent.
- Do not let `Delete*` short-circuit on the first error; collect and join so callers see every teardown failure.

## CALL GRAPHS & DATA FLOW (LOCAL)

### Flow: network ensure {#flow-network-ensure}

- Entry from root flow: `internal/network/service.go:33 (NetworkService.EnsureNetwork)` (caller wants one registered shared network reconciled onto the host)
- Local chain:
  1. `internal/network/service.go:33 (NetworkService.EnsureNetwork)` — guard `ctx`, delegate to the shared `*netpool.Service`
  2. `internal/network/netpool/orchestrate.go:42 (Service.EnsureNetwork)` — load record via `getRecord`, clone `NetworkDefinition`, reconcile in fixed order
  3. `internal/network/netpool/orchestrate.go:52 (Service.EnsureNetwork → link.EnsureBridge)` — bridge from `BridgeName`/`GatewayCIDR`/`BridgeMTU`/`BridgeMAC`
  4. `internal/network/netpool/orchestrate.go:61 (Service.EnsureNetwork → route.CheckIPv4Forwarding)` — require `route.IPv4ForwardingEnabled`; mismatch becomes `networker.ErrNotReady` via `classifyNotReady`
  5. `internal/network/netpool/orchestrate.go:65 (Service.EnsureNetwork → firewall.EnsureMasquerade)` — guest-CIDR source NAT out the egress interface
  6. `internal/network/netpool/orchestrate.go:76 (Service.EnsureNetwork → firewall.EnsureForwardAccept)` — Govirta-owned forward-accept group (egress + conntrack return)
  7. `internal/network/netpool/orchestrate.go:87 (Service.EnsureNetwork → dhcp.Start)` — start the static DHCP server on the bridge; `dhcperr.ErrAlreadyRunning` is tolerated
  8. `internal/network/netpool/orchestrate.go:105 (Service.EnsureNetwork → GetNetworkStatus)` then `:234 (Service.GetNetworkStatus)` — aggregate observed state live
- Data (within module): `NetworkName` → cloned `NetworkDefinition` → primitive specs (`link.BridgeSpec`, `firewall.MasqueradeSpec`, `firewall.ForwardAcceptSpec`, `dhcp.ServerSpec`) → observed `netpool.NetworkStatus`
- Side effects (within module): host bridge, masquerade + forward-accept nftables rules, DHCP listener; no in-memory observed-state cache, no IPv4 forwarding mutation
- Exit / next hop: `internal/hostnet/link/linux/manager_linux.go:33 (Manager.EnsureBridge)` [详见 `../../AGENTS.md#flow-hostnet-bridge`] · `internal/hostnet/route/linux/manager_linux.go:87 (Manager.CheckIPv4Forwarding)` [详见 `../../AGENTS.md#flow-hostnet-route`] · `internal/hostnet/firewall/linux/manager_linux.go:37 (Manager.EnsureMasquerade)` / `:65 (Manager.EnsureForwardAccept)` [详见 `../../AGENTS.md#flow-hostnet-firewall`] · `internal/hostnet/dhcp/coredhcp/manager.go:47 (Manager.Start)` [详见 `../../AGENTS.md#flow-hostnet-dhcp`]

### Flow: NIC ensure {#flow-nic-ensure}

- Entry from root flow: `internal/network/nic_service.go:28 (NICService.EnsureNIC)` (caller wants one registered VM NIC reconciled onto an already-ensured network)
- Local chain:
  1. `internal/network/nic_service.go:28 (NICService.EnsureNIC)` — guard `ctx`, delegate to the shared `*netpool.Service`
  2. `internal/network/netpool/orchestrate.go:111 (Service.EnsureNIC)` — load network record, look up NIC by `VMID` (`networker.ErrNotFound` if absent), clone NIC + network defs
  3. `internal/network/netpool/orchestrate.go:129 (Service.EnsureNIC → link.EnsureTap)` — TAP enslaved to `def.BridgeName`, with the caller-supplied `MAC`, owner UID/GID, MTU, VNetHeader
  4. `internal/network/netpool/orchestrate.go:141 (Service.EnsureNIC → dhcp.ApplyBinding)` — static MAC/IP/hostname binding under `def.DHCPServerID` (same `MAC`)
  5. `internal/network/netpool/orchestrate.go:150 (Service.EnsureNIC → firewall.EnsureEndpointAntiSpoofing)` — bridge-family guard binding the bridge/TAP/MAC/IPv4 (same `MAC`)
  6. `internal/network/netpool/orchestrate.go:163 (Service.EnsureNIC → GetNICStatus)` then `:265 (Service.GetNICStatus)` — aggregate observed TAP + lease state live
- Data (within module): `NetworkName` + `VMID` → cloned `NICDefinition` → primitive specs (`link.TapSpec`, `dhcp.BindingRequest`, `firewall.EndpointAntiSpoofingSpec`) → observed `netpool.NICStatus`; one `MAC` threaded unchanged to all three primitives
- Side effects (within module): host TAP enslaved to the bridge, DHCP static binding, bridge-chain anti-spoofing guard; no observed-state cache
- Exit / next hop: `internal/hostnet/link/linux/manager_linux.go:80 (Manager.EnsureTap)` [详见 `../../AGENTS.md#flow-hostnet-tap`] · `internal/hostnet/dhcp/coredhcp/manager.go:197 (Manager.ApplyBinding)` [详见 `../../AGENTS.md#flow-hostnet-dhcp`] · `internal/hostnet/firewall/linux/manager_linux.go:51 (Manager.EnsureEndpointAntiSpoofing)` [详见 `../../AGENTS.md#flow-hostnet-firewall`]

### Flow: guest egress closure {#flow-guest-egress}

- Entry from root flow: `test/acceptance/network_egress_test.go:43 (TestNetworkEgressEndToEnd)` (Lima acceptance proves real guest internet access through the orchestration API)
- Local chain:
  1. `test/acceptance/network_egress_test.go:126 (TestNetworkEgressEndToEnd → NetworkService.RegisterNetwork)` then `:129 (NetworkService.EnsureNetwork)` — bring up bridge + forwarding readiness + masquerade + forward-accept + DHCP [详见 `#flow-network-ensure`]
  2. `test/acceptance/network_egress_test.go:203 (TestNetworkEgressEndToEnd → NICService.RegisterNIC)` then `:206 (NICService.EnsureNIC)` — TAP + static binding + anti-spoofing for the guest MAC [详见 `#flow-nic-ensure`]
  3. CirrOS boots on the TAP, requests DHCP, and receives IP + default route + DNS from the static binding; no in-guest static IP commands are issued
  4. `test/acceptance/network_egress_test.go:295 (TestNetworkEgressEndToEnd)` — `ping -c 3 -w 10 8.8.8.8` proves NAT + forward-accept + default route
  5. `test/acceptance/network_egress_test.go:308 (TestNetworkEgressEndToEnd)` — `ping -c 3 -w 10 one.one.one.one` proves DNS option delivery
- Data (within module): declarative `NetworkDefinition` + `NICDefinition` → ensured host primitives → guest DHCP lease (IP/route/DNS) → ICMP egress + DNS resolution
- Side effects (within module): a fully wired single-network single-NIC egress path on the host; teardown uses `DeleteNIC` then `DeleteNetwork` with firewall rule refs resolved via `firewall.ListRules`
- Exit / next hop: real guest packets traverse host bridge → TAP → masquerade/forward-accept → egress interface → internet; no further Govirta process hop

## NOTES

- `NetworkService` and `NICService` intentionally share one `*netpool.Service` so network and NIC registrations live in the same record index (`internal/node/agent.go:29` wires both over one `netpool.NewService(...)`).
- `GetNetworkStatus` populates `Bridge`/`Forwarding`/`DHCP` from live reads; masquerade/forward-accept/anti-spoofing `RuleInfo` fields are reserved on the status structs and not re-read here (rule identity is resolved by callers via `firewall.ListRules` for delete refs).
- Focused verification is doc-only for this knowledge base. Behavior is covered by `go test -count=1 ./internal/network/...` (registration, orchestration order, MAC passthrough, live status) and the Lima-only `TestNetworkEgressEndToEnd`.
- Evidence: direct source reads of the orchestration layer, firewall forward-accept, node wiring, and the acceptance test at `base_commit e057cc0`; AFT outline/grep for symbol/line confirmation. `[已验证]` 源码与测试断言；`[降级: LSP call hierarchy]`.
