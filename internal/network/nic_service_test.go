package network

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/network/netpool"
)

func TestNICServiceCanceledContext(t *testing.T) {
	svc := NewNICService(netpool.NewService(
		nil, nil, nil, nil, // managers unused: canceled ctx short-circuits before delegation
	))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.EnsureNIC(canceled, "net0", "vm1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureNIC canceled = %v, want context.Canceled", err)
	}
	if err := svc.DeleteNIC(canceled, "net0", "vm1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteNIC canceled = %v, want context.Canceled", err)
	}
	if _, err := svc.GetNICStatus(canceled, "net0", "vm1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetNICStatus canceled = %v, want context.Canceled", err)
	}
}
