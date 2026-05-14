package qemuimg

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeRunner struct {
	binary string
	args   []string
	result CommandResult
}

func (r *fakeRunner) Run(ctx context.Context, binary string, args []string) (CommandResult, error) {
	select {
	case <-ctx.Done():
		return CommandResult{}, ctx.Err()
	default:
	}
	r.binary = binary
	r.args = append([]string(nil), args...)
	return r.result, nil
}

func TestClientCreateBuildsArgs(t *testing.T) {
	runner := &fakeRunner{}
	client := NewClient("qemu-img", runner)
	err := client.Create(context.Background(), CreateRequest{
		Path:      ".tmp/images/root.qcow2",
		Format:    FormatQCOW2,
		SizeBytes: 117440512,
	})
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	want := []string{"create", "-f", "qcow2", ".tmp/images/root.qcow2", "117440512"}
	if runner.binary != "qemu-img" || !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("runner = %q %v, want qemu-img %v", runner.binary, runner.args, want)
	}
}

func TestClientCreateBuildsBackingArgs(t *testing.T) {
	runner := &fakeRunner{}
	client := NewClient("qemu-img", runner)
	err := client.Create(context.Background(), CreateRequest{
		Path:          ".tmp/images/child.qcow2",
		Format:        FormatQCOW2,
		SizeBytes:     117440512,
		BackingFile:   ".tmp/images/base.qcow2",
		BackingFormat: FormatQCOW2,
	})
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	want := []string{"create", "-f", "qcow2", "-b", ".tmp/images/base.qcow2", "-F", "qcow2", ".tmp/images/child.qcow2", "117440512"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("Create() args = %v, want %v", runner.args, want)
	}
}

func TestClientCreateRejectsBackingFileWithoutBackingFormat(t *testing.T) {
	client := NewClient("qemu-img", &fakeRunner{})
	err := client.Create(context.Background(), CreateRequest{
		Path:        ".tmp/images/child.qcow2",
		Format:      FormatQCOW2,
		SizeBytes:   117440512,
		BackingFile: ".tmp/images/base.qcow2",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Create() error = %v, want ErrInvalidRequest", err)
	}
}

func TestClientInfoParsesJSON(t *testing.T) {
	runner := &fakeRunner{result: CommandResult{Stdout: `{"virtual-size":117440512,"filename":"cirros.qcow2","format":"qcow2","actual-size":25169920,"backing-filename":"base.qcow2"}`}}
	client := NewClient("qemu-img", runner)
	info, err := client.Info(context.Background(), "cirros.qcow2")
	if err != nil {
		t.Fatalf("Info() error = %v, want nil", err)
	}
	if info.Filename != "cirros.qcow2" || info.Format != "qcow2" || info.VirtualSize != 117440512 || info.ActualSize != 25169920 || info.BackingFilename != "base.qcow2" {
		t.Fatalf("Info() = %#v, want parsed qcow2 fields", info)
	}
	want := []string{"info", "--output=json", "cirros.qcow2"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("Info() args = %v, want %v", runner.args, want)
	}
}

func TestClientResizeBuildsArgs(t *testing.T) {
	runner := &fakeRunner{}
	client := NewClient("qemu-img", runner)
	err := client.Resize(context.Background(), ResizeRequest{Path: "root.qcow2", SizeBytes: 234881024})
	if err != nil {
		t.Fatalf("Resize() error = %v, want nil", err)
	}
	want := []string{"resize", "root.qcow2", "234881024"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("Resize() args = %v, want %v", runner.args, want)
	}
}
