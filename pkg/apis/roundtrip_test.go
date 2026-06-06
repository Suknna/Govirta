package apis_test

import (
	"encoding/json"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

// TestEnvelopeRoundTrip proves the shared envelope marshals inline and survives
// a JSON round-trip on a representative object (VM).
func TestEnvelopeRoundTrip(t *testing.T) {
	in := vmv1.VM{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindVM},
		ObjectMeta: metav1.ObjectMeta{Name: "vm1", UID: "u1", ResourceVersion: "42", NodeName: "node0"},
		Spec: vmv1.VMSpec{
			Arch: "aarch64", VCPUs: 2, MemoryMiB: 512,
			VolumeRefs: []string{"vol-root"}, NICRefs: []string{"nic0"},
		},
		Status: vmv1.VMStatus{Phase: vmv1.VMPhaseRunning},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out vmv1.VM
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Kind != metav1.KindVM || out.Name != "vm1" || out.Spec.VCPUs != 2 || out.Status.Phase != vmv1.VMPhaseRunning {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
	// Envelope must be inline: apiVersion/kind/metadata/spec/status at top level.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("raw unmarshal: %v", err)
	}
	for _, k := range []string{"apiVersion", "kind", "metadata", "spec", "status"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("missing top-level key %q in %s", k, b)
		}
	}
}
