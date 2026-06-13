//go:build e2e

package e2e

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// killedExitError synthesizes the exact error c.Run() returns when a child is
// killed by a signal: an *exec.ExitError whose ExitCode() is -1 (the process did
// not exit normally, so there is no real exit status). This is the runtime shape
// limactl produces when ctx 到期/取消、它被 CommandContext 的 watcher 信号杀死。
// Building it locally (start a sleeper, kill it, Wait) lets the regression test
// hit Exec's I-1 override branch without any Lima dependency.
func killedExitError(t *testing.T) error {
	t.Helper()
	c := exec.Command("sh", "-c", "sleep 30")
	if err := c.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	_ = c.Process.Kill()
	runErr := c.Wait() // killed by signal → *exec.ExitError, ExitCode() == -1
	var ee *exec.ExitError
	if !errors.As(runErr, &ee) {
		t.Fatalf("want *exec.ExitError from killed process, got %T (%v)", runErr, runErr)
	}
	if ee.ExitCode() != -1 {
		t.Fatalf("want ExitCode()==-1 from killed process, got %d", ee.ExitCode())
	}
	return runErr
}

// exitNExitError returns the *exec.ExitError a guest command produces when it
// exits non-zero of its own accord (here exit 3). This is the "reached the guest,
// command reported a real status" shape — distinct from killedExitError's -1.
func exitNExitError(t *testing.T, code int) error {
	t.Helper()
	runErr := exec.Command("sh", "-c", "exit 3").Run()
	var ee *exec.ExitError
	if !errors.As(runErr, &ee) {
		t.Fatalf("want *exec.ExitError from `exit 3`, got %T (%v)", runErr, runErr)
	}
	if ee.ExitCode() != code {
		t.Fatalf("want ExitCode()==%d, got %d", code, ee.ExitCode())
	}
	return runErr
}

// TestClassifyExecResult is the local (no-Lima) regression guard for I-1. It
// drives classifyExecResult — the pure function Exec delegates result
// classification to — directly across every branch, so the override (ctx error
// rescue) can be proven without standing up a Lima guest.
//
// The critical case is "override": a mid-flight kill returns *exec.ExitError{-1},
// which errors.As classifies as a guest exit code (err would stay nil), so the
// ctxErr override is the ONLY thing that surfaces the failure as err. Deleting
// the `if ctxErr != nil { err = ctxErr }` line makes that case (and only that
// case) FAIL — that is exactly the regression this pins.
func TestClassifyExecResult(t *testing.T) {
	killed := killedExitError(t)          // *exec.ExitError, ExitCode() == -1
	exit3 := exitNExitError(t, 3)         // *exec.ExitError, ExitCode() == 3
	connErr := errors.New("limactl boom") // non-ExitError connection-layer failure

	tests := []struct {
		name         string
		ctxErr       error
		runErr       error
		wantExitCode int
		wantErr      error // nil ⇒ expect err==nil; non-nil ⇒ expect errors.Is(err, wantErr)
	}{
		{
			// I-1 core: limactl killed mid-flight (runErr is *exec.ExitError{-1},
			// which the switch would classify as exitCode=-1, err=nil) but ctx
			// expired. Only the override rescues ctx.Err() into err. Delete the
			// override → wantErr is not met → this case FAILs.
			name:         "override: mid-flight kill rescues ctx error",
			ctxErr:       context.DeadlineExceeded,
			runErr:       killed,
			wantExitCode: -1,
			wantErr:      context.DeadlineExceeded,
		},
		{
			name:         "success: no error",
			ctxErr:       nil,
			runErr:       nil,
			wantExitCode: 0,
			wantErr:      nil,
		},
		{
			// Guest command's own non-zero exit must NOT be mistaken for a
			// connection-layer failure: exitCode carries it, err stays nil.
			name:         "guest non-zero exit is exitCode not err",
			ctxErr:       nil,
			runErr:       exit3,
			wantExitCode: 3,
			wantErr:      nil,
		},
		{
			// Connection layer failed (limactl itself, non-ExitError) with no ctx
			// cancellation: surfaces as err, exitCode stays 0.
			name:         "connection-layer failure surfaces as err",
			ctxErr:       nil,
			runErr:       connErr,
			wantExitCode: 0,
			wantErr:      connErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotExitCode, gotErr := classifyExecResult(tt.ctxErr, tt.runErr)
			if gotExitCode != tt.wantExitCode {
				t.Errorf("exitCode = %d, want %d", gotExitCode, tt.wantExitCode)
			}
			switch {
			case tt.wantErr == nil && gotErr != nil:
				t.Errorf("err = %v, want nil", gotErr)
			case tt.wantErr != nil && gotErr == nil:
				t.Errorf("err = nil, want %v", tt.wantErr)
			case tt.wantErr != nil && !errors.Is(gotErr, tt.wantErr):
				t.Errorf("err = %v, want errors.Is(_, %v)", gotErr, tt.wantErr)
			}
		})
	}
}

// TestGuestExecCanceledCtxReturnsErr is an end-to-end-style complement to the
// classifyExecResult table test: it exercises the full Exec path with an
// already-cancelled context and asserts the contract holds (err != nil). Unlike
// the table test it does NOT isolate the override branch — a pre-cancelled ctx
// makes c.Run() fail fast (either context.Canceled, or "limactl not found" on a
// host without Lima), so this case can pass even with the override deleted. It is
// kept only as a coarse smoke check that Exec wires ctx through to err; the real
// regression guard for the I-1 override branch is TestClassifyExecResult.
//
// We construct a Guest directly (same-package access to private fields) rather
// than via newGuest, because newGuest requires the Lima env vars and this test
// does not need a reachable guest. Asserting only err != nil keeps it independent
// of whether limactl exists on the CI/dev host.
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

func TestSudoReadStateFileOneLineOpensStateInsideSudoShell(t *testing.T) {
	cmd := sudoReadStateFileOneLine("/var/lib/govirta/runtime/vm-e2e-001/vm.json")
	if !strings.HasPrefix(cmd, "sudo sh -c ") {
		t.Fatalf("sudo state reader = %q, want sudo sh -c wrapper", cmd)
	}
	if strings.Contains(cmd, "sudo tr") {
		t.Fatalf("sudo state reader = %q, must not use outer-shell redirection into sudo tr", cmd)
	}
	if !strings.Contains(cmd, "< '") {
		t.Fatalf("sudo state reader = %q, want redirection inside the sudo shell command", cmd)
	}
}

func TestCDROMArgvProbeDoesNotRejectImageNamesContainingCDROM(t *testing.T) {
	line := "driver=raw,node-name=cdrom0-abc,read-only=on,file.driver=file,file.filename=/var/lib/govirta/image-cache/image-cdrom/v1/image " +
		"virtio-scsi-pci scsi-cd drive=cdrom0-abc"
	stdout := runLocalCDROMProbe(t, line)
	if strings.TrimSpace(stdout) != "PRESENT" {
		t.Fatalf("cdromArgvProbe with image-cdrom path = %q, want PRESENT", stdout)
	}
}

func TestCDROMArgvProbeRejectsRawCDROMShortcutArgument(t *testing.T) {
	line := "driver=raw,node-name=cdrom0-abc,read-only=on,file.driver=file,file.filename=/var/lib/govirta/image-cache/image-cdrom/v1/image " +
		"virtio-scsi-pci scsi-cd drive=cdrom0-abc -cdrom /var/lib/govirta/image-cache/image-cdrom/v1/image"
	stdout := runLocalCDROMProbe(t, line)
	if !strings.Contains(stdout, "forbidden:-cdrom") {
		t.Fatalf("cdromArgvProbe with raw -cdrom arg = %q, want forbidden:-cdrom", stdout)
	}
}

func runLocalCDROMProbe(t *testing.T, line string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", "line="+shellQuote(line)+"; "+cdromArgvProbe("$line")).Output()
	if err != nil {
		t.Fatalf("run local cdrom probe: %v", err)
	}
	return string(out)
}
