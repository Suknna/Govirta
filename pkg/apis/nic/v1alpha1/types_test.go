package v1alpha1

import (
	"errors"
	"testing"
)

func validNICSpec() NICSpec {
	return NICSpec{
		NetworkRef: "net1",
		VMRef:      "vm-uid-1",
		IP:         "192.168.100.10",
	}
}

func TestNICSpecValidate(t *testing.T) {
	if err := validNICSpec().Validate(); err != nil {
		t.Fatalf("valid spec with empty MAC rejected: %v", err)
	}
	withMAC := validNICSpec()
	withMAC.MAC = "52:54:00:12:34:56"
	if err := withMAC.Validate(); err != nil {
		t.Fatalf("valid spec with explicit MAC rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *NICSpec)
	}{
		{"empty networkRef", func(s *NICSpec) { s.NetworkRef = "" }},
		{"empty vmRef", func(s *NICSpec) { s.VMRef = "" }},
		{"bad ip", func(s *NICSpec) { s.IP = "999.1.1.1" }},
		{"bad mac", func(s *NICSpec) { s.MAC = "zz:zz" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validNICSpec()
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}
}
