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
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
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

// doApply submits obj to PUT /apis/{kind}/{name} through the server's handler and
// returns the recorded response. Body is the marshalled obj.
func doApply(t *testing.T, srv *Server, kind metav1.Kind, name string, obj any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/apis/"+string(kind)+"/"+name, bytes.NewReader(data))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
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
		},
	}
}
