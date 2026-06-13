package admission

import (
	"testing"

	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

func TestObjectHelpersAcceptFullResourceObjects(t *testing.T) {
	tests := []struct {
		name string
		obj  any
		kind metav1.Kind
	}{
		{
			name: "pool-a",
			kind: metav1.KindStoragePool,
			obj: storagepoolv1.StoragePool{
				TypeMeta:   typeMeta(metav1.KindStoragePool),
				ObjectMeta: objectMeta("pool-a"),
				Spec: storagepoolv1.StoragePoolSpec{
					Backend:       storagepoolv1.BackendLocalBlock,
					Type:          storagepoolv1.PoolTypeBlock,
					StorageRoot:   "/var/lib/govirta/pools/pool-a",
					CapacityBytes: 1024,
				},
				Status: storagepoolv1.StoragePoolStatus{Phase: storagepoolv1.PoolPhaseReady},
			},
		},
		{
			name: "image-a",
			kind: metav1.KindImage,
			obj: imagev1.Image{
				TypeMeta:   typeMeta(metav1.KindImage),
				ObjectMeta: objectMeta("image-a"),
				Spec: imagev1.ImageSpec{
					Source: imagev1.ImageSource{
						Type:     imagev1.ImageSourceHTTP,
						Location: "https://images.example/base.qcow2",
					},
					Format:            imagev1.ImageFormatQCOW2,
					Version:           "v1",
					DeclaredSizeBytes: 1024,
					SHA256:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
				Status: validReadyImageStatus(),
			},
		},
		{
			name: "volume-a",
			kind: metav1.KindVolume,
			obj: volumev1.Volume{
				TypeMeta:   typeMeta(metav1.KindVolume),
				ObjectMeta: objectMeta("volume-a"),
				Spec: volumev1.VolumeSpec{
					PoolRef:       "block-pool-a",
					VMRef:         "vm-uid-a",
					VMName:        "vm-a",
					DiskIndex:     0,
					CapacityBytes: 1024,
					Role:          volumev1.VolumeRoleRoot,
					ImageRef:      "image-a",
				},
				Status: volumev1.VolumeStatus{Phase: volumev1.VolumePhaseReady},
			},
		},
		{
			name: "network-a",
			kind: metav1.KindNetwork,
			obj: networkv1.Network{
				TypeMeta:   typeMeta(metav1.KindNetwork),
				ObjectMeta: objectMeta("network-a"),
				Spec: networkv1.NetworkSpec{
					BridgeName:      "gvbr0",
					Subnet:          "192.168.100.0/24",
					GatewayCIDR:     "192.168.100.1/24",
					DHCPRangeStart:  "192.168.100.10",
					DHCPRangeEnd:    "192.168.100.100",
					EgressInterface: "en0",
					DNS:             []string{"1.1.1.1"},
					Router:          []string{"192.168.100.1"},
				},
				Status: networkv1.NetworkStatus{Phase: networkv1.NetworkPhaseReady},
			},
		},
		{
			name: "nic-a",
			kind: metav1.KindNIC,
			obj: nicv1.NIC{
				TypeMeta:   typeMeta(metav1.KindNIC),
				ObjectMeta: objectMeta("nic-a"),
				Spec: nicv1.NICSpec{
					NetworkRef: "network-a",
					VMRef:      "vm-uid-a",
					MAC:        "02:00:00:00:00:01",
					IP:         "192.168.100.20",
					Hostname:   "vm-a",
				},
				Status: nicv1.NICStatus{Phase: nicv1.NICPhaseReady},
			},
		},
		{
			name: "vm-a",
			kind: metav1.KindVM,
			obj: vmv1.VM{
				TypeMeta:   typeMeta(metav1.KindVM),
				ObjectMeta: objectMeta("vm-a"),
				Spec: vmv1.VMSpec{
					Arch:       "aarch64",
					VCPUs:      2,
					MemoryMiB:  1024,
					VolumeRefs: []string{"volume-a"},
					NICRefs:    []string{"nic-a"},
					PowerState: vmv1.PowerStateOn,
				},
				Status: vmv1.VMStatus{
					Phase:              vmv1.VMPhaseRunning,
					ObservedPowerState: vmv1.ObservedPowerStateOn,
					PowerTransition:    vmv1.PowerTransitionNone,
				},
			},
		},
		{
			name: "snapshot-a",
			kind: metav1.KindSnapshot,
			obj: snapshotv1.Snapshot{
				TypeMeta:   typeMeta(metav1.KindSnapshot),
				ObjectMeta: objectMeta("snapshot-a"),
				Spec:       snapshotv1.SnapshotSpec{VMRef: "vm-a"},
				Status:     snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhaseReady},
			},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			meta, err := Metadata(tt.obj)
			if err != nil {
				t.Fatalf("Metadata() error = %v, want nil", err)
			}
			if meta.Name != tt.name {
				t.Fatalf("Metadata().Name = %q, want %q", meta.Name, tt.name)
			}

			tm, err := TypeMeta(tt.obj)
			if err != nil {
				t.Fatalf("TypeMeta() error = %v, want nil", err)
			}
			if tm.Kind != tt.kind {
				t.Fatalf("TypeMeta().Kind = %q, want %q", tm.Kind, tt.kind)
			}

			spec, err := Spec(tt.obj)
			if err != nil {
				t.Fatalf("Spec() error = %v, want nil", err)
			}
			if err := spec.Validate(); err != nil {
				t.Fatalf("Spec().Validate() error = %v, want nil", err)
			}

			status, err := Status(tt.obj)
			if err != nil {
				t.Fatalf("Status() error = %v, want nil", err)
			}
			if err := status.Validate(); err != nil {
				t.Fatalf("Status().Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestStatusAcceptsBareStatusObjects(t *testing.T) {
	tests := []struct {
		name string
		obj  any
	}{
		{name: "storagepool", obj: storagepoolv1.StoragePoolStatus{Phase: storagepoolv1.PoolPhaseReady}},
		{name: "image", obj: validReadyImageStatus()},
		{name: "volume", obj: volumev1.VolumeStatus{Phase: volumev1.VolumePhaseReady}},
		{name: "network", obj: networkv1.NetworkStatus{Phase: networkv1.NetworkPhaseReady}},
		{name: "nic", obj: nicv1.NICStatus{Phase: nicv1.NICPhaseReady}},
		{name: "vm", obj: vmv1.VMStatus{Phase: vmv1.VMPhaseRunning, ObservedPowerState: vmv1.ObservedPowerStateOn, PowerTransition: vmv1.PowerTransitionNone}},
		{name: "snapshot", obj: snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhaseReady}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, err := Status(tt.obj)
			if err != nil {
				t.Fatalf("Status() error = %v, want nil", err)
			}
			if err := status.Validate(); err != nil {
				t.Fatalf("Status().Validate() error = %v, want nil", err)
			}
		})
	}
}

func validReadyImageStatus() imagev1.ImageStatus {
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return imagev1.ImageStatus{
		Phase:             imagev1.ImagePhaseReady,
		ObservedVersion:   "v1",
		ObservedSHA256:    sha,
		ObservedSizeBytes: 1024,
		NodeCaches: []imagev1.NodeCacheStatus{{
			NodeName:   "node-a",
			Phase:      imagev1.ImageCachePhaseReady,
			TaskRef:    imagev1.TaskRef{Name: "task-a", UID: "uid-task-a"},
			CachedPath: "/var/lib/govirta/images/image-a/v1",
			SizeBytes:  1024,
			SHA256:     sha,
		}},
	}
}

func TestObjectHelpersAcceptPointerObjects(t *testing.T) {
	obj := vmv1.VM{
		TypeMeta:   typeMeta(metav1.KindVM),
		ObjectMeta: objectMeta("vm-pointer"),
		Spec: vmv1.VMSpec{
			Arch:       "aarch64",
			VCPUs:      2,
			MemoryMiB:  1024,
			VolumeRefs: []string{"volume-a"},
			NICRefs:    []string{"nic-a"},
			PowerState: vmv1.PowerStateOn,
		},
		Status: vmv1.VMStatus{
			Phase:              vmv1.VMPhaseRunning,
			ObservedPowerState: vmv1.ObservedPowerStateOn,
			PowerTransition:    vmv1.PowerTransitionNone,
		},
	}

	meta, err := Metadata(&obj)
	if err != nil {
		t.Fatalf("Metadata() error = %v, want nil", err)
	}
	if meta.Name != "vm-pointer" {
		t.Fatalf("Metadata().Name = %q, want vm-pointer", meta.Name)
	}

	tm, err := TypeMeta(&obj)
	if err != nil {
		t.Fatalf("TypeMeta() error = %v, want nil", err)
	}
	if tm.Kind != metav1.KindVM {
		t.Fatalf("TypeMeta().Kind = %q, want %q", tm.Kind, metav1.KindVM)
	}

	spec, err := Spec(&obj)
	if err != nil {
		t.Fatalf("Spec() error = %v, want nil", err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("Spec().Validate() error = %v, want nil", err)
	}

	status, err := Status(&obj)
	if err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("Status().Validate() error = %v, want nil", err)
	}
}

func TestStatusAcceptsBareStatusPointers(t *testing.T) {
	status := vmv1.VMStatus{
		Phase:              vmv1.VMPhaseRunning,
		ObservedPowerState: vmv1.ObservedPowerStateOn,
		PowerTransition:    vmv1.PowerTransitionNone,
	}

	validator, err := Status(&status)
	if err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}
	if err := validator.Validate(); err != nil {
		t.Fatalf("Status().Validate() error = %v, want nil", err)
	}
}

func TestObjectHelpersAcceptSnapshotPointer(t *testing.T) {
	obj := snapshotv1.Snapshot{
		TypeMeta:   typeMeta(metav1.KindSnapshot),
		ObjectMeta: objectMeta("snapshot-pointer"),
		Spec:       snapshotv1.SnapshotSpec{VMRef: "vm-a"},
		Status:     snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhaseReady},
	}

	meta, err := Metadata(&obj)
	if err != nil {
		t.Fatalf("Metadata() error = %v, want nil", err)
	}
	if meta.Name != "snapshot-pointer" {
		t.Fatalf("Metadata().Name = %q, want snapshot-pointer", meta.Name)
	}

	tm, err := TypeMeta(&obj)
	if err != nil {
		t.Fatalf("TypeMeta() error = %v, want nil", err)
	}
	if tm.Kind != metav1.KindSnapshot {
		t.Fatalf("TypeMeta().Kind = %q, want %q", tm.Kind, metav1.KindSnapshot)
	}

	spec, err := Spec(&obj)
	if err != nil {
		t.Fatalf("Spec() error = %v, want nil", err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("Spec().Validate() error = %v, want nil", err)
	}

	status, err := Status(&obj)
	if err != nil {
		t.Fatalf("Status() error = %v, want nil", err)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("Status().Validate() error = %v, want nil", err)
	}

	bareStatus := snapshotv1.SnapshotStatus{Phase: snapshotv1.SnapshotPhaseReady}
	validator, err := Status(&bareStatus)
	if err != nil {
		t.Fatalf("Status(&SnapshotStatus) error = %v, want nil", err)
	}
	if err := validator.Validate(); err != nil {
		t.Fatalf("Status(&SnapshotStatus).Validate() error = %v, want nil", err)
	}
}

func TestObjectHelpersRejectNilPointers(t *testing.T) {
	var vm *vmv1.VM
	if _, err := Metadata(vm); err == nil {
		t.Fatalf("Metadata(nil *VM) error = nil, want unsupported nil pointer error")
	}

	var status *vmv1.VMStatus
	if _, err := Status(status); err == nil {
		t.Fatalf("Status(nil *VMStatus) error = nil, want unsupported nil pointer error")
	}
}

func TestObjectHelpersRejectUnknownObjectTypes(t *testing.T) {
	unknown := struct{}{}
	tests := []struct {
		name string
		call func() error
	}{
		{name: "metadata", call: func() error { _, err := Metadata(unknown); return err }},
		{name: "typeMeta", call: func() error { _, err := TypeMeta(unknown); return err }},
		{name: "spec", call: func() error { _, err := Spec(unknown); return err }},
		{name: "status", call: func() error { _, err := Status(unknown); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err == nil {
				t.Fatalf("helper error = nil, want unsupported type error")
			}
		})
	}
}

func typeMeta(kind metav1.Kind) metav1.TypeMeta {
	return metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: kind}
}

func objectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, UID: name + "-uid"}
}
