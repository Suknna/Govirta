package create

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

func TestDoBuildsQCOW2CreateArgsWithBackingBase(t *testing.T) {
	runner := &fakeRunner{}

	err := New("/usr/bin/qemu-img", runner).
		Target("/var/lib/govirta/vms/vm-1.qcow2").
		FromBase("/var/lib/govirta/images/base.qcow2").
		SizeBytes(1073741824).
		Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if runner.binary != "/usr/bin/qemu-img" {
		t.Fatalf("Run() binary = %q, want %q", runner.binary, "/usr/bin/qemu-img")
	}
	wantArgs := []string{
		"create",
		"-f", "qcow2",
		"-F", "qcow2",
		"-b", "/var/lib/govirta/images/base.qcow2",
		"/var/lib/govirta/vms/vm-1.qcow2",
		"1073741824",
	}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("Run() args = %#v, want %#v", runner.args, wantArgs)
	}
}

func TestDoRejectsMissingTarget(t *testing.T) {
	err := New("qemu-img", &fakeRunner{}).
		Target(" \t\n").
		FromBase("/var/lib/govirta/images/base.qcow2").
		SizeBytes(1).
		Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want ErrInvalidRequest", err)
	}
}

func TestDoRejectsMissingBase(t *testing.T) {
	err := New("qemu-img", &fakeRunner{}).
		Target("/var/lib/govirta/vms/vm-1.qcow2").
		FromBase(" \t\n").
		SizeBytes(1).
		Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want ErrInvalidRequest", err)
	}
}

func TestDoRejectsNonPositiveSize(t *testing.T) {
	tests := []struct {
		name string
		size int64
	}{
		{name: "zero", size: 0},
		{name: "negative", size: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New("qemu-img", &fakeRunner{}).
				Target("/var/lib/govirta/vms/vm-1.qcow2").
				FromBase("/var/lib/govirta/images/base.qcow2").
				SizeBytes(tt.size).
				Do(context.Background())

			if !errors.Is(err, imgexec.ErrInvalidRequest) {
				t.Fatalf("Do() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestDoReturnsRunnerError(t *testing.T) {
	runnerErr := errors.New("runner failed")

	err := New("qemu-img", &fakeRunner{err: runnerErr}).
		Target("/var/lib/govirta/vms/vm-1.qcow2").
		FromBase("/var/lib/govirta/images/base.qcow2").
		SizeBytes(1).
		Do(context.Background())

	if !errors.Is(err, runnerErr) {
		t.Fatalf("Do() error = %v, want runner error", err)
	}
}

func TestDoWrapsRunnerErrorWithCapturedResult(t *testing.T) {
	runnerErr := errors.New("runner failed")
	runnerResult := imgexec.Result{Stdout: "stdout details", Stderr: "stderr details"}

	err := New("qemu-img", &fakeRunner{result: runnerResult, err: runnerErr}).
		Target("/var/lib/govirta/vms/vm-1.qcow2").
		FromBase("/var/lib/govirta/images/base.qcow2").
		SizeBytes(1).
		Do(context.Background())

	if !errors.Is(err, runnerErr) {
		t.Fatalf("Do() error = %v, want runner error", err)
	}
	if !strings.Contains(err.Error(), "stderr details") {
		t.Fatalf("Do() error = %q, want stderr", err.Error())
	}
	var commandErr *imgexec.CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("Do() error type = %T, want CommandError", err)
	}
	if commandErr.Result != runnerResult {
		t.Fatalf("CommandError.Result = %#v, want %#v", commandErr.Result, runnerResult)
	}
}

func TestDoReturnsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := New("qemu-img", &fakeRunner{returnContextErr: true}).
		Target("/var/lib/govirta/vms/vm-1.qcow2").
		FromBase("/var/lib/govirta/images/base.qcow2").
		SizeBytes(1).
		Do(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context.Canceled", err)
	}
}

type fakeRunner struct {
	binary           string
	args             []string
	result           imgexec.Result
	err              error
	returnContextErr bool
}

func (r *fakeRunner) Run(ctx context.Context, binary string, args []string) (imgexec.Result, error) {
	r.binary = binary
	r.args = append([]string(nil), args...)
	if r.returnContextErr {
		return imgexec.Result{}, ctx.Err()
	}
	return r.result, r.err
}
