package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// newTestServer wires a Server over a fresh fake store and a deterministic MAC
// allocator. The pool uses a locally-administered, unicast OUI (02:00:00) with a
// small suffix interval so allocation order is reproducible across runs.
func newTestServer(t *testing.T) (*Server, *fake.Store) {
	t.Helper()
	st := fake.New()
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	pool, err := mac.NewPool(net.HardwareAddr{0x02, 0x00, 0x00}, 0x000001, 0x0000ff)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	alloc := mac.NewAllocator(pool, st)
	// The handler tests drive routing through Handler() directly and never call
	// Run, so the listen address is irrelevant here; a noop scheduler over a
	// single static node makes VM apply deterministic.
	return NewServer(st, alloc, scheduler.NewNoopScheduler(), []string{"node-1"}, ""), st
}

// doApply submits obj to POST /apis/{kind}/{name} through the server's handler and
// returns the recorded response. Body is the marshalled obj.
func doApply(t *testing.T, srv *Server, kind metav1.Kind, name string, obj any) *httptest.ResponseRecorder {
	t.Helper()
	seedApplyReferences(t, srv.store, obj)
	return doApplyWithoutReferenceSeeds(t, srv, kind, name, obj)
}

func doApplyWithoutReferenceSeeds(t *testing.T, srv *Server, kind metav1.Kind, name string, obj any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/apis/"+string(kind)+"/"+name, bytes.NewReader(data))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func seedApplyReferences(t *testing.T, st store.Store, obj any) {
	t.Helper()
	switch o := obj.(type) {
	case imagev1.Image:
		seedStoragePoolRef(t, st, o.Spec.FilePoolRef, storagepoolv1.PoolTypeFile)
	case volumev1.Volume:
		seedStoragePoolRef(t, st, o.Spec.PoolRef, storagepoolv1.PoolTypeBlock)
		seedOwnerVMRef(t, st, o.Spec.VMRef)
		if o.Spec.ImageRef != "" {
			seedImageRef(t, st, o.Spec.ImageRef, o.Spec.ImageFilePoolRef)
		}
		if o.Spec.ImageFilePoolRef != "" {
			seedStoragePoolRef(t, st, o.Spec.ImageFilePoolRef, storagepoolv1.PoolTypeFile)
		}
	case nicv1.NIC:
		seedNetworkRef(t, st, o.Spec.NetworkRef)
		seedOwnerVMRef(t, st, o.Spec.VMRef)
	case vmv1.VM:
		for _, name := range o.Spec.VolumeRefs {
			seedVolumeRef(t, st, name, o.UID)
		}
		for _, name := range o.Spec.NICRefs {
			seedNICRef(t, st, name, o.UID)
		}
	case snapshotv1.Snapshot:
		seedSnapshotVMRef(t, st, o.Spec.VMRef, "node-1")
	}
}

// seedSnapshotVMRef seeds the VM a Snapshot targets by name, already bound to
// nodeName (a stored VM is always scheduled before it lands) so applySnapshot can
// resolve a non-empty nodeName from it.
func seedSnapshotVMRef(t *testing.T, st store.Store, name, nodeName string) {
	t.Helper()
	if name == "" {
		return
	}
	vm := validVM()
	vm.Name = name
	vm.UID = "uid-" + name
	vm.NodeName = nodeName
	vm.Spec.VolumeRefs = []string{"snap-target-volume-" + name}
	vm.Spec.NICRefs = []string{"snap-target-nic-" + name}
	seedStoreObject(t, st, metav1.KindVM, name, vm)
}

func seedStoragePoolRef(t *testing.T, st store.Store, name string, poolType storagepoolv1.PoolType) {
	t.Helper()
	if name == "" {
		return
	}
	pool := validStoragePool()
	pool.Name = name
	pool.UID = "uid-" + name
	pool.Spec.Type = poolType
	if poolType == storagepoolv1.PoolTypeBlock {
		pool.Spec.Backend = storagepoolv1.BackendLocalBlock
	}
	pool.Spec.StorageRoot = "/var/lib/govirta/" + name
	seedStoreObject(t, st, metav1.KindStoragePool, name, pool)
}

func seedImageRef(t *testing.T, st store.Store, name, filePoolRef string) {
	t.Helper()
	if name == "" {
		return
	}
	image := validImage()
	image.Name = name
	image.UID = "uid-" + name
	image.Spec.FilePoolRef = filePoolRef
	seedStoreObject(t, st, metav1.KindImage, name, image)
}

func seedNetworkRef(t *testing.T, st store.Store, name string) {
	t.Helper()
	if name == "" {
		return
	}
	network := validNetwork()
	network.Name = name
	network.UID = "uid-" + name
	seedStoreObject(t, st, metav1.KindNetwork, name, network)
}

func seedOwnerVMRef(t *testing.T, st store.Store, uid string) {
	t.Helper()
	if uid == "" {
		return
	}
	vm := validVM()
	vm.Name = "owner-" + uid
	vm.UID = uid
	vm.Spec.VolumeRefs = []string{"owner-volume-" + uid}
	vm.Spec.NICRefs = []string{"owner-nic-" + uid}
	seedStoreObject(t, st, metav1.KindVM, vm.Name, vm)
}

func seedVolumeRef(t *testing.T, st store.Store, name, vmUID string) {
	t.Helper()
	if name == "" {
		return
	}
	volume := validVolume()
	volume.Name = name
	volume.UID = "uid-" + name
	volume.Spec.VMRef = vmUID
	seedStoreObject(t, st, metav1.KindVolume, name, volume)
}

func seedNICRef(t *testing.T, st store.Store, name, vmUID string) {
	t.Helper()
	if name == "" {
		return
	}
	nic := validNIC()
	nic.Name = name
	nic.UID = "uid-" + name
	nic.Spec.VMRef = vmUID
	seedStoreObject(t, st, metav1.KindNIC, name, nic)
}

func seedStoreObject(t *testing.T, st store.Store, kind metav1.Kind, name string, obj any) {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal seed %s/%s: %v", kind, name, err)
	}
	if _, err := st.Put(context.Background(), storeKey(kind, name), data, ""); err != nil {
		t.Fatalf("seed %s/%s: %v", kind, name, err)
	}
}

// decodeError extracts the {"error": "..."} envelope from a recorded response.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error
}

// storedRaw fetches the persisted object for kind/name directly from the store,
// failing the test if it is absent.
func storedRaw(t *testing.T, st *fake.Store, kind metav1.Kind, name string) store.RawObject {
	t.Helper()
	raw, err := st.Get(context.Background(), storeKey(kind, name))
	if err != nil {
		t.Fatalf("get stored %s/%s: %v", kind, name, err)
	}
	return raw
}

func validStoragePool() storagepoolv1.StoragePool {
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

func validTask(t *testing.T, name, nodeName string, scope taskv1.TaskScope, operation taskv1.TaskOperation) taskv1.Task {
	t.Helper()
	input, err := json.Marshal(taskv1.NoopInput{Marker: "phase-one"})
	if err != nil {
		t.Fatalf("marshal task input: %v", err)
	}
	return taskv1.Task{
		TypeMeta: metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindTask},
		ObjectMeta: metav1.ObjectMeta{
			Name:     name,
			UID:      "uid-" + name,
			NodeName: nodeName,
		},
		Spec: taskv1.TaskSpec{
			Scope:     scope,
			OwnerKind: metav1.KindTask,
			OwnerName: "phase-one-owner",
			OwnerUID:  "phase-one-owner-uid",
			Operation: operation,
			Input:     input,
		},
		Status: taskv1.TaskStatus{Phase: taskv1.TaskPhasePending},
	}
}

func validImage() imagev1.Image {
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

func validVolume() volumev1.Volume {
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

func validNetwork() networkv1.Network {
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
			LeaseSeconds:    3600,
		},
	}
}

func validNIC() nicv1.NIC {
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

func validVM() vmv1.VM {
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

func validSnapshot() snapshotv1.Snapshot {
	return snapshotv1.Snapshot{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindSnapshot},
		ObjectMeta: metav1.ObjectMeta{Name: "snap-a", UID: "uid-snap-a"},
		Spec:       snapshotv1.SnapshotSpec{VMRef: "vm-a"},
	}
}
