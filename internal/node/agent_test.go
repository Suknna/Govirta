package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestAgentRunCanceledContext(t *testing.T) {
	logger := zerolog.New(io.Discard)
	ctx, cancel := context.WithCancel(logger.WithContext(context.Background()))
	cancel()

	agent := NewAgent()
	err := agent.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
}

func TestAgentRunLogsDependencyNames(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	ctx := logger.WithContext(context.Background())

	agent := NewAgent()
	if err := agent.Run(ctx); err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	var event map[string]any
	if err := json.Unmarshal(buf.Bytes(), &event); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	assertLogField(t, event, "component", "node")
	assertLogField(t, event, "qmp_client", "qmp-noop")
	assertLogField(t, event, "bridge_manager", "bridge-noop")
	assertLogField(t, event, "message", "starting node agent")
	if _, ok := event["qemu_driver"]; ok {
		t.Fatalf("unexpected qemu_driver log field after qemu.Driver removal")
	}
}

func assertLogField(t *testing.T, event map[string]any, key string, want string) {
	t.Helper()

	got, ok := event[key]
	if !ok {
		t.Fatalf("log field %q missing", key)
	}

	if got != want {
		t.Fatalf("log field %q = %v, want %q", key, got, want)
	}
}
