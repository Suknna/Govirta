package qemu

import (
	"context"
	"errors"
	"testing"
)

func TestNoopDriverName(t *testing.T) {
	driver := NewNoopDriver()

	got := driver.Name()
	want := "qemu-noop"

	if got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}

func TestNoopDriverStart(t *testing.T) {
	driver := NewNoopDriver()

	if err := driver.Start(context.Background(), "vm"); err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}
}

func TestNoopDriverStopCanceledContext(t *testing.T) {
	driver := NewNoopDriver()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := driver.Stop(ctx, "vm")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() error = %v, want %v", err, context.Canceled)
	}
}
