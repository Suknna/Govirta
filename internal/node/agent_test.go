package node

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/node/controller"
)

// fakeEventSource is a controller.EventSource that yields no events and closes
// its channel only when the watch ctx is cancelled, so a controller loop blocks
// on it until the manager shuts down. It records every Watch call so a test can
// assert the manager started one loop per controller.
type fakeEventSource struct {
	mu    sync.Mutex
	kinds []string
}

func (s *fakeEventSource) Watch(ctx context.Context, kind, startRevision string) (<-chan controller.Event, error) {
	s.mu.Lock()
	s.kinds = append(s.kinds, kind)
	s.mu.Unlock()

	out := make(chan controller.Event)
	go func() {
		<-ctx.Done()
		close(out)
	}()
	return out, nil
}

func (s *fakeEventSource) watchedKinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.kinds...)
}

// fakeController is a no-op controller.Controller for a named kind.
type fakeController struct{ kind string }

func (c *fakeController) Kind() string { return c.kind }

func (c *fakeController) Reconcile(context.Context, controller.Event) (controller.ReconcileResult, error) {
	return controller.Done(), nil
}

// TestAgentRunStartsManagerAndStopsOnCancel proves the assembled agent runs its
// controller manager (one watch loop per controller) and returns ctx.Err() once
// the context is cancelled, with every loop torn down.
func TestAgentRunStartsManagerAndStopsOnCancel(t *testing.T) {
	source := &fakeEventSource{}
	list := []controller.Controller{
		&fakeController{kind: "StoragePool"},
		&fakeController{kind: "Image"},
		&fakeController{kind: "Volume"},
		&fakeController{kind: "Network"},
		&fakeController{kind: "NIC"},
		&fakeController{kind: "VM"},
	}
	agent := newAgentWithDeps(source, list)

	logger := zerolog.New(io.Discard)
	ctx, cancel := context.WithCancel(logger.WithContext(context.Background()))

	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()

	// The manager starts one watch loop per controller; wait until all six have
	// called Watch, proving the agent wired every controller into the manager.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(source.watchedKinds()) == len(list) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d controllers started a watch loop", len(source.watchedKinds()), len(list))
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run() did not return after context cancel")
	}
}

// TestNewAgentAssemblesControllerManager proves the production constructor wires
// a runnable agent from a Config without touching the master or real devices
// (the cross-platform host managers are no-ops off Linux; on Linux the nftables
// handle is opened, which is why NewAgent can error). The agent is only built
// here, not run, so no network or kernel I/O happens.
func TestNewAgentAssemblesControllerManager(t *testing.T) {
	agent, err := NewAgent(Config{
		MasterURL:      "http://127.0.0.1:0",
		NodeName:       "node-test",
		RuntimeRoot:    t.TempDir(),
		ImageCacheRoot: t.TempDir(),
		QEMUBinary:     "/usr/bin/qemu-system-aarch64",
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v, want nil", err)
	}
	if agent == nil || agent.manager == nil {
		t.Fatalf("NewAgent() returned an agent with no manager")
	}
	if !slices.Contains(agent.controllerKinds, "Task") {
		t.Fatalf("controller kinds = %v, want Task registered", agent.controllerKinds)
	}
	if slices.Contains(agent.controllerKinds, "Image") {
		t.Fatalf("controller kinds = %v, want old Image controller not registered", agent.controllerKinds)
	}
}
