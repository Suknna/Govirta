package v1alpha1

import (
	"errors"
	"testing"
)

func validVMSpec() VMSpec {
	return VMSpec{
		Arch:       "aarch64",
		VCPUs:      2,
		MemoryMiB:  512,
		VolumeRefs: []string{"vol-root"},
		NICRefs:    []string{"nic0"},
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
