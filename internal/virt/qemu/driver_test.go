package qemu

import (
	"context"
	"reflect"
	"testing"
)

type fakeProcessRunner struct {
	binary string
	args   []string
}

func (r *fakeProcessRunner) Start(ctx context.Context, inv Invocation) (Process, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	r.binary = inv.Binary
	r.args = append([]string(nil), inv.Args...)
	return fakeProcess{}, nil
}

type fakeProcess struct{}

func (fakeProcess) PID() int                       { return 1234 }
func (fakeProcess) Wait() error                    { return nil }
func (fakeProcess) Stop(ctx context.Context) error { return ctx.Err() }

func TestDriverStartUsesBuilderAndRunner(t *testing.T) {
	runner := &fakeProcessRunner{}
	driver := NewDriver(NewBuilder(), runner)
	cfg := Config{
		Binary:  "qemu-system-x86_64",
		Name:    "vm",
		Machine: MachineConfig{Type: "q35", Accelerator: AcceleratorTCG},
		Compute: ComputeConfig{MemoryMiB: 256, VCPUs: 1},
		QMP:     QMPConfig{SocketPath: ".tmp/qemu/vm/qmp.sock"},
		Disks:   []DiskConfig{{ID: "root", Path: ".tmp/images/root.qcow2", Format: DiskFormatQCOW2, Interface: DiskInterfaceVirtio}},
	}

	proc, err := driver.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}
	if proc.PID() != 1234 {
		t.Fatalf("PID() = %d, want 1234", proc.PID())
	}
	if runner.binary != "qemu-system-x86_64" {
		t.Fatalf("runner binary = %q, want qemu-system-x86_64", runner.binary)
	}
	if !reflect.DeepEqual(runner.args[:2], []string{"-name", "vm"}) {
		t.Fatalf("runner args prefix = %v, want [-name vm]", runner.args[:2])
	}
}
