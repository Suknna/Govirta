package admission

import (
	"context"
	"testing"

	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

func TestFieldPolicyCreateNoOp(t *testing.T) {
	obj := validAdmissionVM()
	obj.Spec.Arch = "aarch64"

	err := FieldPolicyValidator{}.Validate(context.Background(), Request{
		Operation: OperationCreate,
		Kind:      metav1.KindVM,
		Name:      obj.Name,
		NewObject: obj,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestFieldPolicyRejectsVMArchChange(t *testing.T) {
	old := validAdmissionVM()
	obj := old
	obj.Spec.Arch = "aarch64"

	err := validateFieldPolicyUpdate(metav1.KindVM, old.Name, old, obj)
	assertAdmissionReason(t, err, ReasonConflict)
}

// TestFieldPolicyAllowsVMColdMutableChanges asserts that cold-mutable fields
// (memoryMiB/vcpus/volumeRefs/nicRefs) may all change in one update when the
// desired power intent is Off. Under Gate 1 these changes are only accepted
// while powerState=Off, which is why the fixture sets PowerStateOff.
func TestFieldPolicyAllowsVMColdMutableChanges(t *testing.T) {
	old := validAdmissionVM()
	obj := old
	obj.Spec.MemoryMiB = 4096
	obj.Spec.VCPUs = 4
	obj.Spec.VolumeRefs = []string{"vol-a", "vol-b"}
	obj.Spec.NICRefs = []string{"nic-a", "nic-b"}
	obj.Spec.PowerState = vmv1.PowerStateOff
	obj.Spec.PowerOffMode = vmv1.PowerOffModeAcpi

	err := validateFieldPolicyUpdate(metav1.KindVM, old.Name, old, obj)
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// TestFieldPolicyVMColdMutableGate covers Gate 1: cold-mutable changes require
// powerState=Off, pure power changes are unaffected, and no-op updates pass.
func TestFieldPolicyVMColdMutableGate(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*vmv1.VM)
		wantReason ErrorReason // "" means expect accept
	}{
		{
			name: "On + memoryMiB change rejected",
			mutate: func(obj *vmv1.VM) {
				obj.Spec.PowerState = vmv1.PowerStateOn
				obj.Spec.MemoryMiB = 4096
			},
			wantReason: ReasonBadRequest,
		},
		{
			name: "Off + memoryMiB change accepted",
			mutate: func(obj *vmv1.VM) {
				obj.Spec.PowerState = vmv1.PowerStateOff
				obj.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
				obj.Spec.MemoryMiB = 4096
			},
		},
		{
			name: "Off + volumeRefs addition accepted",
			mutate: func(obj *vmv1.VM) {
				obj.Spec.PowerState = vmv1.PowerStateOff
				obj.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
				obj.Spec.VolumeRefs = []string{"vol-a", "vol-b"}
			},
		},
		{
			name: "On + volumeRefs change rejected",
			mutate: func(obj *vmv1.VM) {
				obj.Spec.PowerState = vmv1.PowerStateOn
				obj.Spec.VolumeRefs = []string{"vol-a", "vol-b"}
			},
			wantReason: ReasonBadRequest,
		},
		{
			name: "pure power change On to Off without cold-mutable change accepted",
			mutate: func(obj *vmv1.VM) {
				obj.Spec.PowerState = vmv1.PowerStateOff
				obj.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
			},
		},
		{
			name:   "no change accepted",
			mutate: func(obj *vmv1.VM) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := validAdmissionVM()
			obj := old
			tt.mutate(&obj)

			err := validateFieldPolicyUpdate(metav1.KindVM, old.Name, old, obj)
			if tt.wantReason == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			assertAdmissionReason(t, err, tt.wantReason)
		})
	}
}

func TestFieldPolicyRejectsVolumePoolRefChange(t *testing.T) {
	old := validAdmissionVolume()
	obj := old
	obj.Spec.PoolRef = "other-pool"

	err := validateFieldPolicyUpdate(metav1.KindVolume, old.Name, old, obj)
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestFieldPolicyRejectsVolumeCapacityDecrease(t *testing.T) {
	old := validAdmissionVolume()
	obj := old
	obj.Spec.CapacityBytes = old.Spec.CapacityBytes - 1

	err := validateFieldPolicyUpdate(metav1.KindVolume, old.Name, old, obj)
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestFieldPolicyAllowsVolumeCapacityIncrease(t *testing.T) {
	old := validAdmissionVolume()
	obj := old
	obj.Spec.CapacityBytes = old.Spec.CapacityBytes + 1

	err := validateFieldPolicyUpdate(metav1.KindVolume, old.Name, old, obj)
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestFieldPolicyRejectsNICMACChange(t *testing.T) {
	old := validAdmissionNIC()
	old.Spec.MAC = "02:00:00:00:00:01"
	obj := old
	obj.Spec.MAC = "02:00:00:00:00:02"

	err := validateFieldPolicyUpdate(metav1.KindNIC, old.Name, old, obj)
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestFieldPolicyRejectsNetworkSpecChange(t *testing.T) {
	old := validAdmissionNetwork()
	obj := old
	obj.Spec.EgressInterface = "eth1"

	err := validateFieldPolicyUpdate(metav1.KindNetwork, old.Name, old, obj)
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestFieldPolicyRejectsNetworkOptionOrderChange(t *testing.T) {
	old := validAdmissionNetwork()
	old.Spec.DNS = []string{"1.1.1.1", "8.8.8.8"}
	old.Spec.Router = []string{"192.168.100.1", "192.168.100.254"}

	tests := []struct {
		name   string
		mutate func(*networkv1.Network)
	}{
		{name: "dns", mutate: func(obj *networkv1.Network) { obj.Spec.DNS = []string{"8.8.8.8", "1.1.1.1"} }},
		{name: "router", mutate: func(obj *networkv1.Network) { obj.Spec.Router = []string{"192.168.100.254", "192.168.100.1"} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := old
			tt.mutate(&obj)
			err := validateFieldPolicyUpdate(metav1.KindNetwork, old.Name, old, obj)
			assertAdmissionReason(t, err, ReasonConflict)
		})
	}
}

func TestFieldPolicyRejectsImageSpecChange(t *testing.T) {
	old := validAdmissionImage()
	obj := old
	obj.Spec.DeclaredSizeBytes++

	err := validateFieldPolicyUpdate(metav1.KindImage, old.Name, old, obj)
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestFieldPolicyRejectsStoragePoolSpecChange(t *testing.T) {
	old := validAdmissionStoragePool()
	obj := old
	obj.Spec.CapacityBytes++

	err := validateFieldPolicyUpdate(metav1.KindStoragePool, old.Name, old, obj)
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestFieldPolicyRejectsSnapshotSpecChange(t *testing.T) {
	old := validAdmissionSnapshot()
	obj := old
	obj.Spec.VMRef = "other-vm"

	err := validateFieldPolicyUpdate(metav1.KindSnapshot, old.Name, old, obj)
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestFieldPolicyAllowsSnapshotUnchangedSpec(t *testing.T) {
	old := validAdmissionSnapshot()
	obj := old

	err := validateFieldPolicyUpdate(metav1.KindSnapshot, old.Name, old, obj)
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func validateFieldPolicyUpdate(kind metav1.Kind, name string, oldObj, newObj any) error {
	return FieldPolicyValidator{}.Validate(context.Background(), Request{
		Operation: OperationUpdate,
		Kind:      kind,
		Name:      name,
		OldRaw:    []byte(`{}`),
		OldObject: oldObj,
		NewObject: newObj,
	})
}

func validAdmissionStoragePool() storagepoolv1.StoragePool {
	return storagepoolv1.StoragePool{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindStoragePool},
		ObjectMeta: metav1.ObjectMeta{Name: "pool-a", UID: "uid-pool-a"},
		Spec: storagepoolv1.StoragePoolSpec{
			Backend:       storagepoolv1.BackendLocalFile,
			Type:          storagepoolv1.PoolTypeFile,
			StorageRoot:   "/var/lib/govirta/pool-a",
			CapacityBytes: 1 << 30,
		},
	}
}

func validAdmissionImage() imagev1.Image {
	return imagev1.Image{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindImage},
		ObjectMeta: metav1.ObjectMeta{Name: "img-a", UID: "uid-img-a"},
		Spec: imagev1.ImageSpec{
			FilePoolRef:       "pool-a",
			Source:            imagev1.ImageSource{Type: imagev1.ImageSourceFile, Location: "/srv/images/base.qcow2"},
			Format:            imagev1.ImageFormatQCOW2,
			DeclaredSizeBytes: 1 << 28,
		},
	}
}

func validAdmissionVolume() volumev1.Volume {
	return volumev1.Volume{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindVolume},
		ObjectMeta: metav1.ObjectMeta{Name: "vol-a", UID: "uid-vol-a"},
		Spec: volumev1.VolumeSpec{
			PoolRef:          "block-pool",
			VMRef:            "uid-vm-a",
			VMName:           "vm-a",
			DiskIndex:        0,
			CapacityBytes:    1 << 30,
			Role:             volumev1.VolumeRoleRoot,
			ImageRef:         "img-a",
			ImageFilePoolRef: "pool-a",
		},
	}
}

func validAdmissionNetwork() networkv1.Network {
	return networkv1.Network{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindNetwork},
		ObjectMeta: metav1.ObjectMeta{Name: "net-a", UID: "uid-net-a"},
		Spec: networkv1.NetworkSpec{
			BridgeName:      "br-a",
			Subnet:          "192.168.100.0/24",
			GatewayCIDR:     "192.168.100.1/24",
			DHCPRangeStart:  "192.168.100.10",
			DHCPRangeEnd:    "192.168.100.200",
			EgressInterface: "eth0",
			DNS:             []string{"1.1.1.1"},
			Router:          []string{"192.168.100.1"},
			LeaseSeconds:    3600,
		},
	}
}
