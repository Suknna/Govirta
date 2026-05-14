package qemu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOSProcessRunnerCapturesOutputToStdoutPath(t *testing.T) {
	runner := NewOSProcessRunner()
	stdoutPath := filepath.Join(t.TempDir(), "serial.log")

	proc, err := runner.Start(context.Background(), helperInvocation(t, "write-output", stdoutPath))
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v, want nil", err)
	}

	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", stdoutPath, err)
	}
	if string(data) != "stdoutstderr" {
		t.Fatalf("captured output = %q, want stdoutstderr", string(data))
	}
}

func TestOSProcessRunnerStopAfterWaitIsNoop(t *testing.T) {
	runner := NewOSProcessRunner()
	proc, err := runner.Start(context.Background(), helperInvocation(t, "write-output", filepath.Join(t.TempDir(), "serial.log")))
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v, want nil", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := proc.Stop(ctx); err != nil {
		t.Fatalf("Stop() after Wait() error = %v, want nil", err)
	}
}

func helperInvocation(t *testing.T, mode string, stdoutPath string) Invocation {
	t.Helper()
	t.Setenv("GOVIRTA_QEMU_RUNNER_HELPER", "1")
	return Invocation{
		Binary:     os.Args[0],
		Args:       []string{"-test.run=TestHelperProcess", "--", mode},
		StdoutPath: stdoutPath,
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GOVIRTA_QEMU_RUNNER_HELPER") != "1" {
		return
	}
	mode := ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
			break
		}
	}
	switch strings.TrimSpace(mode) {
	case "write-output":
		_, _ = os.Stdout.WriteString("stdout")
		_, _ = os.Stderr.WriteString("stderr")
		os.Exit(0)
	default:
		os.Exit(2)
	}
}
