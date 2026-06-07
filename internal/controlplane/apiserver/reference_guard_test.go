package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/controlplane/store"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// seedRaw persists obj under storeKey(kind, name) by marshalling it and writing
// the bytes straight to the fake store. We write the marshalled object directly
// (rather than routing through the apply admission pipeline) because several
// scan fixtures are deliberately not independently apply-valid — the guard only
// reads stored JSON, so what matters is that the bytes the scan lists are the
// real wire shape of the object. The projection is then exercised against
// genuinely-marshalled apis JSON, not a hand-rolled blob.
func seedRaw(t *testing.T, st *fake.Store, kind metav1.Kind, obj any, name string) {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal %s/%s: %v", kind, name, err)
	}
	if _, err := st.Put(context.Background(), storeKey(kind, name), data, ""); err != nil {
		t.Fatalf("seed %s/%s: %v", kind, name, err)
	}
}

// TestReferenceGuardStoragePoolReferencedByPoolRef proves a StoragePool is held
// referenced when a Volume's block poolRef names it.
func TestReferenceGuardStoragePoolReferencedByPoolRef(t *testing.T) {
	srv, st := newTestServer(t)

	vol := validVolume()
	vol.Name = "vol-pool-ref"
	vol.Spec.PoolRef = "pool-target"
	vol.Spec.ImageFilePoolRef = "other-pool"
	seedRaw(t, st, metav1.KindVolume, vol, vol.Name)

	refBy, referenced, err := srv.referenceGuard(context.Background(), metav1.KindStoragePool, "pool-target")
	if err != nil {
		t.Fatalf("referenceGuard error: %v", err)
	}
	if !referenced {
		t.Fatalf("expected pool-target referenced via Volume.poolRef, got referenced=false")
	}
	if want := "Volume/vol-pool-ref"; refBy != want {
		t.Fatalf("referencedBy = %q, want %q", refBy, want)
	}
}

// TestReferenceGuardStoragePoolReferencedByImageFilePoolRef proves the second
// StoragePool-bearing field is scanned: a Volume whose poolRef points at a
// different pool still holds pool-target referenced via imageFilePoolRef.
func TestReferenceGuardStoragePoolReferencedByImageFilePoolRef(t *testing.T) {
	srv, st := newTestServer(t)

	vol := validVolume()
	vol.Name = "vol-imgfile-ref"
	vol.Spec.PoolRef = "block-pool" // a DIFFERENT pool, so poolRef must not be the hit
	vol.Spec.ImageFilePoolRef = "pool-target"
	seedRaw(t, st, metav1.KindVolume, vol, vol.Name)

	refBy, referenced, err := srv.referenceGuard(context.Background(), metav1.KindStoragePool, "pool-target")
	if err != nil {
		t.Fatalf("referenceGuard error: %v", err)
	}
	if !referenced {
		t.Fatalf("expected pool-target referenced via Volume.imageFilePoolRef, got referenced=false")
	}
	if want := "Volume/vol-imgfile-ref"; refBy != want {
		t.Fatalf("referencedBy = %q, want %q", refBy, want)
	}
}

// TestReferenceGuardNetworkReferencedByNIC proves a Network is held referenced
// when a NIC's networkRef names it.
func TestReferenceGuardNetworkReferencedByNIC(t *testing.T) {
	srv, st := newTestServer(t)

	nic := validNIC()
	nic.Name = "nic-net-ref"
	nic.Spec.NetworkRef = "net-target"
	seedRaw(t, st, metav1.KindNIC, nic, nic.Name)

	refBy, referenced, err := srv.referenceGuard(context.Background(), metav1.KindNetwork, "net-target")
	if err != nil {
		t.Fatalf("referenceGuard error: %v", err)
	}
	if !referenced {
		t.Fatalf("expected net-target referenced via NIC.networkRef, got referenced=false")
	}
	if want := "NIC/nic-net-ref"; refBy != want {
		t.Fatalf("referencedBy = %q, want %q", refBy, want)
	}
}

// TestReferenceGuardImageReferencedByVolume proves an Image is held referenced
// when a root Volume's imageRef names it.
func TestReferenceGuardImageReferencedByVolume(t *testing.T) {
	srv, st := newTestServer(t)

	vol := validVolume()
	vol.Name = "vol-img-ref"
	vol.Spec.ImageRef = "img-target"
	seedRaw(t, st, metav1.KindVolume, vol, vol.Name)

	refBy, referenced, err := srv.referenceGuard(context.Background(), metav1.KindImage, "img-target")
	if err != nil {
		t.Fatalf("referenceGuard error: %v", err)
	}
	if !referenced {
		t.Fatalf("expected img-target referenced via Volume.imageRef, got referenced=false")
	}
	if want := "Volume/vol-img-ref"; refBy != want {
		t.Fatalf("referencedBy = %q, want %q", refBy, want)
	}
}

// TestReferenceGuardVolumeReferencedByVM proves a Volume is held referenced when
// a VM's volumeRefs array contains its name.
func TestReferenceGuardVolumeReferencedByVM(t *testing.T) {
	srv, st := newTestServer(t)

	vm := validVM()
	vm.Name = "vm-vol-ref"
	vm.Spec.VolumeRefs = []string{"other-vol", "vol-target"}
	seedRaw(t, st, metav1.KindVM, vm, vm.Name)

	refBy, referenced, err := srv.referenceGuard(context.Background(), metav1.KindVolume, "vol-target")
	if err != nil {
		t.Fatalf("referenceGuard error: %v", err)
	}
	if !referenced {
		t.Fatalf("expected vol-target referenced via VM.volumeRefs, got referenced=false")
	}
	if want := "VM/vm-vol-ref"; refBy != want {
		t.Fatalf("referencedBy = %q, want %q", refBy, want)
	}
}

// TestReferenceGuardNICReferencedByVM proves a NIC is held referenced when a
// VM's nicRefs array contains its name.
func TestReferenceGuardNICReferencedByVM(t *testing.T) {
	srv, st := newTestServer(t)

	vm := validVM()
	vm.Name = "vm-nic-ref"
	vm.Spec.NICRefs = []string{"other-nic", "nic-target"}
	seedRaw(t, st, metav1.KindVM, vm, vm.Name)

	refBy, referenced, err := srv.referenceGuard(context.Background(), metav1.KindNIC, "nic-target")
	if err != nil {
		t.Fatalf("referenceGuard error: %v", err)
	}
	if !referenced {
		t.Fatalf("expected nic-target referenced via VM.nicRefs, got referenced=false")
	}
	if want := "VM/vm-nic-ref"; refBy != want {
		t.Fatalf("referencedBy = %q, want %q", refBy, want)
	}
}

// TestReferenceGuardVMNeverReferenced proves a VM is always reference-clear, even
// when a Volume and a NIC carry a vmRef uid pointing at it. The vmRef uid is
// deliberately NOT scanned: a VM has no name-referencing downstream kind.
func TestReferenceGuardVMNeverReferenced(t *testing.T) {
	srv, st := newTestServer(t)

	const vmUID = "uid-vm-target"

	vol := validVolume()
	vol.Name = "vol-points-at-vm"
	vol.Spec.VMRef = vmUID
	seedRaw(t, st, metav1.KindVolume, vol, vol.Name)

	nic := validNIC()
	nic.Name = "nic-points-at-vm"
	nic.Spec.VMRef = vmUID
	seedRaw(t, st, metav1.KindNIC, nic, nic.Name)

	// Scan by the VM's name: the VM branch must short-circuit to false without
	// listing anything.
	refBy, referenced, err := srv.referenceGuard(context.Background(), metav1.KindVM, "vm-target")
	if err != nil {
		t.Fatalf("referenceGuard error: %v", err)
	}
	if referenced {
		t.Fatalf("VM must never be reference-blocked, got referencedBy=%q", refBy)
	}
	if refBy != "" {
		t.Fatalf("referencedBy = %q, want empty", refBy)
	}

	// And scanning by the uid (the value the downstream vmRef actually holds)
	// must also be clear: the VM branch never inspects vmRef.
	refBy, referenced, err = srv.referenceGuard(context.Background(), metav1.KindVM, vmUID)
	if err != nil {
		t.Fatalf("referenceGuard (by uid) error: %v", err)
	}
	if referenced {
		t.Fatalf("VM must never be reference-blocked even by uid, got referencedBy=%q", refBy)
	}
}

// TestReferenceGuardUnreferenced proves each kind reports false when no
// downstream object references the target. We seed unrelated downstream objects
// so List returns data the scan must correctly reject.
func TestReferenceGuardUnreferenced(t *testing.T) {
	cases := []struct {
		name   string
		kind   metav1.Kind
		target string
		seed   func(t *testing.T, st *fake.Store)
	}{
		{
			name:   "StoragePool with no matching Volume ref",
			kind:   metav1.KindStoragePool,
			target: "pool-orphan",
			seed: func(t *testing.T, st *fake.Store) {
				vol := validVolume()
				vol.Name = "vol-elsewhere"
				vol.Spec.PoolRef = "some-other-pool"
				vol.Spec.ImageFilePoolRef = "yet-another-pool"
				seedRaw(t, st, metav1.KindVolume, vol, vol.Name)
			},
		},
		{
			name:   "Image with no matching Volume ref",
			kind:   metav1.KindImage,
			target: "img-orphan",
			seed: func(t *testing.T, st *fake.Store) {
				vol := validVolume()
				vol.Name = "vol-other-image"
				vol.Spec.ImageRef = "some-other-image"
				seedRaw(t, st, metav1.KindVolume, vol, vol.Name)
			},
		},
		{
			name:   "Network with no matching NIC ref",
			kind:   metav1.KindNetwork,
			target: "net-orphan",
			seed: func(t *testing.T, st *fake.Store) {
				nic := validNIC()
				nic.Name = "nic-other-net"
				nic.Spec.NetworkRef = "some-other-network"
				seedRaw(t, st, metav1.KindNIC, nic, nic.Name)
			},
		},
		{
			name:   "Volume with no matching VM ref",
			kind:   metav1.KindVolume,
			target: "vol-orphan",
			seed: func(t *testing.T, st *fake.Store) {
				vm := validVM()
				vm.Name = "vm-other-vols"
				vm.Spec.VolumeRefs = []string{"some-other-vol"}
				seedRaw(t, st, metav1.KindVM, vm, vm.Name)
			},
		},
		{
			name:   "NIC with no matching VM ref",
			kind:   metav1.KindNIC,
			target: "nic-orphan",
			seed: func(t *testing.T, st *fake.Store) {
				vm := validVM()
				vm.Name = "vm-other-nics"
				vm.Spec.NICRefs = []string{"some-other-nic"}
				seedRaw(t, st, metav1.KindVM, vm, vm.Name)
			},
		},
		{
			name:   "empty store",
			kind:   metav1.KindStoragePool,
			target: "pool-in-empty-store",
			seed:   func(t *testing.T, st *fake.Store) {},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, st := newTestServer(t)
			tc.seed(t, st)

			refBy, referenced, err := srv.referenceGuard(context.Background(), tc.kind, tc.target)
			if err != nil {
				t.Fatalf("referenceGuard error: %v", err)
			}
			if referenced {
				t.Fatalf("expected %s/%s unreferenced, got referencedBy=%q", tc.kind, tc.target, refBy)
			}
			if refBy != "" {
				t.Fatalf("referencedBy = %q, want empty", refBy)
			}
		})
	}
}

// TestReferenceGuardListError proves a store List failure propagates as an error
// (errors 向上传播, never swallowed). We drive the failure by closing the store so
// every subsequent List returns store.ErrClosed.
func TestReferenceGuardListError(t *testing.T) {
	srv, st := newTestServer(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	refBy, referenced, err := srv.referenceGuard(context.Background(), metav1.KindStoragePool, "pool-any")
	if err == nil {
		t.Fatalf("expected an error from a closed store, got nil")
	}
	if !errors.Is(err, store.ErrClosed) {
		t.Fatalf("error = %v, want it to wrap store.ErrClosed", err)
	}
	if referenced || refBy != "" {
		t.Fatalf("on error want zero values, got referencedBy=%q referenced=%v", refBy, referenced)
	}
}

// TestReferenceGuardDecodeError proves a projection decode failure propagates: a
// non-JSON object stored under the downstream kind's prefix must surface as an
// error rather than be silently skipped.
func TestReferenceGuardDecodeError(t *testing.T) {
	srv, st := newTestServer(t)

	if _, err := st.Put(context.Background(), storeKey(metav1.KindVolume, "vol-corrupt"), []byte("not json"), ""); err != nil {
		t.Fatalf("seed corrupt Volume: %v", err)
	}

	refBy, referenced, err := srv.referenceGuard(context.Background(), metav1.KindStoragePool, "pool-any")
	if err == nil {
		t.Fatalf("expected a decode error from corrupt downstream JSON, got nil")
	}
	if referenced || refBy != "" {
		t.Fatalf("on error want zero values, got referencedBy=%q referenced=%v", refBy, referenced)
	}
}
