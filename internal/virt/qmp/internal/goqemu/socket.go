// Copyright 2016 The go-qemu Authors.
// Copyright 2026 suknna.
//
// Licensed under the Apache License, Version 2.0. See the repository LICENSE
// file for license terms.
//
// Package goqemu contains the direct socket QMP monitor adapted from
// github.com/digitalocean/go-qemu/qmp at v0.0.0-20250212194115-ee9b0668d242.
// Govirta vendors only the direct socket monitor subset because importing the
// upstream qmp package also compiles its libvirt RPC source file.
package goqemu

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suknna/govirta/internal/virt/qmp/internal/protocol"
)

// Monitor 描述 Govirta 用到的 QMP 监视器子集。Disconnect 接受 ctx，让
// 上层在底层 IO 卡住时仍可凭兜底超时返回。
type Monitor interface {
	Connect(ctx context.Context) error
	Disconnect(ctx context.Context) error
	Run(ctx context.Context, command []byte) ([]byte, error)
	Events(ctx context.Context) (<-chan Event, error)
}

// Command represents a QMP command.
type Command struct {
	Execute string `json:"execute"`
	Args    any    `json:"arguments,omitempty"`
}

// Event represents a QEMU QMP event.
type Event struct {
	Event string         `json:"event"`
	Data  map[string]any `json:"data"`

	Timestamp struct {
		Seconds      int64 `json:"seconds"`
		Microseconds int64 `json:"microseconds"`
	} `json:"timestamp"`
}

// Version is the QEMU version structure returned during QMP greeting.
type Version struct {
	Package string `json:"package"`
	QEMU    struct {
		Major int `json:"major"`
		Micro int `json:"micro"`
		Minor int `json:"minor"`
	} `json:"qemu"`
}

func (v Version) String() string {
	q := v.QEMU
	return fmt.Sprintf("%d.%d.%d", q.Major, q.Minor, q.Micro)
}

// SocketMonitor speaks directly to a QEMU QMP socket.
//
// 并发与生命周期：
//   - Run 通过 mu 串行化（QMP 协议要求请求/响应一一对应）。
//   - listen goroutine 持续从 conn 读取，按 event/response 分派到 events / stream
//     channel；任何 channel send 都监听 done，避免 buffer 满 + 消费者停止读时死锁。
//   - Disconnect 关闭 done + conn，listen 因 scanner.Scan() 返回 false 自然退出，
//     然后通过 closeOnce 保证幂等。
type SocketMonitor struct {
	Version      *Version
	Capabilities []string

	c net.Conn

	mu        sync.Mutex
	stream    chan streamResponse
	listeners *int32
	events    chan Event

	closeOnce sync.Once
	done      chan struct{}
}

// NewSocketMonitor configures a connection to the provided QMP socket.
func NewSocketMonitor(network string, address string, timeout time.Duration) (*SocketMonitor, error) {
	conn, err := net.DialTimeout(network, address, timeout)
	if err != nil {
		return nil, err
	}
	return &SocketMonitor{c: conn, listeners: new(int32), done: make(chan struct{})}, nil
}

// Connect 完成 QMP greeting + capabilities 握手。
//
// F3：Decode 是无 deadline 的阻塞读，调用方 ctx 取消时必须有兜底。
// 这里启动一个 watchdog goroutine：ctx.Done 时调用 SetReadDeadline(now)
// 强制 Decode 因 i/o timeout 返回；Connect 结束（无论成败）通过 connectDone
// 通知 watchdog 退出，然后用 WaitGroup 同步等待 watchdog 完全离开后再
// 重置 read deadline，避免 watchdog 与后续 listen goroutine 竞争 conn 状态。
func (m *SocketMonitor) Connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	connectDone := make(chan struct{})
	var watchdogWG sync.WaitGroup
	watchdogWG.Add(1)
	go func() {
		defer watchdogWG.Done()
		select {
		case <-ctx.Done():
			_ = m.c.SetReadDeadline(time.Now())
		case <-connectDone:
		}
	}()
	defer func() {
		close(connectDone)
		watchdogWG.Wait()
		// 重置 read deadline，让后续 listen goroutine 走正常无限期读取。
		_ = m.c.SetReadDeadline(time.Time{})
	}()

	enc := json.NewEncoder(m.c)
	dec := json.NewDecoder(m.c)

	var greeting greeting
	if err := dec.Decode(&greeting); err != nil {
		// watchdog 触发的 timeout 应转译为 ctx.Err()，让调用方拿到稳定的
		// context.Canceled / DeadlineExceeded。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	m.Version = &greeting.QMP.Version
	m.Capabilities = greeting.QMP.Capabilities

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := enc.Encode(Command{Execute: "qmp_capabilities"}); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}

	var response response
	if err := dec.Decode(&response); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	if err := response.Err(); err != nil {
		return err
	}

	events := make(chan Event, 16)
	stream := make(chan streamResponse)
	go m.listen(m.c, events, stream)
	m.events = events
	m.stream = stream
	return nil
}

// Disconnect 关闭 socket 并解阻塞 listen goroutine。
//
// F1：此前 Disconnect 通过 `for range m.stream` 同步等待 listen 退出，但
// listen 在 events buffer 满 + 消费者停止读取时卡在 channel send，
// stream 永不 close，造成死锁。现在 closeOnce 保证 done 仅 close 一次：
//   - close(done) 解阻塞 listen 中所有 select { ch <- v: case <-done: return }
//   - m.c.Close() 让 scanner.Scan() 返回 false，listen 自然退出
//
// 不再需要 drain stream，因为 listen 退出时会通过 defer close(stream)
// 自行清理；调用方若并发地 Run，则会从已关闭的 stream 拿到 io.EOF。
func (m *SocketMonitor) Disconnect(ctx context.Context) error {
	m.closeOnce.Do(func() {
		close(m.done)
	})
	atomic.StoreInt32(m.listeners, 0)
	err := m.c.Close()
	// 如果调用方传了已超时/取消的 ctx，仍优先返回 ctx.Err 让上层感知；
	// 关 conn 已尽力释放资源，业务上 Disconnect 视为最终态。
	if ctxErr := ctx.Err(); ctxErr != nil && err == nil {
		return ctxErr
	}
	return err
}

// Run 执行一条 QMP 命令并返回原始响应。
//
// F2：ctx 取消后 socket 进入不可复用状态。早期实现仅 return ctx.Err()，
// 但 QEMU 仍会异步推送一条响应到 stream，下次 Run 写入新命令后会读到
// 上次取消命令的旧响应——QMP 协议在本封装中没有 id 字段，无法靠 id 路由
// 响应，错位无法被检测。安全做法是关闭 conn 让 socket 不可复用，调用方
// 必须重新 Connect。
func (m *SocketMonitor) Run(ctx context.Context, command []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stream == nil {
		return nil, errors.New("qmp monitor is not connected")
	}

	command = append(append([]byte(nil), command...), '\n')
	if _, err := m.c.Write(command); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		// 关闭 conn → scanner.Scan() 返回 false → listen 退出 → stream 关闭。
		// 同时 closeOnce 保证 done 也 close，与 Disconnect 路径汇合。
		m.closeOnce.Do(func() {
			close(m.done)
		})
		_ = m.c.Close()
		return nil, ctx.Err()
	case result, ok := <-m.stream:
		if !ok {
			return nil, io.EOF
		}
		if result.err != nil {
			return nil, result.err
		}

		var response response
		if err := json.Unmarshal(result.buf, &response); err != nil {
			return nil, err
		}
		if err := response.Err(); err != nil {
			return nil, err
		}
		return result.buf, nil
	}
}

// Events streams asynchronous QMP events.
func (m *SocketMonitor) Events(ctx context.Context) (<-chan Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !atomic.CompareAndSwapInt32(m.listeners, 0, 1) {
		return nil, errors.New("qmp events already started")
	}
	return m.events, nil
}

// listen 串行读取 conn，把消息按 event/response 分派到对应 channel。
// 所有 channel send 都监听 done 信道：F1 的核心修复点——避免 buffer 满 +
// 消费者停止读时卡死，让 Disconnect/Run 关闭 done 后 listen 能立即退出。
func (m *SocketMonitor) listen(reader io.Reader, events chan<- Event, stream chan<- streamResponse) {
	defer close(events)
	defer close(stream)

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		buf := append([]byte(nil), scanner.Bytes()...)
		var event Event
		if err := json.Unmarshal(buf, &event); err != nil {
			continue
		}
		if event.Event == "" {
			select {
			case stream <- streamResponse{buf: buf}:
			case <-m.done:
				return
			}
			continue
		}
		if atomic.LoadInt32(m.listeners) == 0 {
			continue
		}
		select {
		case events <- event:
		case <-m.done:
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case stream <- streamResponse{err: err}:
		case <-m.done:
		}
	}
}

type greeting struct {
	QMP struct {
		Capabilities []string `json:"capabilities"`
		Version      Version  `json:"version"`
	} `json:"QMP"`
}

type response struct {
	Return any `json:"return,omitempty"`
	Error  struct {
		Class       string `json:"class"`
		Description string `json:"desc"`
	} `json:"error,omitempty"`
}

func (r response) Err() error {
	if r.Error.Description == "" {
		return nil
	}
	return &protocol.ResponseError{Class: r.Error.Class, Description: r.Error.Description}
}

type streamResponse struct {
	buf []byte
	err error
}
