// Package v1alpha1 defines the NIC API object. MAC may be empty at submit time
// and is filled by the apiserver MAC allocator at admission (spec: 平台分配 MAC).
// TAP owner/vnet-header/anti-spoof identities are not in the spec; the node
// controller derives them deterministically.
package v1alpha1

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// NICPhase is the observed lifecycle phase written by the node controller.
type NICPhase string

const (
	// NICPhasePending means the object exists but the NIC is not ensured.
	NICPhasePending NICPhase = "pending"
	// NICPhaseReady means the TAP + DHCP binding + anti-spoofing are ensured.
	NICPhaseReady NICPhase = "ready"
	// NICPhaseFailed means ensuring failed.
	NICPhaseFailed NICPhase = "failed"
)

// Valid reports whether p is a known NIC phase.
func (p NICPhase) Valid() bool {
	switch p {
	case NICPhasePending, NICPhaseReady, NICPhaseFailed:
		return true
	default:
		return false
	}
}

// ErrInvalidSpec is returned when a NICSpec is not internally valid.
var ErrInvalidSpec = errors.New("nic: invalid spec")

// ErrInvalidStatus is returned when a NICStatus is not internally valid.
var ErrInvalidStatus = errors.New("nic: invalid status")

// NICSpec is the desired state of a VM NIC. MAC is platform-allocated; an empty
// MAC at submit time is valid (the apiserver fills it). A non-empty MAC must be
// well-formed.
type NICSpec struct {
	NetworkRef string `json:"networkRef"`
	VMRef      string `json:"vmRef"`
	MAC        string `json:"mac,omitempty"`
	IP         string `json:"ip"`
	Hostname   string `json:"hostname,omitempty"`
}

// Validate reports whether the spec carries explicit, well-formed fields. An
// empty MAC is allowed (platform allocation pending); a non-empty MAC must parse.
func (s NICSpec) Validate() error {
	if s.NetworkRef == "" {
		return fmt.Errorf("%w: networkRef is required", ErrInvalidSpec)
	}
	if s.VMRef == "" {
		return fmt.Errorf("%w: vmRef is required", ErrInvalidSpec)
	}
	if _, err := netip.ParseAddr(s.IP); err != nil {
		return fmt.Errorf("%w: ip %q: %v", ErrInvalidSpec, s.IP, err)
	}
	if s.MAC != "" {
		if _, err := net.ParseMAC(s.MAC); err != nil {
			return fmt.Errorf("%w: mac %q: %v", ErrInvalidSpec, s.MAC, err)
		}
	}
	return nil
}

// NICStatus is the observed state written by the node controller.
type NICStatus struct {
	Phase   NICPhase `json:"phase"`
	TapName string   `json:"tapName,omitempty"`
	Message string   `json:"message,omitempty"`
}

// Validate reports whether the status carries a known observed phase.
func (s NICStatus) Validate() error {
	if !s.Phase.Valid() {
		return fmt.Errorf("%w: phase %q", ErrInvalidStatus, s.Phase)
	}
	return nil
}

// NIC is a first-class VM NIC API object.
type NIC struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              NICSpec   `json:"spec"`
	Status            NICStatus `json:"status"`
}
