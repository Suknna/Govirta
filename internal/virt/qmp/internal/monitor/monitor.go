package monitor

import (
	"context"
	"time"
)

// Monitor 是 QMP 传输边界，被 status/power/events 等 command 包消费。
//
// Disconnect 接受 ctx 是为了让上层 SocketClient.Disconnect 能传播取消语义；
// 实现可以选择同步等待底层 IO 关闭并返回 ctx.Err() 作为兜底超时。
type Monitor interface {
	Connect(ctx context.Context) error
	Disconnect(ctx context.Context) error
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
