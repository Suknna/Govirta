package v1alpha1

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func validImageSpec() ImageSpec {
	return ImageSpec{
		Source:            ImageSource{Type: ImageSourceHTTP, Location: "https://example/cirros.img"},
		Format:            ImageFormatQCOW2,
		Version:           "2026.06.13",
		DeclaredSizeBytes: 1 << 20,
		SHA256:            "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func validReadyImageStatus() ImageStatus {
	return ImageStatus{
		Phase:             ImagePhaseReady,
		ObservedVersion:   "2026.06.13",
		ObservedSHA256:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ObservedSizeBytes: 1 << 20,
		NodeCaches: []NodeCacheStatus{
			{
				NodeName:   "node0",
				Phase:      ImageCachePhaseReady,
				TaskRef:    TaskRef{Name: "task-cache-cirros", UID: "task-uid"},
				CachedPath: "/var/lib/govirta/images/cirros.qcow2",
				SizeBytes:  1 << 20,
				SHA256:     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
		},
	}
}

func TestImageSpecValidate(t *testing.T) {
	if err := validImageSpec().Validate(); err != nil {
		t.Fatalf("valid http spec rejected: %v", err)
	}
	uploadSpec := validImageSpec()
	uploadSpec.Source = ImageSource{Type: ImageSourceUpload, Location: "uploads/cirros.qcow2"}
	if err := uploadSpec.Validate(); err != nil {
		t.Fatalf("valid upload spec rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *ImageSpec)
	}{
		{"bad source type", func(s *ImageSpec) { s.Source.Type = "registry" }},
		{"empty location", func(s *ImageSpec) { s.Source.Location = "" }},
		{"bad format", func(s *ImageSpec) { s.Format = "vmdk" }},
		{"empty version", func(s *ImageSpec) { s.Version = "" }},
		{"zero size", func(s *ImageSpec) { s.DeclaredSizeBytes = 0 }},
		{"bad sha256", func(s *ImageSpec) { s.SHA256 = "ABC" }},
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

func TestImageSpecValidateRequiresContentIdentity(t *testing.T) {
	tests := []struct {
		name string
		mut  func(s *ImageSpec)
	}{
		{"empty version", func(s *ImageSpec) { s.Version = "" }},
		{"zero declared size", func(s *ImageSpec) { s.DeclaredSizeBytes = 0 }},
		{"empty sha256", func(s *ImageSpec) { s.SHA256 = "" }},
		{"short sha256", func(s *ImageSpec) { s.SHA256 = "0123" }},
		{"uppercase sha256", func(s *ImageSpec) { s.SHA256 = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := validImageSpec()
			tt.mut(&spec)
			if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("Validate() error = %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestImageSpecValidateRejectsUnknownFormat(t *testing.T) {
	spec := validImageSpec()
	spec.Format = ImageFormat("vmdk")
	if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("Validate() error = %v, want ErrInvalidSpec", err)
	}
}

func TestImageEnumsValid(t *testing.T) {
	if !ImageFormatQCOW2.Valid() || !ImageFormatRaw.Valid() || !ImageFormatISO.Valid() {
		t.Error("known formats should be valid")
	}
	if ImageFormat("vmdk").Valid() {
		t.Error("vmdk should be invalid")
	}
	if !ImageSourceUpload.Valid() || !ImageSourceHTTP.Valid() {
		t.Error("known source types should be valid")
	}
	if ImageSourceType("file").Valid() {
		t.Error("file should be invalid")
	}
	if ImageSourceType("registry").Valid() {
		t.Error("registry should be invalid")
	}
}

func TestImageCachePhaseIsSeparateCustomType(t *testing.T) {
	cachePhase := ImageCachePhaseReady
	imagePhase := ImagePhaseReady
	status := NodeCacheStatus{Phase: ImageCachePhaseReady}

	assertImageCachePhase := func(ImageCachePhase) {}
	assertImagePhase := func(ImagePhase) {}
	assertImageCachePhase(cachePhase)
	assertImageCachePhase(status.Phase)
	assertImagePhase(imagePhase)
}

func TestImageStatusValidateAcceptsKnownPhase(t *testing.T) {
	status := ImageStatus{Phase: ImagePhaseCaching}
	if err := status.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestImageStatusValidateRequiresReadyNodeCaches(t *testing.T) {
	if err := validReadyImageStatus().Validate(); err != nil {
		t.Fatalf("valid ready status rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *ImageStatus)
	}{
		{"empty observed version", func(s *ImageStatus) { s.ObservedVersion = "" }},
		{"empty observed sha256", func(s *ImageStatus) { s.ObservedSHA256 = "" }},
		{"zero observed size", func(s *ImageStatus) { s.ObservedSizeBytes = 0 }},
		{"no node caches", func(s *ImageStatus) { s.NodeCaches = nil }},
		{"non-ready node cache", func(s *ImageStatus) { s.NodeCaches[0].Phase = ImageCachePhaseCaching }},
		{"ready node cache missing path", func(s *ImageStatus) { s.NodeCaches[0].CachedPath = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := validReadyImageStatus()
			tt.mut(&status)
			if err := status.Validate(); !errors.Is(err, ErrInvalidStatus) {
				t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
			}
		})
	}
}

func TestImageStatusValidateRequiresNodeCacheTaskRef(t *testing.T) {
	tests := []struct {
		name string
		mut  func(s *NodeCacheStatus)
	}{
		{"empty task name", func(s *NodeCacheStatus) { s.TaskRef.Name = "" }},
		{"empty task uid", func(s *NodeCacheStatus) { s.TaskRef.UID = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := validReadyImageStatus().NodeCaches[0]
			tt.mut(&cache)
			if err := cache.Validate(); !errors.Is(err, ErrInvalidStatus) {
				t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
			}
		})
	}
}

func TestImageStatusValidateRejectsNodeCacheSHA256Mismatch(t *testing.T) {
	status := validReadyImageStatus()
	status.NodeCaches[0].SHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if err := status.Validate(); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
	}
}

func TestImageStatusObservedSHA256JSONTag(t *testing.T) {
	status := validReadyImageStatus()
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"observedSHA256"`)) {
		t.Fatalf("encoded status %s does not include observedSHA256", data)
	}
	if bytes.Contains(data, []byte(`"observedSha256"`)) {
		t.Fatalf("encoded status %s includes wrong observedSha256 casing", data)
	}

	var decoded ImageStatus
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ObservedSHA256 != status.ObservedSHA256 {
		t.Fatalf("decoded ObservedSHA256 = %q, want %q", decoded.ObservedSHA256, status.ObservedSHA256)
	}
}

func TestImageStatusValidateRejectsUnknownPhase(t *testing.T) {
	status := ImageStatus{Phase: ImagePhase("bogus")}
	err := status.Validate()
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
	}
}
