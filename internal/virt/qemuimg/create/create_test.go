package create

import (
	"context"
	"errors"
	"reflect"
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
	err              error
	returnContextErr bool
}

func (r *fakeRunner) Run(ctx context.Context, binary string, args []string) (imgexec.Result, error) {
	r.binary = binary
	r.args = append([]string(nil), args...)
	if r.returnContextErr {
		return imgexec.Result{}, ctx.Err()
	}
	return imgexec.Result{}, r.err
}
