package qmp

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/virt/qmp/internal/monitor"
)

func TestNewSocketClientRejectsEmptySocketPath(t *testing.T) {
	_, err := NewSocketClient(Config{})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewSocketClient() error = %v, want %v", err, ErrInvalidConfig)
	}
}

func TestNewSocketClientDefaultsTimeout(t *testing.T) {
	client, err := newSocketClient(Config{SocketPath: "vm.qmp"}, &fakeFactory{})
	if err != nil {
		t.Fatalf("newSocketClient() error = %v", err)
	}

	if client.timeout != DefaultTimeout {
		t.Fatalf("timeout = %v, want %v", client.timeout, DefaultTimeout)
	}
}

func TestNewSocketClientUsesConfiguredTimeout(t *testing.T) {
	configured := 5 * time.Second
	client, err := newSocketClient(Config{SocketPath: "vm.qmp", Timeout: configured}, &fakeFactory{})
	if err != nil {
		t.Fatalf("newSocketClient() error = %v", err)
	}

	if client.timeout != configured {
		t.Fatalf("timeout = %v, want %v", client.timeout, configured)
	}
}

func TestSocketClientName(t *testing.T) {
	client, err := newSocketClient(Config{SocketPath: "vm.qmp"}, &fakeFactory{})
	if err != nil {
		t.Fatalf("newSocketClient() error = %v", err)
	}

	if got := client.Name(); got != socketClientName {
		t.Fatalf("Name() = %q, want %q", got, socketClientName)
	}
}

func TestSocketClientConnectUsesFactoryAndHandshake(t *testing.T) {
	factory := &fakeFactory{monitor: newFakeMonitor()}
	client, err := newSocketClient(Config{SocketPath: "vm.qmp", Timeout: time.Second}, factory)
	if err != nil {
		t.Fatalf("newSocketClient() error = %v", err)
	}

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	if factory.network != "unix" {
		t.Fatalf("factory network = %q, want unix", factory.network)
	}
	if factory.address != "vm.qmp" {
		t.Fatalf("factory address = %q, want vm.qmp", factory.address)
	}
	if factory.timeout != time.Second {
		t.Fatalf("factory timeout = %v, want %v", factory.timeout, time.Second)
	}
	if !factory.monitor.connected {
		t.Fatalf("monitor Connect() was not called")
	}
}

func TestSocketClientRejectsDuplicateConnect(t *testing.T) {
	mon := newFakeMonitor()
	client, err := connectedTestClient(mon)
	if err != nil {
		t.Fatalf("connectedTestClient() error = %v", err)
	}

	err = client.Connect(context.Background())
	if !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("Connect() error = %v, want %v", err, ErrAlreadyConnected)
	}
}

func TestSocketClientConnectFailureDisconnectsMonitor(t *testing.T) {
	mon := newFakeMonitor()
	mon.connectErr = errors.New("handshake failed")
	client, err := newSocketClient(Config{SocketPath: "vm.qmp"}, &fakeFactory{monitor: mon})
	if err != nil {
		t.Fatalf("newSocketClient() error = %v", err)
	}

	if err := client.Connect(context.Background()); !errors.Is(err, mon.connectErr) {
		t.Fatalf("Connect() error = %v, want %v", err, mon.connectErr)
	}
	if mon.disconnectCalls != 1 {
		t.Fatalf("Disconnect() calls = %d, want 1", mon.disconnectCalls)
	}
}

func TestSocketClientConnectCanceledAfterFactoryDisconnectsMonitor(t *testing.T) {
	mon := newFakeMonitor()
	ctx, cancel := context.WithCancel(context.Background())
	factory := &fakeFactory{monitor: mon, afterNew: cancel}
	client, err := newSocketClient(Config{SocketPath: "vm.qmp"}, factory)
	if err != nil {
		t.Fatalf("newSocketClient() error = %v", err)
	}

	err = client.Connect(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect() error = %v, want %v", err, context.Canceled)
	}
	if mon.disconnectCalls != 1 {
		t.Fatalf("Disconnect() calls = %d, want 1", mon.disconnectCalls)
	}
}

func TestSocketClientConnectCanceledContext(t *testing.T) {
	factory := &fakeFactory{monitor: newFakeMonitor()}
	client, err := newSocketClient(Config{SocketPath: "vm.qmp"}, factory)
	if err != nil {
		t.Fatalf("newSocketClient() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = client.Connect(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect() error = %v, want %v", err, context.Canceled)
	}
	if factory.called {
		t.Fatalf("factory called for canceled context")
	}
}

func TestSocketClientWaitReadyConnects(t *testing.T) {
	factory := &fakeFactory{monitor: newFakeMonitor()}
	client, err := newSocketClient(Config{SocketPath: "vm.qmp"}, factory)
	if err != nil {
		t.Fatalf("newSocketClient() error = %v", err)
	}

	if err := client.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if !factory.monitor.connected {
		t.Fatalf("monitor Connect() was not called")
	}
}

func TestSocketClientOperationsRequireConnection(t *testing.T) {
	client, err := newSocketClient(Config{SocketPath: "vm.qmp"}, &fakeFactory{monitor: newFakeMonitor()})
	if err != nil {
		t.Fatalf("newSocketClient() error = %v", err)
	}

	if _, err := client.QueryStatus(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("QueryStatus() error = %v, want %v", err, ErrNotConnected)
	}
	if err := client.SystemPowerdown(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("SystemPowerdown() error = %v, want %v", err, ErrNotConnected)
	}
	if err := client.Quit(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Quit() error = %v, want %v", err, ErrNotConnected)
	}
	if _, err := client.Events(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Events() error = %v, want %v", err, ErrNotConnected)
	}
}

func TestSocketClientQueryStatus(t *testing.T) {
	mon := newFakeMonitor()
	mon.runResponse = []byte(`{"return":{"running":true,"singlestep":false,"status":"running"}}`)
	client, err := connectedTestClient(mon)
	if err != nil {
		t.Fatalf("connectedTestClient() error = %v", err)
	}

	status, err := client.QueryStatus(context.Background())
	if err != nil {
		t.Fatalf("QueryStatus() error = %v", err)
	}
	want := Status{Running: true, Singlestep: false, State: StateRunning}
	if status != want {
		t.Fatalf("QueryStatus() = %+v, want %+v", status, want)
	}
}

func TestSocketClientPowerCommands(t *testing.T) {
	tests := []struct {
		name    string
		run     func(context.Context, *SocketClient) error
		command string
	}{
		{name: "system powerdown", run: func(ctx context.Context, c *SocketClient) error { return c.SystemPowerdown(ctx) }, command: "system_powerdown"},
		{name: "quit", run: func(ctx context.Context, c *SocketClient) error { return c.Quit(ctx) }, command: "quit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mon := newFakeMonitor()
			client, err := connectedTestClient(mon)
			if err != nil {
				t.Fatalf("connectedTestClient() error = %v", err)
			}

			if err := tt.run(context.Background(), client); err != nil {
				t.Fatalf("run() error = %v", err)
			}
			want := []byte(`{"execute":"` + tt.command + `"}`)
			if !reflect.DeepEqual(mon.lastCommand, want) {
				t.Fatalf("last command = %s, want %s", mon.lastCommand, want)
			}
		})
	}
}

func TestSocketClientEventsCanStartOnlyOnce(t *testing.T) {
	mon := newFakeMonitor()
	client, err := connectedTestClient(mon)
	if err != nil {
		t.Fatalf("connectedTestClient() error = %v", err)
	}

	first, err := client.Events(context.Background())
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if first == nil {
		t.Fatalf("Events() returned nil channel")
	}

	_, err = client.Events(context.Background())
	if !errors.Is(err, ErrEventsAlreadyStarted) {
		t.Fatalf("second Events() error = %v, want %v", err, ErrEventsAlreadyStarted)
	}
}

func TestSocketClientEventsFiltersNames(t *testing.T) {
	mon := newFakeMonitor()
	client, err := connectedTestClient(mon)
	if err != nil {
		t.Fatalf("connectedTestClient() error = %v", err)
	}

	events, err := client.Events(context.Background(), EventShutdown)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	mon.events <- monitor.Event{Name: "RESET", Seconds: 1}
	mon.events <- monitor.Event{Name: "SHUTDOWN", Seconds: 2, Data: map[string]any{"guest": "cirros"}}
	close(mon.events)

	got, ok := <-events
	if !ok {
		t.Fatalf("events channel closed before matching event")
	}
	if got.Name != EventShutdown {
		t.Fatalf("event name = %q, want %q", got.Name, EventShutdown)
	}
	if got.Data["guest"] != "cirros" {
		t.Fatalf("event data = %v, want guest=cirros", got.Data)
	}
	if _, ok := <-events; ok {
		t.Fatalf("events channel should be closed after source closes")
	}
}

func TestSocketClientDisconnectIsIdempotent(t *testing.T) {
	mon := newFakeMonitor()
	client, err := connectedTestClient(mon)
	if err != nil {
		t.Fatalf("connectedTestClient() error = %v", err)
	}

	if err := client.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	if err := client.Disconnect(context.Background()); err != nil {
		t.Fatalf("second Disconnect() error = %v", err)
	}
	if !mon.disconnected {
		t.Fatalf("monitor Disconnect() was not called")
	}
	if _, err := client.QueryStatus(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("QueryStatus() after Disconnect() error = %v, want %v", err, ErrNotConnected)
	}
}

func TestSocketClientDisconnectCanceledContextStillClosesMonitor(t *testing.T) {
	mon := newFakeMonitor()
	client, err := connectedTestClient(mon)
	if err != nil {
		t.Fatalf("connectedTestClient() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = client.Disconnect(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Disconnect() error = %v, want context.Canceled", err)
	}
	if mon.disconnectCalls != 1 {
		t.Fatalf("Disconnect() calls = %d, want 1", mon.disconnectCalls)
	}
}

func TestSocketClientEventsCancelWithoutConsumer(t *testing.T) {
	mon := newFakeMonitor()
	client, err := connectedTestClient(mon)
	if err != nil {
		t.Fatalf("connectedTestClient() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, err := client.Events(ctx)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	mon.events <- monitor.Event{Name: "SHUTDOWN", Seconds: 1}
	cancel()
	close(mon.events)

	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatalf("events channel did not close after cancellation")
	}
}

func TestNoopClientConnectCanceledContext(t *testing.T) {
	client := NewNoopClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := client.Connect(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect() error = %v, want %v", err, context.Canceled)
	}
}

func TestNoopClientName(t *testing.T) {
	client := NewNoopClient()

	got := client.Name()
	want := "qmp-noop"

	if got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}

func connectedTestClient(mon *fakeMonitor) (*SocketClient, error) {
	client, err := newSocketClient(Config{SocketPath: "vm.qmp"}, &fakeFactory{monitor: mon})
	if err != nil {
		return nil, err
	}
	if err := client.Connect(context.Background()); err != nil {
		return nil, err
	}
	return client, nil
}

type fakeFactory struct {
	monitor  *fakeMonitor
	called   bool
	network  string
	address  string
	timeout  time.Duration
	err      error
	afterNew func()
}

func (f *fakeFactory) New(network string, address string, timeout time.Duration) (monitor.Monitor, error) {
	f.called = true
	f.network = network
	f.address = address
	f.timeout = timeout
	if f.err != nil {
		return nil, f.err
	}
	if f.afterNew != nil {
		f.afterNew()
	}
	return f.monitor, nil
}

type fakeMonitor struct {
	connected       bool
	disconnected    bool
	connectErr      error
	disconnectCalls int
	runResponse     []byte
	runErr          error
	lastCommand     []byte
	events          chan monitor.Event
}

func newFakeMonitor() *fakeMonitor {
	return &fakeMonitor{events: make(chan monitor.Event, 4), runResponse: []byte(`{"return":{}}`)}
}

func (m *fakeMonitor) Connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.connectErr != nil {
		return m.connectErr
	}
	m.connected = true
	return nil
}

func (m *fakeMonitor) Disconnect() error {
	m.disconnectCalls++
	m.disconnected = true
	return nil
}

func (m *fakeMonitor) Run(ctx context.Context, command []byte) ([]byte, error) {
	m.lastCommand = append([]byte(nil), command...)
	return m.runResponse, m.runErr
}

func (m *fakeMonitor) Events(ctx context.Context) (<-chan monitor.Event, error) {
	_ = ctx
	return m.events, nil
}
