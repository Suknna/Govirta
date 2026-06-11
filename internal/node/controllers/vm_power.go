package controllers

import (
	"time"

	"github.com/suknna/govirta/internal/vmm"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

const (
	vmPowerRequeueDelay      = 3 * time.Second
	vmDependencyRequeueDelay = 3 * time.Second
	vmTransientRequeueDelay  = 3 * time.Second
)

type vmPowerObservation struct {
	Phase      vmv1.VMPhase
	Observed   vmv1.ObservedPowerState
	Transition vmv1.PowerTransition
	KnownPhase bool
}

func observePower(phase vmm.Phase, desired vmv1.PowerState, mode vmv1.PowerOffMode) vmPowerObservation {
	apiPhase, known := mapVMPhase(phase)
	observed := observedPowerState(phase)
	transition := powerTransition(desired, mode, phase, observed)

	return vmPowerObservation{
		Phase:      apiPhase,
		Observed:   observed,
		Transition: transition,
		KnownPhase: known,
	}
}

func vmStatus(obs vmPowerObservation, message string) vmv1.VMStatus {
	return vmv1.VMStatus{
		Phase:              obs.Phase,
		ObservedPowerState: obs.Observed,
		PowerTransition:    obs.Transition,
		Message:            message,
	}
}

func vmPowerStatus(desired vmv1.PowerState, mode vmv1.PowerOffMode, phase vmm.Phase, message string) (vmv1.VMStatus, bool) {
	obs := observePower(phase, desired, mode)
	return vmStatus(obs, message), obs.KnownPhase
}

func powerNeedsDelayedRequeue(obs vmPowerObservation) bool {
	switch obs.Transition {
	case vmv1.PowerTransitionStarting, vmv1.PowerTransitionShutdownRequested, vmv1.PowerTransitionPoweringOff:
		return true
	default:
		return false
	}
}

func observedPowerState(phase vmm.Phase) vmv1.ObservedPowerState {
	switch phase {
	case vmm.PhaseRunning, vmm.PhaseStarting, vmm.PhaseStopping:
		return vmv1.ObservedPowerStateOn
	default:
		return vmv1.ObservedPowerStateOff
	}
}

// powerTransition derives the active convergence action from the desired
// (powerState, powerOffMode) pair and the live observation. On consumes only the
// powerState; Off consumes powerOffMode to pick the convergence verb while a
// guest process is still live — Acpi → graceful ShutdownRequested (vmm.Stop),
// Force → PoweringOff (vmm.Kill). Once the guest is dead the transition settles
// to None regardless of mode.
func powerTransition(desired vmv1.PowerState, mode vmv1.PowerOffMode, phase vmm.Phase, observed vmv1.ObservedPowerState) vmv1.PowerTransition {
	switch desired {
	case vmv1.PowerStateOn:
		if observed == vmv1.ObservedPowerStateOff || phase == vmm.PhaseStarting {
			return vmv1.PowerTransitionStarting
		}
	case vmv1.PowerStateOff:
		if observed == vmv1.ObservedPowerStateOn {
			if mode == vmv1.PowerOffModeForce {
				return vmv1.PowerTransitionPoweringOff
			}
			return vmv1.PowerTransitionShutdownRequested
		}
	}
	return vmv1.PowerTransitionNone
}
