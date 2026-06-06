package v1alpha1

import (
	"errors"
	"testing"
)

func TestStoragePoolSpecValidate(t *testing.T) {
	valid := StoragePoolSpec{
		Backend:       BackendLocalBlock,
		Type:          PoolTypeBlock,
		StorageRoot:   "/var/lib/govirtlet/pools/p1",
		CapacityBytes: 1 << 30,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *StoragePoolSpec)
	}{
		{"empty backend", func(s *StoragePoolSpec) { s.Backend = "" }},
		{"bad type", func(s *StoragePoolSpec) { s.Type = "object" }},
		{"empty root", func(s *StoragePoolSpec) { s.StorageRoot = "" }},
		{"zero capacity", func(s *StoragePoolSpec) { s.CapacityBytes = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valid
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestStoragePoolEnumsValid(t *testing.T) {
	for _, b := range []BackendType{BackendLocalBlock, BackendLocalFile, BackendNFSBlock, BackendRBDBlock} {
		if !b.Valid() {
			t.Errorf("backend %q should be valid", b)
		}
	}
	if BackendType("bogus").Valid() {
		t.Error("bogus backend should be invalid")
	}
	for _, p := range []PoolType{PoolTypeBlock, PoolTypeFile} {
		if !p.Valid() {
			t.Errorf("pool type %q should be valid", p)
		}
	}
	if PoolType("object").Valid() {
		t.Error("object pool type should be invalid")
	}
}
