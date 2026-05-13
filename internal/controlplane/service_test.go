package controlplane

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/rs/zerolog"
)

func TestServiceRun(t *testing.T) {
	logger := zerolog.New(io.Discard)
	ctx := logger.WithContext(context.Background())

	service := NewService()
	if err := service.Run(ctx); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}

func TestServiceRunCanceledContext(t *testing.T) {
	logger := zerolog.New(io.Discard)
	ctx, cancel := context.WithCancel(logger.WithContext(context.Background()))
	cancel()

	service := NewService()
	err := service.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
}
