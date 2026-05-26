package power

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/suknna/govirta/internal/virt/qmp/internal/monitor"
)

func TestPowerCommands(t *testing.T) {
	tests := []struct {
		name    string
		run     func(context.Context, monitor.Monitor) error
		command []byte
	}{
		{name: "system powerdown", run: SystemPowerdown, command: []byte(`{"execute":"system_powerdown"}`)},
		{name: "quit", run: Quit, command: []byte(`{"execute":"quit"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mon := &fakeMonitor{}
			if err := tt.run(context.Background(), mon); err != nil {
				t.Fatalf("run() error = %v", err)
			}
			if !reflect.DeepEqual(mon.command, tt.command) {
				t.Fatalf("command = %s, want %s", mon.command, tt.command)
			}
		})
	}
}

func TestPowerCommandCanceledContextDoesNotRun(t *testing.T) {
	mon := &fakeMonitor{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Quit(ctx, mon)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Quit() error = %v, want %v", err, context.Canceled)
	}
	if mon.called {
		t.Fatalf("monitor Run() called for canceled context")
	}
}

func TestPowerCommandReturnsMonitorError(t *testing.T) {
	want := errors.New("run failed")
	mon := &fakeMonitor{err: want}

	err := SystemPowerdown(context.Background(), mon)
	if !errors.Is(err, want) {
		t.Fatalf("SystemPowerdown() error = %v, want %v", err, want)
	}
}

type fakeMonitor struct {
	called  bool
	command []byte
	err     error
}

func (m *fakeMonitor) Connect(ctx context.Context) error                        { return nil }
func (m *fakeMonitor) Disconnect(ctx context.Context) error                     { return ctx.Err() }
func (m *fakeMonitor) Events(ctx context.Context) (<-chan monitor.Event, error) { return nil, nil }
func (m *fakeMonitor) Run(ctx context.Context, command []byte) ([]byte, error) {
	m.called = true
	m.command = append([]byte(nil), command...)
	return []byte(`{"return":{}}`), m.err
}
