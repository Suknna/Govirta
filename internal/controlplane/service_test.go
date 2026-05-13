package controlplane

import (
	"context"
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
