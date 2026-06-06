package v1alpha1

import (
	"errors"
	"testing"
)

func TestObjectMetaValidate(t *testing.T) {
	tests := []struct {
		name    string
		meta    ObjectMeta
		wantErr bool
	}{
		{"valid", ObjectMeta{Name: "n1", UID: "u1"}, false},
		{"empty name", ObjectMeta{UID: "u1"}, true},
		{"empty uid", ObjectMeta{Name: "n1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.meta.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidObjectMeta) {
					t.Fatalf("got err %v, want ErrInvalidObjectMeta", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
		})
	}
}
