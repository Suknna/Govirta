package qmp

import (
	"context"
	"os"
	"testing"
)

func TestIntegrationQueryStatus(t *testing.T) {
	if os.Getenv("GOVIRTA_QMP_INTEGRATION") != "1" {
		t.Skip("set GOVIRTA_QMP_INTEGRATION=1 to run live QMP integration tests")
	}
	socketPath := os.Getenv("GOVIRTA_QMP_SOCKET")
	if socketPath == "" {
		t.Fatal("GOVIRTA_QMP_SOCKET is required")
	}

	client, err := NewSocketClient(Config{SocketPath: socketPath})
	if err != nil {
		t.Fatalf("NewSocketClient() error = %v", err)
	}
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() {
		if err := client.Disconnect(ctx); err != nil {
			t.Fatalf("Disconnect() error = %v", err)
		}
	}()

	status, err := client.QueryStatus(ctx)
	if err != nil {
		t.Fatalf("QueryStatus() error = %v", err)
	}
	if want := os.Getenv("GOVIRTA_QMP_EXPECT_STATE"); want != "" && status.State != State(want) {
		t.Fatalf("state = %q, want %q", status.State, want)
	}
}
