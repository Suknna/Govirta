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

// AssertNoQEMUProcess fails if a QEMU process keyed by the VM uid's runtime path
// is still running. pgrep self-match 规避：探针经 `limactl shell -- sh -c` 执行，
// cmd 字符串里已展开 runtimeDir，若用 `pgrep -af`（-f 匹配全 argv）会匹配探针 sh 自身、
// 永远 fail。改用 `pgrep -a`（不带 -f，只按进程名 comm 匹配）——探针 comm 是 sh、不匹配
// qemu-system，结构性排除自匹配；QEMU argv 仍嵌 runtimeDir 供 grep -F 过滤（spec M3）。
func (g *Guest) AssertNoQEMUProcess(ctx context.Context, vmUID string) {
	g.t.Helper()
	runtimeDir := guestRuntimeDir(vmUID)
	cmd := "pgrep -a qemu-system | grep -F " + shellQuote(runtimeDir) + " || true"
	stdout, _, _, err := g.Exec(ctx, cmd)
	if err != nil {
		g.t.Fatalf("probe QEMU process for %q: %v", vmUID, err)
	}
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
