package apiserver

import (
	"context"
	"testing"
)

func TestNoopServerRun(t *testing.T) {
	server := NewNoopServer()

	if err := server.Run(context.Background()); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}
