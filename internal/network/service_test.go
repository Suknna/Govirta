package network

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/network/netpool"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
)

func TestNetworkServiceCanceledContext(t *testing.T) {
	svc := NewNetworkService(netpool.NewService(
		nil, nil, nil, nil, // managers unused: canceled ctx short-circuits before delegation
	))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.EnsureNetwork(canceled, "net0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureNetwork canceled = %v, want context.Canceled", err)
	}
	if err := svc.DeleteNetwork(canceled, "net0", firewall.RuleRef{}, firewall.RuleRef{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteNetwork canceled = %v, want context.Canceled", err)
	}
}
