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
	wantArgs := []string{"check", "--output=json", "/var/lib/govirta/vms/vm-1.qcow2"}
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

	_, err := New("qemu-img", &fakeRunner{err: runnerErr}).
		Path("/var/lib/govirta/vms/vm-1.qcow2").
		Do(context.Background())

	if !errors.Is(err, runnerErr) {
		t.Fatalf("Do() error = %v, want runner error", err)
	}
}

func TestDoReturnsJSONParseError(t *testing.T) {
	_, err := New("qemu-img", &fakeRunner{result: imgexec.Result{Stdout: `{"filename":`}}).
		Path("/var/lib/govirta/vms/vm-1.qcow2").
		Do(context.Background())

	if err == nil {
		t.Fatalf("Do() error = nil, want parse error")
	}
}

type fakeRunner struct {
	binary string
	args   []string
	result imgexec.Result
	err    error
}

func (r *fakeRunner) Run(_ context.Context, binary string, args []string) (imgexec.Result, error) {
	r.binary = binary
	r.args = append([]string(nil), args...)
	return r.result, r.err
}
