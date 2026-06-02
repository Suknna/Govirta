# Hostnet Firewall Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/hostnet/firewall` as a Linux nftables-backed host firewall primitive with explicit IPv4 MASQUERADE and per-TAP endpoint anti-spoofing lifecycle operations.

**Architecture:** Add a root firewall contract package parallel to `internal/hostnet/link` and `internal/hostnet/route`, plus a Linux-only implementation that hides `github.com/google/nftables` behind a narrow handle. The root API exposes stable typed request/result structs and high-level operations, while Linux code translates them into Govirta-owned nftables table/chain/rule state and returns observed state.

**Tech Stack:** Go 1.26, `github.com/google/nftables v0.3.0`, `golang.org/x/sys/unix`, Linux nftables, existing Lima acceptance framework.

---

## Scope and file structure

This plan implements the approved spec at `docs/superpowers/specs/2026-06-02-hostnet-firewall-design.md`.

Expected file responsibilities and size targets:

```text
internal/hostnet/firewall/
├── constants.go                # typed constants and small helpers; target <160 lines
├── firewall.go                 # Manager, request/result structs, doc comments; target <260 lines
├── noop.go                     # no-op Manager for composition tests; target <120 lines
├── noop_test.go                # no-op context/error behavior; target <160 lines
├── firewallerr/
│   └── errors.go               # sentinel errors; target <80 lines
└── linux/
    ├── manager_linux.go        # Manager construction and public methods; target <260 lines
    ├── handle_linux.go         # narrow nftables handle and real adapter; target <260 lines
    ├── validate_linux.go       # explicit validation; target <220 lines
    ├── rules_linux.go          # desired semantic rules and ensure/delete helpers; target <320 lines
    ├── expr_linux.go           # nftables expression builders/parsers; target <320 lines
    ├── info_linux.go           # observed rule translation, matching, sorting; target <320 lines
    ├── errors_linux.go         # error classification; target <160 lines
    ├── fake_handle_test.go     # fake ruleset and failure injection; target <420 lines
    ├── validation_test.go      # validation behavior; target <320 lines
    ├── masquerade_test.go      # NAT behavior; target <360 lines
    ├── anti_spoofing_test.go   # anti-spoofing behavior; target <420 lines
    ├── list_get_test.go        # observation behavior; target <300 lines
    └── errors_test.go          # error translation behavior; target <220 lines
```

Acceptance files:

```text
test/acceptance/hostnet_firewall_test.go  # nftables lifecycle acceptance; target <360 lines
test/acceptance/harness.go                # add nft diagnostics helper; target delta <60 lines
scripts/acceptance.sh                     # verify nft tool in Lima guest; install nftables in provisioning; target delta <80 lines
```

No planned source file exceeds the 800-line hard limit. If a worker discovers a file would exceed the target by more than 150 lines, split by responsibility before writing code rather than appending unrelated helpers.

---

### Task 1: Add root firewall contract and no-op manager

**Files:**
- Create: `internal/hostnet/firewall/constants.go`
- Create: `internal/hostnet/firewall/firewall.go`
- Create: `internal/hostnet/firewall/firewallerr/errors.go`
- Create: `internal/hostnet/firewall/noop.go`
- Create: `internal/hostnet/firewall/noop_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the root package compiles, exposes explicit typed firewall contracts, and provides a no-op manager that validates context before returning `firewallerr.ErrUnsupported`.

Acceptance evidence:
- `go test -count=1 ./internal/hostnet/firewall/...` passes.
- `go test -count=1 ./internal/hostnet/...` passes.
- No root package API imports `github.com/google/nftables`.

- [ ] **Step 2: Create sentinel errors**

Create `internal/hostnet/firewall/firewallerr/errors.go` with this content:

```go
package firewallerr

import "errors"

var (
    // ErrInvalidRequest reports malformed or incomplete firewall inputs.
    ErrInvalidRequest = errors.New("invalid host firewall request")
    // ErrInvalidObservedState reports firewall state that cannot be translated into Govirta types.
    ErrInvalidObservedState = errors.New("invalid observed firewall state")
    // ErrNotFound reports a missing firewall table, chain, or rule.
    ErrNotFound = errors.New("host firewall rule not found")
    // ErrAlreadyExists reports an unexpected duplicate firewall object.
    ErrAlreadyExists = errors.New("host firewall rule already exists")
    // ErrConflict reports existing firewall state that conflicts with the requested rule.
    ErrConflict = errors.New("host firewall rule conflict")
    // ErrPermission reports insufficient privileges for firewall operations.
    ErrPermission = errors.New("host firewall permission denied")
    // ErrIncompleteList reports that firewall enumeration could not return a complete result.
    ErrIncompleteList = errors.New("incomplete host firewall rule list")
    // ErrUnsupported reports firewall operations outside the current implementation scope.
    ErrUnsupported = errors.New("unsupported host firewall operation")
)
```

- [ ] **Step 3: Create typed constants and helper constructors**

Create `internal/hostnet/firewall/constants.go` with these public names:

```go
package firewall

type TableFamily string
type TableName string
type ChainName string
type InterfaceName string
type RuleOwner string
type RuleHandle uint64
type RulePurpose string
type ChainType string
type Hook string
type PriorityName string

const (
    TableFamilyIPv4   TableFamily = "ipv4"
    TableFamilyBridge TableFamily = "bridge"

    ChainTypeNAT    ChainType = "nat"
    ChainTypeFilter ChainType = "filter"

    HookPostrouting Hook = "postrouting"
    HookForward     Hook = "forward"

    PriorityNameSrcNAT       PriorityName = "srcnat"
    PriorityNameBridgeFilter PriorityName = "bridge-filter"

    RulePurposeMasquerade           RulePurpose = "masquerade"
    RulePurposeEndpointAntiSpoofing RulePurpose = "endpoint-anti-spoofing"
)

type Priority struct {
    Value int
    Name  PriorityName
    Set   bool
}

func ExplicitPriority(value int, name PriorityName) Priority {
    return Priority{Value: value, Name: name, Set: true}
}
```

Use numeric values in implementation tasks:
- `PriorityNameSrcNAT` maps to nftables source NAT priority `100`.
- `PriorityNameBridgeFilter` maps to bridge filter priority `-200`.

- [ ] **Step 4: Create request and result structs**

Create `internal/hostnet/firewall/firewall.go` with the following API shape. Include doc comments that state all behavior-affecting fields are explicit and observed state must be returned after ensure operations.

```go
package firewall

import (
    "context"
    "net"
    "net/netip"
)

type Manager interface {
    EnsureMasquerade(ctx context.Context, spec MasqueradeSpec) (RuleInfo, error)
    DeleteMasquerade(ctx context.Context, ref RuleRef) error
    EnsureEndpointAntiSpoofing(ctx context.Context, spec EndpointAntiSpoofingSpec) (RuleInfo, error)
    DeleteEndpointAntiSpoofing(ctx context.Context, ref RuleRef) error
    GetRule(ctx context.Context, query RuleQuery) (RuleInfo, error)
    ListRules(ctx context.Context, filter RuleFilter) ([]RuleInfo, error)
}

type MasqueradeSpec struct {
    TableName           TableName
    ChainName           ChainName
    RuleOwner           RuleOwner
    GuestCIDR           netip.Prefix
    EgressInterfaceName InterfaceName
    Priority            Priority
}

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

type RuleRef struct {
    Owner     RuleOwner
    Purpose   RulePurpose
    Family    TableFamily
    TableName TableName
    ChainName ChainName
    Handle    RuleHandle
}

type RuleQuery struct {
    Ref RuleRef
}

type RuleFilter struct {
    Owner     RuleOwner
    Purpose   RulePurpose
    Family    TableFamily
    TableName TableName
    ChainName ChainName
}

type RuleSummary struct {
    Masquerade           *MasqueradeSummary
    EndpointAntiSpoofing *EndpointAntiSpoofingSummary
}

type MasqueradeSummary struct {
    GuestCIDR           netip.Prefix
    EgressInterfaceName InterfaceName
    Priority            Priority
}

type EndpointAntiSpoofingSummary struct {
    BridgeName InterfaceName
    TapName    InterfaceName
    MAC        net.HardwareAddr
    IPv4       netip.Addr
    Priority   Priority
}

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

- [ ] **Step 5: Create no-op manager**

Create `internal/hostnet/firewall/noop.go` following the `route.NoopManager` style. Each method must:

1. return `firewallerr.ErrInvalidRequest` when `ctx == nil`;
2. return `ctx.Err()` when canceled or expired;
3. return `firewallerr.ErrUnsupported` otherwise.

Use this method helper exactly:

```go
func noopFirewallOperationError(ctx context.Context) error {
    if ctx == nil {
        return firewallerr.ErrInvalidRequest
    }
    if err := ctx.Err(); err != nil {
        return err
    }
    return nil
}
```

- [ ] **Step 6: Add no-op tests**

Create `internal/hostnet/firewall/noop_test.go` with table-driven tests for every method:

```go
func TestNoopManagerReportsUnsupported(t *testing.T) {
    manager := firewall.NewNoopManager()
    ctx := context.Background()
    tests := []struct {
        name string
        run  func() error
    }{
        {name: "EnsureMasquerade", run: func() error { _, err := manager.EnsureMasquerade(ctx, firewall.MasqueradeSpec{}); return err }},
        {name: "DeleteMasquerade", run: func() error { return manager.DeleteMasquerade(ctx, firewall.RuleRef{}) }},
        {name: "EnsureEndpointAntiSpoofing", run: func() error { _, err := manager.EnsureEndpointAntiSpoofing(ctx, firewall.EndpointAntiSpoofingSpec{}); return err }},
        {name: "DeleteEndpointAntiSpoofing", run: func() error { return manager.DeleteEndpointAntiSpoofing(ctx, firewall.RuleRef{}) }},
        {name: "GetRule", run: func() error { _, err := manager.GetRule(ctx, firewall.RuleQuery{}); return err }},
        {name: "ListRules", run: func() error { _, err := manager.ListRules(ctx, firewall.RuleFilter{}); return err }},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            if err := tt.run(); !errors.Is(err, firewallerr.ErrUnsupported) {
                t.Fatalf("error = %v, want ErrUnsupported", err)
            }
        })
    }
}
```

Add companion tests for nil context and canceled context using the same method table.

- [ ] **Step 7: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/firewall/...
```

Expected: PASS.

- [ ] **Step 8: Run broader hostnet verification**

Run:

```bash
go test -count=1 ./internal/hostnet/...
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/hostnet/firewall
git commit -m "feat(hostnet/firewall): add firewall contract"
```

---

### Task 2: Add nftables dependency and Linux handle boundary

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/hostnet/firewall/linux/handle_linux.go`
- Create: `internal/hostnet/firewall/linux/errors_linux.go`
- Create: `internal/hostnet/firewall/linux/errors_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: add `github.com/google/nftables v0.3.0` and define a narrow Linux handle interface so production code can use nftables while tests use fakes.

Acceptance evidence:
- `go test -count=1 ./internal/hostnet/firewall/...` passes.
- `go list -m github.com/google/nftables` reports `v0.3.0`.
- No root `internal/hostnet/firewall` file imports `github.com/google/nftables`.

- [ ] **Step 2: Add dependency explicitly**

Run:

```bash
go get github.com/google/nftables@v0.3.0
go mod tidy
```

Then run:

```bash
go list -m github.com/google/nftables
```

Expected output includes:

```text
github.com/google/nftables v0.3.0
```

- [ ] **Step 3: Create Linux handle interface**

Create `internal/hostnet/firewall/linux/handle_linux.go` with a narrow adapter. Verify method signatures against the downloaded v0.3.0 package while implementing.

Required handle surface:

```go
//go:build linux

package linux

import "github.com/google/nftables"

type handle interface {
    AddTable(table *nftables.Table) *nftables.Table
    DelTable(table *nftables.Table)
    AddChain(chain *nftables.Chain) *nftables.Chain
    DelChain(chain *nftables.Chain)
    AddRule(rule *nftables.Rule) *nftables.Rule
    DelRule(rule *nftables.Rule)
    GetTables() ([]*nftables.Table, error)
    GetChains() ([]*nftables.Chain, error)
    GetRules(table *nftables.Table, chain *nftables.Chain) ([]*nftables.Rule, error)
    Flush() error
}

type realHandle struct {
    conn *nftables.Conn
}

func newRealHandle() (handle, error) {
    conn, err := nftables.New()
    if err != nil {
        return nil, err
    }
    return &realHandle{conn: conn}, nil
}
```

Implement each method by delegating to `h.conn`. If v0.3.0 has a different method return signature, adapt the wrapper while keeping this internal handle interface stable for the rest of Govirta.

- [ ] **Step 4: Create Linux error translation**

Create `internal/hostnet/firewall/linux/errors_linux.go` with:

```go
//go:build linux

package linux

import (
    "errors"
    "fmt"
    "os"

    "github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
    "golang.org/x/sys/unix"
)

func translateError(operation string, err error) error {
    if err == nil {
        return nil
    }
    class := classifyError(err)
    if class == err {
        return fmt.Errorf("%s: %w", operation, err)
    }
    return fmt.Errorf("%s: %w: %w", operation, class, err)
}

func classifyError(err error) error {
    switch {
    case errors.Is(err, firewallerr.ErrInvalidRequest),
        errors.Is(err, firewallerr.ErrInvalidObservedState),
        errors.Is(err, firewallerr.ErrNotFound),
        errors.Is(err, firewallerr.ErrAlreadyExists),
        errors.Is(err, firewallerr.ErrConflict),
        errors.Is(err, firewallerr.ErrPermission),
        errors.Is(err, firewallerr.ErrIncompleteList),
        errors.Is(err, firewallerr.ErrUnsupported):
        return err
    case errors.Is(err, os.ErrPermission), errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
        return firewallerr.ErrPermission
    case errors.Is(err, os.ErrNotExist), errors.Is(err, unix.ENOENT):
        return firewallerr.ErrNotFound
    case errors.Is(err, os.ErrExist), errors.Is(err, unix.EEXIST):
        return firewallerr.ErrAlreadyExists
    default:
        return err
    }
}
```

- [ ] **Step 5: Add error tests**

Create `internal/hostnet/firewall/linux/errors_test.go`. Cover:

- `os.ErrPermission` maps to `firewallerr.ErrPermission` and preserves cause.
- `os.ErrNotExist` maps to `firewallerr.ErrNotFound` and preserves cause.
- `firewallerr.ErrConflict` remains detectable.
- unknown error is wrapped but not misclassified.

Use `errors.Is` assertions for both sentinel and cause.

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/firewall/...
go list -m github.com/google/nftables
```

Expected: tests PASS and module version is `v0.3.0`.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/hostnet/firewall/linux/handle_linux.go internal/hostnet/firewall/linux/errors_linux.go internal/hostnet/firewall/linux/errors_test.go
git commit -m "feat(hostnet/firewall/linux): add nftables handle"
```

---

### Task 3: Implement validation and Linux manager skeleton

**Files:**
- Create: `internal/hostnet/firewall/linux/manager_linux.go`
- Create: `internal/hostnet/firewall/linux/validate_linux.go`
- Create: `internal/hostnet/firewall/linux/fake_handle_test.go`
- Create: `internal/hostnet/firewall/linux/validation_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: Linux manager construction, context handling, and request validation exist before any nftables mutation code is written.

Acceptance evidence:
- `go test -count=1 ./internal/hostnet/firewall/...` passes.
- Validation tests prove invalid requests do not call the fake handle.

- [ ] **Step 2: Create manager skeleton**

Create `manager_linux.go` with:

```go
//go:build linux

package linux

import (
    "context"

    "github.com/suknna/govirta/internal/hostnet/firewall"
)

type Manager struct {
    handle handle
}

var _ firewall.Manager = (*Manager)(nil)

func NewManager() (*Manager, error) {
    h, err := newRealHandle()
    if err != nil {
        return nil, translateError("create nftables handle", err)
    }
    return NewManagerWithHandle(h), nil
}

func NewManagerWithHandle(h handle) *Manager {
    return &Manager{handle: h}
}

func (m *Manager) firewallHandle() handle {
    if m == nil || m.handle == nil {
        h, err := newRealHandle()
        if err != nil {
            return failingHandle{err: translateError("create nftables handle", err)}
        }
        return h
    }
    return m.handle
}
```

Add a small `failingHandle` type implementing `handle` by returning its stored error from read/flush methods and returning inputs from add methods. This prevents nil manager panics while still surfacing construction errors.

- [ ] **Step 3: Add public methods that validate then return unsupported**

In `manager_linux.go`, temporarily implement the public methods so Task 3 compiles:

```go
func (m *Manager) EnsureMasquerade(ctx context.Context, spec firewall.MasqueradeSpec) (firewall.RuleInfo, error) {
    if err := validateMasqueradeSpec(ctx, spec); err != nil {
        return firewall.RuleInfo{}, translateError("ensure masquerade", err)
    }
    return firewall.RuleInfo{}, translateError("ensure masquerade", firewallerr.ErrUnsupported)
}
```

Repeat the same pattern for anti-spoofing, delete, get, and list using their own validation helpers. Import `firewallerr` in this file for the temporary unsupported returns. Later tasks replace these bodies.

- [ ] **Step 4: Implement validation helpers**

Create `validate_linux.go` with:

- `checkContext(ctx context.Context) error`
- `validateMasqueradeSpec(ctx context.Context, spec firewall.MasqueradeSpec) error`
- `validateEndpointAntiSpoofingSpec(ctx context.Context, spec firewall.EndpointAntiSpoofingSpec) error`
- `validateRuleRef(ctx context.Context, ref firewall.RuleRef, purpose firewall.RulePurpose) error`
- `validateRuleQuery(ctx context.Context, query firewall.RuleQuery) error`
- `validateRuleFilter(ctx context.Context, filter firewall.RuleFilter) error`
- `validateSafeName(kind string, value string) error`
- `validateInterfaceName(kind string, value firewall.InterfaceName) error`
- `validatePriority(priority firewall.Priority, expected firewall.PriorityName) error`

Validation rules:

```go
func checkContext(ctx context.Context) error {
    if ctx == nil {
        return firewallerr.ErrInvalidRequest
    }
    return ctx.Err()
}
```

Safe names:

- non-empty;
- not `.` or `..`;
- each byte is ASCII letter, digit, `_`, `.`, or `-`.

Interface names:

- use safe-name validation;
- length must be at most `link.MaxInterfaceNameLength`.

Priority:

- `priority.Set` must be true;
- `priority.Name` must match the operation's expected name;
- `PriorityNameSrcNAT` requires `Value == 100`;
- `PriorityNameBridgeFilter` requires `Value == -200`.

IP and MAC:

- NAT `GuestCIDR` must be valid IPv4 and must not be `/0`.
- endpoint `IPv4` must be valid IPv4, not unspecified, not multicast.
- endpoint `MAC` length must be 6 and unicast. Do not require locally administered MAC for observed VM identity.

- [ ] **Step 5: Add validation tests**

Create `fake_handle_test.go` before writing validation tests. Start with a minimal fake that implements `handle`, records every method call in `calls []string`, and returns empty table/chain/rule lists. The fake will be expanded in Task 4.

```go
type fakeHandle struct {
    calls []string
}

func (f *fakeHandle) record(call string) {
    f.calls = append(f.calls, call)
}
```

Each handle method should call `record` and return a harmless empty result. Validation tests assert that invalid requests leave `calls` empty.

- [ ] **Step 6: Add validation tests**

Create `validation_test.go` with table-driven cases:

Masquerade invalid cases:

- nil context;
- canceled context;
- empty table;
- unsafe table `bad/name`;
- owner `..`;
- invalid `GuestCIDR`;
- IPv6 prefix;
- `/0` prefix;
- empty egress interface;
- missing priority;
- priority name mismatch.

Endpoint invalid cases:

- nil context;
- canceled context;
- empty bridge;
- empty TAP;
- unsafe TAP `tap/name`;
- multicast MAC;
- short MAC;
- zero IPv4;
- IPv6 address;
- missing priority;
- priority value mismatch.

Each test should call the public manager method with the fake handle whose `calls` slice starts empty, assert `errors.Is(err, firewallerr.ErrInvalidRequest)` or direct context error, and assert no handle calls were recorded.

- [ ] **Step 7: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/firewall/...
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/hostnet/firewall/linux/manager_linux.go internal/hostnet/firewall/linux/validate_linux.go internal/hostnet/firewall/linux/fake_handle_test.go internal/hostnet/firewall/linux/validation_test.go
git commit -m "feat(hostnet/firewall/linux): validate firewall requests"
```

---

### Task 4: Implement semantic rule model, observation, get/list, and delete

**Files:**
- Create: `internal/hostnet/firewall/linux/rules_linux.go`
- Create: `internal/hostnet/firewall/linux/info_linux.go`
- Create: `internal/hostnet/firewall/linux/expr_linux.go`
- Modify: `internal/hostnet/firewall/linux/fake_handle_test.go`
- Create: `internal/hostnet/firewall/linux/list_get_test.go`
- Modify: `internal/hostnet/firewall/linux/manager_linux.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: Linux manager can translate Govirta-owned nftables rules into `RuleInfo`, list/filter them, get by `RuleRef`, and delete by compact identity.

Acceptance evidence:
- `go test -count=1 ./internal/hostnet/firewall/linux -run 'Test(ListRules|GetRule|Delete)'` passes.
- `go test -count=1 ./internal/hostnet/firewall/...` passes.

- [ ] **Step 2: Define semantic rule representation**

Create `rules_linux.go` with internal semantic structs:

```go
type desiredRule struct {
    family    firewall.TableFamily
    tableName firewall.TableName
    chainName firewall.ChainName
    purpose   firewall.RulePurpose
    owner     firewall.RuleOwner
    priority  firewall.Priority
    summary   firewall.RuleSummary
}

func ruleRefForInfo(info firewall.RuleInfo) firewall.RuleRef {
    return firewall.RuleRef{
        Owner: info.Owner, Purpose: info.Purpose, Family: info.Family,
        TableName: info.TableName, ChainName: info.ChainName, Handle: info.Handle,
    }
}
```

Add helper functions:

- `tableForDesired(rule desiredRule) *nftables.Table`
- `chainForDesired(table *nftables.Table, rule desiredRule) *nftables.Chain`
- `ruleInfoMatchesFilter(info firewall.RuleInfo, filter firewall.RuleFilter) bool`
- `sameMasquerade(left, right *firewall.MasqueradeSummary) bool`
- `sameEndpointAntiSpoofing(left, right *firewall.EndpointAntiSpoofingSummary) bool`

- [ ] **Step 3: Define expression builders and parsers**

Create `expr_linux.go`. Implement builders for:

- `masqueradeExprs(summary firewall.MasqueradeSummary, owner firewall.RuleOwner) []expr.Any`
- `endpointEtherMACDropExprs(summary firewall.EndpointAntiSpoofingSummary, owner firewall.RuleOwner) []expr.Any`
- `endpointIPv4DropExprs(summary firewall.EndpointAntiSpoofingSummary, owner firewall.RuleOwner) []expr.Any`
- `endpointARPMACDropExprs(summary firewall.EndpointAntiSpoofingSummary, owner firewall.RuleOwner) []expr.Any`
- `endpointARPIPv4DropExprs(summary firewall.EndpointAntiSpoofingSummary, owner firewall.RuleOwner) []expr.Any`

Also implement parsers that recognize only Govirta-owned rules:

- `observedRuleInfo(table *nftables.Table, chain *nftables.Chain, rule *nftables.Rule) (firewall.RuleInfo, bool, error)`

Store owner, purpose, and endpoint guard kind in nftables `Rule.UserData`. The `google/nftables` source defines `Rule.UserData []byte` in `rule.go`, so implementation must use that field rather than inferring ownership from expression shape alone. Encode user data as stable UTF-8 key/value text, for example `govirta-owner=<owner>;govirta-purpose=<purpose>;govirta-guard=<guard-kind>`.

The parser must return:

- `(RuleInfo, true, nil)` for recognized Govirta rules;
- `(RuleInfo{}, false, nil)` for non-Govirta rules;
- `(RuleInfo{}, true, firewallerr.ErrInvalidObservedState)` for owner/purpose-marked but unparsable Govirta rules.

- [ ] **Step 4: Implement observation helpers**

Create `info_linux.go` with:

- `listObservedRules(h handle, filter firewall.RuleFilter) ([]firewall.RuleInfo, error)`
- `getObservedRule(h handle, query firewall.RuleQuery) (firewall.RuleInfo, error)`
- `sortRuleInfos(infos []firewall.RuleInfo)` sorted by family/table/chain/purpose/owner/handle.

`listObservedRules` must:

1. call `h.GetTables()`;
2. call `h.GetChains()`;
3. for each chain matching requested table/chain/family, call `h.GetRules(table, chain)`;
4. translate only recognized Govirta rules;
5. return `firewallerr.ErrInvalidObservedState` for broken recognized rules;
6. never return non-Govirta rules.

- [ ] **Step 5: Implement manager get/list/delete methods**

Replace temporary unsupported bodies in `manager_linux.go`:

```go
func (m *Manager) ListRules(ctx context.Context, filter firewall.RuleFilter) ([]firewall.RuleInfo, error) {
    if err := validateRuleFilter(ctx, filter); err != nil {
        return nil, translateError("list firewall rules", err)
    }
    return listObservedRules(m.firewallHandle(), filter)
}
```

`GetRule` validates `query.Ref`, lists matching owner/purpose/family/table/chain, then matches `Handle`. Missing rule returns `firewallerr.ErrNotFound`.

`DeleteMasquerade` and `DeleteEndpointAntiSpoofing` validate the `RuleRef` purpose, get the observed rule, call `h.DelRule(&nftables.Rule{Table: table, Chain: chain, Handle: uint64(ref.Handle)})`, flush, then verify `GetRule` returns not found. Deleting an already absent rule is idempotent success.

- [ ] **Step 6: Expand fake handle**

Expand the Task 3 `fake_handle_test.go` with an in-memory store:

```go
type fakeHandle struct {
    tables []*nftables.Table
    chains []*nftables.Chain
    rules  []*nftables.Rule
    calls  []string
    failures map[string]error
    nextHandle uint64
}
```

Implement all `handle` methods. `AddRule` assigns incrementing handles when `rule.Handle == 0`. `DelRule` removes by family/table/chain/handle. Each method records a call such as `GetTables`, `GetChains`, `GetRules:ip:gv_nat:postrouting`, `AddRule:bridge:gv_filter:forward`, `Flush`.

- [ ] **Step 7: Add list/get/delete tests**

Create `list_get_test.go` covering:

- List ignores non-Govirta rules.
- List returns sorted Govirta rules.
- List filter by owner/purpose/family/table/chain works.
- Get returns a rule matching compact `RuleRef`.
- Get missing handle returns `firewallerr.ErrNotFound`.
- Delete missing rule is success.
- Delete removes only the matching owner/purpose/handle and leaves other rules in the same chain.

- [ ] **Step 8: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/firewall/linux -run 'Test(ListRules|GetRule|Delete)'
go test -count=1 ./internal/hostnet/firewall/...
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/hostnet/firewall/linux/rules_linux.go internal/hostnet/firewall/linux/info_linux.go internal/hostnet/firewall/linux/expr_linux.go internal/hostnet/firewall/linux/fake_handle_test.go internal/hostnet/firewall/linux/list_get_test.go internal/hostnet/firewall/linux/manager_linux.go
git commit -m "feat(hostnet/firewall/linux): observe nftables rules"
```

---

### Task 5: Implement IPv4 MASQUERADE ensure semantics

**Files:**
- Modify: `internal/hostnet/firewall/linux/manager_linux.go`
- Modify: `internal/hostnet/firewall/linux/rules_linux.go`
- Modify: `internal/hostnet/firewall/linux/expr_linux.go`
- Create: `internal/hostnet/firewall/linux/masquerade_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `EnsureMasquerade` creates or reuses one Govirta-owned IPv4 NAT postrouting masquerade rule and reports observed state.

Acceptance evidence:
- `go test -count=1 ./internal/hostnet/firewall/linux -run TestEnsureMasquerade` passes.
- `go test -count=1 ./internal/hostnet/firewall/...` passes.

- [ ] **Step 2: Implement desired masquerade rule builder**

In `rules_linux.go`, add:

```go
func desiredMasqueradeRule(spec firewall.MasqueradeSpec) desiredRule {
    return desiredRule{
        family: firewall.TableFamilyIPv4,
        tableName: spec.TableName,
        chainName: spec.ChainName,
        purpose: firewall.RulePurposeMasquerade,
        owner: spec.RuleOwner,
        priority: spec.Priority,
        summary: firewall.RuleSummary{Masquerade: &firewall.MasqueradeSummary{
            GuestCIDR: spec.GuestCIDR.Masked(),
            EgressInterfaceName: spec.EgressInterfaceName,
            Priority: spec.Priority,
        }},
    }
}
```

- [ ] **Step 3: Implement ensure helper**

Add `ensureDesiredRule(ctx context.Context, h handle, operation string, desired desiredRule) (firewall.RuleInfo, error)`.

Algorithm:

1. list existing Govirta rules in the desired table/chain/owner/purpose;
2. if an equivalent rule exists, return it;
3. if a conflicting rule exists, return `firewallerr.ErrConflict`;
4. add table if absent;
5. add chain if absent;
6. add rule with expressions for desired purpose;
7. call `h.Flush()`;
8. list observed rules again and return the equivalent observed rule;
9. if observation fails after mutation, return `firewallerr.ErrInvalidObservedState` wrapped through `translateError`.

Do not flush the global ruleset. Do not delete non-Govirta rules.

- [ ] **Step 4: Replace `EnsureMasquerade` body**

In `manager_linux.go`:

```go
func (m *Manager) EnsureMasquerade(ctx context.Context, spec firewall.MasqueradeSpec) (firewall.RuleInfo, error) {
    if err := validateMasqueradeSpec(ctx, spec); err != nil {
        return firewall.RuleInfo{}, translateError("ensure masquerade", err)
    }
    return ensureDesiredRule(ctx, m.firewallHandle(), "ensure masquerade", desiredMasqueradeRule(spec))
}
```

- [ ] **Step 5: Add masquerade tests**

Create `masquerade_test.go` covering:

- creates table, chain, rule, and flushes;
- returned `RuleInfo` contains `RulePurposeMasquerade`, owner, handle, and `MasqueradeSummary`;
- second ensure with same spec is idempotent and does not add another rule;
- same owner/table/chain/purpose with different guest CIDR returns `firewallerr.ErrConflict`;
- same owner/table/chain/purpose with different egress interface returns `firewallerr.ErrConflict`;
- handle failure on `Flush` is returned and no success is claimed;
- context canceled before call returns context error and records no fake-handle calls.

Use this valid spec helper:

```go
func validMasqueradeSpec() firewall.MasqueradeSpec {
    return firewall.MasqueradeSpec{
        TableName: "gv_nat",
        ChainName: "postrouting",
        RuleOwner: "govirta-test",
        GuestCIDR: netip.MustParsePrefix("192.168.100.0/24"),
        EgressInterfaceName: "eth0",
        Priority: firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT),
    }
}
```

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/firewall/linux -run TestEnsureMasquerade
go test -count=1 ./internal/hostnet/firewall/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/hostnet/firewall/linux/manager_linux.go internal/hostnet/firewall/linux/rules_linux.go internal/hostnet/firewall/linux/expr_linux.go internal/hostnet/firewall/linux/masquerade_test.go
git commit -m "feat(hostnet/firewall/linux): ensure masquerade rules"
```

---

### Task 6: Implement endpoint anti-spoofing ensure semantics

**Files:**
- Modify: `internal/hostnet/firewall/linux/manager_linux.go`
- Modify: `internal/hostnet/firewall/linux/rules_linux.go`
- Modify: `internal/hostnet/firewall/linux/expr_linux.go`
- Create: `internal/hostnet/firewall/linux/anti_spoofing_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: `EnsureEndpointAntiSpoofing` creates or reuses four bridge-family drop guards for one explicit TAP/MAC/IPv4 endpoint identity.

Acceptance evidence:
- `go test -count=1 ./internal/hostnet/firewall/linux -run TestEnsureEndpointAntiSpoofing` passes.
- `go test -count=1 ./internal/hostnet/firewall/...` passes.

- [ ] **Step 2: Implement desired anti-spoofing rule builders**

Represent one endpoint anti-spoofing policy as four nftables rules, all sharing owner/purpose and endpoint summary. Add a rule-kind discriminator inside the Linux-only semantic model, for example:

```go
type endpointGuardKind string

const (
    endpointGuardEtherMAC endpointGuardKind = "ether-mac"
    endpointGuardIPv4     endpointGuardKind = "ipv4-source"
    endpointGuardARPMAC   endpointGuardKind = "arp-mac"
    endpointGuardARPIPv4  endpointGuardKind = "arp-ipv4"
)
```

Add `desiredEndpointAntiSpoofingRules(spec firewall.EndpointAntiSpoofingSpec) []desiredRule` returning four desired rules with the same `EndpointAntiSpoofingSummary` and distinct guard kind.

- [ ] **Step 3: Implement grouped ensure helper**

Add `ensureDesiredRuleGroup(ctx context.Context, h handle, operation string, desired []desiredRule) (firewall.RuleInfo, error)`.

Algorithm:

1. validate `len(desired) > 0`;
2. list existing Govirta endpoint anti-spoofing rules for owner/table/chain/TAP;
3. if all four equivalent guard kinds exist, return a synthetic `RuleInfo` whose `Ref.Handle` is the lowest observed guard handle and whose summary is endpoint identity;
4. if any existing guard for the same owner/table/chain/TAP has different MAC or IPv4, return `firewallerr.ErrConflict`;
5. add missing guards only;
6. flush once;
7. re-list and require all four guards to be observed.

Returning a representative `RuleInfo` is acceptable because the public operation manages endpoint anti-spoofing as a logical rule group. `ListRules` may return one logical `RuleInfo` per endpoint group rather than four low-level guards.

- [ ] **Step 4: Replace `EnsureEndpointAntiSpoofing` body**

In `manager_linux.go`:

```go
func (m *Manager) EnsureEndpointAntiSpoofing(ctx context.Context, spec firewall.EndpointAntiSpoofingSpec) (firewall.RuleInfo, error) {
    if err := validateEndpointAntiSpoofingSpec(ctx, spec); err != nil {
        return firewall.RuleInfo{}, translateError("ensure endpoint anti-spoofing", err)
    }
    return ensureDesiredRuleGroup(ctx, m.firewallHandle(), "ensure endpoint anti-spoofing", desiredEndpointAntiSpoofingRules(spec))
}
```

- [ ] **Step 5: Add anti-spoofing tests**

Create `anti_spoofing_test.go` covering:

- creates bridge table, forward chain, four guard rules, and flushes once;
- returned `RuleInfo` has purpose `RulePurposeEndpointAntiSpoofing` and endpoint summary;
- list returns one logical endpoint anti-spoofing rule group, not four public duplicates;
- second ensure with same spec is idempotent and creates no new rules;
- same owner/table/chain/TAP with different MAC returns `firewallerr.ErrConflict`;
- same owner/table/chain/TAP with different IPv4 returns `firewallerr.ErrConflict`;
- missing one guard recreates only the missing guard;
- delete endpoint anti-spoofing removes all four guards for the endpoint and leaves other endpoints intact.

Use this valid spec helper:

```go
func validEndpointAntiSpoofingSpec() firewall.EndpointAntiSpoofingSpec {
    return firewall.EndpointAntiSpoofingSpec{
        TableName: "gv_filter",
        ChainName: "forward",
        RuleOwner: "govirta-test",
        BridgeName: "govirta0",
        TapName: "gv-tap0",
        MAC: net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x10, 0x01},
        IPv4: netip.MustParseAddr("192.168.100.10"),
        Priority: firewall.ExplicitPriority(-200, firewall.PriorityNameBridgeFilter),
    }
}
```

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/hostnet/firewall/linux -run TestEnsureEndpointAntiSpoofing
go test -count=1 ./internal/hostnet/firewall/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/hostnet/firewall/linux/manager_linux.go internal/hostnet/firewall/linux/rules_linux.go internal/hostnet/firewall/linux/expr_linux.go internal/hostnet/firewall/linux/anti_spoofing_test.go
git commit -m "feat(hostnet/firewall/linux): ensure anti-spoofing rules"
```

---

### Task 7: Add Linux acceptance coverage for firewall primitives

**Files:**
- Create: `test/acceptance/hostnet_firewall_test.go`
- Modify: `test/acceptance/harness.go`
- Modify: `scripts/acceptance.sh`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: Lima Linux acceptance verifies nftables lifecycle for MASQUERADE and endpoint anti-spoofing through both the manager and `nft list ruleset`.

Acceptance evidence:
- `go test -v -tags acceptance -count=1 ./test/acceptance/...` passes inside `scripts/acceptance.sh full`.
- On failure, nftables diagnostics are logged.

- [ ] **Step 2: Ensure `nft` is available in Lima acceptance**

Add `nftables` to the package install list used inside the Lima guest provisioning. Do not install nftables on the macOS host.

Add this preflight inside the Lima guest command path before `go test`:

```bash
if ! command -v nft >/dev/null 2>&1; then
  echo "nft command is required for hostnet firewall acceptance" >&2
  exit 1
fi
```

Place this inside the Lima guest command path, not in the host shell path.

- [ ] **Step 3: Add nft diagnostics helper**

In `test/acceptance/harness.go`, add:

```go
func logFirewallDiagnostics(t *testing.T, ctx context.Context) {
    t.Helper()
    stdout, stderr, err := runCommand(ctx, "nft", "list", "ruleset")
    t.Logf("nft list ruleset: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
}
```

- [ ] **Step 4: Add acceptance tests**

Create `test/acceptance/hostnet_firewall_test.go` with two tests.

`TestHostnetFirewallMasqueradePrimitives`:

1. `requireHostnetAcceptanceEnv(t)`;
2. create `ctx` with one minute timeout;
3. create `manager, err := firewalllinux.NewManager()` and fail on error;
4. build explicit spec with table `gv_acc_nat`, chain `postrouting`, owner `govirta-acceptance`, guest CIDR `192.168.100.0/24`, egress interface `lo`, priority `ExplicitPriority(100, PriorityNameSrcNAT)`;
5. `EnsureMasquerade`;
6. `ListRules` with owner/purpose/table/chain and assert one rule;
7. run `nft list ruleset` and assert stdout contains `gv_acc_nat`, `postrouting`, and `masquerade`;
8. cleanup with `DeleteMasquerade` and assert `ListRules` returns zero.

`TestHostnetFirewallAntiSpoofingPrimitives`:

1. create a bridge and TAP using `hostnet/link/linux` with unique names;
2. ensure endpoint anti-spoofing for table `gv_acc_filter`, chain `forward`, owner `govirta-acceptance`, bridge name, TAP name, MAC `02:00:00:00:20:01`, IPv4 `192.168.100.10`, priority `ExplicitPriority(-200, PriorityNameBridgeFilter)`;
3. `ListRules` with owner/purpose/table/chain and assert one logical endpoint group;
4. run `nft list ruleset` and assert stdout contains table, chain, TAP name, MAC string, IPv4 string, and `drop`;
5. cleanup with `DeleteEndpointAntiSpoofing` and host link deletion;
6. after cleanup, assert `ListRules` returns zero.

Use `t.Cleanup` and `errors.Join` for cleanup errors.

- [ ] **Step 5: Run targeted acceptance where available**

Run:

```bash
scripts/acceptance.sh full
```

Expected: PASS. If runtime exceeds local budget, record the last log path under `test/log/` and the failing command output, then fix before proceeding.

- [ ] **Step 6: Commit**

```bash
git add test/acceptance/hostnet_firewall_test.go test/acceptance/harness.go scripts/acceptance.sh
git commit -m "test(acceptance): cover hostnet firewall primitives"
```

---

### Task 8: Update project knowledge base and run final verification

**Files:**
- Modify: `AGENTS.md`
- Modify: `test/acceptance/doc.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: repository knowledge base documents the new firewall primitive boundary, and the full local verification loop passes.

Acceptance evidence:
- `scripts/verify.sh` passes.
- `go test -race ./internal/hostnet/firewall/...` passes.
- `scripts/acceptance.sh full` passes or the user explicitly defers full Linux acceptance with a recorded reason.
- `AGENTS.md` contains a `flow-hostnet-firewall` entry and WHERE TO LOOK row for `internal/hostnet/firewall`.

- [ ] **Step 2: Update root knowledge base**

Update `AGENTS.md`:

- Add `internal/hostnet/firewall` to structure/where-to-look.
- Add code map entries for `firewall.Manager`, `firewalllinux.Manager`, `EnsureMasquerade`, and `EnsureEndpointAntiSpoofing`.
- Add a flow named `flow-hostnet-firewall` after `flow-hostnet-route`.
- Add anti-patterns:
  - firewall package must not enable IPv4 forwarding;
  - firewall package must not create bridge/TAP;
  - firewall package must not infer endpoint MAC/IP/TAP;
  - firewall package must not flush non-Govirta rules.

- [ ] **Step 3: Update acceptance documentation**

Update `test/acceptance/doc.go` because it currently states host networking acceptance covers bridge/TAP and route primitives only and does not verify firewall behavior. Replace that wording with:

```go
// scripts/acceptance.sh runs the suite inside a Lima guest, explicitly enables
// IPv4 forwarding with sysctl before go test, and additionally sets
// GOVIRTA_ACCEPTANCE_LIMA_GUEST=1. Host networking acceptance covers real Linux
// bridge/TAP, route, and firewall primitive lifecycle behavior. It does not yet
// verify full VM internet egress because guest default route, DNS, NAT, and
// orchestration-level connectivity are validated in a later end-to-end flow.
```

- [ ] **Step 4: Run fast verification**

Run:

```bash
scripts/verify.sh
```

Expected: PASS.

- [ ] **Step 5: Run race verification for new package**

Run:

```bash
go test -race -count=1 ./internal/hostnet/firewall/...
```

Expected: PASS.

- [ ] **Step 6: Run full Linux acceptance**

Run:

```bash
scripts/acceptance.sh full
```

Expected: PASS and a new ignored log appears under `test/log/`.

- [ ] **Step 7: Inspect final git state and diff**

Run:

```bash
git status --short
git diff --stat
git diff
```

Expected: only the intended knowledge-base/doc updates remain unstaged if Tasks 1-7 were committed separately.

- [ ] **Step 8: Commit documentation updates**

```bash
git add AGENTS.md test/acceptance/doc.go
git commit -m "docs: document hostnet firewall primitives"
```

---

## Final verification checklist

Before reporting implementation complete, run and record:

```bash
go test -count=1 ./internal/hostnet/firewall/...
go test -count=1 ./internal/hostnet/...
go test -race -count=1 ./internal/hostnet/firewall/...
scripts/verify.sh
scripts/acceptance.sh full
```

If `scripts/acceptance.sh full` cannot run in the current environment, report the exact blocker and do not claim full Linux acceptance passed.
