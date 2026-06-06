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
	return NewServer(st, alloc), st
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

func TestApplyValidObjectsPersist(t *testing.T) {
	cases := []struct {
		name string
		kind metav1.Kind
		obj  func() any
	}{
		{"StoragePool", metav1.KindStoragePool, func() any { return validStoragePool() }},
		{"Image", metav1.KindImage, func() any { return validImage() }},
		{"Volume", metav1.KindVolume, func() any { return validVolume() }},
		{"Network", metav1.KindNetwork, func() any { return validNetwork() }},
		{"VM", metav1.KindVM, func() any { return validVM() }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, st := newTestServer(t)
			obj := tc.obj()

			// Object name is the metadata.Name on each fixture.
			var objName string
			switch v := obj.(type) {
			case storagepoolv1.StoragePool:
				objName = v.Name
			case imagev1.Image:
				objName = v.Name
			case volumev1.Volume:
				objName = v.Name
			case networkv1.Network:
				objName = v.Name
			case vmv1.VM:
				objName = v.Name
			default:
				t.Fatalf("unexpected fixture type %T", v)
			}

			rec := doApply(t, srv, tc.kind, objName, obj)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
			}

			raw := storedRaw(t, st, tc.kind, objName)
			if raw.ResourceVersion == "" {
				t.Fatalf("stored object has empty ResourceVersion")
			}

			// Response body must carry the store-assigned ResourceVersion.
			var meta struct {
				Metadata metav1.ObjectMeta `json:"metadata"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if meta.Metadata.ResourceVersion != raw.ResourceVersion {
				t.Fatalf("response RV %q != stored RV %q", meta.Metadata.ResourceVersion, raw.ResourceVersion)
			}
		})
	}
}

func TestApplyInvalidSpecEmptyNameRejected(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validStoragePool()
	obj.Name = "" // violate ObjectMeta.Validate (and break path/name agreement)

	// Path name is empty too; we route through the handler with a placeholder so
	// the request reaches Apply, but the empty metadata.name must be rejected.
	rec := doApply(t, srv, metav1.KindStoragePool, "ignored", obj)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}

	if raws, err := st.List(context.Background(), "/govirta/"); err != nil {
		t.Fatalf("list store: %v", err)
	} else if len(raws) != 0 {
		t.Fatalf("expected nothing stored on invalid apply, got %d objects", len(raws))
	}
}

func TestApplyVolumeRootMissingImageRefRejected(t *testing.T) {
	srv, _ := newTestServer(t)

	obj := validVolume()
	obj.Spec.ImageRef = "" // root volume must carry imageRef

	rec := doApply(t, srv, metav1.KindVolume, obj.Name, obj)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

func TestApplyNICEmptyMACGetsAllocated(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validNIC()
	if obj.Spec.MAC != "" {
		t.Fatalf("fixture precondition: NIC MAC must be empty")
	}

	rec := doApply(t, srv, metav1.KindNIC, obj.Name, obj)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	raw := storedRaw(t, st, metav1.KindNIC, obj.Name)
	var stored nicv1.NIC
	if err := json.Unmarshal(raw.Value, &stored); err != nil {
		t.Fatalf("decode stored NIC: %v", err)
	}
	if stored.Spec.MAC == "" {
		t.Fatalf("stored NIC MAC is empty; expected an allocated MAC")
	}
	if _, err := net.ParseMAC(stored.Spec.MAC); err != nil {
		t.Fatalf("stored NIC MAC %q does not parse: %v", stored.Spec.MAC, err)
	}

	// The response body must reflect the allocated MAC too.
	var resp nicv1.NIC
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response NIC: %v", err)
	}
	if resp.Spec.MAC != stored.Spec.MAC {
		t.Fatalf("response MAC %q != stored MAC %q", resp.Spec.MAC, stored.Spec.MAC)
	}
}

func TestApplyNICPreservesProvidedMAC(t *testing.T) {
	srv, st := newTestServer(t)

	const providedMAC = "02:00:00:de:ad:be"
	obj := validNIC()
	obj.Spec.MAC = providedMAC

	rec := doApply(t, srv, metav1.KindNIC, obj.Name, obj)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	raw := storedRaw(t, st, metav1.KindNIC, obj.Name)
	var stored nicv1.NIC
	if err := json.Unmarshal(raw.Value, &stored); err != nil {
		t.Fatalf("decode stored NIC: %v", err)
	}
	if stored.Spec.MAC != providedMAC {
		t.Fatalf("stored MAC %q != provided MAC %q (must be preserved as-is)", stored.Spec.MAC, providedMAC)
	}
}

func TestApplyNetworkAdmissionRejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*networkv1.Network)
	}{
		{
			name:   "negative lease",
			mutate: func(n *networkv1.Network) { n.Spec.LeaseSeconds = -1 },
		},
		{
			name: "inverted DHCP range",
			mutate: func(n *networkv1.Network) {
				n.Spec.DHCPRangeStart = "192.168.100.200"
				n.Spec.DHCPRangeEnd = "192.168.100.10"
			},
		},
		{
			name:   "gateway outside subnet",
			mutate: func(n *networkv1.Network) { n.Spec.GatewayCIDR = "10.0.0.1/24" },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, st := newTestServer(t)
			obj := validNetwork()
			tc.mutate(&obj)

			rec := doApply(t, srv, metav1.KindNetwork, obj.Name, obj)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if msg := decodeError(t, rec); msg == "" {
				t.Fatalf("expected non-empty error body")
			}

			if _, err := st.Get(context.Background(), storeKey(metav1.KindNetwork, obj.Name)); err == nil {
				t.Fatalf("rejected Network must not be stored")
			}
		})
	}
}

func TestApplyUnknownKindNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodPut, "/apis/Widget/w-a", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// doGet issues GET against path through the server's handler and returns the
// recorded response.
func doGet(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestGetHitReturnsStoredObject(t *testing.T) {
	srv, _ := newTestServer(t)

	obj := validStoragePool()
	if rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("seed apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	rec := doGet(t, srv, "/apis/"+string(metav1.KindStoragePool)+"/"+obj.Name)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// The body is the raw stored JSON; it must decode back to the same object.
	var got storagepoolv1.StoragePool
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Name != obj.Name {
		t.Fatalf("response name = %q, want %q", got.Name, obj.Name)
	}
	if got.Spec.StorageRoot != obj.Spec.StorageRoot {
		t.Fatalf("response storageRoot = %q, want %q", got.Spec.StorageRoot, obj.Spec.StorageRoot)
	}

	// ResourceVersion is store metadata, not part of the persisted object bytes
	// (Apply assigns it only on its own response), so the get body — a verbatim
	// pass-through of the stored JSON — carries none. The version is surfaced on
	// the X-Resource-Version header instead, and must be the store-assigned value.
	if hv := rec.Header().Get(resourceVersionHeader); hv == "" {
		t.Fatalf("%s header is empty; expected store-assigned ResourceVersion", resourceVersionHeader)
	}
}

func TestGetMissingReturns404(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := doGet(t, srv, "/apis/"+string(metav1.KindStoragePool)+"/nonexistent")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

func TestGetListEmptyReturnsEmptyArray(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := doGet(t, srv, "/apis/"+string(metav1.KindStoragePool))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Hard requirement: an empty collection is "[]", never "null".
	if body := bytes.TrimSpace(rec.Body.Bytes()); !bytes.Equal(body, []byte("[]")) {
		t.Fatalf("empty list body = %q, want %q", body, "[]")
	}

	var arr []storagepoolv1.StoragePool
	if err := json.Unmarshal(rec.Body.Bytes(), &arr); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(arr) != 0 {
		t.Fatalf("empty list len = %d, want 0", len(arr))
	}
}

func TestGetListReturnsSortedByName(t *testing.T) {
	srv, _ := newTestServer(t)

	// Seed out of order; store.List sorts by key (/govirta/<kind>/<name>), so the
	// response array must come back ordered by name regardless of insertion order.
	names := []string{"pool-c", "pool-a", "pool-b"}
	for _, n := range names {
		obj := validStoragePool()
		obj.Name = n
		obj.UID = "uid-" + n
		if rec := doApply(t, srv, metav1.KindStoragePool, n, obj); rec.Code != http.StatusCreated {
			t.Fatalf("seed apply %q status = %d, want 201; body=%s", n, rec.Code, rec.Body.String())
		}
	}

	rec := doGet(t, srv, "/apis/"+string(metav1.KindStoragePool))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var arr []storagepoolv1.StoragePool
	if err := json.Unmarshal(rec.Body.Bytes(), &arr); err != nil {
		t.Fatalf("decode list: %v", err)
	}

	want := []string{"pool-a", "pool-b", "pool-c"}
	if len(arr) != len(want) {
		t.Fatalf("list len = %d, want %d", len(arr), len(want))
	}
	for i, w := range want {
		if arr[i].Name != w {
			t.Fatalf("list[%d].Name = %q, want %q", i, arr[i].Name, w)
		}
	}
}
