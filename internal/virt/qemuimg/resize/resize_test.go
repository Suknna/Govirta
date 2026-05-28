package resize

import (
	"context"
	"errors"
	"reflect"
	"testing"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

type recordingRunner struct {
	binary string
	args   []string
	err    error
}

func (r *recordingRunner) Run(ctx context.Context, binary string, args []string) (imgexec.Result, error) {
	r.binary = binary
	r.args = append([]string(nil), args...)
	if err := ctx.Err(); err != nil {
		return imgexec.Result{}, err
	}
	return imgexec.Result{}, r.err
}

func TestDoBuildsResizeArgv(t *testing.T) {
	runner := &recordingRunner{}

	err := New("qemu-img", runner).
		Path("disk.qcow2").
		SizeBytes(1073741824).
		Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if runner.binary != "qemu-img" {
		t.Fatalf("binary = %q, want qemu-img", runner.binary)
	}
	wantArgs := []string{"resize", "-f", "qcow2", "disk.qcow2", "1073741824"}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", runner.args, wantArgs)
	}
}

func TestDoRequiresPath(t *testing.T) {
	err := New("qemu-img", &recordingRunner{}).
		SizeBytes(1073741824).
		Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
}

func TestDoRejectsLeadingDashPath(t *testing.T) {
	runner := &recordingRunner{}

	err := New("qemu-img", runner).
		Path("--help").
		SizeBytes(1073741824).
		Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
	if runner.binary != "" || runner.args != nil {
		t.Fatalf("Run() was called with binary %q args %#v, want no call", runner.binary, runner.args)
	}
}

func TestDoRequiresPositiveSize(t *testing.T) {
	runner := &recordingRunner{}

	err := New("qemu-img", runner).
		Path("disk.qcow2").
		SizeBytes(0).
		Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
	if runner.binary != "" || runner.args != nil {
		t.Fatalf("Run() was called with binary %q args %#v, want no call", runner.binary, runner.args)
	}
}

func TestDoReturnsRunnerError(t *testing.T) {
	wantErr := errors.New("runner failed")
	err := New("qemu-img", &recordingRunner{err: wantErr}).
		Path("disk.qcow2").
		SizeBytes(1073741824).
		Do(context.Background())

	if !errors.Is(err, wantErr) {
		t.Fatalf("Do() error = %v, want %v", err, wantErr)
	}
}

func TestDoReturnsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := New("qemu-img", &recordingRunner{}).
		Path("disk.qcow2").
		SizeBytes(1073741824).
		Do(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context canceled", err)
	}
}
