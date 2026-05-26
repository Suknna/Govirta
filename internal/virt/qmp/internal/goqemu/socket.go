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

// Monitor represents the subset of go-qemu's QMP monitor used by Govirta.
type Monitor interface {
	Connect(ctx context.Context) error
	Disconnect() error
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
type SocketMonitor struct {
	Version      *Version
	Capabilities []string

	c net.Conn

	mu        sync.Mutex
	stream    <-chan streamResponse
	listeners *int32
	events    <-chan Event
}

// NewSocketMonitor configures a connection to the provided QMP socket.
func NewSocketMonitor(network string, address string, timeout time.Duration) (*SocketMonitor, error) {
	conn, err := net.DialTimeout(network, address, timeout)
	if err != nil {
		return nil, err
	}
	return &SocketMonitor{c: conn, listeners: new(int32)}, nil
}

// Connect completes the QMP greeting and capabilities handshake.
func (m *SocketMonitor) Connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	enc := json.NewEncoder(m.c)
	dec := json.NewDecoder(m.c)

	var greeting greeting
	if err := dec.Decode(&greeting); err != nil {
		return err
	}
	m.Version = &greeting.QMP.Version
	m.Capabilities = greeting.QMP.Capabilities

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := enc.Encode(Command{Execute: "qmp_capabilities"}); err != nil {
		return err
	}

	var response response
	if err := dec.Decode(&response); err != nil {
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

// Disconnect closes the monitor socket connection.
func (m *SocketMonitor) Disconnect() error {
	atomic.StoreInt32(m.listeners, 0)
	err := m.c.Close()
	if m.stream != nil {
		for range m.stream {
		}
	}
	return err
}

// Run executes a raw QMP command and returns the raw response.
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
			stream <- streamResponse{buf: buf}
			continue
		}
		if atomic.LoadInt32(m.listeners) == 0 {
			continue
		}
		events <- event
	}
	if err := scanner.Err(); err != nil {
		stream <- streamResponse{err: err}
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
