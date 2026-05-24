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

func (m goQEMUMonitor) Connect() error {
	return m.monitor.Connect()
}

func (m goQEMUMonitor) Disconnect() error {
	return m.monitor.Disconnect()
}

func (m goQEMUMonitor) Run(command []byte) ([]byte, error) {
	return m.monitor.Run(command)
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
				events <- Event{
					Name:         event.Event,
					Data:         event.Data,
					Seconds:      event.Timestamp.Seconds,
					Microseconds: event.Timestamp.Microseconds,
				}
			}
		}
	}()
	return events, nil
}
