package v1alpha1

import (
	"errors"
	"testing"
)

func validNetworkSpec() NetworkSpec {
	return NetworkSpec{
		BridgeName:      "govirta0",
		Subnet:          "192.168.100.0/24",
		GatewayCIDR:     "192.168.100.1/24",
		DHCPRangeStart:  "192.168.100.10",
		DHCPRangeEnd:    "192.168.100.200",
		EgressInterface: "eth0",
		DNS:             []string{"8.8.8.8"},
		LeaseSeconds:    3600,
	}
}

func TestNetworkSpecValidate(t *testing.T) {
	if err := validNetworkSpec().Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *NetworkSpec)
	}{
		{"empty bridge", func(s *NetworkSpec) { s.BridgeName = "" }},
		{"empty egress", func(s *NetworkSpec) { s.EgressInterface = "" }},
		{"bad subnet", func(s *NetworkSpec) { s.Subnet = "192.168.100.0" }},
		{"bad gateway", func(s *NetworkSpec) { s.GatewayCIDR = "nope" }},
		{"empty range start", func(s *NetworkSpec) { s.DHCPRangeStart = "" }},
		{"bad dns", func(s *NetworkSpec) { s.DNS = []string{"not-an-ip"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validNetworkSpec()
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestNetworkStatusValidateAcceptsKnownPhase(t *testing.T) {
	status := NetworkStatus{Phase: NetworkPhaseReady}
	if err := status.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestNetworkStatusValidateRejectsUnknownPhase(t *testing.T) {
	status := NetworkStatus{Phase: NetworkPhase("bogus")}
	err := status.Validate()
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
	}
}
