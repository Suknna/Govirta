package qmp

import (
	"context"
	"fmt"
	"sync"
	"time"

	qevents "github.com/suknna/govirta/internal/virt/qmp/internal/events"
	"github.com/suknna/govirta/internal/virt/qmp/internal/monitor"
	"github.com/suknna/govirta/internal/virt/qmp/internal/power"
	"github.com/suknna/govirta/internal/virt/qmp/internal/status"
)

const socketClientName = "qmp-socket"

// Client defines the project-owned QMP protocol boundary.
type Client interface {
	Name() string
	Connect(ctx context.Context) error
	Disconnect(ctx context.Context) error
	QueryStatus(ctx context.Context) (Status, error)
	WaitReady(ctx context.Context) error
	SystemPowerdown(ctx context.Context) error
	Quit(ctx context.Context) error
	Events(ctx context.Context, names ...EventName) (<-chan Event, error)
}

// SocketClient talks to a QEMU QMP unix socket.
type SocketClient struct {
	socketPath string
	timeout    time.Duration
	factory    monitor.Factory

	mu            sync.Mutex
	monitor       monitor.Monitor
	eventsStarted bool
}

// NewSocketClient creates a QMP client for a unix monitor socket.
func NewSocketClient(config Config) (*SocketClient, error) {
	return newSocketClient(config, monitor.GoQEMUFactory{})
}

func newSocketClient(config Config, factory monitor.Factory) (*SocketClient, error) {
	if config.SocketPath == "" {
		return nil, fmt.Errorf("%w: socket path is required", ErrInvalidConfig)
	}
	if factory == nil {
		return nil, fmt.Errorf("%w: monitor factory is required", ErrInvalidConfig)
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return &SocketClient{socketPath: config.SocketPath, timeout: timeout, factory: factory}, nil
}

// Name returns the client implementation name.
func (c *SocketClient) Name() string {
	return socketClientName
}

// Connect dials the configured QMP socket and completes the capabilities handshake.
func (c *SocketClient) Connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	mon, err := c.factory.New("unix", c.socketPath, c.timeout)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := mon.Connect(); err != nil {
		return err
	}

	c.mu.Lock()
	c.monitor = mon
	c.eventsStarted = false
	c.mu.Unlock()
	return nil
}

// WaitReady waits until QMP is ready to accept commands.
func (c *SocketClient) WaitReady(ctx context.Context) error {
	if _, err := c.connectedMonitor(); err == nil {
		return nil
	}
	return c.Connect(ctx)
}

// Disconnect closes the active QMP connection if one exists.
func (c *SocketClient) Disconnect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	mon := c.monitor
	c.monitor = nil
	c.eventsStarted = false
	c.mu.Unlock()

	if mon == nil {
		return nil
	}
	return mon.Disconnect()
}

// QueryStatus returns QEMU's current run-state.
func (c *SocketClient) QueryStatus(ctx context.Context) (Status, error) {
	mon, err := c.connectedMonitor()
	if err != nil {
		return Status{}, err
	}
	result, err := status.Query(ctx, mon)
	if err != nil {
		return Status{}, err
	}
	return Status{Running: result.Running, Singlestep: result.Singlestep, State: State(result.State)}, nil
}

// SystemPowerdown asks QEMU to initiate a graceful guest shutdown.
func (c *SocketClient) SystemPowerdown(ctx context.Context) error {
	mon, err := c.connectedMonitor()
	if err != nil {
		return err
	}
	return power.SystemPowerdown(ctx, mon)
}

// Quit asks QEMU to terminate the VM process.
func (c *SocketClient) Quit(ctx context.Context) error {
	mon, err := c.connectedMonitor()
	if err != nil {
		return err
	}
	return power.Quit(ctx, mon)
}

// Events returns a filtered stream of QMP events.
func (c *SocketClient) Events(ctx context.Context, names ...EventName) (<-chan Event, error) {
	c.mu.Lock()
	if c.monitor == nil {
		c.mu.Unlock()
		return nil, ErrNotConnected
	}
	if c.eventsStarted {
		c.mu.Unlock()
		return nil, ErrEventsAlreadyStarted
	}
	c.eventsStarted = true
	mon := c.monitor
	c.mu.Unlock()

	stream, err := mon.Events(ctx)
	if err != nil {
		c.mu.Lock()
		c.eventsStarted = false
		c.mu.Unlock()
		return nil, err
	}
	return convertEvents(qevents.Stream(ctx, stream, eventNameStrings(names)...)), nil
}

func (c *SocketClient) connectedMonitor() (monitor.Monitor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.monitor == nil {
		return nil, ErrNotConnected
	}
	return c.monitor, nil
}

func eventNameStrings(names []EventName) []string {
	if len(names) == 0 {
		return nil
	}
	values := make([]string, 0, len(names))
	for _, name := range names {
		values = append(values, string(name))
	}
	return values
}

func convertEvents(stream <-chan qevents.Event) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		for event := range stream {
			out <- Event{Name: EventName(event.Name), Data: event.Data, Timestamp: event.Timestamp}
		}
	}()
	return out
}

// NoopClient is a non-operational QMP client for skeleton composition tests.
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
func (c *NoopClient) Connect(ctx context.Context) error {
	return ctx.Err()
}

// Disconnect validates context cancellation and performs no socket operation.
func (c *NoopClient) Disconnect(ctx context.Context) error {
	return ctx.Err()
}

// WaitReady validates context cancellation and performs no socket operation.
func (c *NoopClient) WaitReady(ctx context.Context) error {
	return ctx.Err()
}

// QueryStatus validates context cancellation and returns an empty status.
func (c *NoopClient) QueryStatus(ctx context.Context) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	return Status{}, nil
}

// SystemPowerdown validates context cancellation and performs no socket operation.
func (c *NoopClient) SystemPowerdown(ctx context.Context) error {
	return ctx.Err()
}

// Quit validates context cancellation and performs no socket operation.
func (c *NoopClient) Quit(ctx context.Context) error {
	return ctx.Err()
}

// Events validates context cancellation and returns a closed event stream.
func (c *NoopClient) Events(ctx context.Context, names ...EventName) (<-chan Event, error) {
	_ = names
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events := make(chan Event)
	close(events)
	return events, nil
}
