package snapshot

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

func TestDoBuildsSnapshotArgv(t *testing.T) {
	runner := &recordingRunner{}

	err := New("qemu-img", runner).
		Name("before-upgrade").
		Path("disk.qcow2").
		Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if runner.binary != "qemu-img" {
		t.Fatalf("binary = %q, want qemu-img", runner.binary)
	}
	wantArgs := []string{"snapshot", "-c", "before-upgrade", "disk.qcow2"}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", runner.args, wantArgs)
	}
}

func TestDoRequiresPath(t *testing.T) {
	err := New("qemu-img", &recordingRunner{}).
		Name("before-upgrade").
		Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
}

func TestDoRequiresName(t *testing.T) {
	err := New("qemu-img", &recordingRunner{}).
		Path("disk.qcow2").
		Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
}

func TestDoReturnsRunnerError(t *testing.T) {
	wantErr := errors.New("runner failed")
	err := New("qemu-img", &recordingRunner{err: wantErr}).
		Name("before-upgrade").
		Path("disk.qcow2").
		Do(context.Background())

	if !errors.Is(err, wantErr) {
		t.Fatalf("Do() error = %v, want %v", err, wantErr)
	}
}

func TestDoReturnsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := New("qemu-img", &recordingRunner{}).
		Name("before-upgrade").
		Path("disk.qcow2").
		Do(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context canceled", err)
	}
}
