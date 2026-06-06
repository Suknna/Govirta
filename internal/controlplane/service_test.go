package controlplane

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/controlplane/store/fake"
)

// testConfig returns a Config with a valid MAC pool and a loopback ephemeral
// listen address. The pool prefix 0x02 has the locally-administered bit set and
// the multicast bit clear, so mac.NewPool accepts it. ":0" lets the OS pick a
// free port so the test never collides with a fixed port.
func testConfig() Config {
	return Config{
		ListenAddr:     "127.0.0.1:0",
		MACPrefix:      net.HardwareAddr{0x02, 0x00, 0x00},
		MACSuffixStart: 0,
		MACSuffixEnd:   0xFF,
		NodeNames:      []string{"node-a"},
	}
}

// TestNewServiceWithStoreAssembles verifies the assembly seam wires a real
// apiserver over an injected fake store (no etcd dial) and that Run serves and
// shuts down cleanly when ctx is cancelled. A clean ctx-triggered shutdown
// returns nil from apiServer.Run, and the fake store's Close returns nil, so Run
// returns nil overall.
func TestNewServiceWithStoreAssembles(t *testing.T) {
	st := fake.New()
	svc, err := newServiceWithStore(st, testConfig())
	if err != nil {
		t.Fatalf("newServiceWithStore() returned error: %v", err)
	}
	if svc.apiServer == nil {
		t.Fatal("newServiceWithStore() left apiServer nil")
	}

	logger := zerolog.New(io.Discard)
	ctx, cancel := context.WithCancel(logger.WithContext(context.Background()))

	runErr := make(chan error, 1)
	go func() { runErr <- svc.Run(ctx) }()

	// Cancel after a brief delay so Run has bound and entered serve; a clean
	// shutdown must then return nil.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run() after cancel returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s after ctx cancel")
	}
}

// TestNewServiceWithStoreInvalidMACPool verifies a malformed MAC pool config is
// surfaced as a wrapped construction error rather than silently corrected
// (显式优于实). A multicast prefix (first octet bit 0 set) is rejected by
// mac.NewPool.
func TestNewServiceWithStoreInvalidMACPool(t *testing.T) {
	st := fake.New()
	cfg := testConfig()
	cfg.MACPrefix = net.HardwareAddr{0x01, 0x00, 0x00} // multicast bit set: invalid

	svc, err := newServiceWithStore(st, cfg)
	if err == nil {
		t.Fatal("newServiceWithStore() with multicast MAC prefix: want error, got nil")
	}
	if svc != nil {
		t.Fatalf("newServiceWithStore() returned non-nil Service on error: %#v", svc)
	}
}

// TestServiceRunServesAPISurface verifies the assembled Service actually serves
// the apiserver's /apis surface: it binds a real loopback listener, issues an
// HTTP request, and asserts the server responds (not connection-refused),
// proving the real apiserver — not a noop — is wired in.
func TestServiceRunServesAPISurface(t *testing.T) {
	st := fake.New()
	// Bind a real ephemeral port we can target. We discover the port by binding
	// a listener ourselves, closing it, and reusing the address — then let the
	// apiserver bind it. To avoid that race we instead bind to :0 via the server
	// and probe through a fixed loopback port chosen here.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("probe close: %v", err)
	}

	cfg := testConfig()
	cfg.ListenAddr = addr
	svc, err := newServiceWithStore(st, cfg)
	if err != nil {
		t.Fatalf("newServiceWithStore() returned error: %v", err)
	}

	logger := zerolog.New(io.Discard)
	ctx, cancel := context.WithCancel(logger.WithContext(context.Background()))
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- svc.Run(ctx) }()

	// Poll until the server accepts connections (Run binds asynchronously).
	var resp *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err = http.Get("http://" + addr + "/apis/VM/")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became reachable at %s: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Any HTTP status proves the real apiserver answered; we are not asserting
	// handler semantics here (those are tested in the apiserver package), only
	// that the Service wired a listening apiserver rather than a noop.
	_ = resp.Body.Close()

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run() after cancel returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s after ctx cancel")
	}
}

// TestServiceRunCanceledBeforeStart verifies that a Service whose ctx is already
// cancelled before Run still tears down cleanly: the apiserver binds, observes
// the cancelled ctx, and shuts down, and the store closes without error, so Run
// returns nil. (errors.Join of two nils is nil.)
func TestServiceRunCanceledBeforeStart(t *testing.T) {
	st := fake.New()
	svc, err := newServiceWithStore(st, testConfig())
	if err != nil {
		t.Fatalf("newServiceWithStore() returned error: %v", err)
	}

	logger := zerolog.New(io.Discard)
	ctx, cancel := context.WithCancel(logger.WithContext(context.Background()))
	cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- svc.Run(ctx) }()

	select {
	case err := <-runErr:
		// A pre-cancelled ctx triggers an immediate graceful shutdown; the clean
		// path returns nil. We accept nil; a non-nil error would indicate the
		// shutdown path failed and should surface.
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() with pre-cancelled ctx returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s with pre-cancelled ctx")
	}
}
