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

// Valid reports whether p is a known VM phase.
func (p VMPhase) Valid() bool {
	switch p {
	case VMPhaseDefined, VMPhaseStarting, VMPhaseRunning, VMPhaseStopping, VMPhaseStopped, VMPhaseFailed:
		return true
	default:
		return false
	}
}

// PowerState is the user's desired VM power intent.
type PowerState string

const (
	// PowerStateOn requests the node to converge the guest to a running QEMU process.
	PowerStateOn PowerState = "On"
	// PowerStateShutdown requests graceful guest OS shutdown through the VM control plane.
	PowerStateShutdown PowerState = "Shutdown"
	// PowerStateOff requests the guest to remain powered down without a running QEMU process.
	PowerStateOff PowerState = "Off"
)

// Valid reports whether p is one of the supported desired power intents.
func (p PowerState) Valid() bool {
	switch p {
	case PowerStateOn, PowerStateShutdown, PowerStateOff:
		return true
	default:
		return false
	}
}

// ObservedPowerState is the physical power state derived from live QEMU/QMP.
type ObservedPowerState string

const (
	// ObservedPowerStateOn means live node observation shows a powered guest process.
	ObservedPowerStateOn ObservedPowerState = "On"
	// ObservedPowerStateOff means live node observation shows no powered guest process.
	ObservedPowerStateOff ObservedPowerState = "Off"
)

// Valid reports whether p is one of the supported live power observations.
func (p ObservedPowerState) Valid() bool {
	switch p {
	case ObservedPowerStateOn, ObservedPowerStateOff:
		return true
	default:
		return false
	}
}

// PowerTransition describes the current convergence action.
type PowerTransition string

const (
	// PowerTransitionNone means no power convergence action is currently active.
	PowerTransitionNone PowerTransition = "None"
	// PowerTransitionStarting means the node is creating or starting the guest process.
	PowerTransitionStarting PowerTransition = "Starting"
	// PowerTransitionShutdownRequested means graceful shutdown has been requested and is still converging.
	PowerTransitionShutdownRequested PowerTransition = "ShutdownRequested"
	// PowerTransitionPoweringOff means forced power-off is in progress for a live guest process.
	PowerTransitionPoweringOff PowerTransition = "PoweringOff"
)

// Valid reports whether p is one of the supported convergence actions.
func (p PowerTransition) Valid() bool {
	switch p {
	case PowerTransitionNone, PowerTransitionStarting, PowerTransitionShutdownRequested, PowerTransitionPoweringOff:
		return true
	default:
		return false
	}
}

// ErrInvalidSpec is returned when a VMSpec is not internally valid.
var ErrInvalidSpec = errors.New("vm: invalid spec")

// ErrInvalidStatus is returned when a VMStatus is not internally valid.
var ErrInvalidStatus = errors.New("vm: invalid status")

// VMSpec is the desired state of a VM. VolumeRefs and NICRefs name the Volume
// and NIC objects this VM depends on; the VM controller gates on their readiness.
type VMSpec struct {
	Arch       string     `json:"arch"`
	VCPUs      int        `json:"vcpus"`
	MemoryMiB  int        `json:"memoryMiB"`
	VolumeRefs []string   `json:"volumeRefs"`
	NICRefs    []string   `json:"nicRefs"`
	PowerState PowerState `json:"powerState"`
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
	if !s.PowerState.Valid() {
		return fmt.Errorf("%w: powerState %q must be one of On, Shutdown, Off", ErrInvalidSpec, s.PowerState)
	}
	return nil
}

// VMStatus is the observed state written by the node VM controller.
type VMStatus struct {
	Phase              VMPhase            `json:"phase"`
	ObservedPowerState ObservedPowerState `json:"observedPowerState"`
	PowerTransition    PowerTransition    `json:"powerTransition"`
	Message            string             `json:"message,omitempty"`
}

// Validate reports whether the status carries known observed state values.
func (s VMStatus) Validate() error {
	if !s.Phase.Valid() {
		return fmt.Errorf("%w: phase %q", ErrInvalidStatus, s.Phase)
	}
	if !s.ObservedPowerState.Valid() {
		return fmt.Errorf("%w: observedPowerState %q", ErrInvalidStatus, s.ObservedPowerState)
	}
	if !s.PowerTransition.Valid() {
		return fmt.Errorf("%w: powerTransition %q", ErrInvalidStatus, s.PowerTransition)
	}
	return nil
}

// VM is a first-class VM API object.
type VM struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              VMSpec   `json:"spec"`
	Status            VMStatus `json:"status"`
}
