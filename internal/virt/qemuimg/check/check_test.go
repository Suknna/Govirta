package check

import (
	"context"
	"errors"
	"reflect"
	"testing"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

func TestDoBuildsCheckArgsParsesResultAndKeepsRawOutput(t *testing.T) {
	stdout := `{
		"filename": "/var/lib/govirta/vms/vm-1.qcow2",
		"format": "qcow2",
		"check-errors": 0,
		"image-end-offset": 1073807360,
		"corruptions": 1,
		"leaks": 2
	}`
	runner := &fakeRunner{result: imgexec.Result{Stdout: stdout}}

	result, err := New("/usr/bin/qemu-img", runner).
		Path("/var/lib/govirta/vms/vm-1.qcow2").
		Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if runner.binary != "/usr/bin/qemu-img" {
		t.Fatalf("Run() binary = %q, want %q", runner.binary, "/usr/bin/qemu-img")
	}
	wantArgs := []string{"check", "-f", "qcow2", "--output=json", "/var/lib/govirta/vms/vm-1.qcow2"}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("Run() args = %#v, want %#v", runner.args, wantArgs)
	}
	wantResult := Result{
		Filename:       "/var/lib/govirta/vms/vm-1.qcow2",
		Format:         "qcow2",
		CheckErrors:    0,
		ImageEndOffset: 1073807360,
		Corruptions:    1,
		Leaks:          2,
		RawOutput:      stdout,
	}
	if !reflect.DeepEqual(result, wantResult) {
		t.Fatalf("Do() = %#v, want %#v", result, wantResult)
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
