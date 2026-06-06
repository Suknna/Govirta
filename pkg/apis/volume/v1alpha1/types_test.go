package v1alpha1

import (
	"errors"
	"testing"
)

func validRootSpec() VolumeSpec {
	return VolumeSpec{
		PoolRef:          "blocks",
		VMRef:            "vm-uid-1",
		VMName:           "vm1",
		DiskIndex:        0,
		CapacityBytes:    2 << 30,
		Role:             VolumeRoleRoot,
		ImageRef:         "cirros",
		ImageFilePoolRef: "files",
	}
}

func validDataSpec() VolumeSpec {
	return VolumeSpec{
		PoolRef:       "blocks",
		VMRef:         "vm-uid-1",
		VMName:        "vm1",
		DiskIndex:     1,
		CapacityBytes: 1 << 30,
		Role:          VolumeRoleData,
	}
}

func TestVolumeSpecValidate(t *testing.T) {
	if err := validRootSpec().Validate(); err != nil {
		t.Fatalf("valid root rejected: %v", err)
	}
	if err := validDataSpec().Validate(); err != nil {
		t.Fatalf("valid data rejected: %v", err)
	}

	rootTests := []struct {
		name string
		mut  func(s *VolumeSpec)
	}{
		{"root missing imageRef", func(s *VolumeSpec) { s.ImageRef = "" }},
		{"root missing imageFilePoolRef", func(s *VolumeSpec) { s.ImageFilePoolRef = "" }},
		{"empty poolRef", func(s *VolumeSpec) { s.PoolRef = "" }},
		{"empty vmRef", func(s *VolumeSpec) { s.VMRef = "" }},
		{"negative disk index", func(s *VolumeSpec) { s.DiskIndex = -1 }},
		{"zero capacity", func(s *VolumeSpec) { s.CapacityBytes = 0 }},
		{"bad role", func(s *VolumeSpec) { s.Role = "swap" }},
	}
	for _, tt := range rootTests {
		t.Run(tt.name, func(t *testing.T) {
			s := validRootSpec()
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}

	t.Run("data with imageRef", func(t *testing.T) {
		s := validDataSpec()
		s.ImageRef = "cirros"
		if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("got %v, want ErrInvalidSpec", err)
		}
	})
}
