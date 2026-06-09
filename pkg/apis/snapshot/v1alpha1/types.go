// Package v1alpha1 defines the Snapshot API object. A Snapshot is a whole-VM
// cold snapshot (ESXi-style): spec.vmRef points at a VM by name, and the node
// controller fans out one qcow2 internal snapshot per the VM's volumeRefs, all
// named by the Snapshot's UID. Snapshot is an immutable entity (like Image): its
// spec never changes after creation. It depends only on the standard library
// and the shared meta envelope; it never imports internal/.
package v1alpha1

import (
	"errors"
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// ErrInvalidSpec is returned when a SnapshotSpec is not internally valid.
var ErrInvalidSpec = errors.New("snapshot: invalid spec")

// ErrInvalidStatus is returned when a SnapshotStatus is not internally valid.
var ErrInvalidStatus = errors.New("snapshot: invalid status")

// SnapshotSpec is the desired state of a whole-VM cold snapshot.
type SnapshotSpec struct {
	// VMRef is the target VM's metadata.name (whole-VM snapshot target). It is
	// a NAME (like VM.volumeRefs/nicRefs), not a UID.
	VMRef string `json:"vmRef"`
}

// Validate reports whether the spec carries explicit, internally consistent fields.
func (s SnapshotSpec) Validate() error {
	if s.VMRef == "" {
		return fmt.Errorf("%w: vmRef is required", ErrInvalidSpec)
	}
	return nil
}

// SnapshotPhase is the observed lifecycle phase written by the node controller.
// State-machine enum (项目铁律: 专用类型 + 命名常量).
type SnapshotPhase string

const (
	// SnapshotPhasePending means the snapshot object exists but the fan-out has
	// not completed (waiting for VM cold, or fan-out in progress).
	SnapshotPhasePending SnapshotPhase = "pending"
	// SnapshotPhaseReady means every disk's internal snapshot has been created.
	SnapshotPhaseReady SnapshotPhase = "ready"
	// SnapshotPhaseDeleting means teardown is in progress (waiting for VM cold,
	// or per-disk delete in progress).
	SnapshotPhaseDeleting SnapshotPhase = "deleting"
	// SnapshotPhaseFailed means the fan-out failed and already-created disk
	// snapshots were rolled back; the snapshot can be retried.
	SnapshotPhaseFailed SnapshotPhase = "failed"
)

// Valid reports whether p is a known snapshot phase.
func (p SnapshotPhase) Valid() bool {
	switch p {
	case SnapshotPhasePending, SnapshotPhaseReady, SnapshotPhaseDeleting, SnapshotPhaseFailed:
		return true
	default:
		return false
	}
}

// DiskSnapshotState is a single disk's snapshot result (state-machine enum).
type DiskSnapshotState string

const (
	// DiskSnapshotStateCreated means the disk's internal snapshot was created.
	DiskSnapshotStateCreated DiskSnapshotState = "created"
	// DiskSnapshotStateFailed means the disk's internal snapshot creation failed.
	DiskSnapshotStateFailed DiskSnapshotState = "failed"
)

// Valid reports whether s is a known disk snapshot state.
func (s DiskSnapshotState) Valid() bool {
	return s == DiskSnapshotStateCreated || s == DiskSnapshotStateFailed
}

// DiskSnapshotResult is the per-disk fan-out result projection.
type DiskSnapshotResult struct {
	VolumeRef string            `json:"volumeRef"`
	Result    DiskSnapshotState `json:"result"`
}

// SnapshotStatus is the observed state written by the node controller.
type SnapshotStatus struct {
	Phase         SnapshotPhase        `json:"phase"`
	DiskSnapshots []DiskSnapshotResult `json:"diskSnapshots,omitempty"`
	Message       string               `json:"message,omitempty"`
}

// Validate reports whether the status carries a known observed phase and, when
// present, known per-disk states.
func (s SnapshotStatus) Validate() error {
	if !s.Phase.Valid() {
		return fmt.Errorf("%w: phase %q", ErrInvalidStatus, s.Phase)
	}
	for _, d := range s.DiskSnapshots {
		if !d.Result.Valid() {
			return fmt.Errorf("%w: diskSnapshot %q result %q", ErrInvalidStatus, d.VolumeRef, d.Result)
		}
	}
	return nil
}

// Snapshot is a first-class whole-VM cold snapshot API object.
type Snapshot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              SnapshotSpec   `json:"spec"`
	Status            SnapshotStatus `json:"status"`
}
