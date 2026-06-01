# Host Network Link Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement Govirta's `internal/hostnet/link` abstraction over Linux netlink and prove bridge+TAP networking works end-to-end with a CirrOS guest in acceptance.

**Architecture:** The cross-platform root `internal/hostnet/link` package owns stable Govirta contracts and constants, `internal/hostnet/link/linkerr` owns stable sentinel errors, and Linux-only files under `internal/hostnet/link/linux` hide all `github.com/vishvananda/netlink` details behind a fakeable handle. The Linux acceptance test is build-tagged `acceptance && linux`, runs only inside the Lima guest, creates bridge/TAP through `link/linux.Manager`, starts QEMU with direct-kernel CirrOS arguments, verifies host-to-guest ping, and archives full host-side logs under `test/log/`.

**Tech Stack:** Go 1.26, `github.com/vishvananda/netlink v1.3.1`, Go stdlib `context` / `errors` / `net` / `os/exec` / `testing`, existing QMP acceptance helpers, POSIX `sh`, Lima Linux acceptance VM, QEMU aarch64 direct-kernel CirrOS boot.

---

## Platform and Build-Tag Strategy

The repository is developed on macOS, but real netlink operations are Linux-only.

| Area | Build tags | Verification |
| --- | --- | --- |
| `internal/hostnet/link` root package | none | macOS and Linux `go test ./...` |
| `internal/hostnet/link/linkerr` | none | macOS and Linux `go test ./...` |
| `internal/hostnet/link/linux/*.go` | `//go:build linux` | Lima Linux only; `GOOS=linux` compile check allowed on macOS |
| `internal/hostnet/link/linux/*_test.go` | `//go:build linux` | Lima Linux only |
| `test/acceptance/hostnet_link_test.go` | `//go:build acceptance && linux` | `scripts/acceptance.sh full` |

This is mandatory: do not make macOS `scripts/verify.sh` compile Linux-only netlink files.

## File Structure

Create and modify these files:

- Modify: `go.mod` and `go.sum` — add `github.com/vishvananda/netlink v1.3.1` and transitive checksums.
- Create: `internal/hostnet/link/constants.go` — root strong types and constants.
- Create: `internal/hostnet/link/link.go` — root public request/result contracts.
- Create: `internal/hostnet/link/noop.go` and `internal/hostnet/link/noop_test.go` — cross-platform no-op manager.
- Create: `internal/hostnet/link/linkerr/errors.go` — stable sentinel errors.
- Create: `internal/hostnet/link/linux/handle_linux.go` — Linux-only fakeable handle and real adapter.
- Create: `internal/hostnet/link/linux/manager_linux.go` — Linux-only manager methods and rollback orchestration.
- Create: `internal/hostnet/link/linux/validate_linux.go` — Linux-only request validation.
- Create: `internal/hostnet/link/linux/errors_linux.go` — Linux-only netlink/syscall error translation.
- Create: `internal/hostnet/link/linux/info_linux.go` — Linux-only `LinkInfo` conversion.
- Create: `internal/hostnet/link/linux/*_test.go` — Linux-only fake handle and behavior tests.
- Modify: `.gitignore`; create `test/log/.gitkeep` — keep log directory, ignore log files.
- Modify: `scripts/acceptance.sh` — portable checksum, CirrOS kernel/initramfs cache, Lima env vars, log capture without losing exit codes.
- Modify: `test/acceptance/doc.go` and `test/acceptance/harness.go` — env docs, hostnet env parsing, QEMU start helper, diagnostics helpers.
- Create: `test/acceptance/hostnet_link_test.go` — Linux-only acceptance test.
- Modify: `AGENTS.md` — document the new package and acceptance behavior after implementation.

Expected source file sizes for AGENTS.md 第十八章 planning discipline:

| File | Expected lines | Boundary |
| --- | ---: | --- |
| `internal/hostnet/link/constants.go` | 45-80 | Types and constants only. |
| `internal/hostnet/link/link.go` | 70-110 | Public contract only. |
| `internal/hostnet/link/noop.go` | 60-90 | No-op implementation only. |
| `internal/hostnet/link/linkerr/errors.go` | 20-40 | Sentinel errors only. |
| `internal/hostnet/link/linux/handle_linux.go` | 55-90 | Linux handle interface and real adapter only. |
| `internal/hostnet/link/linux/manager_linux.go` | 260-380 | Linux methods and rollback orchestration. |
| `internal/hostnet/link/linux/validate_linux.go` | 120-190 | Validation helpers only. |
| `internal/hostnet/link/linux/errors_linux.go` | 60-100 | Error translation only. |
| `internal/hostnet/link/linux/info_linux.go` | 120-190 | State projection only. |
| `internal/hostnet/link/linux/*_test.go` | 120-300 each | Split by behavior area. |
| `scripts/acceptance.sh` | Existing ~109 + 100-160 | Shell harness remains below soft limit. |
| `test/acceptance/harness.go` | Existing ~330 + 100-160 | Shared helpers only; remains below soft limit. |
| `test/acceptance/hostnet_link_test.go` | 180-280 | One end-to-end network scenario. |
| `test/acceptance/doc.go` | Existing ~20 + 20-40 | Package docs only. |

Dependency order:

```text
root contracts -> Linux build-tagged foundation -> bridge Ensure -> TAP Ensure -> read/delete/list -> unit tests in Linux -> script logging/cache -> acceptance test -> docs -> verification
```

---

### Task 1: Add dependency and cross-platform root contracts

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/hostnet/link/constants.go`
- Create: `internal/hostnet/link/link.go`
- Create: `internal/hostnet/link/linkerr/errors.go`
- Create: `internal/hostnet/link/noop.go`
- Create: `internal/hostnet/link/noop_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: root contracts compile on macOS and Linux, require all behavior-affecting parameters explicitly, and do not import `github.com/vishvananda/netlink`.

Acceptance evidence:
- `go test -count=1 ./internal/hostnet/link ./internal/hostnet/link/linkerr` passes on macOS.
- `go list -m github.com/vishvananda/netlink` prints `github.com/vishvananda/netlink v1.3.1`.
- `grep -R "github.com/vishvananda/netlink" internal/hostnet/link --include='*.go'` returns only `internal/hostnet/link/linux/*` after Linux files exist.

- [ ] **Step 2: Add the netlink dependency explicitly**

Run:

```bash
go get github.com/vishvananda/netlink@v1.3.1
go list -m github.com/vishvananda/netlink
```

Expected output from the second command:

```text
github.com/vishvananda/netlink v1.3.1
```

- [ ] **Step 3: Create `internal/hostnet/link/linkerr/errors.go`**

```go
// Package linkerr defines stable host link error classes.
package linkerr

import "errors"

var (
	ErrInvalidRequest = errors.New("invalid host link request")
	ErrNotFound       = errors.New("host link not found")
	ErrAlreadyExists  = errors.New("host link already exists")
	ErrConflict       = errors.New("host link conflict")
	ErrPermission     = errors.New("host link permission denied")
	ErrIncompleteList = errors.New("host link list incomplete")
	ErrUnsupported    = errors.New("host link operation unsupported")
)
```

- [ ] **Step 4: Create `internal/hostnet/link/constants.go`**

```go
package link

type Name string
type Kind string
type AdminState string
type VNetHeaderMode string

type UID struct {
	Value uint32
	Set   bool
}

type GID struct {
	Value uint32
	Set   bool
}

const (
	KindAny    Kind = "any"
	KindBridge Kind = "bridge"
	KindTap    Kind = "tap"

	AdminStateUp   AdminState = "up"
	AdminStateDown AdminState = "down"

	VNetHeaderEnabled  VNetHeaderMode = "enabled"
	VNetHeaderDisabled VNetHeaderMode = "disabled"

	MaxInterfaceNameLength = 15
)

func ExplicitUID(value uint32) UID { return UID{Value: value, Set: true} }
func ExplicitGID(value uint32) GID { return GID{Value: value, Set: true} }
```

- [ ] **Step 5: Create `internal/hostnet/link/link.go`**

```go
package link

import (
	"context"
	"net"
)

type Manager interface {
	EnsureBridge(ctx context.Context, spec BridgeSpec) (LinkInfo, error)
	EnsureTap(ctx context.Context, spec TapSpec) (LinkInfo, error)
	Delete(ctx context.Context, name Name) error
	Exists(ctx context.Context, name Name) (bool, error)
	Get(ctx context.Context, name Name) (LinkInfo, error)
	List(ctx context.Context, filter ListFilter) ([]LinkInfo, error)
}

type BridgeSpec struct {
	Name        Name
	GatewayCIDR string
	MTU         int
	MAC         net.HardwareAddr
}

type TapSpec struct {
	Name       Name
	BridgeName Name
	OwnerUID   UID
	OwnerGID   GID
	MTU        int
	MAC        net.HardwareAddr
	VNetHeader VNetHeaderMode
}

type ListFilter struct { Kind Kind }

type LinkInfo struct {
	Name       Name
	Kind       Kind
	Index      int
	MTU        int
	MAC        net.HardwareAddr
	AdminState AdminState
	MasterName Name
	Addresses  []string
}
```

- [ ] **Step 6: Create cross-platform no-op manager and tests**

`internal/hostnet/link/noop.go` must return `linkerr.ErrInvalidRequest` for nil context, return `ctx.Err()` for canceled context, and return `linkerr.ErrUnsupported` for live operations. Add `noop_test.go` with tests for `ExplicitUID(0)`, nil context, canceled context, and unsupported operation.

Verification command:

```bash
gofmt -w internal/hostnet/link
go test -count=1 ./internal/hostnet/link ./internal/hostnet/link/linkerr
```

Expected: PASS.

- [ ] **Step 7: Commit this slice if commits are approved for the execution session**

If the user explicitly allowed commits:

```bash
git add go.mod go.sum internal/hostnet/link
git commit -m "feat: add host link contracts"
```

If commits are not approved, skip the commit and record changed files in the execution summary.

---

### Task 2: Add Linux-only foundation, validation, and error translation

**Files:**
- Create: `internal/hostnet/link/linux/handle_linux.go`
- Create: `internal/hostnet/link/linux/validate_linux.go`
- Create: `internal/hostnet/link/linux/errors_linux.go`
- Create: `internal/hostnet/link/linux/info_linux.go`
- Create: `internal/hostnet/link/linux/fake_handle_test.go`
- Create: `internal/hostnet/link/linux/validation_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the Linux package compiles only on Linux, wraps netlink behind a fakeable handle, rejects invalid requests, translates kernel/netlink errors, and projects actual state without hiding discovery errors.

Acceptance evidence:
- `GOOS=linux go test -c ./internal/hostnet/link/linux` succeeds on macOS.
- Inside Lima, `go test -count=1 ./internal/hostnet/link/linux -run 'TestValidate|TestTranslate|TestLinkInfo'` passes.

- [ ] **Step 2: Create `handle_linux.go` with Linux build tag**

```go
//go:build linux

package linux

import (
	"net"

	"github.com/vishvananda/netlink"
)

type handle interface {
	LinkByName(name string) (netlink.Link, error)
	LinkAdd(link netlink.Link) error
	LinkDel(link netlink.Link) error
	LinkSetUp(link netlink.Link) error
	LinkSetMTU(link netlink.Link, mtu int) error
	LinkSetHardwareAddr(link netlink.Link, hw net.HardwareAddr) error
	LinkSetMaster(link netlink.Link, master netlink.Link) error
	AddrReplace(link netlink.Link, addr *netlink.Addr) error
	LinkList() ([]netlink.Link, error)
	AddrList(link netlink.Link, family int) ([]netlink.Addr, error)
}

type realHandle struct{}

func (realHandle) LinkByName(name string) (netlink.Link, error) { return netlink.LinkByName(name) }
func (realHandle) LinkAdd(link netlink.Link) error { return netlink.LinkAdd(link) }
func (realHandle) LinkDel(link netlink.Link) error { return netlink.LinkDel(link) }
func (realHandle) LinkSetUp(link netlink.Link) error { return netlink.LinkSetUp(link) }
func (realHandle) LinkSetMTU(link netlink.Link, mtu int) error { return netlink.LinkSetMTU(link, mtu) }
func (realHandle) LinkSetHardwareAddr(link netlink.Link, hw net.HardwareAddr) error { return netlink.LinkSetHardwareAddr(link, hw) }
func (realHandle) LinkSetMaster(link netlink.Link, master netlink.Link) error { return netlink.LinkSetMaster(link, master) }
func (realHandle) AddrReplace(link netlink.Link, addr *netlink.Addr) error { return netlink.AddrReplace(link, addr) }
func (realHandle) LinkList() ([]netlink.Link, error) { return netlink.LinkList() }
func (realHandle) AddrList(link netlink.Link, family int) ([]netlink.Addr, error) { return netlink.AddrList(link, family) }
```

- [ ] **Step 3: Create validation and error translation**

Every file in `internal/hostnet/link/linux` starts with `//go:build linux`.

Validation semantics:
- `ctx == nil` → wraps `linkerr.ErrInvalidRequest`.
- canceled/deadline context → return `ctx.Err()` directly.
- interface name required and `len(name) <= 15`.
- MTU must be positive.
- MAC must be 6-byte locally administered unicast.
- `TapSpec.OwnerUID.Set` and `OwnerGID.Set` must be true.
- `VNetHeader` must be `enabled` or `disabled`.
- `ListFilter.Kind` must be `KindAny`, `KindBridge`, or `KindTap`; empty is invalid.

Error translation semantics:
- An error already wrapping a `linkerr` sentinel is preserved as that class; this keeps fakes deterministic without constructing dependency-private netlink errors.
- `netlink.LinkNotFoundError` → wraps `linkerr.ErrNotFound`.
- `os.ErrPermission`, `syscall.EPERM`, `syscall.EACCES` → wraps `linkerr.ErrPermission`.
- `syscall.EEXIST` → wraps `linkerr.ErrAlreadyExists`.
- `syscall.EINVAL` → wraps `linkerr.ErrInvalidRequest`.
- `netlink.ErrDumpInterrupted` → wraps `linkerr.ErrIncompleteList`.

When fake tests need a not-found error, return a project sentinel instead of constructing
`netlink.LinkNotFoundError` directly:

```go
fmt.Errorf("fake link %q: %w", name, linkerr.ErrNotFound)
```

Production `translateError` still handles real `netlink.LinkNotFoundError`; tests avoid depending on
the dependency's internal struct field shape.

- [ ] **Step 4: Create `info_linux.go` with actual state projection**

Required behavior:
- `kindOf` returns `KindBridge` only for `*netlink.Bridge`.
- `kindOf` returns `KindTap` only for `*netlink.Tuntap` with `Mode == netlink.TUNTAP_MODE_TAP`.
- `*netlink.Tuntap` with TUN mode returns `linkerr.ErrConflict` or `linkerr.ErrUnsupported`, not `KindTap`.
- `linkInfo` returns IPv4 and IPv6 CIDR strings from `AddrList(link, netlink.FAMILY_ALL)`, sorted lexicographically.
- `linkInfo` resolves `MasterName` through a prebuilt `index -> name` map or propagates `LinkList` errors; it must not silently return empty master on `ErrDumpInterrupted`.
- `List` output is sorted by `Name` for deterministic callers and tests.
- VNET_HDR observation is Linux-manager-internal only and does not enter cross-platform `LinkInfo`. Use a private typed enum such as `vnetHeaderObservedEnabled`, `vnetHeaderObservedDisabled`, and `vnetHeaderObservedUnknown`; unknown existing TAP state returns `linkerr.ErrUnsupported`.

- [ ] **Step 5: Create Linux fake handle tests**

`fake_handle_test.go` must also start with `//go:build linux`. The fake must:
- store links by name;
- record call order;
- support injected per-call failures;
- return `fmt.Errorf("fake link %q: %w", name, linkerr.ErrNotFound)` for missing links;
- mutate fake attrs for `LinkSet*` calls;
- return `ErrDumpInterrupted` when injected for `LinkList` or `AddrList`.

- [ ] **Step 6: Add validation matrix tests**

Create `validation_test.go` with table-driven tests covering:
- nil context;
- canceled context;
- empty name;
- 16-byte name;
- invalid `GatewayCIDR`;
- `MTU <= 0`;
- nil MAC;
- multicast MAC;
- globally administered MAC;
- unset `OwnerUID.Set`;
- unset `OwnerGID.Set`;
- empty `VNetHeader`;
- empty `ListFilter.Kind`.

Each invalid request assertion uses `errors.Is(err, linkerr.ErrInvalidRequest)`, except canceled context which uses `errors.Is(err, context.Canceled)`.

- [ ] **Step 7: Run targeted verification**

Run on macOS:

```bash
CGO_ENABLED=0 GOOS=linux go test -c -o .tmp/link-linux.test ./internal/hostnet/link/linux
go test -count=1 ./internal/hostnet/link ./internal/hostnet/link/linkerr
```

Run inside Lima during acceptance work:

```bash
go test -count=1 ./internal/hostnet/link/linux -run 'TestValidate|TestTranslate|TestLinkInfo'
```

Expected: PASS.

- [ ] **Step 8: Commit this slice if commits are approved for the execution session**

If commits are approved:

```bash
git add internal/hostnet/link/linux
git commit -m "feat: add Linux host link foundation"
```

---

### Task 3: Implement Linux manager methods with rollback and actual-state reads

**Files:**
- Create or modify: `internal/hostnet/link/linux/manager_linux.go`
- Create or modify: `internal/hostnet/link/linux/bridge_test.go`
- Create or modify: `internal/hostnet/link/linux/tap_test.go`
- Create or modify: `internal/hostnet/link/linux/list_delete_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: Linux `Manager` implements bridge/TAP Ensure, Delete, Exists, Get, and List with explicit validation, rollback for newly-created links, deterministic List output, VNET_HDR conflict handling, and no stale state returns.

Acceptance evidence:
- Inside Lima, `go test -count=1 ./internal/hostnet/link/linux` passes.
- `CGO_ENABLED=0 GOOS=linux go test -c -o .tmp/link-linux.test ./internal/hostnet/link/linux` succeeds on macOS.
- Tests cover all cases listed in Step 6.

- [ ] **Step 2: Implement manager constructor**

`manager_linux.go` starts with `//go:build linux` and defines:

```go
type Manager struct { handle handle }

func NewManager() *Manager { return NewManagerWithHandle(realHandle{}) }

func NewManagerWithHandle(h handle) *Manager {
	if h == nil { h = realHandle{} }
	return &Manager{handle: h}
}
```

- [ ] **Step 3: Implement `EnsureBridge`**

Required sequence:
1. `checkContext(ctx)` and `validateBridgeSpec(spec)`.
2. `LinkByName(spec.Name)`.
3. If missing, `LinkAdd(&netlink.Bridge{LinkAttrs: attrs})`; mark `created=true` only if `LinkAdd` succeeds.
4. If existing type is not `*netlink.Bridge`, return `linkerr.ErrConflict`.
5. Run `LinkSetHardwareAddr`, `LinkSetMTU`, `AddrReplace`, `LinkSetUp`, checking `ctx.Err()` before each netlink call.
6. If a post-create step fails and `created == true`, call `LinkDel` and return `errors.Join(primaryErr, rollbackErr)`.
7. If the bridge existed before the call, do not delete it on reconcile failure.
8. After successful configuration, call `LinkByName(spec.Name)` again and return `linkInfo(actual)` so `LinkInfo` reflects actual kernel state, not stale Go attrs.

- [ ] **Step 4: Implement `EnsureTap`**

Required sequence:
1. `checkContext(ctx)` and `validateTapSpec(spec)`.
2. `LinkByName(spec.BridgeName)`, require `*netlink.Bridge`.
3. `LinkByName(spec.Name)`.
4. If missing, `LinkAdd(&netlink.Tuntap{Mode:TUNTAP_MODE_TAP, Flags:TUNTAP_NO_PI plus optional TUNTAP_VNET_HDR, Owner:spec.OwnerUID.Value, Group:spec.OwnerGID.Value})`; mark `created=true` only if `LinkAdd` succeeds.
5. If existing type is not `*netlink.Tuntap` or `Mode != netlink.TUNTAP_MODE_TAP`, return `linkerr.ErrConflict`.
6. For existing TAP, compare observable VNET_HDR state to `spec.VNetHeader`; mismatch returns `linkerr.ErrConflict`. If the implementation cannot distinguish unknown from disabled for an existing TAP, return `linkerr.ErrUnsupported` and require caller to delete/recreate.
7. Run `LinkSetHardwareAddr`, `LinkSetMTU`, `LinkSetMaster`, `LinkSetUp`, checking `ctx.Err()` before each netlink call.
8. If a post-create step fails and `created == true`, call `LinkDel` and return `errors.Join(primaryErr, rollbackErr)`.
9. After successful configuration, call `LinkByName(spec.Name)` again and return `linkInfo(actual)`.

- [ ] **Step 5: Implement Delete, Exists, Get, and List**

Required behavior:
- `Delete(ctx, missing)` returns nil.
- `Delete(ctx, existing)` calls `LinkDel` and returns translated errors.
- `Exists(ctx, missing)` returns `(false, nil)`.
- `Get(ctx, missing)` wraps `linkerr.ErrNotFound`.
- `List(ctx, ListFilter{Kind: KindAny|KindBridge|KindTap})` filters explicitly and sorts returned `[]LinkInfo` by `Name`.
- `List(ctx, ListFilter{})` wraps `linkerr.ErrInvalidRequest`.
- `List` propagates `linkerr.ErrIncompleteList` for `netlink.ErrDumpInterrupted` from `LinkList` or address/master lookup.

- [ ] **Step 6: Add complete Linux manager tests**

Test functions required:
- `TestEnsureBridgeCreatesBridge`.
- `TestEnsureBridgeIsIdempotent`.
- `TestEnsureBridgeRejectsExistingNonBridge`.
- `TestEnsureBridgeRollsBackCreatedBridgeOnAddressFailure`.
- `TestEnsureBridgeJoinsRollbackFailure`.
- `TestEnsureTapCreatesTapAttachedToBridge`.
- `TestEnsureTapIsIdempotent`.
- `TestEnsureTapRejectsExistingNonTap`.
- `TestEnsureTapRejectsExistingTunMode`.
- `TestEnsureTapRejectsVNetHeaderConflict`.
- `TestEnsureTapReturnsUnsupportedWhenVNetHeaderCannotBeObserved` if implementation has an unknown observation state.
- `TestEnsureTapRollsBackCreatedTapOnMasterFailure`.
- `TestEnsureTapJoinsRollbackFailure`.
- `TestDeleteMissingLinkIsIdempotent`.
- `TestExistsMissingLinkReturnsFalse`.
- `TestGetMissingLinkReturnsNotFound`.
- `TestListRequiresExplicitKind`.
- `TestListFiltersAndSortsBridgeAndTap`.
- `TestListReturnsIncompleteListOnDumpInterrupted`.
- `TestGetPropagatesMasterLookupDumpInterrupted`.
- `TestEnsureTapPropagatesFinalLinkInfoMasterLookupError`.
- `TestListDoesNotSilentlyDropMasterNameOnLinkListError`.
- `TestPermissionErrorsTranslateToPermission`.
- `TestCanceledContextForEveryManagerMethod`.

All tests use fake handles; no unit test creates real host links.

- [ ] **Step 7: Run verification**

Run on macOS:

```bash
GOOS=linux go test -c ./internal/hostnet/link/linux
go test -count=1 ./internal/hostnet/link ./internal/hostnet/link/linkerr
```

Run inside Lima after acceptance harness is available:

```bash
go test -count=1 ./internal/hostnet/link/linux
go test -race -count=1 ./internal/hostnet/link/linux
```

Expected: PASS.

- [ ] **Step 8: Commit this slice if commits are approved for the execution session**

If commits are approved:

```bash
git add internal/hostnet/link/linux
git commit -m "feat: manage Linux host links"
```

---

### Task 4: Add portable acceptance cache and host-side log archival

**Files:**
- Modify: `.gitignore`
- Create: `test/log/.gitkeep`
- Modify: `scripts/acceptance.sh`
- Modify: `test/acceptance/doc.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: acceptance runs archive full logs without swallowing failures, cache disk/kernel/initramfs with checksum verification on macOS, and inject explicit Lima-only environment flags.

Acceptance evidence:
- `scripts/acceptance.sh check-tools` verifies host `limactl`, `curl`, `awk`, and one supported MD5 command.
- `scripts/acceptance.sh full` returns the real acceptance status even when logging is enabled.
- `test/log/*.log` is ignored and `test/log/.gitkeep` is trackable.

- [ ] **Step 2: Update `.gitignore` and create log marker**

Add:

```gitignore
test/log/*.log
!test/log/.gitkeep
```

Create empty `test/log/.gitkeep`.

Run:

```bash
git check-ignore test/log/2099-01-01-000000-acceptance-full.log
```

Expected output:

```text
test/log/2099-01-01-000000-acceptance-full.log
```

- [ ] **Step 3: Add portable checksum helpers to `scripts/acceptance.sh`**

Keep the script POSIX `sh`. Add host-side helpers:

```sh
md5_file() {
	file=$1
	if command -v md5sum >/dev/null 2>&1; then
		md5sum "$file" | awk '{ print $1 }'
	elif command -v md5 >/dev/null 2>&1; then
		md5 -q "$file"
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -md5 -r "$file" | awk '{ print $1 }'
	else
		printf 'missing md5 checksum tool: install md5sum, md5, or openssl\n' >&2
		exit 1
	fi
}
```

Update `check_tools()` to require `limactl`, `curl`, `awk`, and one MD5 tool. Do not require Linux `ip` or `ping` on macOS; those are checked inside the guest before `go test`.

- [ ] **Step 4: Add CirrOS resources and checksum verification**

Use explicit variables:

```sh
cirros_base_url="https://download.cirros-cloud.net/0.6.2"
cirros_md5_url="$cirros_base_url/MD5SUMS"
cirros_md5_file="$cache_dir/images/cirros-0.6.2-MD5SUMS"
cirros_url="$cirros_base_url/cirros-0.6.2-aarch64-disk.img"
cirros_kernel_url="$cirros_base_url/cirros-0.6.2-aarch64-kernel"
cirros_initramfs_url="$cirros_base_url/cirros-0.6.2-aarch64-initramfs"
cirros_image="$cache_dir/images/cirros-aarch64.qcow2"
cirros_kernel="$cache_dir/images/cirros-0.6.2-aarch64-kernel"
cirros_initramfs="$cache_dir/images/cirros-0.6.2-aarch64-initramfs"
log_dir="$repo_root/test/log"
```

Implement `download_file`, `ensure_md5sums`, and `verify_md5(target, upstream_name)`. `verify_md5` uses `md5_file`; on mismatch it removes the corrupted target and exits non-zero.

`prepare_cache()` downloads and verifies:

```sh
ensure_md5sums
download_file "$cirros_url" "$cirros_image"
download_file "$cirros_kernel_url" "$cirros_kernel"
download_file "$cirros_initramfs_url" "$cirros_initramfs"
verify_md5 "$cirros_image" "cirros-0.6.2-aarch64-disk.img"
verify_md5 "$cirros_kernel" "cirros-0.6.2-aarch64-kernel"
verify_md5 "$cirros_initramfs" "cirros-0.6.2-aarch64-initramfs"
```

- [ ] **Step 5: Add log capture that preserves failure status**

Do not use `run_acceptance 2>&1 | tee "$log_file"` because POSIX `sh` has no `pipefail`.

Add:

```sh
timestamp() { date '+%Y-%m-%d-%H%M%S'; }

run_acceptance_logged() {
	mkdir -p "$log_dir"
	log_file="$log_dir/$(timestamp)-acceptance-$mode.log"
	printf 'writing acceptance log to %s\n' "$log_file"
	set +e
	run_acceptance >"$log_file" 2>&1
	status=$?
	set -e
	cat "$log_file" || true
	return "$status"
}
```

Update the `full | linux)` case to call `run_acceptance_logged`.

- [ ] **Step 6: Inject hostnet env and guest tool checks**

Inside the `limactl shell ... sh -eu -c` command, before `go test`, run:

```sh
command -v ip >/dev/null || { printf 'missing guest tool: ip\n' >&2; exit 1; }
command -v ping >/dev/null || { printf 'missing guest tool: ping\n' >&2; exit 1; }
```

Add env vars to `sudo -E env`:

```sh
GOVIRTA_ACCEPTANCE_LIMA_GUEST=1 \
GOVIRTA_ACCEPTANCE_CIRROS_KERNEL=/govirta-cache/images/cirros-0.6.2-aarch64-kernel \
GOVIRTA_ACCEPTANCE_CIRROS_INITRAMFS=/govirta-cache/images/cirros-0.6.2-aarch64-initramfs \
```

- [ ] **Step 7: Update `test/acceptance/doc.go`**

Document:
- `GOVIRTA_ACCEPTANCE_LIMA_GUEST=1` protects host-network tests from arbitrary Linux namespaces.
- `GOVIRTA_ACCEPTANCE_CIRROS_KERNEL` and `GOVIRTA_ACCEPTANCE_CIRROS_INITRAMFS` point to direct-kernel CirrOS assets.
- `scripts/acceptance.sh` writes host-side full logs to `test/log/YYYY-MM-DD-HHMMSS-acceptance-<mode>.log`.

- [ ] **Step 8: Run targeted verification**

Run:

```bash
scripts/acceptance.sh check-tools
git check-ignore test/log/2099-01-01-000000-acceptance-full.log
```

Expected: both commands succeed with the expected ignore output.

- [ ] **Step 9: Commit this slice if commits are approved**

If commits are approved:

```bash
git add .gitignore scripts/acceptance.sh test/log/.gitkeep test/acceptance/doc.go
git commit -m "test: archive acceptance logs"
```

---

### Task 5: Add Linux-only hostnet acceptance test and diagnostics

**Files:**
- Modify: `test/acceptance/harness.go`
- Create: `test/acceptance/hostnet_link_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: the acceptance suite proves real bridge+TAP networking can boot CirrOS with a static guest IP and reach the guest from the host.

Acceptance evidence:
- Inside `scripts/acceptance.sh full`, `TestHostnetLinkBridgeTapEndToEnd` passes.
- The host log under `test/log/` includes bridge/TAP info, QEMU argv, serial output, QMP status, and ping output.
- Failure paths run host link cleanup and print diagnostics.

- [ ] **Step 2: Extend `harness.go` environment parsing**

Add constants and parser:

```go
const (
	envCirrosKernel         = "GOVIRTA_ACCEPTANCE_CIRROS_KERNEL"
	envCirrosInitramfs     = "GOVIRTA_ACCEPTANCE_CIRROS_INITRAMFS"
	envAcceptanceLimaGuest = "GOVIRTA_ACCEPTANCE_LIMA_GUEST"
)

type hostnetAcceptanceEnv struct {
	acceptanceEnv
	Kernel    string
	Initramfs string
}

func requireHostnetAcceptanceEnv(t *testing.T) hostnetAcceptanceEnv {
	t.Helper()
	env := requireAcceptanceEnv(t)
	if os.Getenv(envAcceptanceLimaGuest) != "1" {
		t.Fatalf("%s=1 is required for hostnet acceptance", envAcceptanceLimaGuest)
	}
	return hostnetAcceptanceEnv{
		acceptanceEnv: env,
		Kernel:        requireFileEnv(t, envCirrosKernel),
		Initramfs:     requireFileEnv(t, envCirrosInitramfs),
	}
}
```

- [ ] **Step 3: Add final-form diagnostics helpers**

Do not add a fatal `waitForPing` and later replace it. Add a boolean helper immediately:

```go
func pingUntilSuccess(t *testing.T, ctx context.Context, ip string) bool {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var lastStdout []byte
	var lastStderr []byte
	var lastErr error
	for time.Now().Before(deadline) {
		stdout, stderr, err := runCommand(ctx, "ping", "-c", "3", "-W", "1", ip)
		lastStdout, lastStderr, lastErr = stdout, stderr, err
		if err == nil {
			t.Logf("ping %s succeeded:\nstdout:\n%s\nstderr:\n%s", ip, stdout, stderr)
			return true
		}
		select {
		case <-ctx.Done():
			t.Logf("ping %s stopped by context: %v", ip, ctx.Err())
			t.Logf("last ping err=%v\nstdout:\n%s\nstderr:\n%s", lastErr, lastStdout, lastStderr)
			return false
		case <-time.After(1 * time.Second):
		}
	}
	t.Logf("last ping err=%v\nstdout:\n%s\nstderr:\n%s", lastErr, lastStdout, lastStderr)
	return false
}

func logNetworkDiagnostics(t *testing.T, ctx context.Context) {
	t.Helper()
	for _, args := range [][]string{{"addr", "show"}, {"route", "show"}, {"link", "show", "type", "bridge"}, {"link", "show", "gvtap0"}} {
		stdout, stderr, err := runCommand(ctx, "ip", args...)
		t.Logf("ip %s err=%v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
}
```

Use the existing `strings` import in `harness.go`; add it only if absent.

- [ ] **Step 4: Add QEMU start helper compatible with existing `stopQEMU`**

If no helper exists, add:

```go
func startQEMUCommand(cmd *exec.Cmd) (*bytes.Buffer, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return &stderr, err
	}
	return &stderr, nil
}
```

This helper does not use shell command strings.

- [ ] **Step 5: Create `test/acceptance/hostnet_link_test.go`**

The file starts with:

```go
//go:build acceptance && linux
```

Test structure:
1. `env := requireHostnetAcceptanceEnv(t)`.
2. Create `ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)`.
3. Create `manager := linklinux.NewManager()`.
4. Run initial `Delete(tap)` / `Delete(bridge)` cleanup.
5. Immediately register `t.Cleanup` for host links before `EnsureBridge` so failures never leak interfaces.
6. `EnsureBridge` with `gvbr0`, `192.168.100.1/24`, MTU 1500, MAC `02:00:00:00:01:01`.
7. `EnsureTap` with `gvtap0`, explicit root UID/GID, MTU 1500, MAC `02:00:00:00:01:02`, VNET_HDR enabled.
8. `Get(gvtap0)` and assert `MasterName == gvbr0` before QEMU start.
9. Copy the CirrOS disk to `t.TempDir()`.
10. Start QEMU with `exec.CommandContext(ctx, env.QEMU, args...)` and this explicit `[]string` args list; do not use a shell command string:

```go
guestIP := "192.168.100.10"
gatewayIP := "192.168.100.1"
appendLine := "console=ttyAMA0 ip=192.168.100.10::192.168.100.1:255.255.255.0:govirta-net:eth0:off"
args := []string{
	"-machine", "virt,accel=kvm",
	"-cpu", "host",
	"-m", "256M",
	"-smp", "1",
	"-kernel", env.Kernel,
	"-initrd", env.Initramfs,
	"-append", appendLine,
	"-drive", "file=" + diskPath + ",if=virtio,format=qcow2",
	"-netdev", "tap,id=net0,ifname=gvtap0,script=no,downscript=no,vhost=on",
	"-device", "virtio-net-pci,netdev=net0,mac=02:00:00:00:01:02,romfile=",
	"-qmp", "unix:" + qmpPath + ",server=on,wait=off",
	"-serial", "unix:" + serialPath + ",server=on,wait=off",
	"-display", "none",
	"-no-reboot",
	"-no-shutdown",
}
t.Logf("guest ip: %s gateway: %s append: %s", guestIP, gatewayIP, appendLine)
t.Logf("qemu argv: %s %v", env.QEMU, args)
```

11. Register QEMU cleanup that calls existing `stopQEMU(cleanupCtx, qmpPath, cmd)` and logs returned errors; do not pass `*testing.T` to `stopQEMU`.
12. Wait for QMP running and serial login marker.
13. Call `pingUntilSuccess(t, ctx, guestIP)`; if false, log bridge/TAP info, QEMU argv, QMP query result or error, serial output, QEMU stderr, `ip addr show`, `ip route show`, `ip link show type bridge`, and `ip link show gvtap0`, then fail.

Host link cleanup helper must return error instead of calling `t.Fatalf` from cleanup:

```go
func cleanupHostLinks(ctx context.Context, manager hostlink.Manager, tapName hostlink.Name, bridgeName hostlink.Name) error {
	return errors.Join(manager.Delete(ctx, tapName), manager.Delete(ctx, bridgeName))
}
```

When used at test start, a cleanup error is fatal. When used in `t.Cleanup`, log it with `t.Errorf` so the original failure is not hidden.
The cleanup log must include the TAP and bridge names, for example `cleanup host links tap=gvtap0 bridge=gvbr0: <err>`.

- [ ] **Step 6: Run Linux compile check from macOS and full acceptance**

Run on macOS:

```bash
GOOS=linux go test -tags acceptance -run TestHostnetLinkBridgeTapEndToEnd -count=0 ./test/acceptance/...
```

Expected: package compiles for Linux.

Run:

```bash
scripts/acceptance.sh full
```

Expected:
- Exit code 0.
- Output includes `--- PASS: TestHostnetLinkBridgeTapEndToEnd`.
- `test/log/*-acceptance-full.log` exists and contains the test output.

- [ ] **Step 7: Commit this slice if commits are approved**

If commits are approved:

```bash
git add test/acceptance/harness.go test/acceptance/hostnet_link_test.go
git commit -m "test: verify host bridge TAP networking"
```

---

### Task 6: Update AGENTS and run final verification

**Files:**
- Modify: `AGENTS.md`
- Any source/test/script files from previous tasks

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: project knowledge reflects the new hostnet boundary and every required verification path passes with archived acceptance logs.

Acceptance evidence:
- `scripts/verify.sh` passes on macOS.
- `GOOS=linux go test -c ./internal/hostnet/link/linux` passes on macOS.
- `scripts/acceptance.sh full` passes and creates a `test/log/*-acceptance-full.log` file.
- `git status --short` does not show `test/log/*.log`.

- [ ] **Step 2: Update `AGENTS.md`**

Add real paths and line numbers after implementation:
- `STRUCTURE`: `internal/hostnet/link` host link primitive boundary.
- `WHERE TO LOOK`: host bridge/TAP primitives.
- `CODE MAP`: `link.Manager`, `link/linux.Manager`, and `linkerr` entries.
- `CALL GRAPHS & DATA FLOW`: `EnsureBridge` and `EnsureTap` flow.
- `ACCEPTANCE TESTS`: bridge+TAP+direct-kernel CirrOS ping and `test/log/` archival.

Do not add dangling anchors or references.

- [ ] **Step 3: Run final verification**

Run:

```bash
gofmt -l .
go test -count=1 ./internal/hostnet/link ./internal/hostnet/link/linkerr
GOOS=linux go test -c ./internal/hostnet/link/linux
GOOS=linux go test -tags acceptance -run TestHostnetLinkBridgeTapEndToEnd -count=0 ./test/acceptance/...
scripts/verify.sh
scripts/acceptance.sh full
git status --short
```

Expected:
- `gofmt -l .` prints nothing.
- Go test and compile commands exit 0.
- `scripts/verify.sh` exits 0 on macOS.
- `scripts/acceptance.sh full` exits 0 and creates a dated log file under `test/log/`.
- `git status --short` lists only intentional source/docs/config changes and no `test/log/*.log`.

- [ ] **Step 4: Commit final docs slice if commits are approved**

If commits are approved:

```bash
git add AGENTS.md
git commit -m "docs: document host link networking"
```

---

## Final Verification Checklist

Run before reporting completion:

```bash
go test -count=1 ./internal/hostnet/link ./internal/hostnet/link/linkerr
GOOS=linux go test -c ./internal/hostnet/link/linux
GOOS=linux go test -tags acceptance -run TestHostnetLinkBridgeTapEndToEnd -count=0 ./test/acceptance/...
scripts/verify.sh
scripts/acceptance.sh full
git status --short
```

Expected final evidence:
- All verification commands exit 0 except `git status --short`, which reports intended changes if commits were not approved.
- `scripts/acceptance.sh full` creates `test/log/YYYY-MM-DD-HHMMSS-acceptance-full.log`.
- The acceptance log contains `TestHostnetLinkBridgeTapEndToEnd`, bridge/TAP `LinkInfo`, QEMU argv, serial output, QMP state, diagnostics on failure, and ping output on success.
- `test/log/*.log` files are ignored by git.

## Plan Self-Review

- Spec coverage: root contract, `linkerr`, Linux netlink implementation, explicit UID/GID, VNET_HDR, bridge MAC, MTU, Delete/Exists/Get/List, rollback, CirrOS kernel/initramfs resources, checksum, acceptance logs, Linux-only build tags, and AGENTS updates are covered.
- Placeholder scan: no unresolved placeholder markers remain.
- Type consistency: tasks consistently use `hostlink.ExplicitUID`, `hostlink.ExplicitGID`, `VNetHeaderMode`, `KindAny`, `BridgeSpec`, `TapSpec`, and `LinkInfo` as defined in Task 1.
