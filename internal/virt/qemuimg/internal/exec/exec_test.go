package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestInvalidRequestWrapsSentinel(t *testing.T) {
	err := InvalidRequest("path is required")

	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("errors.Is(%v, ErrInvalidRequest) = false, want true", err)
	}
}

func TestOSRunnerRunPreservesStdoutAndStderrOnSuccess(t *testing.T) {
	result, err := runHelper(t, context.Background(), "success")
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if result.Stdout != "stdout-success" || result.Stderr != "stderr-success" {
		t.Fatalf("Run() = %#v, want stdout and stderr", result)
	}
}

func TestOSRunnerRunPreservesStdoutAndStderrOnFailure(t *testing.T) {
	result, err := runHelper(t, context.Background(), "failure")
	if err == nil {
		t.Fatalf("Run() error = nil, want error")
	}
	if result.Stdout != "stdout-failure" || result.Stderr != "stderr-failure" {
		t.Fatalf("Run() = %#v, want stdout and stderr", result)
	}
}

func TestOSRunnerRunReturnsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runHelper(t, ctx, "success")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestCommandErrorIncludesStderrAndUnwrapsErr(t *testing.T) {
	runnerErr := errors.New("runner failed")
	result := Result{Stdout: "stdout details", Stderr: "stderr details"}

	err := WrapError(result, runnerErr)

	if !errors.Is(err, runnerErr) {
		t.Fatalf("errors.Is(%v, runnerErr) = false, want true", err)
	}
	if !strings.Contains(err.Error(), "runner failed") {
		t.Fatalf("Error() = %q, want underlying error", err.Error())
	}
	if !strings.Contains(err.Error(), "stderr details") {
		t.Fatalf("Error() = %q, want stderr", err.Error())
	}
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("errors.As(%T) = false, want true", commandErr)
	}
	if commandErr.Result != result {
		t.Fatalf("CommandError.Result = %#v, want %#v", commandErr.Result, result)
	}
}

func runHelper(t *testing.T, ctx context.Context, mode string) (Result, error) {
	t.Helper()
	return OSRunner{}.Run(ctx, os.Args[0], []string{"-test.run=TestOSRunnerHelperProcess", "--", mode})
}

func TestOSRunnerHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}

	switch mode := os.Args[len(os.Args)-1]; mode {
	case "success":
		fmt.Fprint(os.Stdout, "stdout-success")
		fmt.Fprint(os.Stderr, "stderr-success")
	case "failure":
		fmt.Fprint(os.Stdout, "stdout-failure")
		fmt.Fprint(os.Stderr, "stderr-failure")
		os.Exit(17)
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q", mode)
		os.Exit(2)
	}

	os.Exit(0)
}
