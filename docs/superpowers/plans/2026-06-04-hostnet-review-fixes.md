# Plan: internal/hostnet review fixes

**Date:** 2026-06-04
**Scope:** `internal/hostnet` (link, route, firewall, dhcp)
**Source:** `/review-deep internal/hostnet` → BLOCKING. This plan fixes **all** findings (2 blocking, 7 important, 6 suggestions, 5 nits).
**Branch:** execute in an isolated git worktree (not `main`), per workflow rule.

---

## Goal

Eliminate the two functional blockers (firewall anti-spoofing is inert; default-route ops fail on a real kernel), fix the seven important correctness/lifecycle defects, and clear the suggestion/nit cleanup. The two blockers share one root pattern: **a test fake that does not match kernel reality, so the suite is green while production fails.** Durable fixes therefore make the fakes faithful to the kernel, not just patch production.

## Non-goals

- No behavior change to legitimate egress (already proven by `TestNetworkEgressEndToEnd`).
- No new firewall scope (IPv6/VLAN isolation stays out; documented only).
- No change to the explicit-parameters / observed-state-as-truth / no-libvirt conventions.

## Verification commands

```bash
# Fast (macOS host) — run after every task
gofmt -l internal/hostnet/
go build ./...
go test ./internal/hostnet/...

# Concurrency regression (DHCP tasks)
go test -race ./internal/hostnet/dhcp/...

# Authoritative kernel gate (Linux/Lima) — run after route + firewall blockers
scripts/acceptance.sh full
```

The macOS unit suite must stay green after each task. The Lima acceptance gate is the authoritative proof for the two blockers (real netlink + real nftables); the unit fakes are made faithful so the bugs are *also* caught without a kernel.

---

## Root-cause notes (verified from source)

- **Route filter (netlink v1.3.1, `route_linux.go:1299-1307`):** when `RT_FILTER_DST` is set, netlink calls `ipNetEqual(route.Dst, filter.Dst)`. The kernel dumps a default route with `Dst==nil`; `ipNetEqual(nil, 0.0.0.0/0)==false`. So our non-nil `0.0.0.0/0` filter silently drops the very route we just wrote. `prepareRouteReq:917` only emits `RTA_DST` for non-nil `Dst`, and the kernel normalizes `/0` back to "no RTA_DST" → `Dst==nil` on dump. Mutation with `Dst=0.0.0.0/0` is fine; only the **filter mask** is wrong.
- **Firewall bridge hook:** in the kernel bridge `forward` hook, `iifname` = the receive port (the TAP) and `ibrname`/`MetaKeyBRIIFNAME` = the bridge. `expr_linux.go:100-103` binds them backwards, so the DROP guard conjunction is never true. The build+parse pair is symmetric, so every round-trip test agrees with the wrong wiring.
- **`validateRouteShape` (`validate_linux.go:219-231`):** guarantees a `DestinationDefault` spec always carries `GatewayIPv4` + `RouteScopeGlobal`. So the default-route path always has a gateway (no `prepareRouteReq:910` nil-Gw concern).
- **`configureCreatedLink`:** runs the configure closure for **both** freshly created and reconciled bridges → address pruning placed there covers the reconcile path.
- **DHCP ownership (`manager.go:152-160`):** the codebase already commits to "once a call takes cleanup ownership, it finishes Close/Wait even if ctx is canceled." The Stop-cancel fix is consistent with that existing intent, not a new philosophy.

---

# Tasks

Ordered: blockers first, then important, then suggestions/nits per package. Each task is independently testable.

---

## Task 1 — [blocking] Route: default-route filter mask (`route/linux`)

**Finding:** `0.0.0.0/0` Add/Replace return `ErrNotFound` after a successful kernel mutation; `ListRoutes(DestinationDefault)` returns empty; stale default routes leak.

**Files:**
- `internal/hostnet/route/linux/info_linux.go` (filter builders: `observedRouteForSpec` ~:75-95, `cleanupStaleRoutesAfterReplace` ~:97-119, list-filter build ~:180-187, `destinationIPNet` ~:197-208)
- `internal/hostnet/route/linux/fake_handle_test.go` (make fake faithful)
- `internal/hostnet/route/linux/route_test.go` + `list_get_test.go` (unit regressions)
- `test/acceptance/hostnet_route_test.go` (real-kernel case)

**Production change:**
1. Add a helper to decide the DST filter contribution by destination mode:
   ```go
   // dstFilterForDestination reports the netlink filter mask bit and Dst for a
   // route destination. Default routes are dumped by the kernel with Dst==nil,
   // so RT_FILTER_DST must NOT be set for them — netlink's ipNetEqual would
   // otherwise compare nil against 0.0.0.0/0 and drop the route. Go-side
   // exactRouteMatch / routeInfoMatchesFilter narrows the result instead.
   func dstFilterForDestination(d route.Destination) (mask uint64, dst *net.IPNet) {
       if d.Mode == route.DestinationDefault {
           return 0, nil
       }
       return unix.RT_FILTER_DST, destinationIPNet(d)
   }
   ```
2. In `observedRouteForSpec`, `cleanupStaleRoutesAfterReplace`, and the `ListRoutes` filter builder, OR-in the mask and set `filter.Dst` only via this helper (do not unconditionally set `RT_FILTER_DST`). Keep the existing Go-side `exactRouteMatch` / `routeInfoMatchesFilter` narrowing — it already handles default correctly once netlink stops dropping the route.
3. Leave the mutation path (`AddRoute`/`ReplaceRoute`/`DeleteRoute`) unchanged: passing `Dst=0.0.0.0/0` is the canonical default-route mutation and `validateRouteShape` guarantees a gateway.

**Fake fidelity (root-cause fix):** in `fake_handle_test.go`,
- store/dump default routes with `Dst == nil` (mirror the kernel), and
- make the fake's `RouteListFiltered` honor `RT_FILTER_DST` with netlink's `ipNetEqual` semantics (nil vs `0.0.0.0/0` must NOT match).
Without this, the unit suite cannot reproduce the bug.

**Tests:**
- Unit: add `DestinationDefault` cases to add → re-read, replace → cleanup, and `ListRoutes(DestinationDefault)` / `GetRoute` default path. With the faithful fake these fail before the production fix and pass after.
- Acceptance: add a real-Linux `0.0.0.0/0` route case (add → list → replace → delete) to `hostnet_route_test.go`.

**Verify:** `go test ./internal/hostnet/route/...`; then `scripts/acceptance.sh full` (TestHostnetRoutePrimitives + new default case).

---

## Task 2 — [blocking] Firewall: anti-spoofing interface match reversed (`firewall/linux`)

**Finding:** the MAC/IP guard never fires; every guest can spoof source MAC/IPv4/ARP.

**Files:**
- `internal/hostnet/firewall/linux/expr_linux.go` (`endpointInterfaceExprs` ~:98-105 builder; `parseEndpointDropExprs` ~:364-377 parser)
- `internal/hostnet/firewall/linux/anti_spoofing_test.go` + helpers (`assertEndpointGuards`, `assertEndpointSummary`)
- `test/acceptance/` (new spoof-drop case)

**Production change (builder):** swap the two interface bindings so they match the kernel bridge `forward` hook:
- `MetaKeyIIFNAME` cmp → `interfaceNameData(spec.TapName)` (was `BridgeName`)
- `MetaKeyBRIIFNAME` cmp → `interfaceNameData(spec.BridgeName)` (was `TapName`)

**Production change (parser):** apply the symmetric swap so the observed `RuleInfo` still reports correct identity:
- data read from `iifname` → `TapName`
- data read from `BRIIFNAME` → `BridgeName`

(The swap must be applied to **both** sides together; fixing only one would break the round-trip.)

**Tests:**
- Unit (expr-level, absolute — the key gap): assert the raw built expressions bind `MetaKeyIIFNAME` data == `TapName` and `MetaKeyBRIIFNAME` data == `BridgeName`. This pins the kernel fact and cannot be satisfied by a symmetric build+parse swap.
- Acceptance: spoof-drop case — a second identity on the TAP sends a frame with a spoofed source MAC/IPv4; assert it is dropped while the legitimate endpoint still passes.

**Verify:** `go test ./internal/hostnet/firewall/...`; then `scripts/acceptance.sh full` (anti-spoofing + new spoof-drop).

---

## Task 3 — [important] Route: reject non-canonical CIDR (`route/linux`)

**Finding:** specs with host bits set (e.g. `198.51.100.5/24`) pass validation, get masked for the kernel, but `exactRouteMatch` compares the raw `spec.Destination` against the canonical observed dest → never equal.

**File:** `internal/hostnet/route/linux/validate_linux.go` (`validateIPv4Prefix` ~:178-184).

**Change:** in `validateIPv4Prefix`, after the existing validity/`Is4`/bits checks, require canonical form:
```go
if prefix != prefix.Masked() {
    return routeerr.ErrInvalidRequest
}
```
**Test:** add a validation case rejecting a prefix with host bits set, in `validation_test.go`.

**Verify:** `go test ./internal/hostnet/route/...`.

---

## Task 4 — [important] Link: `EnsureBridge` prune stale addresses (`link/linux`)

**Finding:** reconcile only `AddrReplace`s the desired gateway; a changed `GatewayCIDR` leaves the old gateway on the bridge → observed `Addresses` diverges from spec, contradicting "exactly match spec" + observed-as-truth.

**Files:**
- `internal/hostnet/link/linux/handle_linux.go` (add `AddrDel` + `AddrList` to the `handle` interface and `realHandle`)
- `internal/hostnet/link/linux/manager_linux.go` (prune in the configure closure used by `configureCreatedLink`, ~:62)
- `internal/hostnet/link/linux/fake_handle_test.go` (model address list/del)
- `internal/hostnet/link/linux/bridge_test.go` (regression)

**Change:** in the bridge configure closure, after `AddrReplace(desired)`:
1. `AddrList` the link's current addresses.
2. Delete any address that is not the desired gateway (prune only Govirta-managed/global-scope IPv4 addresses; do not touch link-local). Compose any delete failures with `errors.Join` (no silent best-effort).

**Test:** `EnsureBridge(gwA)` then `EnsureBridge(gwB)` → observed `Addresses == [gwB]` (not `[gwA, gwB]`).

**Verify:** `go test ./internal/hostnet/link/...`.

---

## Task 5 — [important] Link: `List` O(n²) master resolution (`link/linux`)

**Finding:** `List` (~:202) dumps all links, then for each enslaved link `masterName` (~:97-108) issues a **fresh full `LinkList()`** → N TAPs = N extra full kernel dumps + a TOCTOU window.

**File:** `internal/hostnet/link/linux/info_linux.go` (+ `manager_linux.go` `List`).

**Change:** in `List`, build an `index → name` map once from the already-fetched link slice and resolve master names from it. Keep the per-link `linkInfo` path that takes the prebuilt map (the standalone `Get` path may keep a single targeted lookup).

**Test:** the fake counts `LinkList` calls; assert `List` over N enslaved TAPs issues a constant number of dumps (not N+1).

**Verify:** `go test ./internal/hostnet/link/...`.

---

## Task 6 — [important] DHCP: `Stop` cancel-before-ownership leak (`dhcp/coredhcp`)

**Finding:** `Stop` calls `checkContext(ctx)` and returns `ctx.Err()` **before** taking cleanup ownership (~:126-129); cancel-root-then-`Stop` returns `Canceled` and leaves the server running + listener leaked. The stale test `TestStopCanceledContextBeforeOwnershipDoesNotCleanup` asserts the buggy behavior.

**Files:**
- `internal/hostnet/dhcp/coredhcp/manager.go` (`Stop` ~:126-160)
- `internal/hostnet/dhcp/coredhcp/manager_test.go` (rewrite stale test)

**Change:** validate only `ctx == nil` and the server ID up front (return `ErrInvalidRequest` for nil ctx). Once `Stop` takes cleanup ownership of the runtime, run Close/Wait to completion on a background context **even if the caller ctx is canceled** — symmetric with the existing `:152-160` ownership guard. Do not return `ctx.Err()` after ownership is taken.

**Test (architecture-transition discipline):** the old test encodes the wrong contract — rewrite it. New `TestStopCanceledContextStillCleansUp`: start a server, cancel the root ctx, call `Stop(rootCtx, id)` → server reaches stopped, listener closed (`closed == true`). Delete the stale assertion entirely (no compat shim).

**Verify:** `go test -race ./internal/hostnet/dhcp/...`.

---

## Task 7 — [important] DHCP: `Start` ctx lifecycle contract (`dhcp/coredhcp`)

**Finding:** `Start` uses `ctx` only at `:48` (`checkContext`) then drops it; listener goroutines stop only via explicit `Stop()`. Ambiguous against the goroutine-ownership rule.

**Decision (documentation fix, not behavior change):** server lifecycle is owned solely by `Start`/`Stop`; the `Start` ctx is **operation-scoped** (covers the start handshake), not a teardown trigger. Wiring a second ctx-driven teardown would compete with the `startDone`/Stop ownership model and create two owners for one listener.

**Files:**
- `internal/hostnet/dhcp/dhcp.go` (`Manager.Start` doc comment) — state explicitly: ctx scopes the start operation only; callers must call `Stop` to release the listener; cancelling the start ctx after a successful `Start` does not stop the server.
- Root `AGENTS.md` DHCP conventions — add one line mirroring the Start/Stop-owned lifecycle (consistent with the existing replay-after-restart convention).

**Verify:** docs only; `go build ./...` + `gofmt -l`.

---

## Task 8 — [important] DHCP: handler `-race` regression test (`dhcp/coredhcp`)

**Finding:** highest-risk path (DHCPv4 handler concurrent with `ApplyBinding`/`RemoveBinding`) has no `-race` coverage; lock discipline is unproven.

**File:** `internal/hostnet/dhcp/coredhcp/handler_test.go` (reuse existing packet/runtime builders).

**Change:** add `TestHandlerConcurrentWithBindingMutation` — spawn goroutines driving `DISCOVER`/`REQUEST` through the handler while other goroutines `ApplyBinding`/`RemoveBinding` on the same runtime; run under `-race`. Use the existing handler test's packet helpers.

**Verify:** `go test -race -run TestHandlerConcurrent ./internal/hostnet/dhcp/...`.

---

## Task 9 — [suggestion] Firewall: group-delete by full logical identity (`firewall/linux`)

**Finding:** group delete keyed on lowest handle (`endpointGroupLowestHandle` ~:151-159, `forward_linux.go:153-162`); out-of-band removal of the lowest guard makes `ref.Handle` unresolvable → delete returns nil while remaining guards leak.

**File:** `internal/hostnet/firewall/linux/info_linux.go` + `forward_linux.go`.

**Change:** resolve the group by full logical identity (owner/family/table/chain + `TapName`/`BridgeName` for endpoint, guest-CIDR/egress for forward), treating `ref.Handle` present in **any** member as a valid selector. Delete all members matching the logical identity.

**Test:** remove the lowest-handle member out-of-band, then group-delete → remaining members are gone (no leak).

**Verify:** `go test ./internal/hostnet/firewall/...`.

---

## Task 10 — [suggestion] Firewall: document IPv4+ARP/untagged scope (`firewall`)

**Finding:** anti-spoofing only covers untagged IPv4/ARP (`endpointProtocolDropExprs` ~:86-96); VLAN-tagged (0x8100) or IPv6 frames bypass under accept default policy.

**Change:** document the scope in the `firewall.Manager.EnsureEndpointAntiSpoofing` contract comment and root `AGENTS.md` firewall conventions: the guard covers untagged IPv4 + ARP only; non-IPv4/tagged isolation is out of current scope. No code change.

**Verify:** docs; `gofmt -l` + `go build ./...`.

---

## Task 11 — [suggestion] DHCP: reconcile / gate / validate cluster (`dhcp/coredhcp`)

Three small explicit-behavior fixes:

1. **Idempotent hostname (`runtime.go` ~:108-113):** on re-`ApplyBinding` with matching MAC+IP but changed `Hostname`, either reconcile the hostname or return `ErrAlreadyExists` so the change is not silently dropped. Pick **reconcile** (update mutable fields) to match "observed truth"; document it.
2. **State gate (`manager.go` ~:197-222 / `runtime.go` ~:103):** gate `ApplyBinding`/`RemoveBinding` on runtime state — return `ErrNotRunning` for stopping/stopped (mirror `bindLease` ~:172) so a concurrent `Stop` cannot orphan a binding.
3. **ServerAddr ∉ Pool (`validate.go` ~:89-100):** reject a pool range that contains `spec.ServerAddr` (a guest must not be bound to the responder's own IP).

**Tests:** one unit per fix (changed-hostname reconcile; ApplyBinding after Stop → `ErrNotRunning`; pool-contains-ServerAddr → `ErrInvalidRequest`).

**Verify:** `go test ./internal/hostnet/dhcp/...`.

---

## Task 12 — [suggestion] DHCP: restart-replay test (`dhcp/coredhcp`)

**Finding:** no test proves the memory-only/replay contract (bindings discarded across Stop/Start).

**File:** `internal/hostnet/dhcp/coredhcp/manager_test.go`.

**Change:** `TestBindingsDoNotSurviveRestart` — Start(id) → ApplyBinding → Stop → Start(**same** logical config/id) → assert the prior binding is absent (caller must replay).

**Verify:** `go test ./internal/hostnet/dhcp/...`.

---

## Task 13 — [nit] Cleanup cluster

Mechanical, no behavior change. Batch into one commit:

- **Firewall `expr_linux.go` ~:112-115:** remove dead `observedRuleInfo` (0 callers; `grep observedRuleInfo(`).
- **Firewall `info_linux.go` ~:157-158 / `forward_linux.go` ~:262-263:** delete the redundant `ref.Handle = base.info.Ref.Handle` self-assignment (no-op; recomputed in loop).
- **Firewall `validate_linux.go` ~:180-196:** replace the per-purpose magic `Priority` values with Govirta-owned named constants (srcnat=100, bridge=-200, forward=0) and document that priority is canonical-fixed per purpose.
- **DHCP `runtime.go` ~:103 / `manager.go` ~:221:** drop the unused `_ time.Time` parameter on `applyBinding` and its `time.Now()` call site.
- **DHCP `manager.go` ~:329-331:** move `placeholderSetup4` (test-only) into the test file or inline it in the conflict test.

**Verify:** `go test ./internal/hostnet/...`; `gofmt -l internal/hostnet/`.

---

## Final gate

```bash
gofmt -l internal/hostnet/
go build ./...
go test ./internal/hostnet/...
go test -race ./internal/hostnet/dhcp/...
scripts/acceptance.sh full   # authoritative: route default-route + firewall spoof-drop on real kernel
```

After green: spec-compliance + code-quality review by independent subagents (workflow rule), fix blocking/important items, then finishing-a-development-branch.

## Sequencing & commits (small-step, single logical change each)

1. Task 1 (route blocker + faithful fake) — commit
2. Task 2 (firewall blocker + expr-level test) — commit
3. Task 3 (route CIDR validation) — commit
4. Task 4 (link prune) — commit
5. Task 5 (link List O(n²)) — commit
6. Task 6 (DHCP Stop leak + test rewrite) — commit
7. Task 7 (DHCP Start contract docs) — commit
8. Task 8 (DHCP handler -race test) — commit
9. Tasks 9–12 (suggestions) — one commit each
10. Task 13 (nits) — one commit

Acceptance (`scripts/acceptance.sh full`) is required before pushing `main`; never bypass with `--no-verify`.
