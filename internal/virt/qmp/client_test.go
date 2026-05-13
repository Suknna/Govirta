package qmp

import (
	"context"
	"errors"
	"testing"
)

func TestNoopClientName(t *testing.T) {
	client := NewNoopClient()

	got := client.Name()
	want := "qmp-noop"

	if got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}

func TestNoopClientConnectCanceledContext(t *testing.T) {
	client := NewNoopClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := client.Connect(ctx, "/tmp/qmp.sock")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect() error = %v, want %v", err, context.Canceled)
	}
}
