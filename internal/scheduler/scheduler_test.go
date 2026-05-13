package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/types"
)

func TestNoopSchedulerScheduleReturnsFirstNode(t *testing.T) {
	scheduler := NewNoopScheduler()
	nodes := []types.Node{{Name: "n1"}, {Name: "n2"}}

	got, err := scheduler.Schedule(context.Background(), types.VirtualMachine{Name: "vm"}, nodes)
	if err != nil {
		t.Fatalf("Schedule() error = %v, want nil", err)
	}

	if got.Name != "n1" {
		t.Fatalf("Schedule() node = %q, want %q", got.Name, "n1")
	}
}

func TestNoopSchedulerScheduleCanceledContext(t *testing.T) {
	scheduler := NewNoopScheduler()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := scheduler.Schedule(ctx, types.VirtualMachine{Name: "vm"}, []types.Node{{Name: "n1"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Schedule() error = %v, want %v", err, context.Canceled)
	}
}

func TestNoopSchedulerScheduleNoNodesReturnsZeroNodeForSkeleton(t *testing.T) {
	scheduler := NewNoopScheduler()

	got, err := scheduler.Schedule(context.Background(), types.VirtualMachine{Name: "vm"}, nil)
	if err != nil {
		t.Fatalf("Schedule() error = %v, want nil", err)
	}

	if got != (types.Node{}) {
		t.Fatalf("Schedule() node = %+v, want zero node", got)
	}
}
