package apis_test

import (
	"encoding/json"
	"testing"

	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

func intPtr(value int) *int {
	return &value
}

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
						Source:            imagev1.ImageSource{Type: imagev1.ImageSourceHTTP, Location: "https://example/cirros.img"},
						Format:            imagev1.ImageFormatQCOW2,
						Version:           "2026.06.13",
						DeclaredSizeBytes: 1 << 20,
						SHA256:            "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
					},
					Status: imagev1.ImageStatus{
						Phase:             imagev1.ImagePhaseReady,
						ObservedVersion:   "2026.06.13",
						ObservedSHA256:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
						ObservedSizeBytes: 1 << 20,
						NodeCaches: []imagev1.NodeCacheStatus{{
							NodeName:   "node0",
							Phase:      imagev1.ImageCachePhaseReady,
							TaskRef:    imagev1.TaskRef{Name: "task-cache-cirros", UID: "task-uid"},
							CachedPath: "/var/lib/govirta/images/cirros.qcow2",
							SizeBytes:  1 << 20,
							SHA256:     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
						}},
					},
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
				if out.Spec.Source.Type != imagev1.ImageSourceHTTP || out.Spec.Source.Location != "https://example/cirros.img" {
					t.Fatalf("source mismatch: %+v", out.Spec.Source)
				}
				if out.Spec.Format != imagev1.ImageFormatQCOW2 || out.Spec.Version != "2026.06.13" || out.Spec.DeclaredSizeBytes != 1<<20 || out.Spec.SHA256 != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
					t.Fatalf("spec mismatch: %+v", out.Spec)
				}
				if out.Status.Phase != imagev1.ImagePhaseReady || out.Status.ObservedVersion != "2026.06.13" || out.Status.ObservedSHA256 != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" || out.Status.ObservedSizeBytes != 1<<20 || len(out.Status.NodeCaches) != 1 {
					t.Fatalf("status mismatch: %+v", out.Status)
				}
				cache := out.Status.NodeCaches[0]
				if cache.NodeName != "node0" || cache.Phase != imagev1.ImageCachePhaseReady || cache.TaskRef.Name != "task-cache-cirros" || cache.TaskRef.UID != "task-uid" || cache.CachedPath != "/var/lib/govirta/images/cirros.qcow2" || cache.SizeBytes != 1<<20 || cache.SHA256 != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
					t.Fatalf("node cache mismatch: %+v", cache)
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
						PoolRef:       "blocks",
						VMRef:         "vm-uid-1",
						VMName:        "vm1",
						DiskIndex:     0,
						CapacityBytes: 2 << 30,
						Role:          volumev1.VolumeRoleRoot,
						ImageRef:      "cirros",
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
				if out.Spec.Role != volumev1.VolumeRoleRoot || out.Spec.ImageRef != "cirros" {
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
						CDROMImageRefs: []vmv1.CDROMImageRef{
							{ImageRef: "installer", BootIndexMode: vmv1.BootIndexModeIndex, BootIndex: intPtr(1)},
						},
						PowerState: vmv1.PowerStateOn,
					},
					Status: vmv1.VMStatus{
						Phase:              vmv1.VMPhaseRunning,
						ObservedPowerState: vmv1.ObservedPowerStateOn,
						PowerTransition:    vmv1.PowerTransitionNone,
					},
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
				if out.Spec.VCPUs != 2 || len(out.Spec.VolumeRefs) != 1 || len(out.Spec.NICRefs) != 1 || out.Spec.PowerState != vmv1.PowerStateOn {
					t.Fatalf("spec mismatch: %+v", out.Spec)
				}
				if len(out.Spec.CDROMImageRefs) != 1 {
					t.Fatalf("cdrom image refs = %+v, want one ref", out.Spec.CDROMImageRefs)
				}
				cdrom := out.Spec.CDROMImageRefs[0]
				if cdrom.ImageRef != "installer" || cdrom.BootIndexMode != vmv1.BootIndexModeIndex || cdrom.BootIndex == nil || *cdrom.BootIndex != 1 {
					t.Fatalf("cdrom image ref mismatch: %+v", cdrom)
				}
				if out.Status.Phase != vmv1.VMPhaseRunning || out.Status.ObservedPowerState != vmv1.ObservedPowerStateOn || out.Status.PowerTransition != vmv1.PowerTransitionNone {
					t.Fatalf("status mismatch: %+v", out.Status)
				}
			},
		},
		{
			name: "Task",
			obj: func() taskv1.Task {
				input, err := json.Marshal(taskv1.NoopInput{Marker: "phase-one"})
				if err != nil {
					t.Fatalf("marshal task input: %v", err)
				}
				tm, om := meta(metav1.KindTask, "task1")
				return taskv1.Task{
					TypeMeta:   tm,
					ObjectMeta: om,
					Spec: taskv1.TaskSpec{
						Scope:     taskv1.TaskScopeNode,
						OwnerKind: metav1.KindTask,
						OwnerName: "phase-one-owner",
						OwnerUID:  "phase-one-owner-uid",
						Operation: taskv1.TaskOperationNoopNode,
						Input:     input,
					},
					Status: taskv1.TaskStatus{Phase: taskv1.TaskPhasePending, ErrorClass: taskv1.TaskErrorClassNone},
				}
			}(),
			verify: func(t *testing.T, b []byte) {
				var out taskv1.Task
				if err := json.Unmarshal(b, &out); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if out.Kind != metav1.KindTask || out.Name != "task1" {
					t.Fatalf("identity mismatch: %+v", out)
				}
				if out.Spec.Scope != taskv1.TaskScopeNode || out.Spec.Operation != taskv1.TaskOperationNoopNode {
					t.Fatalf("spec mismatch: %+v", out.Spec)
				}
				if out.Status.Phase != taskv1.TaskPhasePending {
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

// TestObjectMetaDeletionRoundTrip proves the刀1 finalizer 两阶段删除 envelope
// fields survive a JSON marshal→unmarshal round-trip without loss: a fully
// populated StoragePool carrying DeletionTimestamp + a typed Finalizer must
// come back byte-for-byte equal in those fields. This guards Task 2 of the
// delete-lifecycle plan, on which the apiserver inject / DELETE handler /
// node teardown controller all depend.
func TestObjectMetaDeletionRoundTrip(t *testing.T) {
	in := storagepoolv1.StoragePool{
		TypeMeta: metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindStoragePool},
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pool-deleting",
			UID:               "u-pool-deleting",
			ResourceVersion:   "99",
			NodeName:          "node0",
			DeletionTimestamp: "2026-06-07T10:00:00Z",
			Finalizers:        []metav1.Finalizer{metav1.FinalizerNodeTeardown},
		},
		Spec: storagepoolv1.StoragePoolSpec{
			Backend:       storagepoolv1.BackendLocalBlock,
			Type:          storagepoolv1.PoolTypeBlock,
			StorageRoot:   "/var/lib/govirtlet/pools/p1",
			CapacityBytes: 1 << 30,
		},
		Status: storagepoolv1.StoragePoolStatus{Phase: storagepoolv1.PoolPhaseReady},
	}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The new fields must surface inside the inline metadata envelope.
	var raw struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("raw unmarshal: %v", err)
	}
	for _, k := range []string{"deletionTimestamp", "finalizers"} {
		if _, ok := raw.Metadata[k]; !ok {
			t.Fatalf("missing metadata key %q in %s", k, b)
		}
	}

	var out storagepoolv1.StoragePool
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.DeletionTimestamp != in.DeletionTimestamp {
		t.Fatalf("deletionTimestamp mismatch: got %q want %q", out.DeletionTimestamp, in.DeletionTimestamp)
	}
	if len(out.Finalizers) != 1 || out.Finalizers[0] != metav1.FinalizerNodeTeardown {
		t.Fatalf("finalizers mismatch: got %+v want [%q]", out.Finalizers, metav1.FinalizerNodeTeardown)
	}
}
