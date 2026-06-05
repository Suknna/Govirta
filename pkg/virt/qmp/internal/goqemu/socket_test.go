package goqemu

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/suknna/govirta/pkg/virt/qmp/internal/protocol"
)

func TestSocketMonitorConnectRunAndEvents(t *testing.T) {
	testDir := filepath.Join("..", "..", "..", "..", "..", ".tmp", "qmp-goqemu-test")
	if err := os.MkdirAll(testDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testDir) })
	socketPath := filepath.Join(testDir, "qmp.sock")
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	serverErr := make(chan error, 1)
	go serveQMP(t, listener, serverErr)

	monitor, err := NewSocketMonitor("unix", socketPath, time.Second)
	if err != nil {
		t.Fatalf("NewSocketMonitor() error = %v", err)
	}
	if err := monitor.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	events, err := monitor.Events(t.Context())
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}

	response, err := monitor.Run(context.Background(), []byte(`{"execute":"query-status"}`))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(response) != `{"return":{"running":true,"status":"running"}}` {
		t.Fatalf("response = %s", response)
	}

	event := <-events
	if event.Event != "SHUTDOWN" {
		t.Fatalf("event = %q, want SHUTDOWN", event.Event)
	}

	if err := monitor.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestSocketMonitorRunReturnsWhenContextCanceled(t *testing.T) {
	testDir := filepath.Join("..", "..", "..", "..", "..", ".tmp", "qmp-goqemu-cancel-test")
	if err := os.MkdirAll(testDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testDir) })
	socketPath := filepath.Join(testDir, "qmp.sock")
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	go serveQMPWithoutCommandResponse(t, listener)

	monitor, err := NewSocketMonitor("unix", socketPath, time.Second)
	if err != nil {
		t.Fatalf("NewSocketMonitor() error = %v", err)
	}
	if err := monitor.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer monitor.Disconnect(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := monitor.Run(ctx, []byte(`{"execute":"query-status"}`))
		done <- err
	}()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Run() did not return after context cancellation")
	}
}

func TestSocketMonitorRunReturnsTypedResponseError(t *testing.T) {
	testDir := filepath.Join("..", "..", "..", "..", "..", ".tmp", "qmp-goqemu-error-test")
	if err := os.MkdirAll(testDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testDir) })
	socketPath := filepath.Join(testDir, "qmp.sock")
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	go serveQMPCommandError(t, listener)

	monitor, err := NewSocketMonitor("unix", socketPath, time.Second)
	if err != nil {
		t.Fatalf("NewSocketMonitor() error = %v", err)
	}
	if err := monitor.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer monitor.Disconnect(context.Background())

	_, err = monitor.Run(context.Background(), []byte(`{"execute":"query-status"}`))
	var responseErr *protocol.ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("Run() error = %v, want ResponseError", err)
	}
	if responseErr.Class != "GenericError" || responseErr.Description != "bad command" {
		t.Fatalf("ResponseError = %+v, want class and description", responseErr)
	}
}

func TestWriteWithContextReturnsContextCanceledWhenWriteBlocks(t *testing.T) {
	conn := newBlockingWriteConn()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- writeWithContext(ctx, conn, []byte("command\n"))
	}()

	<-conn.writeStarted
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("writeWithContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("writeWithContext() did not return after context cancellation")
	}

	select {
	case <-conn.resetDeadline:
	case <-time.After(time.Second):
		t.Fatalf("writeWithContext() did not reset write deadline")
	}
}

func TestSocketMonitorRunClosesConnectionWhenCommandWriteCanceled(t *testing.T) {
	conn := newBlockingWriteConn()
	monitor := &SocketMonitor{
		c:         conn,
		stream:    make(chan streamResponse),
		listeners: new(int32),
		done:      make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := monitor.Run(ctx, []byte(`{"execute":"query-status"}`))
		done <- err
	}()

	<-conn.writeStarted
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Run() did not return after command write cancellation")
	}

	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		t.Fatalf("Run() did not close conn after command write cancellation")
	}

	select {
	case <-monitor.done:
	case <-time.After(time.Second):
		t.Fatalf("Run() did not close done after command write cancellation")
	}
}

type blockingWriteConn struct {
	writeStarted  chan struct{}
	writeDeadline chan struct{}
	resetDeadline chan struct{}
	closed        chan struct{}

	startOnce         sync.Once
	writeDeadlineOnce sync.Once
	resetDeadlineOnce sync.Once
	closeOnce         sync.Once
}

func newBlockingWriteConn() *blockingWriteConn {
	return &blockingWriteConn{
		writeStarted:  make(chan struct{}),
		writeDeadline: make(chan struct{}),
		resetDeadline: make(chan struct{}),
		closed:        make(chan struct{}),
	}
}

func (c *blockingWriteConn) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (c *blockingWriteConn) Write([]byte) (int, error) {
	c.startOnce.Do(func() { close(c.writeStarted) })
	<-c.writeDeadline
	return 0, os.ErrDeadlineExceeded
}

func (c *blockingWriteConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *blockingWriteConn) LocalAddr() net.Addr {
	return nil
}

func (c *blockingWriteConn) RemoteAddr() net.Addr {
	return nil
}

func (c *blockingWriteConn) SetDeadline(time.Time) error {
	return nil
}

func (c *blockingWriteConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *blockingWriteConn) SetWriteDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		c.resetDeadlineOnce.Do(func() { close(c.resetDeadline) })
		return nil
	}
	c.writeDeadlineOnce.Do(func() { close(c.writeDeadline) })
	return nil
}

func serveQMP(t *testing.T, listener net.Listener, serverErr chan<- error) {
	t.Helper()
	conn, err := listener.Accept()
	if err != nil {
		serverErr <- err
		return
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(`{"QMP":{"version":{"qemu":{"major":6,"minor":2,"micro":0},"package":""},"capabilities":[]}}` + "\n")); err != nil {
		serverErr <- err
		return
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		serverErr <- err
		return
	}
	var handshake Command
	if err := json.Unmarshal(line, &handshake); err != nil {
		serverErr <- err
		return
	}
	if handshake.Execute != "qmp_capabilities" {
		serverErr <- nil
		return
	}
	if _, err := conn.Write([]byte(`{"return":{}}` + "\n")); err != nil {
		serverErr <- err
		return
	}

	line, err = reader.ReadBytes('\n')
	if err != nil {
		serverErr <- err
		return
	}
	var command Command
	if err := json.Unmarshal(line, &command); err != nil {
		serverErr <- err
		return
	}
	if command.Execute != "query-status" {
		serverErr <- nil
		return
	}
	if _, err := conn.Write([]byte(`{"return":{"running":true,"status":"running"}}` + "\n")); err != nil {
		serverErr <- err
		return
	}
	if _, err := conn.Write([]byte(`{"event":"SHUTDOWN","timestamp":{"seconds":1,"microseconds":2}}` + "\n")); err != nil {
		serverErr <- err
		return
	}
	serverErr <- nil
}

func serveQMPWithoutCommandResponse(t *testing.T, listener net.Listener) {
	t.Helper()
	conn, err := listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(`{"QMP":{"version":{"qemu":{"major":6,"minor":2,"micro":0},"package":""},"capabilities":[]}}` + "\n"))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var handshake Command
	if err := json.Unmarshal(line, &handshake); err != nil {
		return
	}
	if handshake.Execute != "qmp_capabilities" {
		return
	}
	_, _ = conn.Write([]byte(`{"return":{}}` + "\n"))
	_, _ = reader.ReadBytes('\n')
	<-time.After(2 * time.Second)
}

func serveQMPCommandError(t *testing.T, listener net.Listener) {
	t.Helper()
	conn, err := listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(`{"QMP":{"version":{"qemu":{"major":6,"minor":2,"micro":0},"package":""},"capabilities":[]}}` + "\n"))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var handshake Command
	if err := json.Unmarshal(line, &handshake); err != nil || handshake.Execute != "qmp_capabilities" {
		return
	}
	_, _ = conn.Write([]byte(`{"return":{}}` + "\n"))
	_, _ = reader.ReadBytes('\n')
	_, _ = conn.Write([]byte(`{"error":{"class":"GenericError","desc":"bad command"}}` + "\n"))
}
