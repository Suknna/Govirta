package goqemu

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	if err := monitor.Connect(); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	events, err := monitor.Events(t.Context())
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}

	response, err := monitor.Run([]byte(`{"execute":"query-status"}`))
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

	if err := monitor.Disconnect(); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
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
