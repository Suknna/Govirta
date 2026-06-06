// Package v1alpha1 defines the StoragePool API object. Depends only on stdlib
// and the shared meta envelope; never imports internal/ or pkg/hostnet.
package v1alpha1

import (
	"errors"
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// BackendType names the storage backend family. Mirrors internal/storage/pool
// values by string but is defined independently (契约层不依赖 internal).
type BackendType string

const (
	// BackendLocalBlock identifies host-local block storage.
	BackendLocalBlock BackendType = "local-block"
	// BackendLocalFile identifies host-local file image storage.
	BackendLocalFile BackendType = "local-file"
	// BackendNFSBlock identifies NFS-backed block storage.
	BackendNFSBlock BackendType = "nfs-block"
	// BackendRBDBlock identifies RBD-backed block storage.
	BackendRBDBlock BackendType = "rbd-block"
)

// Valid reports whether b is a known backend type.
func (b BackendType) Valid() bool {
	switch b {
	case BackendLocalBlock, BackendLocalFile, BackendNFSBlock, BackendRBDBlock:
		return true
	default:
		return false
	}
}

// PoolType describes the storage object model exposed by a pool.
type PoolType string

const (
	// PoolTypeBlock identifies pools that manage block volumes.
	PoolTypeBlock PoolType = "block"
	// PoolTypeFile identifies pools that manage file images.
	PoolTypeFile PoolType = "file"
)

// Valid reports whether t is a known pool type.
func (t PoolType) Valid() bool {
	return t == PoolTypeBlock || t == PoolTypeFile
}

// PoolPhase is the observed lifecycle phase reported by the node controller.
type PoolPhase string

const (
	// PoolPhasePending means the pool object exists but is not yet registered.
	PoolPhasePending PoolPhase = "pending"
	// PoolPhaseReady means the node has registered the pool.
	PoolPhaseReady PoolPhase = "ready"
	// PoolPhaseFailed means registration failed.
	PoolPhaseFailed PoolPhase = "failed"
)

// ErrInvalidSpec is returned when a StoragePoolSpec is not internally valid.
var ErrInvalidSpec = errors.New("storagepool: invalid spec")

// StoragePoolSpec is the desired state of a storage pool (explicit semantic
// intent only). StorageRoot is the host path the node driver registers under.
type StoragePoolSpec struct {
	Backend       BackendType `json:"backend"`
	Type          PoolType    `json:"type"`
	StorageRoot   string      `json:"storageRoot"`
	CapacityBytes int64       `json:"capacityBytes"`
}

// Validate reports whether the spec carries explicit, internally consistent fields.
func (s StoragePoolSpec) Validate() error {
	if !s.Backend.Valid() {
		return fmt.Errorf("%w: backend %q", ErrInvalidSpec, s.Backend)
	}
	if !s.Type.Valid() {
		return fmt.Errorf("%w: type %q", ErrInvalidSpec, s.Type)
	}
	if s.StorageRoot == "" {
		return fmt.Errorf("%w: storageRoot is required", ErrInvalidSpec)
	}
	if s.CapacityBytes <= 0 {
		return fmt.Errorf("%w: capacityBytes must be positive", ErrInvalidSpec)
	}
	return nil
}

// StoragePoolStatus is the observed state written by the node controller.
type StoragePoolStatus struct {
	Phase          PoolPhase `json:"phase"`
	AllocatedBytes int64     `json:"allocatedBytes,omitempty"`
	Message        string    `json:"message,omitempty"`
}

// StoragePool is a first-class storage pool API object.
type StoragePool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              StoragePoolSpec   `json:"spec"`
	Status            StoragePoolStatus `json:"status"`
}
