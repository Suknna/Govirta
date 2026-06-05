//go:build linux

package linux

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/suknna/govirta/pkg/hostnet/route"
	"github.com/suknna/govirta/pkg/hostnet/route/routeerr"
)

type fakeForwardingReader struct {
	value string
	err   error
}

func (f fakeForwardingReader) ReadIPv4Forwarding(context.Context) (string, error) {
	if f.err != nil {
		return "", f.err
	}

	return f.value, nil
}

func TestGetIPv4ForwardingMapsEnabled(t *testing.T) {
	manager := &Manager{forwarding: fakeForwardingReader{value: "1\n"}}

	info, err := manager.GetIPv4Forwarding(context.Background())
	if err != nil {
		t.Fatalf("GetIPv4Forwarding error = %v, want nil", err)
	}
	if info.State != route.IPv4ForwardingEnabled {
		t.Fatalf("state = %q, want %q", info.State, route.IPv4ForwardingEnabled)
	}
	if info.Path != ipv4ForwardingPath {
		t.Fatalf("path = %q, want %q", info.Path, ipv4ForwardingPath)
	}
}

func TestGetIPv4ForwardingMapsDisabled(t *testing.T) {
	manager := &Manager{forwarding: fakeForwardingReader{value: "0\n"}}

	info, err := manager.GetIPv4Forwarding(context.Background())
	if err != nil {
		t.Fatalf("GetIPv4Forwarding error = %v, want nil", err)
	}
	if info.State != route.IPv4ForwardingDisabled {
		t.Fatalf("state = %q, want %q", info.State, route.IPv4ForwardingDisabled)
	}
}

func TestGetIPv4ForwardingRejectsInvalidObservedState(t *testing.T) {
	manager := &Manager{forwarding: fakeForwardingReader{value: "2\n"}}

	_, err := manager.GetIPv4Forwarding(context.Background())
	if !errors.Is(err, routeerr.ErrInvalidObservedState) {
		t.Fatalf("GetIPv4Forwarding error = %v, want ErrInvalidObservedState", err)
	}
}

func TestGetIPv4ForwardingMapsMissingProcPathToUnsupported(t *testing.T) {
	manager := &Manager{forwarding: fakeForwardingReader{err: os.ErrNotExist}}

	_, err := manager.GetIPv4Forwarding(context.Background())
	if !errors.Is(err, routeerr.ErrUnsupported) {
		t.Fatalf("GetIPv4Forwarding error = %v, want ErrUnsupported", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("GetIPv4Forwarding error = %v, want preserved os.ErrNotExist", err)
	}
}

func TestGetIPv4ForwardingReturnsCanceledContextDirectly(t *testing.T) {
	manager := &Manager{forwarding: fakeForwardingReader{value: "1\n"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := manager.GetIPv4Forwarding(ctx)
	if err != context.Canceled {
		t.Fatalf("GetIPv4Forwarding error = %v, want direct context.Canceled", err)
	}
}

func TestCheckIPv4ForwardingReturnsCanceledContextDirectly(t *testing.T) {
	manager := &Manager{forwarding: fakeForwardingReader{value: "1\n"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := manager.CheckIPv4Forwarding(ctx, route.IPv4ForwardingEnabled)
	if err != context.Canceled {
		t.Fatalf("CheckIPv4Forwarding error = %v, want direct context.Canceled", err)
	}
}

func TestCheckIPv4ForwardingReportsNotReady(t *testing.T) {
	manager := &Manager{forwarding: fakeForwardingReader{value: "0\n"}}

	info, err := manager.CheckIPv4Forwarding(context.Background(), route.IPv4ForwardingEnabled)
	if !errors.Is(err, routeerr.ErrNotReady) {
		t.Fatalf("CheckIPv4Forwarding error = %v, want ErrNotReady", err)
	}
	if info.State != route.IPv4ForwardingDisabled {
		t.Fatalf("observed state = %q, want %q", info.State, route.IPv4ForwardingDisabled)
	}
}
