package apis_test

import (
	"encoding/json"
	"testing"

	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// TestEnvelopeRoundTrip proves all six first-class objects share an inline
// envelope (apiVersion/kind/metadata at top level) and survive a JSON
// marshal→unmarshal round-trip without loss. Each case marshals a fully
// populated object, asserts the shared envelope keys are present at the top
// level (proving `,inline` + metadata tag), then unmarshals back and verifies
// representative identity/spec/status fields.
func TestEnvelopeRoundTrip(t *testing.T) {
	meta := func(kind metav1.Kind, name string) (metav1.TypeMeta, metav1.ObjectMeta) {
		return metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: kind},
			metav1.ObjectMeta{Name: name, UID: "u-" + name, ResourceVersion: "42", NodeName: "node0"}
	}

	cases := []struct {
		name   string
		obj    any
		verify func(t *testing.T, b []byte)
	}{
		{
			name: "StoragePool",
			obj: func() storagepoolv1.StoragePool {
				tm, om := meta(metav1.KindStoragePool, "pool1")
				return storagepoolv1.StoragePool{
					TypeMeta:   tm,
					ObjectMeta: om,
					Spec: storagepoolv1.StoragePoolSpec{
						Backend:       storagepoolv1.BackendLocalBlock,
						Type:          storagepoolv1.PoolTypeBlock,
						StorageRoot:   "/var/lib/govirtlet/pools/p1",
						CapacityBytes: 1 << 30,
					},
					Status: storagepoolv1.StoragePoolStatus{Phase: storagepoolv1.PoolPhaseReady},
				}
			}(),
			verify: func(t *testing.T, b []byte) {
				var out storagepoolv1.StoragePool
				if err := json.Unmarshal(b, &out); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if out.Kind != metav1.KindStoragePool || out.Name != "pool1" || out.UID != "u-pool1" {
					t.Fatalf("identity mismatch: %+v", out)
				}
				if out.Spec.Backend != storagepoolv1.BackendLocalBlock || out.Spec.CapacityBytes != 1<<30 {
					t.Fatalf("spec mismatch: %+v", out.Spec)
				}
				if out.Status.Phase != storagepoolv1.PoolPhaseReady {
					t.Fatalf("status mismatch: %+v", out.Status)
				}
			},
		},
		{
			name: "Image",
			obj: func() imagev1.Image {
				tm, om := meta(metav1.KindImage, "cirros")
				return imagev1.Image{
					TypeMeta:   tm,
					ObjectMeta: om,
					Spec: imagev1.ImageSpec{
						FilePoolRef:       "files",
						Source:            imagev1.ImageSource{Type: imagev1.ImageSourceHTTP, Location: "https://example/cirros.img"},
						Format:            imagev1.ImageFormatQCOW2,
						DeclaredSizeBytes: 1 << 20,
					},
					Status: imagev1.ImageStatus{Phase: imagev1.ImagePhaseReady},
				}
			}(),
			verify: func(t *testing.T, b []byte) {
				var out imagev1.Image
				if err := json.Unmarshal(b, &out); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if out.Kind != metav1.KindImage || out.Name != "cirros" {
					t.Fatalf("identity mismatch: %+v", out)
				}
				if out.Spec.Format != imagev1.ImageFormatQCOW2 || out.Spec.Source.Type != imagev1.ImageSourceHTTP {
					t.Fatalf("spec mismatch: %+v", out.Spec)
				}
				if out.Status.Phase != imagev1.ImagePhaseReady {
					t.Fatalf("status mismatch: %+v", out.Status)
				}
			},
		},
		{
			name: "Volume",
			obj: func() volumev1.Volume {
				tm, om := meta(metav1.KindVolume, "vol-root")
				return volumev1.Volume{
					TypeMeta:   tm,
					ObjectMeta: om,
					Spec: volumev1.VolumeSpec{
						PoolRef:          "blocks",
						VMRef:            "vm-uid-1",
						VMName:           "vm1",
						DiskIndex:        0,
						CapacityBytes:    2 << 30,
						Role:             volumev1.VolumeRoleRoot,
						ImageRef:         "cirros",
						ImageFilePoolRef: "files",
					},
					Status: volumev1.VolumeStatus{Phase: volumev1.VolumePhaseReady},
				}
			}(),
			verify: func(t *testing.T, b []byte) {
				var out volumev1.Volume
				if err := json.Unmarshal(b, &out); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if out.Kind != metav1.KindVolume || out.Name != "vol-root" {
					t.Fatalf("identity mismatch: %+v", out)
				}
				if out.Spec.Role != volumev1.VolumeRoleRoot || out.Spec.ImageRef != "cirros" || out.Spec.ImageFilePoolRef != "files" {
					t.Fatalf("spec mismatch: %+v", out.Spec)
				}
				if out.Status.Phase != volumev1.VolumePhaseReady {
					t.Fatalf("status mismatch: %+v", out.Status)
				}
			},
		},
		{
			name: "Network",
			obj: func() networkv1.Network {
				tm, om := meta(metav1.KindNetwork, "net1")
				return networkv1.Network{
					TypeMeta:   tm,
					ObjectMeta: om,
					Spec: networkv1.NetworkSpec{
						BridgeName:      "govirta0",
						Subnet:          "192.168.100.0/24",
						GatewayCIDR:     "192.168.100.1/24",
						DHCPRangeStart:  "192.168.100.10",
						DHCPRangeEnd:    "192.168.100.200",
						EgressInterface: "eth0",
						DNS:             []string{"8.8.8.8"},
						LeaseSeconds:    3600,
					},
					Status: networkv1.NetworkStatus{Phase: networkv1.NetworkPhaseReady},
				}
			}(),
			verify: func(t *testing.T, b []byte) {
				var out networkv1.Network
				if err := json.Unmarshal(b, &out); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if out.Kind != metav1.KindNetwork || out.Name != "net1" {
					t.Fatalf("identity mismatch: %+v", out)
				}
				if out.Spec.BridgeName != "govirta0" || out.Spec.Subnet != "192.168.100.0/24" {
					t.Fatalf("spec mismatch: %+v", out.Spec)
				}
				if out.Status.Phase != networkv1.NetworkPhaseReady {
					t.Fatalf("status mismatch: %+v", out.Status)
				}
			},
		},
		{
			name: "NIC",
			obj: func() nicv1.NIC {
				tm, om := meta(metav1.KindNIC, "nic0")
				return nicv1.NIC{
					TypeMeta:   tm,
					ObjectMeta: om,
					Spec: nicv1.NICSpec{
						NetworkRef: "net1",
						VMRef:      "vm-uid-1",
						MAC:        "52:54:00:12:34:56",
						IP:         "192.168.100.10",
					},
					Status: nicv1.NICStatus{Phase: nicv1.NICPhaseReady},
				}
			}(),
			verify: func(t *testing.T, b []byte) {
				var out nicv1.NIC
				if err := json.Unmarshal(b, &out); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if out.Kind != metav1.KindNIC || out.Name != "nic0" {
					t.Fatalf("identity mismatch: %+v", out)
				}
				if out.Spec.MAC != "52:54:00:12:34:56" || out.Spec.IP != "192.168.100.10" {
					t.Fatalf("spec mismatch: %+v", out.Spec)
				}
				if out.Status.Phase != nicv1.NICPhaseReady {
					t.Fatalf("status mismatch: %+v", out.Status)
				}
			},
		},
		{
			name: "VM",
			obj: func() vmv1.VM {
				tm, om := meta(metav1.KindVM, "vm1")
				return vmv1.VM{
					TypeMeta:   tm,
					ObjectMeta: om,
					Spec: vmv1.VMSpec{
						Arch:       "aarch64",
						VCPUs:      2,
						MemoryMiB:  512,
						VolumeRefs: []string{"vol-root"},
						NICRefs:    []string{"nic0"},
					},
					Status: vmv1.VMStatus{Phase: vmv1.VMPhaseRunning},
				}
			}(),
			verify: func(t *testing.T, b []byte) {
				var out vmv1.VM
				if err := json.Unmarshal(b, &out); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if out.Kind != metav1.KindVM || out.Name != "vm1" {
					t.Fatalf("identity mismatch: %+v", out)
				}
				if out.Spec.VCPUs != 2 || len(out.Spec.VolumeRefs) != 1 || len(out.Spec.NICRefs) != 1 {
					t.Fatalf("spec mismatch: %+v", out.Spec)
				}
				if out.Status.Phase != vmv1.VMPhaseRunning {
					t.Fatalf("status mismatch: %+v", out.Status)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.obj)
			if err != nil {
				t.Fatalf("marshal: %v", err)
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
			tc.verify(t, b)
		})
	}
}
