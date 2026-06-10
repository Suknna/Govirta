//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
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
	exitCode, err = classifyExecResult(ctx.Err(), runErr)
	return outBuf.String(), errBuf.String(), exitCode, err
}

// classifyExecResult maps the raw (ctxErr, runErr) of a guest exec into the
// (exitCode, err) contract: connection-layer failures (limactl itself failed,
// or ctx cancelled mid-flight) surface as err; a guest command's own non-zero
// exit is exitCode with err==nil, so the caller can distinguish "could not reach
// the guest" from "reached it, command reported absent/present".
//
// ctx cancellation takes priority because a mid-flight kill returns
// *exec.ExitError{ExitCode()==-1}: when ctx 到期/取消，limactl 被信号杀死，
// c.Run() 返回的 *exec.ExitError 会被 errors.As 命中、当成 guest 命令退出码
// (err 本会保持 nil)，从而**丢弃 ctx.Err()**——下游布尔探针就会把一个被杀死的
// 探针的 -1 读成 "资源不存在" 而静默 PASS (I-1: 见 commit 7050d25 / fdaeaa0)。
// 因此只要 ctxErr!=nil 就无条件覆盖为连接层 err。这不会误伤 guest 命令的正常
// 非零退出：那种情况 ctxErr==nil，仍走 exitCode 路径 (exitCode!=0, err==nil)。
func classifyExecResult(ctxErr, runErr error) (exitCode int, err error) {
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		exitCode = 0
	case errors.As(runErr, &exitErr):
		exitCode = exitErr.ExitCode() // guest command exit code; err stays nil
	default:
		err = runErr // limactl failure (connection layer)
	}
	if ctxErr != nil {
		err = ctxErr
	}
	return exitCode, err
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
// exactly one of three markers on stdout — PRESENT / ABSENT / PROBEERR — produced
// by mapping the underlying check's exit code through an explicit `case $?` (see
// the callers). That explicit stdout marker, rather than a bare exit code or the
// fragile `<check> && echo PRESENT || echo ABSENT` shape, is what separates
// "probe ran, resource is gone" from "probe never ran". The old `&&/||` shape
// folded *any* failure of the check command (sudo auth failure with no tty, a
// missing test/ip binary → exit 126/127) into the `|| echo ABSENT` branch, i.e.
// silently PASSed a probe that never executed (假阴性). The三态 markers close that:
//   - connection-layer err (limactl failed / ctx cancelled) → Fatalf
//   - non-zero exit (the guest command always exits 0, so non-zero ⇒ limactl
//     itself failed) → Fatalf
//   - PRESENT → Fatalf (resource still there)
//   - PROBEERR → Fatalf (the check command itself failed inside the guest:
//     missing binary, sudo auth failure, pipe error — NOT a "resource absent"
//     signal, must never be read as ABSENT — 上下一致铁律)
//   - ABSENT → pass
//   - any other stdout (probe did not run as written) → Fatalf
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
	case "PROBEERR":
		g.t.Fatalf("probe %s could not run: the probe command itself failed inside the guest "+
			"(missing binary, sudo auth failure, or pipe error); refusing to read this as ABSENT: %s", label, stderr)
	default:
		g.t.Fatalf("probe %s returned unexpected output %q (probe did not run as written)", label, stdout)
	}
}

// assertGuestPathAbsent fails the test unless path is gone from the guest
// filesystem, probed with `sudo test -e`. Shared by the qcow2 and runtime-dir
// checks (M2): both are pure existence probes, so the only difference is the
// path and the failure wording. The probe always exits 0 and maps `test -e`'s
// exit code to a三态 marker via `case $?`: 0 ⇒ exists (PRESENT), 1 ⇒ does not
// exist (ABSENT), anything else ⇒ the probe could not run (PROBEERR) — sudo auth
// failure with no tty, or test/sudo missing (exit 126/127). Mapping the "other"
// bucket to PROBEERR (rather than the old `||`-folded ABSENT) lets
// assertGuestAbsent hard-fail a probe that never ran instead of silently PASSing.
func (g *Guest) assertGuestPathAbsent(ctx context.Context, path, label, presentMsg string) {
	g.t.Helper()
	// `sudo test -e <path>; case $? in ...`: test -e exit semantics — 0=present,
	// 1=absent, 126/127/其它=探针本身没跑成（缺二进制 / sudo 鉴权失败）。
	probe := "sudo test -e " + shellQuote(path) +
		"; case $? in 0) echo PRESENT;; 1) echo ABSENT;; *) echo PROBEERR;; esac"
	g.assertGuestAbsent(ctx, probe, label, presentMsg)
}

// AssertNoQEMUProcess fails if a QEMU process keyed by the VM uid's runtime path
// is still running. 早先版本用「空 stdout = absent」判定，但 pgrep/grep 缺失（127）
// 或管道左侧失败经 `|| true` 归 0 后 stdout 同样为空 → 把「探针没跑成」误判为「无残留」
// 静默 PASS。改成显式三态，把存在性结论写进 stdout：
//
// pgrep self-match 规避：探针经 `limactl shell -- sh -c` 执行，cmd 字符串里已展开
// runtimeDir，若用 `pgrep -af`（-f 匹配全 argv）会匹配探针 sh 自身、永远 fail。改用
// `pgrep -a`（不带 -f，只按进程名 comm 匹配）——探针 comm 是 sh、不匹配 qemu-system，
// 结构性排除自匹配；QEMU argv 仍嵌 runtimeDir 供 grep -F 过滤（spec M3）。
//
// 退出码依据（POSIX sh / dash，无 pipefail）：管道整体退出码取最右侧 grep 的：
//   - grep 0 ⇒ 匹配到 ⇒ PRESENT；
//   - grep 1 ⇒ 无匹配（含 pgrep 无 qemu 进程时输出空、grep 读空 stdin 退 1）⇒ ABSENT；
//   - grep ≥2 ⇒ grep 自身出错 ⇒ PROBEERR。
//
// 二进制缺失（127）会被「grep 读空 stdin 退 1」吞成 ABSENT，故先用 `command -v` 显式
// 守卫 pgrep/grep 存在，缺失即 echo PROBEERR；这样「探针没跑成」结构性归 PROBEERR。
// 整条命令恒 exit 0，经 assertGuestAbsent 裁决：PRESENT/PROBEERR/未知→Fatalf、
// ABSENT→pass，绝不静默 absent。
func (g *Guest) AssertNoQEMUProcess(ctx context.Context, vmUID string) {
	g.t.Helper()
	runtimeDir := guestRuntimeDir(vmUID)
	probe := "command -v pgrep >/dev/null 2>&1 || { echo PROBEERR; exit 0; }; " +
		"command -v grep >/dev/null 2>&1 || { echo PROBEERR; exit 0; }; " +
		"pgrep -a qemu-system | grep -F " + shellQuote(runtimeDir) + " >/dev/null 2>&1; " +
		"case $? in 0) echo PRESENT;; 1) echo ABSENT;; *) echo PROBEERR;; esac"
	g.assertGuestAbsent(ctx, probe,
		fmt.Sprintf("QEMU process for VM uid %q", vmUID),
		fmt.Sprintf("QEMU process still running for VM uid %q (runtime %q)", vmUID, runtimeDir))
}

// AssertNoLink fails if a network link (TAP or bridge) named linkName still
// exists. Rather than relying on `ip link show` 的退出码直接当布尔，用整体 exit 0
// 的三态探针把结论写进 stdout（PRESENT/ABSENT/PROBEERR），这样"链确实不在"的正常
// 退出与"探针根本没跑成"被彻底分开：
//   - ip link show <name>：0=链存在，1=链不存在（iproute2 在接口不存在时
//     "Device does not exist" 退出 1），其它退出码（127=ip 缺失等）=探针错误。
//   - 0 ⇒ PRESENT，1 ⇒ ABSENT，* ⇒ PROBEERR。
//
// PRESENT→Fatalf、ABSENT→pass、PROBEERR/未知→Fatalf，消除假阴性静默 PASS。
// 注：exit 码语义依据 Linux iproute2 行为；macOS 无 ip，真实验证留 Task 6 e2e。
func (g *Guest) AssertNoLink(ctx context.Context, linkName string) {
	g.t.Helper()
	probe := "ip link show " + shellQuote(linkName) +
		" >/dev/null 2>&1; case $? in 0) echo PRESENT;; 1) echo ABSENT;; *) echo PROBEERR;; esac"
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

// --- 卷容量 live 实况（刀 5 冷扩容落地）---

// QcowVirtualSize 在 guest 内跑 `sudo qemu-img info --output=json <path>`，解析
// JSON 里的 `virtual-size` 返回 int64。之所以读 qcow2 的 virtual-size 而非信
// master 的 status 投影：冷扩容真正落地与否的唯一权威是 qcow2 自身（决策 3 不加
// 容量 status 字段），这正是「上下一致铁律」要求的下层实况验证。
// 用 --output=json 而非解析人类可读文本，是因为 qemu-img info 的文本格式跨版本
// 不稳定（带单位、本地化），JSON 的 virtual-size 字段是稳定的字节数契约。
func (g *Guest) QcowVirtualSize(ctx context.Context, qcowPath string) (int64, error) {
	stdout, stderr, code, err := g.Exec(ctx, "sudo qemu-img info --output=json "+shellQuote(qcowPath))
	if err != nil {
		return 0, err
	}
	if code != 0 {
		return 0, fmt.Errorf("qemu-img info --output=json %q exit %d: %s", qcowPath, code, stderr)
	}
	// 只取 virtual-size：guest 的 qemu-img 版本可能带其它字段，匿名结构体按需解码。
	var info struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		return 0, fmt.Errorf("decode qemu-img info JSON for %q: %w (raw: %s)", qcowPath, err, stdout)
	}
	return info.VirtualSize, nil
}

// AssertQcowVirtualSize 断言 guest qcow2 的 virtual-size 恰等于 want，否则 t.Fatalf。
// 这是冷扩容端到端的 live 铁证：master 报 Volume 仍 Ready 之外，下层 qcow2 的真实
// 虚拟容量必须已等于新目标值，才证明 resize 真落到磁盘（不只信 status 投影）。
func (g *Guest) AssertQcowVirtualSize(ctx context.Context, qcowPath string, want int64) {
	g.t.Helper()
	got, err := g.QcowVirtualSize(ctx, qcowPath)
	if err != nil {
		g.t.Fatalf("read qcow virtual size %q: %v", qcowPath, err)
	}
	if got != want {
		g.t.Fatalf("qcow %q virtual size = %d, want %d (cold resize not reflected in live qcow2)", qcowPath, got, want)
	}
}

// shellQuote single-quotes a path for safe `sh -c` interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
