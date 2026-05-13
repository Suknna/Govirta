package bridge

import (
	"context"
	"errors"
	"testing"
)

func TestNoopManagerName(t *testing.T) {
	manager := NewNoopManager()

	got := manager.Name()
	want := "bridge-noop"

	if got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}

func TestNoopManagerEnsureCanceledContext(t *testing.T) {
	manager := NewNoopManager()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := manager.Ensure(ctx, "br0")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure() error = %v, want %v", err, context.Canceled)
	}
}
