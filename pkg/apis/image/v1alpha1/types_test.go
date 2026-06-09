package v1alpha1

import (
	"errors"
	"testing"
)

func validImageSpec() ImageSpec {
	return ImageSpec{
		FilePoolRef:       "files",
		Source:            ImageSource{Type: ImageSourceHTTP, Location: "https://example/cirros.img"},
		Format:            ImageFormatQCOW2,
		DeclaredSizeBytes: 1 << 20,
	}
}

func TestImageSpecValidate(t *testing.T) {
	if err := validImageSpec().Validate(); err != nil {
		t.Fatalf("valid http spec rejected: %v", err)
	}
	fileSpec := validImageSpec()
	fileSpec.Source = ImageSource{Type: ImageSourceFile, Location: "/var/img/cirros.img"}
	if err := fileSpec.Validate(); err != nil {
		t.Fatalf("valid file spec rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *ImageSpec)
	}{
		{"empty pool", func(s *ImageSpec) { s.FilePoolRef = "" }},
		{"bad source type", func(s *ImageSpec) { s.Source.Type = "registry" }},
		{"empty location", func(s *ImageSpec) { s.Source.Location = "" }},
		{"bad format", func(s *ImageSpec) { s.Format = "vmdk" }},
		{"zero size", func(s *ImageSpec) { s.DeclaredSizeBytes = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validImageSpec()
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestImageEnumsValid(t *testing.T) {
	if !ImageFormatQCOW2.Valid() || !ImageFormatRaw.Valid() {
		t.Error("known formats should be valid")
	}
	if ImageFormat("vmdk").Valid() {
		t.Error("vmdk should be invalid")
	}
	if !ImageSourceFile.Valid() || !ImageSourceHTTP.Valid() {
		t.Error("known source types should be valid")
	}
	if ImageSourceType("registry").Valid() {
		t.Error("registry should be invalid")
	}
}

func TestImageStatusValidateAcceptsKnownPhase(t *testing.T) {
	status := ImageStatus{Phase: ImagePhaseReady}
	if err := status.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestImageStatusValidateRejectsUnknownPhase(t *testing.T) {
	status := ImageStatus{Phase: ImagePhase("bogus")}
	err := status.Validate()
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
	}
}
