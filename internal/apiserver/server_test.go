package apiserver

import (
	"context"
	"errors"
	"testing"
)

func TestNoopServerRun(t *testing.T) {
	server := NewNoopServer()

	if err := server.Run(context.Background()); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}

func TestNoopServerRunCanceledContext(t *testing.T) {
	server := NewNoopServer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := server.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
}
