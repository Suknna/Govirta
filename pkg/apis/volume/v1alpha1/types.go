// Package v1alpha1 defines the Volume API object. A root volume is always a
// full independent copy derived from an Image's bytes (no backing-file chain);
// the source byte format authority is Image.Spec.Format, so Volume carries no
// source format field.
package v1alpha1

import (
	"errors"
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// VolumeRole names the disk role. Mirrors internal/storage/volume by string but
// defined independently.
type VolumeRole string

const (
	// VolumeRoleRoot identifies the VM boot/root disk.
	VolumeRoleRoot VolumeRole = "root"
	// VolumeRoleData identifies an additional non-boot data disk.
	VolumeRoleData VolumeRole = "data"
)

// Valid reports whether r is a known role.
func (r VolumeRole) Valid() bool {
	return r == VolumeRoleRoot || r == VolumeRoleData
}

// VolumePhase is the observed lifecycle phase written by the node controller.
type VolumePhase string

const (
	// VolumePhasePending means the volume object exists but is not yet created.
	VolumePhasePending VolumePhase = "pending"
	// VolumePhaseReady means the qcow2 volume exists on the node.
	VolumePhaseReady VolumePhase = "ready"
	// VolumePhaseFailed means creation failed.
	VolumePhaseFailed VolumePhase = "failed"
)

// Valid reports whether p is a known volume phase.
func (p VolumePhase) Valid() bool {
	switch p {
	case VolumePhasePending, VolumePhaseReady, VolumePhaseFailed:
		return true
	default:
		return false
	}
}

// ErrInvalidSpec is returned when a VolumeSpec is not internally valid.
var ErrInvalidSpec = errors.New("volume: invalid spec")

// ErrInvalidStatus is returned when a VolumeStatus is not internally valid.
var ErrInvalidStatus = errors.New("volume: invalid status")

// VolumeSpec is the desired state of a block volume. ImageRef + ImageFilePoolRef
// are required for a root volume (the source image and the file pool holding it)
// and must be empty for a data volume.
type VolumeSpec struct {
	PoolRef          string     `json:"poolRef"` // block pool object name
	VMRef            string     `json:"vmRef"`   // owning VM uid
	VMName           string     `json:"vmName"`
	DiskIndex        int        `json:"diskIndex"`
	CapacityBytes    int64      `json:"capacityBytes"`
	Role             VolumeRole `json:"role"`
	ImageRef         string     `json:"imageRef,omitempty"`         // root only
	ImageFilePoolRef string     `json:"imageFilePoolRef,omitempty"` // root only
}

// Validate reports whether the spec carries explicit, internally consistent fields.
func (s VolumeSpec) Validate() error {
	if s.PoolRef == "" {
		return fmt.Errorf("%w: poolRef is required", ErrInvalidSpec)
	}
	if s.VMRef == "" {
		return fmt.Errorf("%w: vmRef is required", ErrInvalidSpec)
	}
	if s.DiskIndex < 0 {
		return fmt.Errorf("%w: diskIndex must be non-negative", ErrInvalidSpec)
	}
	if s.CapacityBytes <= 0 {
		return fmt.Errorf("%w: capacityBytes must be positive", ErrInvalidSpec)
	}
	if !s.Role.Valid() {
		return fmt.Errorf("%w: role %q", ErrInvalidSpec, s.Role)
	}
	switch s.Role {
	case VolumeRoleRoot:
		if s.ImageRef == "" {
			return fmt.Errorf("%w: root volume requires imageRef", ErrInvalidSpec)
		}
		if s.ImageFilePoolRef == "" {
			return fmt.Errorf("%w: root volume requires imageFilePoolRef", ErrInvalidSpec)
		}
	case VolumeRoleData:
		if s.ImageRef != "" || s.ImageFilePoolRef != "" {
			return fmt.Errorf("%w: data volume must not carry image refs", ErrInvalidSpec)
		}
	}
	return nil
}

// VolumeStatus is the observed state written by the node controller.
type VolumeStatus struct {
	Phase      VolumePhase `json:"phase"`
	VolumePath string      `json:"volumePath,omitempty"`
	Message    string      `json:"message,omitempty"`
}

// Validate reports whether the status carries a known observed phase.
func (s VolumeStatus) Validate() error {
	if !s.Phase.Valid() {
		return fmt.Errorf("%w: phase %q", ErrInvalidStatus, s.Phase)
	}
	return nil
}

// Volume is a first-class block volume API object.
type Volume struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              VolumeSpec   `json:"spec"`
	Status            VolumeStatus `json:"status"`
}
