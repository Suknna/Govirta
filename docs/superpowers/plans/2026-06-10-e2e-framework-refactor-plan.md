# e2e 测试框架重构实现计划：guest-side live 断言

**日期**：2026-06-10
**关联 spec**：`docs/superpowers/specs/2026-06-10-e2e-framework-refactor-design.md`
**关联**：memory 1061、note #34（刀 4 遗留前置项）；刀 4 冷快照 spec items 2/5

## 目标

把 e2e 升级为顶层权威门禁：新增 Go 层 guest-exec 能力，让 closure_test 能在资源存活期进 Lima guest 做 live 实况断言。落地刀 4 遗留的快照 items 2/5（建后 `qemu-img snapshot -l` 看到、删后看不到），并把 `verify_no_orphans` 的 shell 检查迁进 Go 层统一为 Guest handle 断言。

## 范围

- **只触 e2e 层**：`test/e2e/` + `scripts/e2e.sh`。
- **不碰** verify.sh / acceptance.sh / manifest / 任何被测产品代码。
- **不引入**第三方测试框架（自研轻量 helper）。
- 内核身份派生**复用** `internal/node/identity`（同 module import），不重新实现。

## 真实签名核实（已读源码）

- `identity.DeriveNICIdentity(vmUID string, nicIndex int) NICIdentity`（`.TapName link.Name`、`.AntiSpoofChain firewall.ChainName`）
- `identity.DeriveNetworkIdentity(networkName string) NetworkIdentity`（`.MasqueradeChain`、`.ForwardChain firewall.ChainName`）
- closure_test 现有 helper：`runCtl(ctx, ctl, args...) (string, error)`、`waitObjectPhase`、`waitGone`、`deleteAndWaitGone`、`requireEnv`、`expectReferencedRejection`、`snapshotColdCycle`（line 155，待替换）
- e2e.sh：`verify_no_orphans`（line 335-416，待迁移）、`run_closure`（line 312-319，env 注入点）、`run_full`（line 418-430，调 verify_no_orphans 处）
- govirtlet 启动**无** `--storage-root`；block qcow2 根 = StoragePool manifest `spec.storageRoot` = `/var/lib/govirta/block`（`01-storagepool-block.json`）
- 现有 env：`GOVIRTA_E2E`/`_SERVER`/`_GOVIRTCTL`/`_MANIFESTS`/`_NODE`（e2e.sh line 313-318）

## 行数预判（第十八章）

| 文件 | 预估 | 说明 |
| --- | --- | --- |
| `test/e2e/guest_paths.go` | ~90 | 常量 + qcow2 路径派生 + identity 包薄封装 |
| `test/e2e/guest_paths_test.go` | ~110 | qcow2 路径 / identity 封装单测（不依赖 Lima） |
| `test/e2e/guest.go` | ~210 | Guest handle + Exec + 语义化 live 查询/断言方法 |
| `test/e2e/lifecycle.go` | ~120 | resourceLifecycle + applyAndVerify/deleteAndVerify |
| `test/e2e/closure_test.go` | 443→~460 | 接入 Guest handle，替换 snapshotColdCycle，teardown 后调 orphan 断言 |
| `scripts/e2e.sh` | 461→~380 | 移除 verify_no_orphans，加 2 env |

无文件逼近 800 硬上限。

## 任务拆分（依赖序）

### Task 1：guest 路径/身份派生（`guest_paths.go` + 单测）

无依赖，先行。建立路径常量与派生封装，让后续 Guest 方法有权威输入。

**新建 `test/e2e/guest_paths.go`**（`//go:build e2e`）：

```go
//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"

	"github.com/suknna/govirta/internal/node/identity"
)

const (
	// guestStateRoot 是 e2e 约定的 guest 状态根。e2e.sh 的 guest_state_root 必须
	// 等于此值（跨语言契约，见 spec 组件 5）。
	guestStateRoot   = "/var/lib/govirta"
	guestRuntimeRoot = guestStateRoot + "/runtime"
	// guestBlockStorageRoot 是 block StoragePool 的 storageRoot，必须等于
	// 01-storagepool-block.json 的 spec.storageRoot。local.Driver 在其下拼
	// pool/<poolName>/...，所以它是 guestQcowPath 的权威输入。
	guestBlockStorageRoot = guestStateRoot + "/block"
)

// guestQcowPath 对齐 local.Driver 的 qcow2 布局：
//
//	<storageRoot>/pool/<poolName>/<vmUID>/<vmName>-disk-<diskIndex>.qcow2
//
// 这与 local.Driver.pathForCreate 的布局一致（该函数 unexported，无法直接复用，
// 故在测试侧重建；storageRoot 以 StoragePool manifest 的 spec.storageRoot 为权威输入）。
func guestQcowPath(storageRoot, poolName, vmUID, vmName string, diskIndex int) string {
	return filepath.Join(
		storageRoot, "pool", poolName, vmUID,
		fmt.Sprintf("%s-disk-%d.qcow2", vmName, diskIndex),
	)
}

// guestRuntimeDir 是 vmm 以 VM uid 为名的 runtime 目录。
func guestRuntimeDir(vmUID string) string {
	return filepath.Join(guestRuntimeRoot, vmUID)
}

// guestTAPName 复用控制器的权威派生，绝不重抄 sha256 逻辑。
func guestTAPName(vmUID string, nicIndex int) string {
	return string(identity.DeriveNICIdentity(vmUID, nicIndex).TapName)
}

// guestAntiSpoofChain 复用控制器派生的 NIC 反欺骗链名（gv-as-<tap>）。
func guestAntiSpoofChain(vmUID string, nicIndex int) string {
	return string(identity.DeriveNICIdentity(vmUID, nicIndex).AntiSpoofChain)
}

// guestMasqueradeChain 复用控制器派生的网络 masquerade 链名（gv-masq-<network>）。
func guestMasqueradeChain(network string) string {
	return string(identity.DeriveNetworkIdentity(network).MasqueradeChain)
}

// guestForwardChain 复用控制器派生的网络 forward-accept 链名（gv-fwd-<network>）。
func guestForwardChain(network string) string {
	return string(identity.DeriveNetworkIdentity(network).ForwardChain)
}
```

**新建 `test/e2e/guest_paths_test.go`**（带 `//go:build e2e`——必须与 `guest_paths.go` 同 tag 才能引用其符号；这些纯逻辑测试不依赖 Lima，`go test -tags e2e ./test/e2e/` 在 macOS 本地即可跑，作为快速反馈）：

```go
//go:build e2e

package e2e

import "testing"

func TestGuestQcowPath(t *testing.T) {
	got := guestQcowPath("/var/lib/govirta/block", "pool-block", "vm-e2e-001", "vm-e2e", 0)
	want := "/var/lib/govirta/block/pool/pool-block/vm-e2e-001/vm-e2e-disk-0.qcow2"
	if got != want {
		t.Fatalf("guestQcowPath = %q, want %q", got, want)
	}
}

func TestGuestTAPNameMatchesIdentity(t *testing.T) {
	// 复用 identity 包，验证封装调用对了（identity 自身有派生正确性单测）。
	got := guestTAPName("vm-e2e-001", 0)
	if got == "" || got[:2] != "gv" {
		t.Fatalf("guestTAPName = %q, want gv-prefixed name", got)
	}
}
```

注意：`guest_paths.go` 带 `//go:build e2e`，但 `guest_paths_test.go` 不带 tag 则无法引用它（编译不到）。**两个文件必须同 build tag**。决策：`guest_paths_test.go` 也加 `//go:build e2e`，单测通过 `go test -tags e2e ./test/e2e/`（不依赖 Lima 的纯逻辑测试，能在 macOS 本地跑，作为快速反馈）。在 spec 验证策略里这是「Go 单测覆盖纯逻辑」。

**验证**：`go test -tags e2e -run TestGuest ./test/e2e/`（macOS 本地，不需 Lima）通过。

**提交**：`test(e2e): add guest path/identity derivation helpers`

### Task 2：Guest handle（`guest.go`）

依赖 Task 1（路径派生）。建 Guest handle + Exec 底层原语 + 语义化 live 查询/断言方法。

**新建 `test/e2e/guest.go`**（`//go:build e2e`）。完整骨架：

```go
//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const (
	e2eLimaInstanceEnv = "GOVIRTA_E2E_LIMA_INSTANCE"
	e2eLimaHomeEnv     = "GOVIRTA_E2E_LIMA_HOME"
)

// Guest is a handle to the Lima guest the e2e closure runs against. It exposes
// guest-side live verification (exec into the guest, query real kernel/QEMU/disk
// state) so a test can assert lower-layer reality, not just the master's API
// projection (上下一致铁律).
type Guest struct {
	t        *testing.T
	instance string
	limaHome string
}

// newGuest reads the Lima coordinates e2e.sh passes via env. Missing env is a
// hard failure (same gate as requireEnv): a guest-side assertion without a guest
// to reach is a test bug, not a skip.
func newGuest(t *testing.T) *Guest {
	t.Helper()
	return &Guest{
		t:        t,
		instance: requireEnv(t, e2eLimaInstanceEnv),
		limaHome: requireEnv(t, e2eLimaHomeEnv),
	}
}

// Exec runs cmd inside the Lima guest and returns its result. stdout/stderr are
// the guest command's output; exitCode is the guest command's exit code; err is
// reserved for the connection layer (limactl itself failed, or ctx cancelled).
// A non-zero guest exit code is NOT an err — the caller decides whether that is
// a failure, so a check can distinguish "could not reach the guest" from
// "reached it, command reported absent/present".
func (g *Guest) Exec(ctx context.Context, cmd string) (stdout, stderr string, exitCode int, err error) {
	g.t.Helper()
	// LIMA_HOME via process env mirrors e2e.sh `LIMA_HOME=... limactl shell`.
	// `sh -c cmd` wrapping makes exitCode the guest command's exit code.
	c := exec.CommandContext(ctx, "limactl", "shell", g.instance, "--", "sh", "-c", cmd)
	c.Env = append(os.Environ(), "LIMA_HOME="+g.limaHome)
	var outBuf, errBuf bytes.Buffer
	c.Stdout, c.Stderr = &outBuf, &errBuf
	runErr := c.Run()
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		exitCode = 0
	case errors.As(runErr, &exitErr):
		exitCode = exitErr.ExitCode() // guest command exit code; err stays nil
	default:
		err = runErr // limactl failure / ctx cancel (connection layer)
	}
	return outBuf.String(), errBuf.String(), exitCode, err
}

// --- 快照 live 实况（items 2/5 落地）---

// QcowSnapshots returns the internal snapshot tags in a guest qcow2 file by
// parsing `sudo qemu-img snapshot -l <path>`. The tag is the second
// whitespace-delimited field of each data row (ID is first); header/empty rows
// yield nothing. Mirrors local.Driver.snapshotListContains' column model.
func (g *Guest) QcowSnapshots(ctx context.Context, qcowPath string) ([]string, error) {
	stdout, stderr, code, err := g.Exec(ctx, "sudo qemu-img snapshot -l "+qcowPath)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("qemu-img snapshot -l %q exit %d: %s", qcowPath, code, stderr)
	}
	var tags []string
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		// data rows: ID TAG ... ; skip header ("Snapshot list:", "ID TAG ...")
		if len(fields) >= 2 && fields[0] != "ID" && fields[0] != "Snapshot" {
			// ID is numeric on data rows; header "ID" already excluded.
			tags = append(tags, fields[1])
		}
	}
	return tags, nil
}

// AssertQcowHasSnapshot fails the test unless tag is an internal snapshot in the
// guest qcow2 file (post-create live proof the node ran qemu-img snapshot -c).
func (g *Guest) AssertQcowHasSnapshot(ctx context.Context, qcowPath, tag string) {
	g.t.Helper()
	tags, err := g.QcowSnapshots(ctx, qcowPath)
	if err != nil {
		g.t.Fatalf("list qcow snapshots %q: %v", qcowPath, err)
	}
	for _, got := range tags {
		if got == tag {
			return
		}
	}
	g.t.Fatalf("qcow %q missing expected snapshot tag %q; live tags: %v", qcowPath, tag, tags)
}

// AssertQcowNoSnapshot fails the test if tag is still an internal snapshot in
// the guest qcow2 file (post-delete live proof the node ran qemu-img snapshot -d).
func (g *Guest) AssertQcowNoSnapshot(ctx context.Context, qcowPath, tag string) {
	g.t.Helper()
	tags, err := g.QcowSnapshots(ctx, qcowPath)
	if err != nil {
		g.t.Fatalf("list qcow snapshots %q: %v", qcowPath, err)
	}
	for _, got := range tags {
		if got == tag {
			g.t.Fatalf("qcow %q still has snapshot tag %q after delete; live tags: %v", qcowPath, tag, tags)
		}
	}
}

// --- orphan 检查（从 e2e.sh verify_no_orphans 迁移，逐项对应）---

// assertAbsent is the shared shape: run a guest probe, fail if the resource is
// still present. probeCmd must exit 0 when the resource EXISTS (so the helper
// treats exit 0 as "leaked"). connection-layer err always fails (上下一致:
// an unreadable probe must not be mistaken for "absent").
//
// 见实现注意：每个 orphan 断言的 probe 语义不同（test -e / ip link show / nft
// grep / pgrep），不能强行套一个 probeCmd 模板——下面逐个实现。

// AssertNoQEMUProcess fails if a QEMU process keyed by the VM uid's runtime path
// is still running. Matches the uid-keyed runtime path QEMU embeds in its argv
// (-pidfile/-chardev under <runtimeRoot>/<vmUID>), mirroring the e2e.sh probe.
// pgrep self-match 规避：探针经 `limactl shell -- sh -c "<cmd>"` 执行，cmd 字符串
// 里已展开 runtimeDir，故探针 sh 自身 argv 同时含该路径——若用 `pgrep -af`（-f 匹配
// 全 argv）会匹配到探针 sh 自己，导致 orphan 检查永远非空、永远 fail。改用 `pgrep -a`
// （不带 -f，只按进程名 comm 匹配），探针的 comm 是 `sh`、不匹配 `qemu-system`，结构性
// 排除自匹配；QEMU 进程的 argv 仍嵌 runtimeDir，故 `grep -F` 过滤照常生效（spec M3）。
func (g *Guest) AssertNoQEMUProcess(ctx context.Context, vmUID string) {
	g.t.Helper()
	runtimeDir := guestRuntimeDir(vmUID)
	cmd := "pgrep -a qemu-system | grep -F " + shellQuote(runtimeDir) + " || true"
	stdout, _, code, err := g.Exec(ctx, cmd)
	if err != nil {
		g.t.Fatalf("probe QEMU process for %q: %v", vmUID, err)
	}
	_ = code // `|| true` makes exit always 0; presence is judged by stdout
	if strings.TrimSpace(stdout) != "" {
		g.t.Fatalf("QEMU process still running for VM uid %q:\n%s", vmUID, stdout)
	}
}

// AssertNoLink fails if a network link (TAP or bridge) named linkName still
// exists. `ip link show <name>` exits 0 when present, non-zero when absent.
func (g *Guest) AssertNoLink(ctx context.Context, linkName string) {
	g.t.Helper()
	_, _, code, err := g.Exec(ctx, "ip link show "+shellQuote(linkName))
	if err != nil {
		g.t.Fatalf("probe link %q: %v", linkName, err)
	}
	if code == 0 {
		g.t.Fatalf("network link still present: %q", linkName)
	}
}

// AssertNoNftablesChain fails if chain still appears in the guest nftables
// ruleset. Reading the ruleset must succeed: a probe that cannot read the
// ruleset is a hard failure, never silently "absent" (spec M1, e2e.sh L385).
func (g *Guest) AssertNoNftablesChain(ctx context.Context, chain string) {
	g.t.Helper()
	stdout, stderr, code, err := g.Exec(ctx, "sudo nft list ruleset")
	if err != nil {
		g.t.Fatalf("read nftables ruleset: %v", err)
	}
	if code != 0 {
		g.t.Fatalf("cannot read nftables ruleset (exit %d): %s", code, stderr)
	}
	if strings.Contains(stdout, chain) {
		g.t.Fatalf("nftables chain still present: %q", chain)
	}
}

// AssertNoQcow2 fails if the guest qcow2 file still exists. `sudo test -e`
// exits 0 when present.
func (g *Guest) AssertNoQcow2(ctx context.Context, qcowPath string) {
	g.t.Helper()
	_, _, code, err := g.Exec(ctx, "sudo test -e "+shellQuote(qcowPath))
	if err != nil {
		g.t.Fatalf("probe qcow2 %q: %v", qcowPath, err)
	}
	if code == 0 {
		g.t.Fatalf("block volume qcow2 still present: %q", qcowPath)
	}
}

// AssertNoRuntimeDir fails if the VM's runtime dir still exists.
func (g *Guest) AssertNoRuntimeDir(ctx context.Context, vmUID string) {
	g.t.Helper()
	dir := guestRuntimeDir(vmUID)
	_, _, code, err := g.Exec(ctx, "sudo test -e "+shellQuote(dir))
	if err != nil {
		g.t.Fatalf("probe runtime dir %q: %v", dir, err)
	}
	if code == 0 {
		g.t.Fatalf("VM runtime dir still present: %q", dir)
	}
}

// shellQuote single-quotes a path for safe `sh -c` interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

实现注意：
- `shellQuote` 防注入（路径含特殊字符）；qcow 路径/链名都来自受控派生，但保持防御。
- `AssertNoQEMUProcess` 的 pgrep 自匹配规避：原 shell 用 `grep -vx "$$"`，迁到 Go 经 `limactl shell` 后进程树不同，改用 `pgrep -a`（不带 `-f`，只按进程名 comm 匹配）+ `grep -F 运行时路径`——探针 comm 是 `sh` 不匹配 `qemu-system`，结构性排除自匹配，QEMU argv 仍嵌运行时路径供 grep 过滤；**实现时在真实 e2e 跑一次确认不自匹配**（spec M3）。
- `AssertNoLink` 合并了原 shell 的 TAP 与 bridge 两个检查（同 `ip link show` 语义）；调用方分别传 tap 名和 bridge 名。

**验证**：`go build -tags e2e ./test/e2e/`（编译通过；本任务不跑 Lima，真实验证在 Task 6）。

**提交**：`test(e2e): add Guest handle with exec and live-state assertions`

### Task 3：生命周期阶段 helper（`lifecycle.go`）

依赖无（纯编排，用现有 closure helper）。把 apply→waitReady→delete→waitGone 抽成带钩子的可复用动作。

**新建 `test/e2e/lifecycle.go`**（`//go:build e2e`）：

```go
//go:build e2e

package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resourceLifecycle declares one resource's standard lifecycle with optional
// guest-side live assertion hooks. The skeleton (apply→waitReady, delete→
// waitGone) is reused; afterReady/afterGone are the declarative insertion points
// for live verification (上下一致: assert lower-layer reality, not just API).
type resourceLifecycle struct {
	manifest  string // file name under the manifests dir, e.g. "08-snapshot.json"
	kind      string // "Snapshot"
	name      string // "snap-e2e"
	waitPhase string // "ready"
	waitFor   time.Duration

	afterReady func(ctx context.Context) // runs after the object reaches waitPhase
	afterGone  func(ctx context.Context) // runs after the object reaches 404
}

// applyAndVerify applies the manifest, waits for waitPhase, then runs afterReady.
func applyAndVerify(ctx context.Context, t *testing.T, ctl, server, manifests string, spec resourceLifecycle) {
	t.Helper()
	path := filepath.Join(manifests, spec.manifest)
	out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", path)
	if err != nil {
		t.Fatalf("apply %s failed: %v\noutput:\n%s", spec.manifest, err, out)
	}
	t.Logf("applied %s: %s", spec.manifest, strings.TrimSpace(out))
	waitObjectPhase(ctx, t, ctl, server, spec.kind, spec.name, spec.waitPhase, spec.waitFor)
	if spec.afterReady != nil {
		spec.afterReady(ctx)
	}
}

// deleteAndVerify deletes the object, waits for 404, then runs afterGone.
func deleteAndVerify(ctx context.Context, t *testing.T, ctl, server string, spec resourceLifecycle) {
	t.Helper()
	deleteAndWaitGone(ctx, t, ctl, server, spec.kind, spec.name, spec.waitFor)
	if spec.afterGone != nil {
		spec.afterGone(ctx)
	}
}
```

实现注意：
- `deleteAndWaitGone`（closure_test.go:393）已封装「delete→断言 accepted→waitGone」，直接复用。
- `waitObjectPhase`（closure_test.go:211）已封装轮询，直接复用。
- 钩子是同步执行（spec M4）：`afterReady`/`afterGone` 在 helper 的 goroutine 里直接调用，`t.Fatalf` 语义正确。**不要**在 goroutine 里跑钩子。

**验证**：`go build -tags e2e ./test/e2e/`。

**提交**：`test(e2e): add resource lifecycle helper with guest assertion hooks`

### Task 4：closure_test 接入（`closure_test.go`）

依赖 Task 1-3。三处改动：(a) 构造 Guest handle，(b) 替换 `snapshotColdCycle` 为 lifecycle + 钩子落地 items 2/5，(c) teardown 后调迁移来的 orphan 断言。

**改动 a — 构造 Guest handle**：在 `TestDistributedSpineClosure`（line 66）创建 ctx 后加：
```go
g := newGuest(t)
```

**改动 b — 替换 snapshotColdCycle**：现有 `snapshotColdCycle`（line 155-173）用 lifecycle + 钩子重写。落地 items 2/5：
```go
func snapshotColdCycle(ctx context.Context, t *testing.T, ctl, server, manifests string, g *Guest) {
	t.Helper()
	// qcow2 root = block StoragePool spec.storageRoot (01-storagepool-block.json).
	qcow := guestQcowPath(guestBlockStorageRoot, poolBlock, vmUID, vmName, diskIndex)

	applyAndVerify(ctx, t, ctl, server, manifests, resourceLifecycle{
		manifest: snapshotManifestName, kind: "Snapshot", name: snapName,
		waitPhase: "ready", waitFor: 2 * time.Minute,
		afterReady: func(ctx context.Context) {
			// items 2/5: post-create the node really ran qemu-img snapshot -c.
			g.AssertQcowHasSnapshot(ctx, qcow, snapUID)
		},
	})

	// reverse-reference edge: deleting the VM is refused while the snapshot pins it.
	expectReferencedRejection(ctx, t, ctl, server, "VM", vmName)

	deleteAndVerify(ctx, t, ctl, server, resourceLifecycle{
		kind: "Snapshot", name: snapName, waitFor: 2 * time.Minute,
		afterGone: func(ctx context.Context) {
			// items 2/5: post-delete the node really ran qemu-img snapshot -d.
			g.AssertQcowNoSnapshot(ctx, qcow, snapUID)
		},
	})
}
```
调用点（line 100 附近）改为传 `g`：`snapshotColdCycle(ctx, t, ctl, server, manifests, g)`。

需要的常量：`vmUID`/`diskIndex` 当前散在别处或硬编码——核实并补常量（`vmUID = "vm-e2e-001"`、`diskIndex = 0`，与 manifest 一致）。`snapName`/`snapUID`/`poolBlock`/`vmName` 已有（snapName line 35、snapshotManifestName line 53、poolBlock line 33、vmName line 28）；`snapUID` 若无则补（`= "snap-e2e-001"`，与 08-snapshot.json 一致）。

**改动 c — teardown 后 orphan 断言**：`teardownSpine`（line 317）结尾、所有资源 404 后，加 Go 层 orphan 断言（等价替换 e2e.sh 的 verify_no_orphans）：
```go
// 迁移自 e2e.sh verify_no_orphans：teardown 后证明 guest 内无 live 残留
// （上下一致：API 404 不等于 live 资源真没了）。
func assertNoOrphans(ctx context.Context, t *testing.T, g *Guest) {
	t.Helper()
	g.AssertNoQEMUProcess(ctx, vmUID)
	g.AssertNoRuntimeDir(ctx, vmUID)
	g.AssertNoLink(ctx, guestTAPName(vmUID, 0))             // NIC TAP
	g.AssertNoNftablesChain(ctx, guestAntiSpoofChain(vmUID, 0))
	g.AssertNoLink(ctx, orphanBridge)                       // network bridge
	g.AssertNoNftablesChain(ctx, guestMasqueradeChain(netName))
	g.AssertNoNftablesChain(ctx, guestForwardChain(netName))
	g.AssertNoQcow2(ctx, guestQcowPath(guestBlockStorageRoot, poolBlock, vmUID, vmName, diskIndex))
	t.Logf("host-side orphan check passed: no live VM/TAP/bridge/nftables/qcow2 resources remain")
}
```
`teardownSpine` 需要拿到 `g`——改其签名 `teardownSpine(ctx, t, ctl, server, g)`，结尾调 `assertNoOrphans(ctx, t, g)`。`orphanBridge` 常量 = `"govirta0"`（05-network.json spec.bridgeName）；`netName` 已有（line 30）。

实现注意：
- bridge 名 `govirta0` 来自 network manifest 的 `spec.bridgeName`，不是派生值——补常量 `orphanBridge = "govirta0"`。
- nic index 固定 0（06-nic.json 单 nicRef，与原 shell 注释一致）。

**验证**：`go build -tags e2e ./test/e2e/` + `go vet -tags e2e ./test/e2e/`。

**提交**：`test(e2e): wire Guest handle into closure for live snapshot + orphan checks`

### Task 5：e2e.sh 职责收缩（`scripts/e2e.sh`）

依赖 Task 4（Go 侧 orphan 断言就位后才能删 shell 的）。

改动：
1. **删除 `verify_no_orphans` 函数**（line 335-416，~80 行）。
2. **`run_full`**（line 418-430）去掉 `verify_no_orphans` 调用（orphan 检查现在在 go test 内）。
3. **`run_closure`**（line 312-319）新增两个 env：
   ```sh
   GOVIRTA_E2E_LIMA_INSTANCE="$instance_name" \
   GOVIRTA_E2E_LIMA_HOME="$lima_home" \
   ```
4. **删除现在仅 verify_no_orphans 用的 orphan_* 变量**（line 76-81 `orphan_vm_uid` 等）——核实它们除 verify_no_orphans 外无其他引用（`git grep orphan_ scripts/e2e.sh`）后删除，连同 line 63-75 的注释块。
5. **跨语言路径契约注释**：在 `guest_state_root`（line 54）处加注释，标注「必须等于 Go 的 `guestStateRoot` 常量（test/e2e/guest_paths.go），见 spec 组件 5」。

实现注意：
- 删 orphan_* 变量前必须 `git grep` 确认无其他引用（避免删了还在用的变量）。
- `run_full` 去掉 `verify_no_orphans` 后，guest live 验证完全靠 go test——`run_closure` 失败即 e2e 失败，门禁不削弱。

**验证**：`sh -n scripts/e2e.sh`（语法检查）+ `git grep -n verify_no_orphans scripts/e2e.sh`（确认无残留引用）。

**提交**：`test(e2e): shrink e2e.sh to orchestrator, move orphan checks into Go`

### Task 6：全量验证 + 真实 e2e full

依赖 Task 1-5。

1. `go build -tags e2e ./test/e2e/` + `go vet -tags e2e ./test/e2e/`
2. `go test -tags e2e -run TestGuest ./test/e2e/`（纯逻辑单测，macOS 本地）
3. `scripts/verify.sh`（确认没碰到产品代码、单元层仍绿）
4. **`scripts/e2e.sh full`**（最终铁证）：
   - 跑通 `TestDistributedSpineClosure`
   - **新断言全部命中**：快照 `afterReady`（`AssertQcowHasSnapshot` 看到 `snap-e2e-001`）+ `afterGone`（`AssertQcowNoSnapshot` 看不到）
   - teardown 后 `assertNoOrphans` 全过（等价替换原 shell verify_no_orphans）
   - **特别核实 spec M3**：`AssertNoQEMUProcess` 的 pgrep 不自匹配（看日志确认无误报）

实现注意：
- e2e full 跑在主仓库（Lima 实例名/LIMA_HOME 由 e2e.sh 计算），需在 worktree 内跑——确认 e2e.sh 的 `main_repo_root` 路径规范化逻辑在 worktree 内正确（line 20-30 已处理 worktree 路径漂移）。
- 若 `AssertNoQEMUProcess` 自匹配误报，按 spec M3 调整 pgrep 策略（`pgrep -a` 按 comm 匹配，不带 `-f`），重跑确认。

**提交**：无（验证任务；若有修正则并入对应 Task 的修复提交）。

## 验证策略总结

- **纯逻辑层**（macOS 本地，秒级）：`go test -tags e2e -run TestGuest ./test/e2e/` —— guestQcowPath 派生、identity 封装。
- **真实门禁层**（Lima，最终铁证）：`scripts/e2e.sh full` —— 快照 live 断言命中 + orphan 检查等价 + pgrep 不自匹配。

## 风险与对策

| 风险 | 对策 |
| --- | --- |
| `qemu-img snapshot -l` 输出列格式解析错 | QcowSnapshots 镜像 local.Driver.snapshotListContains 的列模型（field[1]=tag）；e2e full 实跑验证 |
| pgrep 经 limactl 自匹配误报 | 用 `pgrep -a`（按 comm 匹配，不带 `-f`）结构性排除探针 sh；Task 6 实跑确认（spec M3） |
| 删 orphan_* 变量误删仍用的 | 删前 git grep 确认无其他引用 |
| guest_state_root 跨语言漂移 | 注释 + spec 契约锁死；e2e full 跑通即证明一致 |
| sudo 在 guest 内需密码 | 现有 verify_no_orphans 已用 sudo 无密码（Lima guest 配置），同款环境，无新风险 |

## 交付物

- `test/e2e/guest_paths.go` + `test/e2e/guest_paths_test.go`
- `test/e2e/guest.go`
- `test/e2e/lifecycle.go`
- `test/e2e/closure_test.go`（接入改动）
- `scripts/e2e.sh`（收缩）
