package netpool

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/suknna/govirta/internal/network/networker"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
)

func TestRegisterNetworkRejectsEmptyName(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	def := sampleNetwork()
	def.Name = ""

	err := svc.RegisterNetwork(def)
	if !errors.Is(err, networker.ErrInvalidRequest) {
		t.Fatalf("RegisterNetwork(empty name) error = %v, want ErrInvalidRequest", err)
	}
}

func TestRegisterNetworkRejectsGatewayOutsideSubnet(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	def := sampleNetwork()
	def.GatewayCIDR = netip.MustParsePrefix("10.0.0.1/24")

	err := svc.RegisterNetwork(def)
	if !errors.Is(err, networker.ErrInvalidRequest) {
		t.Fatalf("RegisterNetwork(gateway outside subnet) error = %v, want ErrInvalidRequest", err)
	}
}

func TestRegisterNetworkRejectsDuplicate(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	def := sampleNetwork()

	if err := svc.RegisterNetwork(def); err != nil {
		t.Fatalf("RegisterNetwork(first) error = %v, want nil", err)
	}
	err := svc.RegisterNetwork(def)
	if !errors.Is(err, networker.ErrAlreadyExists) {
		t.Fatalf("RegisterNetwork(duplicate) error = %v, want ErrAlreadyExists", err)
	}
}

func TestRegisterNICRejectsUnknownNetwork(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()

	err := svc.RegisterNIC(sampleNIC())
	if !errors.Is(err, networker.ErrNotFound) {
		t.Fatalf("RegisterNIC(unknown network) error = %v, want ErrNotFound", err)
	}
}

func TestRegisterNICRejectsIPOutsidePool(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}

	nic := sampleNIC()
	// 192.168.100.250 is inside the subnet but above the pool end (.200).
	nic.IP = netip.MustParseAddr("192.168.100.250")

	err := svc.RegisterNIC(nic)
	if !errors.Is(err, networker.ErrInvalidRequest) {
		t.Fatalf("RegisterNIC(IP above pool) error = %v, want ErrInvalidRequest", err)
	}
}

func TestRegisterNICRejectsDuplicateIP(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC(first) error = %v, want nil", err)
	}

	// Second NIC under a different VM/TAP/MAC but the same IP must conflict.
	nic2 := sampleNIC()
	nic2.VMID = "vm2"
	nic2.TapName = "tap-vm2"
	nic2.MAC = net.HardwareAddr{0x52, 0x54, 0x00, 0xaa, 0xbb, 0xcc}

	err := svc.RegisterNIC(nic2)
	if !errors.Is(err, networker.ErrConflict) {
		t.Fatalf("RegisterNIC(duplicate IP) error = %v, want ErrConflict", err)
	}
}

func TestGetNetworkReturnsClone(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}

	got, err := svc.GetNetwork("net0")
	if err != nil {
		t.Fatalf("GetNetwork error = %v, want nil", err)
	}
	if len(got.BridgeMAC) == 0 {
		t.Fatalf("GetNetwork returned empty BridgeMAC")
	}

	// Mutating the returned clone must not affect the stored definition.
	got.BridgeMAC[0] = 0xff

	reGot, err := svc.GetNetwork("net0")
	if err != nil {
		t.Fatalf("GetNetwork(second) error = %v, want nil", err)
	}
	if reGot.BridgeMAC[0] == 0xff {
		t.Fatalf("stored BridgeMAC mutated through returned clone: %v", reGot.BridgeMAC)
	}
	if reGot.BridgeMAC[0] != 0x02 {
		t.Fatalf("stored BridgeMAC[0] = %#x, want 0x02", reGot.BridgeMAC[0])
	}
}

func TestDeleteNetworkRejectsWhenNICsPresent(t *testing.T) {
	svc, _, _, _, _, _ := newTestService()
	if err := svc.RegisterNetwork(sampleNetwork()); err != nil {
		t.Fatalf("RegisterNetwork error = %v, want nil", err)
	}
	if err := svc.RegisterNIC(sampleNIC()); err != nil {
		t.Fatalf("RegisterNIC error = %v, want nil", err)
	}

	err := svc.DeleteNetwork(context.Background(), "net0", firewall.RuleRef{}, firewall.RuleRef{})
	if !errors.Is(err, networker.ErrConflict) {
		t.Fatalf("DeleteNetwork(NICs present) error = %v, want ErrConflict", err)
	}
}
