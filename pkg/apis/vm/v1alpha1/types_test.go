package v1alpha1

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func validVMSpec() VMSpec {
	return VMSpec{
		Arch:       "aarch64",
		VCPUs:      2,
		MemoryMiB:  512,
		VolumeRefs: []string{"vol-root"},
		NICRefs:    []string{"nic0"},
		PowerState: PowerStateOn,
	}
}

func TestVMSpecValidate(t *testing.T) {
	if err := validVMSpec().Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *VMSpec)
	}{
		{"empty arch", func(s *VMSpec) { s.Arch = "" }},
		{"zero vcpus", func(s *VMSpec) { s.VCPUs = 0 }},
		{"zero memory", func(s *VMSpec) { s.MemoryMiB = 0 }},
		{"no volumes", func(s *VMSpec) { s.VolumeRefs = nil }},
		{"no nics", func(s *VMSpec) { s.NICRefs = nil }},
		{"empty powerState", func(s *VMSpec) { s.PowerState = "" }},
		{"unknown powerState", func(s *VMSpec) { s.PowerState = PowerState("Paused") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validVMSpec()
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestVMSpecValidateRequiresExplicitPowerState(t *testing.T) {
	spec := VMSpec{Arch: "aarch64", VCPUs: 1, MemoryMiB: 512, VolumeRefs: []string{"root"}, NICRefs: []string{"nic"}}
	if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("Validate() error = %v, want ErrInvalidSpec", err)
	}
}

func TestVMSpecValidateReportsInvalidPowerStateValue(t *testing.T) {
	spec := validVMSpec()
	spec.PowerState = PowerState("Paused")
	err := spec.Validate()
	if !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("Validate() error = %v, want ErrInvalidSpec", err)
	}
	if got := err.Error(); !bytes.Contains([]byte(got), []byte(`powerState "Paused" must be one of On, Off`)) {
		t.Fatalf("Validate() error = %q, want invalid value included", got)
	}
}

func TestVMSpecValidateAcceptsKnownPowerStates(t *testing.T) {
	tests := []struct {
		powerState   PowerState
		powerOffMode PowerOffMode
	}{
		{PowerStateOn, ""},
		{PowerStateOff, PowerOffModeAcpi},
	}
	for _, tt := range tests {
		spec := VMSpec{Arch: "aarch64", VCPUs: 1, MemoryMiB: 512, VolumeRefs: []string{"root"}, NICRefs: []string{"nic"}, PowerState: tt.powerState, PowerOffMode: tt.powerOffMode}
		if err := spec.Validate(); err != nil {
			t.Fatalf("Validate(%s) error = %v", tt.powerState, err)
		}
	}
}

func TestPowerOffModeValid(t *testing.T) {
	tests := []struct {
		mode PowerOffMode
		want bool
	}{
		{PowerOffModeAcpi, true},
		{PowerOffModeForce, true},
		{PowerOffMode("Maybe"), false},
		{PowerOffMode(""), false},
	}
	for _, tt := range tests {
		if got := tt.mode.Valid(); got != tt.want {
			t.Fatalf("PowerOffMode(%q).Valid() = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

func TestVMSpecValidatePowerOffModeConditional(t *testing.T) {
	tests := []struct {
		name         string
		powerState   PowerState
		powerOffMode PowerOffMode
		wantErr      bool
	}{
		{"On with powerOffMode set", PowerStateOn, PowerOffModeAcpi, true},
		{"Off with empty powerOffMode", PowerStateOff, "", true},
		{"Off with invalid powerOffMode", PowerStateOff, PowerOffMode("Maybe"), true},
		{"On with empty powerOffMode", PowerStateOn, "", false},
		{"Off with Acpi powerOffMode", PowerStateOff, PowerOffModeAcpi, false},
		{"Off with Force powerOffMode", PowerStateOff, PowerOffModeForce, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := validVMSpec()
			spec.PowerState = tt.powerState
			spec.PowerOffMode = tt.powerOffMode
			err := spec.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidSpec) {
					t.Fatalf("Validate() error = %v, want ErrInvalidSpec", err)
				}
			} else if err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestVMStatusPowerFieldsRoundTrip(t *testing.T) {
	status := VMStatus{Phase: VMPhaseDefined, ObservedPowerState: ObservedPowerStateOff, PowerTransition: PowerTransitionNone}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"observedPowerState":"Off"`)) {
		t.Fatalf("encoded status %s does not include explicit observedPowerState", data)
	}
	if !bytes.Contains(data, []byte(`"powerTransition":"None"`)) {
		t.Fatalf("encoded status %s does not include explicit powerTransition", data)
	}

	var decoded VMStatus
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != status {
		t.Fatalf("decoded status = %+v, want %+v", decoded, status)
	}
}

func TestVMStatusValidateAcceptsKnownPhase(t *testing.T) {
	status := VMStatus{
		Phase:              VMPhaseRunning,
		ObservedPowerState: ObservedPowerStateOn,
		PowerTransition:    PowerTransitionNone,
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestVMStatusValidateRejectsUnknownPhase(t *testing.T) {
	status := VMStatus{
		Phase:              VMPhase("bogus"),
		ObservedPowerState: ObservedPowerStateOn,
		PowerTransition:    PowerTransitionNone,
	}
	err := status.Validate()
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
	}
}

func TestVMStatusValidateRejectsUnknownObservedPowerState(t *testing.T) {
	status := VMStatus{
		Phase:              VMPhaseRunning,
		ObservedPowerState: ObservedPowerState("bogus"),
		PowerTransition:    PowerTransitionNone,
	}
	err := status.Validate()
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
	}
}

func TestVMStatusValidateRejectsUnknownPowerTransition(t *testing.T) {
	status := VMStatus{
		Phase:              VMPhaseRunning,
		ObservedPowerState: ObservedPowerStateOn,
		PowerTransition:    PowerTransition("bogus"),
	}
	err := status.Validate()
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
	}
}

func TestVMSpecPowerStateRoundTrip(t *testing.T) {
	spec := validVMSpec()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"powerState":"On"`)) {
		t.Fatalf("encoded spec %s does not include explicit powerState", data)
	}

	var decoded VMSpec
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.PowerState != PowerStateOn {
		t.Fatalf("decoded powerState = %q, want %q", decoded.PowerState, PowerStateOn)
	}
	if !reflect.DeepEqual(decoded, spec) {
		t.Fatalf("decoded spec = %+v, want %+v", decoded, spec)
	}
}
