package admission

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/suknna/govirta/internal/controlplane/store/fake"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

func TestReferenceValidatorRejectsMissingVMVolumeRef(t *testing.T) {
	st := newReferenceTestStore(t)
	seedReferenceObject(t, st, validAdmissionNIC())

	obj := validAdmissionVM()
	err := ReferenceValidator{Store: st}.Validate(context.Background(), Request{
		Operation: OperationCreate,
		Kind:      metav1.KindVM,
		Name:      obj.Name,
		NewObject: obj,
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
}

func TestReferenceValidatorRejectsDeletingNetworkRef(t *testing.T) {
	st := newReferenceTestStore(t)
	seedReferenceObject(t, st, vmWithNameAndUID("stored-vm", "uid-vm-a"))
	network := validAdmissionNetwork()
	network.DeletionTimestamp = "2026-06-09T00:00:00Z"
	seedReferenceObject(t, st, network)

	obj := validAdmissionNIC()
	err := ReferenceValidator{Store: st}.Validate(context.Background(), Request{
		Operation: OperationCreate,
		Kind:      metav1.KindNIC,
		Name:      obj.Name,
		NewObject: obj,
	})
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestReferenceValidatorRejectsMissingVolumeVMUID(t *testing.T) {
	st := newReferenceTestStore(t)
	seedReferenceObject(t, st, blockAdmissionStoragePool("block-pool"))
	seedReferenceObject(t, st, validAdmissionStoragePool())
	seedReferenceObject(t, st, validAdmissionImage())
	seedReferenceObject(t, st, vmWithNameAndUID("vm-name-does-not-match", "uid-other-vm"))

	obj := validAdmissionVolume()
	err := ReferenceValidator{Store: st}.Validate(context.Background(), Request{
		Operation: OperationCreate,
		Kind:      metav1.KindVolume,
		Name:      obj.Name,
		NewObject: obj,
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
}

func TestReferenceValidatorAllowsReadyReferenceGraph(t *testing.T) {
	st := newReferenceTestStore(t)
	seedReferenceObject(t, st, validAdmissionStoragePool())
	seedReferenceObject(t, st, blockAdmissionStoragePool("block-pool"))
	seedReferenceObject(t, st, validAdmissionImage())
	seedReferenceObject(t, st, validAdmissionNetwork())
	seedReferenceObject(t, st, vmWithNameAndUID("vm-stored-by-name", "uid-vm-a"))
	seedReferenceObject(t, st, validAdmissionVolume())
	seedReferenceObject(t, st, validAdmissionNIC())

	cases := []struct {
		name string
		kind metav1.Kind
		obj  any
	}{
		{name: "image", kind: metav1.KindImage, obj: validAdmissionImage()},
		{name: "volume", kind: metav1.KindVolume, obj: validAdmissionVolume()},
		{name: "nic", kind: metav1.KindNIC, obj: validAdmissionNIC()},
		{name: "vm", kind: metav1.KindVM, obj: validAdmissionVM()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := Metadata(tc.obj)
			if err != nil {
				t.Fatalf("metadata: %v", err)
			}
			err = ReferenceValidator{Store: st}.Validate(context.Background(), Request{
				Operation: OperationUpdate,
				Kind:      tc.kind,
				Name:      meta.Name,
				OldRaw:    []byte(`{}`),
				OldObject: tc.obj,
				NewObject: tc.obj,
			})
			if err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func newReferenceTestStore(t *testing.T) *fake.Store {
	t.Helper()
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return st
}

func seedReferenceObject(t *testing.T, st *fake.Store, obj any) {
	t.Helper()
	meta, err := Metadata(obj)
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
	typeMeta, err := TypeMeta(obj)
	if err != nil {
		t.Fatalf("type metadata: %v", err)
	}
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal %s/%s: %v", typeMeta.Kind, meta.Name, err)
	}
	if _, err := st.Put(context.Background(), StoreKey(typeMeta.Kind, meta.Name), data, ""); err != nil {
		t.Fatalf("seed %s/%s: %v", typeMeta.Kind, meta.Name, err)
	}
}

func blockAdmissionStoragePool(name string) any {
	pool := validAdmissionStoragePool()
	pool.Name = name
	pool.UID = "uid-" + name
	pool.Spec.Type = "block"
	pool.Spec.StorageRoot = "/var/lib/govirta/" + name
	return pool
}

func vmWithNameAndUID(name, uid string) vmv1.VM {
	vm := validAdmissionVM()
	vm.Name = name
	vm.UID = uid
	return vm
}

func dataAdmissionVolume(name string) volumev1.Volume {
	volume := validAdmissionVolume()
	volume.Name = name
	volume.UID = "uid-" + name
	volume.Spec.Role = volumev1.VolumeRoleData
	volume.Spec.ImageRef = ""
	volume.Spec.ImageFilePoolRef = ""
	return volume
}

func admissionNIC(name string) nicv1.NIC {
	nic := validAdmissionNIC()
	nic.Name = name
	nic.UID = "uid-" + name
	return nic
}
