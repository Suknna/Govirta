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
// to reach is a test bug, not a skip. The check is inlined rather than calling
// requireEnv because requireEnv lives in closure_test.go (a _test.go file) and
// is therefore invisible to `go build`; guest.go is a regular build-tagged file.
func newGuest(t *testing.T) *Guest {
	t.Helper()
	require := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			t.Fatalf("%s is required for guest-side e2e assertions", name)
		}
		return v
	}
	return &Guest{
		t:        t,
		instance: require(e2eLimaInstanceEnv),
		limaHome: require(e2eLimaHomeEnv),
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
		err = runErr // limactl failure (connection layer)
	}
	// ctx 取消/超时优先判为连接层失败。当 ctx 到期时 limactl 被信号杀死，c.Run()
	// 返回的是 *exec.ExitError（ExitCode()==-1），会被上面的 switch 误当成 guest
	// 命令退出码（err 保持 nil），从而**丢弃 ctx.Err()**——下游布尔探针就会把一个
	// 被杀死的探针的 -1 读成 "资源不存在" 而静默 PASS。这里在 return 前覆盖：只要
	// ctx.Err()!=nil 就归为连接层 err，无条件优先。注意这不会误伤 guest 命令的正常
	// 非零退出：那种情况 ctx.Err()==nil，仍走 exitCode 路径（exitCode!=0, err==nil）。
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	return outBuf.String(), errBuf.String(), exitCode, err
}

// --- 快照 live 实况（items 2/5 落地）---

// QcowSnapshots returns the internal snapshot tags in a guest qcow2 file by
// parsing `sudo qemu-img snapshot -l <path>`. The tag is the second
// whitespace-delimited field of each data row (ID is first); header/empty rows
// yield nothing. Mirrors local.Driver.snapshotListContains' column model.
func (g *Guest) QcowSnapshots(ctx context.Context, qcowPath string) ([]string, error) {
	stdout, stderr, code, err := g.Exec(ctx, "sudo qemu-img snapshot -l "+shellQuote(qcowPath))
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

// assertGuestAbsent runs an existence probe and fails the test unless it reports
// the resource is ABSENT. probe MUST be a command that always exits 0 and prints
// exactly PRESENT or ABSENT on stdout (the `<check> && echo PRESENT || echo
// ABSENT` shape). That explicit stdout marker — rather than a bare exit code —
// is what separates "probe ran, resource is gone" from "probe never ran":
//   - connection-layer err (limactl failed / ctx cancelled) → Fatalf
//   - non-zero exit (the guest command always exits 0, so non-zero ⇒ limactl
//     itself failed) → Fatalf
//   - unrecognised stdout (probe did not run as written) → Fatalf
//
// so a dead probe can never be silently read as "absent" (the假阴性 this fixes).
// label identifies the probe in failure messages; presentMsg is the Fatalf
// message used when the resource is reported PRESENT.
func (g *Guest) assertGuestAbsent(ctx context.Context, probe, label, presentMsg string) {
	g.t.Helper()
	stdout, stderr, code, err := g.Exec(ctx, probe)
	if err != nil {
		g.t.Fatalf("probe %s: %v", label, err)
	}
	if code != 0 {
		g.t.Fatalf("probe %s failed (exit %d): %s", label, code, stderr)
	}
	switch strings.TrimSpace(stdout) {
	case "ABSENT":
		return
	case "PRESENT":
		g.t.Fatalf("%s", presentMsg)
	default:
		g.t.Fatalf("probe %s returned unexpected output %q (probe did not run as written)", label, stdout)
	}
}

// assertGuestPathAbsent fails the test unless path is gone from the guest
// filesystem, probed with `sudo test -e`. Shared by the qcow2 and runtime-dir
// checks (M2): both are pure existence probes, so the only difference is the
// path and the failure wording. The probe always exits 0 and emits PRESENT or
// ABSENT, so assertGuestAbsent can hard-fail a probe that never ran.
func (g *Guest) assertGuestPathAbsent(ctx context.Context, path, label, presentMsg string) {
	g.t.Helper()
	probe := "sudo test -e " + shellQuote(path) + " && echo PRESENT || echo ABSENT"
	g.assertGuestAbsent(ctx, probe, label, presentMsg)
}

// AssertNoQEMUProcess fails if a QEMU process keyed by the VM uid's runtime path
// is still running. pgrep self-match 规避：探针经 `limactl shell -- sh -c` 执行，
// cmd 字符串里已展开 runtimeDir，若用 `pgrep -af`（-f 匹配全 argv）会匹配探针 sh 自身、
// 永远 fail。改用 `pgrep -a`（不带 -f，只按进程名 comm 匹配）——探针 comm 是 sh、不匹配
// qemu-system，结构性排除自匹配；QEMU argv 仍嵌 runtimeDir 供 grep -F 过滤（spec M3）。
// `|| true` 让 guest 命令整体 exit 0，于是 absent=stdout 空；任何连接层 err 或非零
// exit（guest 命令恒 0，非零必是 limactl 失败）都判探针失败 → Fatalf，绝不静默 absent。
func (g *Guest) AssertNoQEMUProcess(ctx context.Context, vmUID string) {
	g.t.Helper()
	runtimeDir := guestRuntimeDir(vmUID)
	cmd := "pgrep -a qemu-system | grep -F " + shellQuote(runtimeDir) + " || true"
	stdout, stderr, code, err := g.Exec(ctx, cmd)
	if err != nil {
		g.t.Fatalf("probe QEMU process for %q: %v", vmUID, err)
	}
	if code != 0 {
		g.t.Fatalf("probe QEMU process for %q failed (exit %d): %s", vmUID, code, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		g.t.Fatalf("QEMU process still running for VM uid %q:\n%s", vmUID, stdout)
	}
}

// AssertNoLink fails if a network link (TAP or bridge) named linkName still
// exists. Rather than relying on `ip link show` 的退出码（present=0 / absent≠0），
// 用整体 exit 0 的存在性探针把结论写进 stdout（PRESENT/ABSENT），这样"链确实不在"
// 的正常非零退出与"探针根本没跑成"（连接层失败、limactl 非零退出）被彻底分开：前者
// stdout=ABSENT 通过，后者落 err/非零 exit → Fatalf，消除假阴性静默 PASS。
func (g *Guest) AssertNoLink(ctx context.Context, linkName string) {
	g.t.Helper()
	probe := "ip link show " + shellQuote(linkName) + " >/dev/null 2>&1 && echo PRESENT || echo ABSENT"
	g.assertGuestAbsent(ctx, probe,
		fmt.Sprintf("link %q", linkName),
		fmt.Sprintf("network link still present: %q", linkName))
}

// AssertNoNftablesChain fails if chain still appears in the guest nftables
// ruleset. Reading the ruleset must succeed: a probe that cannot read the
// ruleset is a hard failure, never silently "absent" (spec M1, e2e.sh L385).
//
// M1: 此处用 strings.Contains（子串匹配）而非精确 token 拆分是有意为之，且方向偏安全。
// 链名是由 VM identity 派生的（见 guest_paths.go 的 *Chain helpers），命名空间稀疏、
// 碰撞概率极低；子串匹配的唯一失真方向是"把某条恰好把该名作为子串的无关行也算命中"，
// 即偏向**误报 FAIL**（门禁更严）而非**漏报 PASS**（放过残留链）。对一个 orphan 门禁
// 探针，宁可误报也绝不漏报，所以这里容忍子串匹配，不引入 nft -j json 解析的复杂度。
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

// AssertNoQcow2 fails if the guest qcow2 file still exists, probed with
// `sudo test -e` via the shared existence probe (M2).
func (g *Guest) AssertNoQcow2(ctx context.Context, qcowPath string) {
	g.t.Helper()
	g.assertGuestPathAbsent(ctx, qcowPath,
		fmt.Sprintf("qcow2 %q", qcowPath),
		fmt.Sprintf("block volume qcow2 still present: %q", qcowPath))
}

// AssertNoRuntimeDir fails if the VM's runtime dir still exists, probed with
// `sudo test -e` via the shared existence probe (M2).
func (g *Guest) AssertNoRuntimeDir(ctx context.Context, vmUID string) {
	g.t.Helper()
	dir := guestRuntimeDir(vmUID)
	g.assertGuestPathAbsent(ctx, dir,
		fmt.Sprintf("runtime dir %q", dir),
		fmt.Sprintf("VM runtime dir still present: %q", dir))
}

// shellQuote single-quotes a path for safe `sh -c` interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
