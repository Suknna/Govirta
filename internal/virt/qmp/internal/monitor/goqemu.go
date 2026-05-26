package monitor

import (
	"context"
	"time"

	"github.com/suknna/govirta/internal/virt/qmp/internal/goqemu"
)

// GoQEMUFactory creates monitors backed by the vendored go-qemu socket monitor.
type GoQEMUFactory struct{}

// New creates a QMP monitor using the configured network, address, and timeout.
func (f GoQEMUFactory) New(network string, address string, timeout time.Duration) (Monitor, error) {
	mon, err := goqemu.NewSocketMonitor(network, address, timeout)
	if err != nil {
		return nil, err
	}
	return goQEMUMonitor{monitor: mon}, nil
}

type goQEMUMonitor struct {
	monitor goqemu.Monitor
}

func (m goQEMUMonitor) Connect(ctx context.Context) error {
	return m.monitor.Connect(ctx)
}

func (m goQEMUMonitor) Disconnect(ctx context.Context) error {
	return m.monitor.Disconnect(ctx)
}

func (m goQEMUMonitor) Run(ctx context.Context, command []byte) ([]byte, error) {
	return m.monitor.Run(ctx, command)
}

func (m goQEMUMonitor) Events(ctx context.Context) (<-chan Event, error) {
	rawEvents, err := m.monitor.Events(ctx)
	if err != nil {
		return nil, err
	}

	events := make(chan Event)
	go func() {
		defer close(events)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-rawEvents:
				if !ok {
					return
				}
				converted := Event{
					Name:         event.Event,
					Data:         event.Data,
					Seconds:      event.Timestamp.Seconds,
					Microseconds: event.Timestamp.Microseconds,
				}
				select {
				case events <- converted:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return events, nil
}
