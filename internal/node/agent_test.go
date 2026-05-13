package node

import (
	"context"
	"io"
	"testing"

	"github.com/rs/zerolog"
)

func TestAgentRun(t *testing.T) {
	logger := zerolog.New(io.Discard)
	ctx := logger.WithContext(context.Background())

	agent := NewAgent()
	if err := agent.Run(ctx); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}
