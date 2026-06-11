package admission

import (
	"context"
	"errors"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

func TestEnvelopeValidatorRejectsUserFinalizersOnCreate(t *testing.T) {
	obj := validAdmissionVM()
	obj.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}

	err := EnvelopeValidator{}.Validate(context.Background(), Request{
		Operation: OperationCreate,
		Kind:      metav1.KindVM,
		Name:      obj.Name,
		NewObject: obj,
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
}

func TestSpecValidatorRunsSpecValidate(t *testing.T) {
	obj := validAdmissionVM()
	obj.Spec.VCPUs = 0

	err := SpecValidator{}.Validate(context.Background(), Request{NewObject: obj})
	assertAdmissionReason(t, err, ReasonBadRequest)
	if !errors.Is(err, vmv1.ErrInvalidSpec) {
		t.Fatalf("Validate() error = %v, want vm invalid spec cause", err)
	}
}

func TestSpecValidatorChecksNetworkRangeConsistency(t *testing.T) {
	obj := networkv1.Network{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindNetwork},
		ObjectMeta: metav1.ObjectMeta{Name: "net-a", UID: "uid-net-a"},
		Spec: networkv1.NetworkSpec{
			BridgeName:      "br-a",
			Subnet:          "192.168.100.0/24",
			GatewayCIDR:     "192.168.100.1/24",
			DHCPRangeStart:  "192.168.100.200",
			DHCPRangeEnd:    "192.168.100.10",
			EgressInterface: "eth0",
		},
	}

	err := SpecValidator{}.Validate(context.Background(), Request{NewObject: obj})
	assertAdmissionReason(t, err, ReasonBadRequest)
}

// TestPreApplyChainRejectsCreateOffMissingPowerOffMode proves the On↔powerOffMode
// conditional-required rule reaches callers through the real apply entry chain
// (SpecValidator's VMSpec.Validate), not via VMPowerStateValidator, which no
// longer does powerState value checks.
func TestPreApplyChainRejectsCreateOffMissingPowerOffMode(t *testing.T) {
	st := newReferenceTestStore(t)
	obj := validAdmissionVM()
	obj.Spec.PowerState = vmv1.PowerStateOff
	obj.Spec.PowerOffMode = ""

	err := PreApplyChain(st).Validate(context.Background(), Request{
		Operation: OperationCreate,
		Kind:      metav1.KindVM,
		Name:      obj.Name,
		NewObject: obj,
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
	if !errors.Is(err, vmv1.ErrInvalidSpec) {
		t.Fatalf("Validate() error = %v, want vm invalid spec cause", err)
	}
}

// TestPreApplyChainRejectsCreateOnWithPowerOffMode proves the inverse half of the
// conditional-required rule (powerOffMode must be empty when powerState is On)
// is also enforced through the full apply chain.
func TestPreApplyChainRejectsCreateOnWithPowerOffMode(t *testing.T) {
	st := newReferenceTestStore(t)
	obj := validAdmissionVM()
	obj.Spec.PowerState = vmv1.PowerStateOn
	obj.Spec.PowerOffMode = vmv1.PowerOffModeAcpi

	err := PreApplyChain(st).Validate(context.Background(), Request{
		Operation: OperationCreate,
		Kind:      metav1.KindVM,
		Name:      obj.Name,
		NewObject: obj,
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
	if !errors.Is(err, vmv1.ErrInvalidSpec) {
		t.Fatalf("Validate() error = %v, want vm invalid spec cause", err)
	}
}

func TestNICFinalMACValidatorRejectsInvalidFinalMAC(t *testing.T) {
	obj := validAdmissionNIC()
	obj.Spec.MAC = "not-a-mac"

	err := NICFinalMACValidator{}.Validate(context.Background(), Request{
		Operation: OperationCreate,
		Kind:      metav1.KindNIC,
		Name:      obj.Name,
		NewObject: obj,
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
}

func TestEnvelopeValidatorRejectsUpdateUIDChangeWithConflict(t *testing.T) {
	old := validAdmissionVM()
	old.ResourceVersion = "7"
	obj := old
	obj.UID = "uid-other"

	err := EnvelopeValidator{}.Validate(context.Background(), Request{
		Operation: OperationUpdate,
		Kind:      metav1.KindVM,
		Name:      obj.Name,
		OldRaw:    []byte(`{}`),
		OldObject: old,
		NewObject: obj,
	})
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestVMPowerStateValidatorRejectsNodeNameMismatchWithConflict(t *testing.T) {
	old := validAdmissionVM()
	old.NodeName = "node-1"
	obj := old
	obj.NodeName = "node-2"

	err := VMPowerStateValidator{}.Validate(context.Background(), Request{
		Operation: OperationUpdate,
		Kind:      metav1.KindVM,
		Name:      obj.Name,
		OldRaw:    []byte(`{}`),
		OldObject: old,
		NewObject: obj,
	})
	assertAdmissionReason(t, err, ReasonConflict)
}

func assertAdmissionReason(t *testing.T, err error, reason ErrorReason) {
	t.Helper()
	var admissionErr *Error
	if !errors.As(err, &admissionErr) {
		t.Fatalf("Validate() error = %v, want admission error", err)
	}
	if admissionErr.Reason != reason {
		t.Fatalf("Reason = %q, want %q", admissionErr.Reason, reason)
	}
}

func validAdmissionVM() vmv1.VM {
	return vmv1.VM{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindVM},
		ObjectMeta: metav1.ObjectMeta{Name: "vm-a", UID: "uid-vm-a"},
		Spec: vmv1.VMSpec{
			Arch:       "x86_64",
			VCPUs:      2,
			MemoryMiB:  2048,
			VolumeRefs: []string{"vol-a"},
			NICRefs:    []string{"nic-a"},
			PowerState: vmv1.PowerStateOn,
		},
	}
}

func validAdmissionNIC() nicv1.NIC {
	return nicv1.NIC{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindNIC},
		ObjectMeta: metav1.ObjectMeta{Name: "nic-a", UID: "uid-nic-a"},
		Spec: nicv1.NICSpec{
			NetworkRef: "net-a",
			VMRef:      "uid-vm-a",
			IP:         "192.168.100.50",
		},
	}
}

func validAdmissionSnapshot() snapshotv1.Snapshot {
	return snapshotv1.Snapshot{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindSnapshot},
		ObjectMeta: metav1.ObjectMeta{Name: "snap-a", UID: "uid-snap-a"},
		Spec:       snapshotv1.SnapshotSpec{VMRef: "vm-a"},
	}
}
