package monitor

import (
	"context"
	"time"
)

// Monitor is the internal QMP transport boundary used by command packages.
type Monitor interface {
	Connect(ctx context.Context) error
	Disconnect() error
	Run(ctx context.Context, command []byte) ([]byte, error)
	Events(ctx context.Context) (<-chan Event, error)
}

// Event is the internal transport representation of a QMP event.
type Event struct {
	Name         string
	Data         map[string]any
	Seconds      int64
	Microseconds int64
}

// Factory creates QMP monitor connections.
type Factory interface {
	New(network string, address string, timeout time.Duration) (Monitor, error)
}
