package apiserver

import "context"

// Server defines the control plane API server boundary.
type Server interface {
	Run(ctx context.Context) error
}

// NoopServer is a non-listening API server for the initial skeleton.
type NoopServer struct{}

// NewNoopServer creates an API server that does not listen on a network port.
func NewNoopServer() *NoopServer {
	return &NoopServer{}
}

// Run validates context cancellation and performs no network operation.
func (s *NoopServer) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
