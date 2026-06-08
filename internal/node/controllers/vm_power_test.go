package controllers

import (
	"testing"
	"time"

	"github.com/suknna/govirta/internal/vmm"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

func TestVMPowerObservationMatrix(t *testing.T) {
	tests := []struct {
		name       string
		desired    vmv1.PowerState
		phase      vmm.Phase
		wantPhase  vmv1.VMPhase
		wantPower  vmv1.ObservedPowerState
		wantAction vmv1.PowerTransition
	}{
		{name: "On Running", desired: vmv1.PowerStateOn, phase: vmm.PhaseRunning, wantPhase: vmv1.VMPhaseRunning, wantPower: vmv1.ObservedPowerStateOn, wantAction: vmv1.PowerTransitionNone},
		{name: "On Defined", desired: vmv1.PowerStateOn, phase: vmm.PhaseDefined, wantPhase: vmv1.VMPhaseDefined, wantPower: vmv1.ObservedPowerStateOff, wantAction: vmv1.PowerTransitionStarting},
		{name: "On Stopped", desired: vmv1.PowerStateOn, phase: vmm.PhaseStopped, wantPhase: vmv1.VMPhaseStopped, wantPower: vmv1.ObservedPowerStateOff, wantAction: vmv1.PowerTransitionStarting},
		{name: "On Failed", desired: vmv1.PowerStateOn, phase: vmm.PhaseFailed, wantPhase: vmv1.VMPhaseFailed, wantPower: vmv1.ObservedPowerStateOff, wantAction: vmv1.PowerTransitionStarting},
		{name: "On Starting", desired: vmv1.PowerStateOn, phase: vmm.PhaseStarting, wantPhase: vmv1.VMPhaseStarting, wantPower: vmv1.ObservedPowerStateOn, wantAction: vmv1.PowerTransitionStarting},
		{name: "Shutdown Running", desired: vmv1.PowerStateShutdown, phase: vmm.PhaseRunning, wantPhase: vmv1.VMPhaseRunning, wantPower: vmv1.ObservedPowerStateOn, wantAction: vmv1.PowerTransitionShutdownRequested},
		{name: "Shutdown Starting", desired: vmv1.PowerStateShutdown, phase: vmm.PhaseStarting, wantPhase: vmv1.VMPhaseStarting, wantPower: vmv1.ObservedPowerStateOn, wantAction: vmv1.PowerTransitionShutdownRequested},
		{name: "Shutdown Stopping", desired: vmv1.PowerStateShutdown, phase: vmm.PhaseStopping, wantPhase: vmv1.VMPhaseStopping, wantPower: vmv1.ObservedPowerStateOn, wantAction: vmv1.PowerTransitionShutdownRequested},
		{name: "Shutdown Defined", desired: vmv1.PowerStateShutdown, phase: vmm.PhaseDefined, wantPhase: vmv1.VMPhaseDefined, wantPower: vmv1.ObservedPowerStateOff, wantAction: vmv1.PowerTransitionNone},
		{name: "Shutdown Stopped", desired: vmv1.PowerStateShutdown, phase: vmm.PhaseStopped, wantPhase: vmv1.VMPhaseStopped, wantPower: vmv1.ObservedPowerStateOff, wantAction: vmv1.PowerTransitionNone},
		{name: "Shutdown Failed", desired: vmv1.PowerStateShutdown, phase: vmm.PhaseFailed, wantPhase: vmv1.VMPhaseFailed, wantPower: vmv1.ObservedPowerStateOff, wantAction: vmv1.PowerTransitionNone},
		{name: "Off Running", desired: vmv1.PowerStateOff, phase: vmm.PhaseRunning, wantPhase: vmv1.VMPhaseRunning, wantPower: vmv1.ObservedPowerStateOn, wantAction: vmv1.PowerTransitionPoweringOff},
		{name: "Off Starting", desired: vmv1.PowerStateOff, phase: vmm.PhaseStarting, wantPhase: vmv1.VMPhaseStarting, wantPower: vmv1.ObservedPowerStateOn, wantAction: vmv1.PowerTransitionPoweringOff},
		{name: "Off Stopping", desired: vmv1.PowerStateOff, phase: vmm.PhaseStopping, wantPhase: vmv1.VMPhaseStopping, wantPower: vmv1.ObservedPowerStateOn, wantAction: vmv1.PowerTransitionPoweringOff},
		{name: "Off Defined", desired: vmv1.PowerStateOff, phase: vmm.PhaseDefined, wantPhase: vmv1.VMPhaseDefined, wantPower: vmv1.ObservedPowerStateOff, wantAction: vmv1.PowerTransitionNone},
		{name: "Off Stopped", desired: vmv1.PowerStateOff, phase: vmm.PhaseStopped, wantPhase: vmv1.VMPhaseStopped, wantPower: vmv1.ObservedPowerStateOff, wantAction: vmv1.PowerTransitionNone},
		{name: "Off Failed", desired: vmv1.PowerStateOff, phase: vmm.PhaseFailed, wantPhase: vmv1.VMPhaseFailed, wantPower: vmv1.ObservedPowerStateOff, wantAction: vmv1.PowerTransitionNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := observePower(tt.phase, tt.desired)
			if obs.Phase != tt.wantPhase {
				t.Fatalf("Phase = %q, want %q", obs.Phase, tt.wantPhase)
			}
			if obs.Observed != tt.wantPower {
				t.Fatalf("Observed = %q, want %q", obs.Observed, tt.wantPower)
			}
			if obs.Transition != tt.wantAction {
				t.Fatalf("Transition = %q, want %q", obs.Transition, tt.wantAction)
			}
			if !obs.KnownPhase {
				t.Fatalf("KnownPhase = false, want true")
			}
			status := vmStatus(obs, "test message")
			if status.Phase != tt.wantPhase || status.ObservedPowerState == "" || status.PowerTransition == "" || status.Message != "test message" {
				t.Fatalf("vmStatus() = %+v, want phase/power/transition/message populated", status)
			}
		})
	}
}

func TestVMPowerUnknownPhaseMapsFailedWithPowerFields(t *testing.T) {
	obs := observePower(vmm.Phase("mystery"), vmv1.PowerStateOn)
	if obs.KnownPhase {
		t.Fatalf("KnownPhase = true, want false for unknown phase")
	}
	status := vmStatus(obs, "unknown phase")
	if status.Phase != vmv1.VMPhaseFailed {
		t.Fatalf("Phase = %q, want %q", status.Phase, vmv1.VMPhaseFailed)
	}
	if status.ObservedPowerState == "" {
		t.Fatalf("ObservedPowerState is empty, want non-empty")
	}
	if status.PowerTransition == "" {
		t.Fatalf("PowerTransition is empty, want non-empty")
	}
	if status.PowerTransition != vmv1.PowerTransitionStarting {
		t.Fatalf("PowerTransition = %q, want %q", status.PowerTransition, vmv1.PowerTransitionStarting)
	}
}

func TestVMPowerStatusDerivesCompleteStatus(t *testing.T) {
	status, known := vmPowerStatus(vmv1.PowerStateShutdown, vmm.PhaseStopping, "shutdown requested")
	if !known {
		t.Fatalf("known = false, want true")
	}
	if status.Phase != vmv1.VMPhaseStopping {
		t.Fatalf("Phase = %q, want %q", status.Phase, vmv1.VMPhaseStopping)
	}
	if status.ObservedPowerState != vmv1.ObservedPowerStateOn {
		t.Fatalf("ObservedPowerState = %q, want %q", status.ObservedPowerState, vmv1.ObservedPowerStateOn)
	}
	if status.PowerTransition != vmv1.PowerTransitionShutdownRequested {
		t.Fatalf("PowerTransition = %q, want %q", status.PowerTransition, vmv1.PowerTransitionShutdownRequested)
	}
	if status.Message != "shutdown requested" {
		t.Fatalf("Message = %q, want shutdown requested", status.Message)
	}
}

func TestVMPowerRequeueConstantsAndPredicate(t *testing.T) {
	if vmPowerRequeueDelay != 3*time.Second || vmDependencyRequeueDelay != 3*time.Second || vmTransientRequeueDelay != 3*time.Second {
		t.Fatalf("requeue delays = %s/%s/%s, want all 3s", vmPowerRequeueDelay, vmDependencyRequeueDelay, vmTransientRequeueDelay)
	}
	if !powerNeedsDelayedRequeue(vmPowerObservation{Transition: vmv1.PowerTransitionStarting}) {
		t.Fatalf("powerNeedsDelayedRequeue(Starting) = false, want true")
	}
	if !powerNeedsDelayedRequeue(vmPowerObservation{Transition: vmv1.PowerTransitionShutdownRequested}) {
		t.Fatalf("powerNeedsDelayedRequeue(ShutdownRequested) = false, want true")
	}
	if !powerNeedsDelayedRequeue(vmPowerObservation{Transition: vmv1.PowerTransitionPoweringOff}) {
		t.Fatalf("powerNeedsDelayedRequeue(PoweringOff) = false, want true")
	}
	if powerNeedsDelayedRequeue(vmPowerObservation{Transition: vmv1.PowerTransitionNone}) {
		t.Fatalf("powerNeedsDelayedRequeue(None) = true, want false")
	}
}
