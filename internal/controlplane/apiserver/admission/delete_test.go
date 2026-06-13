package admission

import (
	"context"
	"strings"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// deleteRequest builds a DELETE admission request the way the handler does: the
// target's decoded metadata travels in OldObject. All kinds match downstream
// dependents by Name; the UID field is retained in the helper signature for
// callers that still seed it but no reverse edge consumes it.
func deleteRequest(kind metav1.Kind, name, uid string) Request {
	return Request{
		Operation: OperationDelete,
		Kind:      kind,
		Name:      name,
		OldObject: metav1.ObjectMeta{Name: name, UID: uid},
	}
}

// TestDeleteReferenceValidatorRejectsStoragePoolReferencedByVolumePoolRef proves
// the Volume.poolRef edge is scanned and reported before Image.
func TestDeleteReferenceValidatorRejectsStoragePoolReferencedByVolumePoolRef(t *testing.T) {
	st := newReferenceTestStore(t)
	vol := dataAdmissionVolume("vol-on-pool")
	vol.Spec.PoolRef = "block-pool"
	seedReferenceObject(t, st, vol)

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindStoragePool, "block-pool", "uid-block-pool"))
	assertAdmissionReason(t, err, ReasonConflict)
	assertReferencedBy(t, err, "Volume/vol-on-pool")
}

func TestDeleteReferenceValidatorIgnoresLegacyVolumeImageFilePoolRef(t *testing.T) {
	st := newReferenceTestStore(t)
	legacy := []byte(`{"apiVersion":"govirta.io/v1alpha1","kind":"Volume","metadata":{"name":"legacy-vol","uid":"uid-legacy-vol"},"spec":{"poolRef":"block-pool","imageRef":"img-a","imageFilePoolRef":"legacy-file-pool"}}`)
	if _, err := st.Put(context.Background(), StoreKey(metav1.KindVolume, "legacy-vol"), legacy, ""); err != nil {
		t.Fatalf("seed legacy Volume JSON: %v", err)
	}

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindStoragePool, "legacy-file-pool", "uid-legacy-file-pool"))
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil (legacy imageFilePoolRef must not block StoragePool delete)", err)
	}
}

// TestDeleteReferenceValidatorRejectsImageReferencedByVolume proves an Image is
// reference-blocked while a root Volume's imageRef names it.
func TestDeleteReferenceValidatorRejectsImageReferencedByVolume(t *testing.T) {
	st := newReferenceTestStore(t)
	seedReferenceObject(t, st, validAdmissionVolume()) // imageRef: img-a

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindImage, "img-a", "uid-img-a"))
	assertAdmissionReason(t, err, ReasonConflict)
	assertReferencedBy(t, err, "Volume/vol-a")
}

// TestDeleteReferenceValidatorRejectsNetworkReferencedByNIC proves a Network is
// reference-blocked while a NIC's networkRef names it.
func TestDeleteReferenceValidatorRejectsNetworkReferencedByNIC(t *testing.T) {
	st := newReferenceTestStore(t)
	seedReferenceObject(t, st, validAdmissionNIC()) // networkRef: net-a

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindNetwork, "net-a", "uid-net-a"))
	assertAdmissionReason(t, err, ReasonConflict)
	assertReferencedBy(t, err, "NIC/nic-a")
}

// TestDeleteReferenceValidatorRejectsVolumeReferencedByVM proves a Volume is
// reference-blocked while a VM's volumeRefs array contains its name.
func TestDeleteReferenceValidatorRejectsVolumeReferencedByVM(t *testing.T) {
	st := newReferenceTestStore(t)
	seedReferenceObject(t, st, validAdmissionVM()) // volumeRefs: [vol-a]

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindVolume, "vol-a", "uid-vol-a"))
	assertAdmissionReason(t, err, ReasonConflict)
	assertReferencedBy(t, err, "VM/vm-a")
}

// TestDeleteReferenceValidatorRejectsNICReferencedByVM proves a NIC is
// reference-blocked while a VM's nicRefs array contains its name.
func TestDeleteReferenceValidatorRejectsNICReferencedByVM(t *testing.T) {
	st := newReferenceTestStore(t)
	seedReferenceObject(t, st, validAdmissionVM()) // nicRefs: [nic-a]

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindNIC, "nic-a", "uid-nic-a"))
	assertAdmissionReason(t, err, ReasonConflict)
	assertReferencedBy(t, err, "VM/vm-a")
}

// TestDeleteReferenceValidatorAllowsVMOwningVolumeAndNIC proves a VM has no
// reverse-delete edge: it is the apex of the ownership tree, so deleting it must
// be allowed even while a Volume and a NIC still carry its UID in their vmRef
// ownership backpointer. Blocking VM deletion on those backpointers would
// deadlock reverse teardown (the Volume cannot go because VM.volumeRefs names
// it, and the VM cannot go because Volume.vmRef points back). The vmRef
// backpointer is enforced only on the apply side, never on delete.
func TestDeleteReferenceValidatorAllowsVMOwningVolumeAndNIC(t *testing.T) {
	st := newReferenceTestStore(t)
	vol := dataAdmissionVolume("vol-owned")
	vol.Spec.VMRef = "uid-vm-a"
	seedReferenceObject(t, st, vol)
	nic := admissionNIC("nic-owned")
	nic.Spec.VMRef = "uid-vm-a"
	seedReferenceObject(t, st, nic)

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindVM, "vm-a", "uid-vm-a"))
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil (VM is the ownership apex, no reverse edge)", err)
	}
}

// TestDeleteReferenceValidatorAllowsUnreferencedObject proves a non-VM kind with
// no downstream referrer is reference-clear. We seed unrelated objects so List
// returns data the scan must correctly reject.
func TestDeleteReferenceValidatorAllowsUnreferencedObject(t *testing.T) {
	st := newReferenceTestStore(t)
	vol := dataAdmissionVolume("vol-elsewhere")
	vol.Spec.PoolRef = "some-other-pool"
	seedReferenceObject(t, st, vol)

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindStoragePool, "pool-orphan", "uid-pool-orphan"))
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil (pool unreferenced)", err)
	}
}

// TestDeleteReferenceValidatorIgnoresNonDeleteOperation proves the validator is a
// no-op for any non-Delete operation, even when a live reference exists: apply's
// own ReferenceValidator owns the create/update direction.
func TestDeleteReferenceValidatorIgnoresNonDeleteOperation(t *testing.T) {
	st := newReferenceTestStore(t)
	seedReferenceObject(t, st, validAdmissionVM()) // would block a Volume delete

	for _, op := range []Operation{OperationCreate, OperationUpdate, OperationStatusPatch, OperationFinalizersPatch} {
		err := ReverseReferenceValidator{Store: st}.Validate(context.Background(), Request{
			Operation: op,
			Kind:      metav1.KindVolume,
			Name:      "vol-a",
			OldObject: metav1.ObjectMeta{Name: "vol-a", UID: "uid-vol-a"},
		})
		if err != nil {
			t.Fatalf("Validate(op=%s) error = %v, want nil (non-Delete is a no-op)", op, err)
		}
	}
}

// TestDeleteReferenceValidatorListErrorIsInternal proves a store List failure
// surfaces as Internal (500), never a false unreferenced pass.
func TestDeleteReferenceValidatorListErrorIsInternal(t *testing.T) {
	st := newReferenceTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindVolume, "vol-a", "uid-vol-a"))
	assertAdmissionReason(t, err, ReasonInternal)
}

// TestDeleteReferenceValidatorDecodeErrorIsInternal proves a corrupt downstream
// projection surfaces as Internal (500) rather than being silently skipped.
func TestDeleteReferenceValidatorDecodeErrorIsInternal(t *testing.T) {
	st := newReferenceTestStore(t)
	if _, err := st.Put(context.Background(), StoreKey(metav1.KindVM, "vm-corrupt"), []byte("not json"), ""); err != nil {
		t.Fatalf("seed corrupt VM: %v", err)
	}

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindVolume, "vol-a", "uid-vol-a"))
	assertAdmissionReason(t, err, ReasonInternal)
}

// TestDeleteReferenceValidatorRejectsVMReferencedBySnapshot proves a VM is
// reference-blocked while a Snapshot's spec.vmRef names it (by NAME). This is
// the VM's only reverse-delete edge: Knife 3 made VM the apex, and Snapshot is
// the first legitimate downstream reference.
func TestDeleteReferenceValidatorRejectsVMReferencedBySnapshot(t *testing.T) {
	st := newReferenceTestStore(t)
	seedReferenceObject(t, st, validAdmissionSnapshot()) // vmRef: vm-a

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindVM, "vm-a", "uid-vm-a"))
	assertAdmissionReason(t, err, ReasonConflict)
	assertReferencedBy(t, err, "Snapshot/snap-a")
}

// TestDeleteReferenceValidatorAllowsVMWithoutSnapshot proves a VM may be deleted
// when no Snapshot names it, even while a Volume and a NIC still carry its UID in
// their vmRef ownership backpointer. Those backpointers are ownership, not a VM
// dependency, so they must not block deleting the VM (blocking would deadlock
// reverse teardown). Snapshot is the only edge that blocks a VM delete.
func TestDeleteReferenceValidatorAllowsVMWithoutSnapshot(t *testing.T) {
	st := newReferenceTestStore(t)
	vol := dataAdmissionVolume("vol-owned")
	vol.Spec.VMRef = "uid-vm-a"
	seedReferenceObject(t, st, vol)
	nic := admissionNIC("nic-owned")
	nic.Spec.VMRef = "uid-vm-a"
	seedReferenceObject(t, st, nic)
	// A Snapshot that names a different VM must not block this VM's delete.
	otherSnap := validAdmissionSnapshot()
	otherSnap.Name = "snap-other"
	otherSnap.UID = "uid-snap-other"
	otherSnap.Spec.VMRef = "vm-elsewhere"
	seedReferenceObject(t, st, otherSnap)

	err := ReverseReferenceValidator{Store: st}.Validate(
		context.Background(), deleteRequest(metav1.KindVM, "vm-a", "uid-vm-a"))
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil (no Snapshot names this VM)", err)
	}
}

// assertReferencedBy checks that the admission error preserves the historical
// "still referenced by <Kind>/<name>" message contract and names the referrer.
func assertReferencedBy(t *testing.T, err error, refIdentity string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want a reference conflict naming %q", refIdentity)
	}
	msg := err.Error()
	if !strings.Contains(msg, "still referenced by") {
		t.Fatalf("error %q does not contain the \"still referenced by\" contract", msg)
	}
	if !strings.Contains(msg, refIdentity) {
		t.Fatalf("error %q does not name the referencing object %q", msg, refIdentity)
	}
}
