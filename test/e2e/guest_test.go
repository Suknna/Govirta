//go:build e2e

package e2e

import (
	"context"
	"testing"
)

// TestGuestExecCanceledCtxReturnsErr is the local (no-Lima) regression guard for
// I-1: a cancelled context must surface as a connection-layer err from Exec, and
// must never be silently dropped (which would let a killed probe's -1 exit code
// be read downstream as "resource absent" → 假阴性 PASS).
//
// We construct a Guest directly (same-package access to private fields) rather
// than via newGuest, because newGuest requires the Lima env vars; this test does
// not need a reachable guest. The instance/limaHome values are placeholders — the
// ctx is cancelled before Exec runs, so one of two things happens, both of which
// satisfy the assertion (err != nil):
//   - the host has limactl: exec.CommandContext sees the already-cancelled ctx and
//     Start/Run fails, and Exec's `if ctxErr := ctx.Err(); ctxErr != nil` override
//     forces err = ctx.Err() regardless of how c.Run() classified the failure;
//   - the host lacks limactl: c.Run() fails to start the binary → connection-layer
//     err as well.
//
// Either way the contract under test is the same: a probe that could not run
// returns err != nil and the caller (assertGuestAbsent et al.) hard-fails instead
// of reading absence. Asserting only err != nil keeps the test independent of
// whether limactl exists on the CI/dev host.
func TestGuestExecCanceledCtxReturnsErr(t *testing.T) {
	g := &Guest{t: t, instance: "nonexistent-e2e-instance", limaHome: "/nonexistent"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Exec so the ctx is already done when the probe starts

	_, _, _, err := g.Exec(ctx, "echo should-not-matter")
	if err == nil {
		t.Fatal("Exec with a cancelled context returned err == nil; " +
			"a cancelled/unreachable probe must surface as a connection-layer err, " +
			"never be silently swallowed (I-1 regression)")
	}
}
