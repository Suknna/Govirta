// Package v1alpha1 defines the Network API object. The spec exposes only
// high-level semantic intent; the node controller deterministically derives the
// underlying nftables table/chain/priority identities (spec 决策: Spec 不泄漏内核身份).
package v1alpha1

import (
	"errors"
	"fmt"
	"net/netip"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// NetworkPhase is the observed lifecycle phase written by the node controller.
type NetworkPhase string

const (
	// NetworkPhasePending means the object exists but the network is not ensured.
	NetworkPhasePending NetworkPhase = "pending"
	// NetworkPhaseReady means the bridge + rules + DHCP are ensured.
	NetworkPhaseReady NetworkPhase = "ready"
	// NetworkPhaseFailed means ensuring failed.
	NetworkPhaseFailed NetworkPhase = "failed"
)

// Valid reports whether p is a known network phase.
func (p NetworkPhase) Valid() bool {
	switch p {
	case NetworkPhasePending, NetworkPhaseReady, NetworkPhaseFailed:
		return true
	default:
		return false
	}
}

// ErrInvalidSpec is returned when a NetworkSpec is not internally valid.
var ErrInvalidSpec = errors.New("network: invalid spec")

// ErrInvalidStatus is returned when a NetworkStatus is not internally valid.
var ErrInvalidStatus = errors.New("network: invalid status")

// NetworkSpec is the desired state of a network segment (semantic intent only).
// Addresses are strings on the wire; Validate parses them with net/netip.
type NetworkSpec struct {
	BridgeName      string   `json:"bridgeName"`
	Subnet          string   `json:"subnet"`      // CIDR, e.g. 192.168.100.0/24
	GatewayCIDR     string   `json:"gatewayCIDR"` // CIDR, e.g. 192.168.100.1/24
	DHCPRangeStart  string   `json:"dhcpRangeStart"`
	DHCPRangeEnd    string   `json:"dhcpRangeEnd"`
	EgressInterface string   `json:"egressInterface"`
	DNS             []string `json:"dns,omitempty"`
	Router          []string `json:"router,omitempty"`
	LeaseSeconds    int      `json:"leaseSeconds,omitempty"`
}

// Validate reports whether the spec carries explicit, well-formed fields.
func (s NetworkSpec) Validate() error {
	if s.BridgeName == "" {
		return fmt.Errorf("%w: bridgeName is required", ErrInvalidSpec)
	}
	if s.EgressInterface == "" {
		return fmt.Errorf("%w: egressInterface is required", ErrInvalidSpec)
	}
	if _, err := netip.ParsePrefix(s.Subnet); err != nil {
		return fmt.Errorf("%w: subnet %q: %v", ErrInvalidSpec, s.Subnet, err)
	}
	if _, err := netip.ParsePrefix(s.GatewayCIDR); err != nil {
		return fmt.Errorf("%w: gatewayCIDR %q: %v", ErrInvalidSpec, s.GatewayCIDR, err)
	}
	if err := requireAddr(s.DHCPRangeStart, "dhcpRangeStart"); err != nil {
		return err
	}
	if err := requireAddr(s.DHCPRangeEnd, "dhcpRangeEnd"); err != nil {
		return err
	}
	for _, d := range s.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			return fmt.Errorf("%w: dns %q: %v", ErrInvalidSpec, d, err)
		}
	}
	for _, r := range s.Router {
		if _, err := netip.ParseAddr(r); err != nil {
			return fmt.Errorf("%w: router %q: %v", ErrInvalidSpec, r, err)
		}
	}
	return nil
}

func requireAddr(v, field string) error {
	if v == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidSpec, field)
	}
	if _, err := netip.ParseAddr(v); err != nil {
		return fmt.Errorf("%w: %s %q: %v", ErrInvalidSpec, field, v, err)
	}
	return nil
}

// NetworkStatus is the observed state written by the node controller.
type NetworkStatus struct {
	Phase   NetworkPhase `json:"phase"`
	Message string       `json:"message,omitempty"`
}

// Validate reports whether the status carries a known observed phase.
func (s NetworkStatus) Validate() error {
	if !s.Phase.Valid() {
		return fmt.Errorf("%w: phase %q", ErrInvalidStatus, s.Phase)
	}
	return nil
}

// Network is a first-class network API object.
type Network struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              NetworkSpec   `json:"spec"`
	Status            NetworkStatus `json:"status"`
}
