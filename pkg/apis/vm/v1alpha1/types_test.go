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
	if got := err.Error(); !bytes.Contains([]byte(got), []byte(`powerState "Paused" must be one of On, Shutdown, Off`)) {
		t.Fatalf("Validate() error = %q, want invalid value included", got)
	}
}

func TestVMSpecValidateAcceptsKnownPowerStates(t *testing.T) {
	for _, state := range []PowerState{PowerStateOn, PowerStateShutdown, PowerStateOff} {
		spec := VMSpec{Arch: "aarch64", VCPUs: 1, MemoryMiB: 512, VolumeRefs: []string{"root"}, NICRefs: []string{"nic"}, PowerState: state}
		if err := spec.Validate(); err != nil {
			t.Fatalf("Validate(%s) error = %v", state, err)
		}
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
