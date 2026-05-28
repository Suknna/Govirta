package qemuimg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

func TestNewClientDefaultsBinary(t *testing.T) {
	client := NewClient(Config{})

	if got := client.QCOW2().Binary(); got != "qemu-img" {
		t.Fatalf("QCOW2().Binary() = %q, want %q", got, "qemu-img")
	}
}

func TestNewClientUsesConfiguredBinary(t *testing.T) {
	client := NewClient(Config{Binary: "/usr/bin/qemu-img"})

	if got := client.QCOW2().Binary(); got != "/usr/bin/qemu-img" {
		t.Fatalf("QCOW2().Binary() = %q, want %q", got, "/usr/bin/qemu-img")
	}
}

func TestErrInvalidRequestAliasesExecBoundary(t *testing.T) {
	err := imgexec.InvalidRequest("path is required")

	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("errors.Is(%v, qemuimg.ErrInvalidRequest) = false, want true", err)
	}
}

func TestCommandErrorAliasesExecBoundary(t *testing.T) {
	runnerErr := errors.New("runner failed")
	runner := &recordingRunner{result: imgexec.Result{Stderr: "qemu-img failed"}, err: runnerErr}
	client := NewClient(Config{Runner: runner})

	_, err := client.QCOW2().Info().Path("disk.qcow2").Do(context.Background())

	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("errors.As(... CommandError) = false, err = %v", err)
	}
	if commandErr.Result.Stderr != "qemu-img failed" {
		t.Fatalf("CommandError.Result.Stderr = %q, want qemu-img failed", commandErr.Result.Stderr)
	}
}

func TestDecodeErrorAliasesExecBoundary(t *testing.T) {
	runResult := imgexec.Result{Stdout: `{"filename":`, Stderr: "decode stderr"}
	runner := &recordingRunner{result: runResult}
	client := NewClient(Config{Runner: runner})

	_, err := client.QCOW2().Info().Path("disk.qcow2").Do(context.Background())

	var decodeErr *DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("errors.As(... DecodeError) = false, err = %v", err)
	}
	if decodeErr.Result != runResult {
		t.Fatalf("DecodeError.Result = %#v, want %#v", decodeErr.Result, runResult)
	}
	var commandErr *CommandError
	if errors.As(err, &commandErr) {
		t.Fatalf("errors.As(... CommandError) = true, want false")
	}
}

func TestQCOW2InfoRejectsLeadingDashPath(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(Config{Runner: runner})

	_, err := client.QCOW2().Info().Path("--help").Do(context.Background())

	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want ErrInvalidRequest", err)
	}
	if runner.calls != 0 {
		t.Fatalf("Run() calls = %d, want 0", runner.calls)
	}
}

func TestQCOW2ReturnsCommandBuilders(t *testing.T) {
	qcow2 := NewClient(Config{}).QCOW2()

	if qcow2.Create() == nil {
		t.Fatalf("Create() = nil, want builder")
	}
	if qcow2.Info() == nil {
		t.Fatalf("Info() = nil, want builder")
	}
	if qcow2.Convert() == nil {
		t.Fatalf("Convert() = nil, want builder")
	}
	if qcow2.Resize() == nil {
		t.Fatalf("Resize() = nil, want builder")
	}
	if qcow2.Snapshot() == nil {
		t.Fatalf("Snapshot() = nil, want builder")
	}
	if qcow2.Check() == nil {
		t.Fatalf("Check() = nil, want builder")
	}
	if qcow2.Remove() == nil {
		t.Fatalf("Remove() = nil, want builder")
	}
}

func TestQCOW2CreateUsesConfiguredRunner(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(Config{Binary: "/custom/qemu-img", Runner: runner})

	err := client.QCOW2().Create().
		Target("child.qcow2").
		FromBase("base.qcow2").
		SizeBytes(117440512).
		Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	assertRun(t, runner, "/custom/qemu-img", []string{"create", "-f", "qcow2", "-F", "qcow2", "-b", "base.qcow2", "child.qcow2", "117440512"})
}

func TestQCOW2InfoUsesConfiguredRunnerAndParsesResult(t *testing.T) {
	runner := &recordingRunner{result: imgexec.Result{Stdout: `{
		"filename": "disk.qcow2",
		"format": "qcow2",
		"virtual-size": 117440512,
		"actual-size": 65536,
		"backing-filename": "base.qcow2",
		"backing-filename-format": "qcow2"
	}`}}
	client := NewClient(Config{Binary: "/custom/qemu-img", Runner: runner})

	info, err := client.QCOW2().Info().Path("disk.qcow2").Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	assertRun(t, runner, "/custom/qemu-img", []string{"info", "-f", "qcow2", "--output=json", "disk.qcow2"})
	if info.Filename != "disk.qcow2" || info.Format != "qcow2" || info.VirtualSize != 117440512 || info.ActualSize != 65536 || info.BackingFilename != "base.qcow2" || info.BackingFilenameFormat != "qcow2" {
		t.Fatalf("Do() = %#v, want parsed qcow2 info", info)
	}
}

func TestQCOW2CheckUsesConfiguredRunnerAndParsesResult(t *testing.T) {
	stdout := `{
		"filename": "disk.qcow2",
		"format": "qcow2",
		"check-errors": 0,
		"image-end-offset": 117506048,
		"corruptions": 1,
		"leaks": 2
	}`
	runner := &recordingRunner{result: imgexec.Result{Stdout: stdout}}
	client := NewClient(Config{Binary: "/custom/qemu-img", Runner: runner})

	check, err := client.QCOW2().Check().Path("disk.qcow2").Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	assertRun(t, runner, "/custom/qemu-img", []string{"check", "-f", "qcow2", "--output=json", "disk.qcow2"})
	if check.Filename != "disk.qcow2" || check.Format != "qcow2" || check.CheckErrors != 0 || check.ImageEndOffset != 117506048 || check.Corruptions != 1 || check.Leaks != 2 || check.RawOutput != stdout {
		t.Fatalf("Do() = %#v, want parsed qcow2 check result", check)
	}
}

func TestQCOW2ConvertUsesConfiguredRunner(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(Config{Binary: "/custom/qemu-img", Runner: runner})

	err := client.QCOW2().Convert().Source("src.qcow2").Target("dst.qcow2").Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	assertRun(t, runner, "/custom/qemu-img", []string{"convert", "-f", "qcow2", "-O", "qcow2", "src.qcow2", "dst.qcow2"})
}

func TestQCOW2ConvertUsesConfiguredRunnerWithRawSource(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(Config{Binary: "/custom/qemu-img", Runner: runner})

	err := client.QCOW2().Convert().Source("src.raw").SourceFormat("raw").Target("dst.qcow2").Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	assertRun(t, runner, "/custom/qemu-img", []string{"convert", "-f", "raw", "-O", "qcow2", "src.raw", "dst.qcow2"})
}

func TestQCOW2ResizeUsesConfiguredRunner(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(Config{Binary: "/custom/qemu-img", Runner: runner})

	err := client.QCOW2().Resize().Path("disk.qcow2").SizeBytes(1073741824).Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	assertRun(t, runner, "/custom/qemu-img", []string{"resize", "-f", "qcow2", "disk.qcow2", "1073741824"})
}

func TestQCOW2SnapshotUsesConfiguredRunner(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(Config{Binary: "/custom/qemu-img", Runner: runner})

	err := client.QCOW2().Snapshot().Path("disk.qcow2").Name("before-upgrade").Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	assertRun(t, runner, "/custom/qemu-img", []string{"snapshot", "-c", "before-upgrade", "disk.qcow2"})
}

func TestQCOW2RemoveDeletesFileWithoutRunner(t *testing.T) {
	runner := &recordingRunner{}
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(path, []byte("qcow2"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v, want nil", err)
	}
	client := NewClient(Config{Binary: "/custom/qemu-img", Runner: runner})

	err := client.QCOW2().Remove().Path(path).Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat() error = %v, want os.ErrNotExist", err)
	}
	if runner.calls != 0 {
		t.Fatalf("Run() calls = %d, want 0", runner.calls)
	}
}

type recordingRunner struct {
	binary string
	args   []string
	calls  int
	result imgexec.Result
	err    error
}

func (r *recordingRunner) Run(_ context.Context, binary string, args []string) (imgexec.Result, error) {
	r.calls++
	r.binary = binary
	r.args = append([]string(nil), args...)
	return r.result, r.err
}

func assertRun(t *testing.T, runner *recordingRunner, wantBinary string, wantArgs []string) {
	t.Helper()
	if runner.calls != 1 {
		t.Fatalf("Run() calls = %d, want 1", runner.calls)
	}
	if runner.binary != wantBinary {
		t.Fatalf("Run() binary = %q, want %q", runner.binary, wantBinary)
	}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("Run() args = %#v, want %#v", runner.args, wantArgs)
	}
}
