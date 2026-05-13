package qmp

import "context"

// Client defines the QMP protocol boundary.
type Client interface {
	Name() string
	Connect(ctx context.Context, socketPath string) error
}

// NoopClient is a non-operational QMP client for the initial skeleton.
type NoopClient struct{}

// NewNoopClient creates a QMP client that does not open sockets.
func NewNoopClient() *NoopClient {
	return &NoopClient{}
}

// Name returns the client name.
func (c *NoopClient) Name() string {
	return "qmp-noop"
}

// Connect validates context cancellation and performs no socket operation.
func (c *NoopClient) Connect(ctx context.Context, socketPath string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
