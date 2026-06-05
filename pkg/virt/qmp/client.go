package qmp

import (
	"context"
	"fmt"
	"sync"
	"time"

	qevents "github.com/suknna/govirta/pkg/virt/qmp/internal/events"
	"github.com/suknna/govirta/pkg/virt/qmp/internal/monitor"
	"github.com/suknna/govirta/pkg/virt/qmp/internal/power"
	"github.com/suknna/govirta/pkg/virt/qmp/internal/status"
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
//
// 并发与生命周期：
//   - lifecycleMu 串行化 Connect 与 Disconnect 的整个执行周期，避免在
//     Connect 持有耗时 IO 的窗口里 Disconnect 看到 nil monitor 而提前
//     return，造成 Connect 写回的 monitor 永远无法释放（F4 修复点）。
//   - mu 仍然保护 monitor / eventsStarted 字段的并发读写。
type SocketClient struct {
	socketPath string
	timeout    time.Duration
	factory    monitor.Factory

	// lifecycleMu 串行化 Connect / Disconnect / WaitReady 全程；普通 IO
	// 操作（QueryStatus / SystemPowerdown / Quit / Events）仍只走 mu，
	// 不抢 lifecycleMu，避免长时间命令阻塞 Disconnect。
	lifecycleMu sync.Mutex

	mu            sync.Mutex
	monitor       monitor.Monitor
	eventsStarted bool
	eventsCancel  context.CancelFunc
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
//
// F4：lifecycleMu 串行化整个 Connect 流程，包括 factory.New + mon.Connect 的耗时
// IO；这样并发的 Disconnect 调用必须等当前 Connect 走完再观察 c.monitor 字段，
// 不会在 Connect 中段拿到 nil monitor 提前 return 而漏掉新建的 socket。
func (c *SocketClient) Connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	c.mu.Lock()
	if c.monitor != nil {
		c.mu.Unlock()
		return ErrAlreadyConnected
	}
	c.mu.Unlock()

	mon, err := c.factory.New("unix", c.socketPath, c.timeout)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed {
			// 清理失败的 monitor。这里用 Background 而不是 ctx：ctx 可能已取消，
			// 但 monitor 资源仍必须释放；底层实现负责自带兜底超时。
			_ = mon.Disconnect(context.Background())
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := mon.Connect(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.monitor != nil {
		// 在 lifecycleMu 串行化下不应出现这种情况，留作 defense-in-depth。
		return ErrAlreadyConnected
	}
	c.monitor = mon
	c.eventsStarted = false
	c.eventsCancel = nil
	installed = true
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
//
// F4 + F5：lifecycleMu 与 Connect 串行化，避免 Connect 中段被并发 Disconnect
// 跳过；ctx 透传给底层 monitor.Disconnect 让上层取消语义生效，不再依赖
// ctx.Err() 兜底。
func (c *SocketClient) Disconnect(ctx context.Context) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	c.mu.Lock()
	mon := c.monitor
	eventsCancel := c.eventsCancel
	c.monitor = nil
	c.eventsStarted = false
	c.eventsCancel = nil
	c.mu.Unlock()
	if eventsCancel != nil {
		eventsCancel()
	}

	if mon == nil {
		// 即使没有连接也保留 ctx 检查，让调用方能感知到自己传入的 ctx 已取消。
		return ctx.Err()
	}
	return mon.Disconnect(ctx)
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
	eventCtx, cancel := context.WithCancel(ctx)
	c.eventsStarted = true
	mon := c.monitor
	c.mu.Unlock()

	stream, err := mon.Events(eventCtx)
	if err != nil {
		cancel()
		c.mu.Lock()
		if c.monitor == mon {
			c.eventsStarted = false
			c.eventsCancel = nil
		}
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Lock()
	if c.monitor == mon {
		c.eventsCancel = cancel
	} else {
		cancel()
	}
	c.mu.Unlock()
	return convertEvents(eventCtx, qevents.Stream(eventCtx, stream, eventNameStrings(names)...)), nil
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

func convertEvents(ctx context.Context, stream <-chan qevents.Event) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		for event := range stream {
			converted := Event{Name: EventName(event.Name), Data: event.Data, Timestamp: event.Timestamp}
			select {
			case out <- converted:
			case <-ctx.Done():
				return
			}
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
