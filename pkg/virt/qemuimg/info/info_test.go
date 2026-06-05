package info

import (
	"context"
	"errors"
	"reflect"
	"testing"

	imgexec "github.com/suknna/govirta/pkg/virt/qemuimg/internal/exec"
)

func TestDoBuildsInfoArgsAndParsesResult(t *testing.T) {
	runner := &fakeRunner{result: imgexec.Result{Stdout: `{
		"filename": "/var/lib/govirta/vms/vm-1.qcow2",
		"format": "qcow2",
		"virtual-size": 1073741824,
		"actual-size": 65536,
		"backing-filename": "/var/lib/govirta/images/base.qcow2",
		"backing-filename-format": "qcow2"
	}`}}

	info, err := New("/usr/bin/qemu-img", runner).
		Path("/var/lib/govirta/vms/vm-1.qcow2").
		Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if runner.binary != "/usr/bin/qemu-img" {
		t.Fatalf("Run() binary = %q, want %q", runner.binary, "/usr/bin/qemu-img")
	}
	wantArgs := []string{"info", "-f", "qcow2", "--output=json", "/var/lib/govirta/vms/vm-1.qcow2"}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("Run() args = %#v, want %#v", runner.args, wantArgs)
	}
	wantInfo := Result{
		Filename:              "/var/lib/govirta/vms/vm-1.qcow2",
		Format:                "qcow2",
		VirtualSize:           1073741824,
		ActualSize:            65536,
		BackingFilename:       "/var/lib/govirta/images/base.qcow2",
		BackingFilenameFormat: "qcow2",
	}
	if !reflect.DeepEqual(info, wantInfo) {
		t.Fatalf("Do() = %#v, want %#v", info, wantInfo)
	}
}

func TestDoRejectsMissingPath(t *testing.T) {
	_, err := New("qemu-img", &fakeRunner{}).
		Path(" \t\n").
		Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want ErrInvalidRequest", err)
	}
}

func TestDoReturnsRunnerError(t *testing.T) {
	runnerErr := errors.New("runner failed")
	runner := &fakeRunner{result: imgexec.Result{Stdout: "stdout details", Stderr: "stderr details"}, err: runnerErr}

	_, err := New("qemu-img", runner).
		Path("/var/lib/govirta/vms/vm-1.qcow2").
		Do(context.Background())

	if !errors.Is(err, runnerErr) {
		t.Fatalf("Do() error = %v, want runner error", err)
	}
	var commandErr *imgexec.CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("errors.As(... CommandError) = false, err = %v", err)
	}
	if commandErr.Result != runner.result {
		t.Fatalf("CommandError.Result = %#v, want %#v", commandErr.Result, runner.result)
	}
}

func TestDoReturnsDecodeErrorForJSONParseFailure(t *testing.T) {
	runResult := imgexec.Result{Stdout: `{"filename":`, Stderr: "stderr details"}
	_, err := New("qemu-img", &fakeRunner{result: runResult}).
		Path("/var/lib/govirta/vms/vm-1.qcow2").
		Do(context.Background())

	if err == nil {
		t.Fatalf("Do() error = nil, want parse error")
	}
	var decodeErr *imgexec.DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("errors.As(... DecodeError) = false, err = %v", err)
	}
	if decodeErr.Result != runResult {
		t.Fatalf("DecodeError.Result = %#v, want %#v", decodeErr.Result, runResult)
	}
	var commandErr *imgexec.CommandError
	if errors.As(err, &commandErr) {
		t.Fatalf("errors.As(... CommandError) = true, want false")
	}
}

func TestDoReturnsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := New("qemu-img", &fakeRunner{}).
		Path("/var/lib/govirta/vms/vm-1.qcow2").
		Do(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context.Canceled", err)
	}
}

type fakeRunner struct {
	binary string
	args   []string
	result imgexec.Result
	err    error
}

func (r *fakeRunner) Run(ctx context.Context, binary string, args []string) (imgexec.Result, error) {
	r.binary = binary
	r.args = append([]string(nil), args...)
	if err := ctx.Err(); err != nil {
		return r.result, err
	}
	return r.result, r.err
}
