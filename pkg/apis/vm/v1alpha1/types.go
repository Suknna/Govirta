// Package v1alpha1 defines the VM API object. VMPhase mirrors internal/vmm.Phase
// by string but is defined independently (契约层不依赖 internal).
package v1alpha1

import (
	"errors"
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// VMPhase is the observed VM run phase derived live by the node VM controller.
type VMPhase string

const (
	// VMPhaseDefined means created but never started.
	VMPhaseDefined VMPhase = "defined"
	// VMPhaseStarting means the process is alive but QMP is not yet running.
	VMPhaseStarting VMPhase = "starting"
	// VMPhaseRunning means the process is alive and QMP reports running.
	VMPhaseRunning VMPhase = "running"
	// VMPhaseStopping means a powerdown was sent but the process is still alive.
	VMPhaseStopping VMPhase = "stopping"
	// VMPhaseStopped means the process is dead with stopped intent.
	VMPhaseStopped VMPhase = "stopped"
	// VMPhaseFailed means the process is dead with running intent.
	VMPhaseFailed VMPhase = "failed"
)

// ErrInvalidSpec is returned when a VMSpec is not internally valid.
var ErrInvalidSpec = errors.New("vm: invalid spec")

// VMSpec is the desired state of a VM. VolumeRefs and NICRefs name the Volume
// and NIC objects this VM depends on; the VM controller gates on their readiness.
type VMSpec struct {
	Arch       string   `json:"arch"`
	VCPUs      int      `json:"vcpus"`
	MemoryMiB  int      `json:"memoryMiB"`
	VolumeRefs []string `json:"volumeRefs"`
	NICRefs    []string `json:"nicRefs"`
}

// Validate reports whether the spec carries explicit, internally consistent fields.
func (s VMSpec) Validate() error {
	if s.Arch == "" {
		return fmt.Errorf("%w: arch is required", ErrInvalidSpec)
	}
	if s.VCPUs <= 0 {
		return fmt.Errorf("%w: vcpus must be positive", ErrInvalidSpec)
	}
	if s.MemoryMiB <= 0 {
		return fmt.Errorf("%w: memoryMiB must be positive", ErrInvalidSpec)
	}
	if len(s.VolumeRefs) == 0 {
		return fmt.Errorf("%w: at least one volumeRef is required", ErrInvalidSpec)
	}
	if len(s.NICRefs) == 0 {
		return fmt.Errorf("%w: at least one nicRef is required", ErrInvalidSpec)
	}
	return nil
}

// VMStatus is the observed state written by the node VM controller.
type VMStatus struct {
	Phase   VMPhase `json:"phase"`
	Message string  `json:"message,omitempty"`
}

// VM is a first-class VM API object.
type VM struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              VMSpec   `json:"spec"`
	Status            VMStatus `json:"status"`
}
