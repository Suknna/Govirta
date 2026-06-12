package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suknna/govirta/internal/controlplane/store"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

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
		{"Snapshot", metav1.KindSnapshot, func() any { return validSnapshot() }},
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
			case snapshotv1.Snapshot:
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

func TestApplyNICRejectsDeletingNetworkReference(t *testing.T) {
	srv, st := newTestServer(t)

	network := validNetwork()
	if rec := doApply(t, srv, metav1.KindNetwork, network.Name, network); rec.Code != http.StatusCreated {
		t.Fatalf("seed network apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	stored := storedRaw(t, st, metav1.KindNetwork, network.Name)
	network.DeletionTimestamp = "2026-06-09T00:00:00Z"
	network.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	data, err := json.Marshal(network)
	if err != nil {
		t.Fatalf("marshal deleting Network: %v", err)
	}
	if _, err := st.Put(context.Background(), storeKey(metav1.KindNetwork, network.Name), data, stored.ResourceVersion); err != nil {
		t.Fatalf("stamp deleting Network: %v", err)
	}
	seedOwnerVMRef(t, st, "uid-vm-a")

	nic := validNIC()
	rec := doApplyWithoutReferenceSeeds(t, srv, metav1.KindNIC, nic.Name, nic)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

func TestApplyVMRejectsMissingVolumeReference(t *testing.T) {
	srv, _ := newTestServer(t)

	vm := validVM()
	rec := doApplyWithoutReferenceSeeds(t, srv, metav1.KindVM, vm.Name, vm)
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

	req := httptest.NewRequest(http.MethodPost, "/apis/Widget/w-a", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestApplyInjectsNodeTeardownFinalizer 验证：apply 一个 Finalizers 为空的对象时，
// apiserver 在落 etcd 前注入默认 node-teardown finalizer，使对象一持久化就带 finalizer，
// 消除"finalizer 还没加就被 DELETE 真删 → 节点已建部分资源 → 泄漏"的竞态窗口。
func TestApplyInjectsNodeTeardownFinalizer(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validStoragePool()
	if len(obj.Finalizers) != 0 {
		t.Fatalf("fixture precondition: StoragePool Finalizers must be empty")
	}

	rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	raw := storedRaw(t, st, metav1.KindStoragePool, obj.Name)
	var stored storagepoolv1.StoragePool
	if err := json.Unmarshal(raw.Value, &stored); err != nil {
		t.Fatalf("decode stored StoragePool: %v", err)
	}

	want := []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	if len(stored.Finalizers) != len(want) || stored.Finalizers[0] != want[0] {
		t.Fatalf("stored finalizers = %v, want %v", stored.Finalizers, want)
	}
}

func TestApplyRejectsUserProvidedFinalizers(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validStoragePool()
	obj.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}

	rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
	if _, err := st.Get(context.Background(), storeKey(metav1.KindStoragePool, obj.Name)); err == nil {
		t.Fatalf("rejected StoragePool must not be stored")
	}
}

func TestApplyRejectsMissingMetadataUID(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validStoragePool()
	obj.UID = ""

	rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
	if _, err := st.Get(context.Background(), storeKey(metav1.KindStoragePool, obj.Name)); err == nil {
		t.Fatalf("rejected StoragePool must not be stored")
	}
}

func TestApplyUpdateRejectsServerOwnedMetadataInBody(t *testing.T) {
	serverOwnedCases := []struct {
		name   string
		mutate func(*storagepoolv1.StoragePool)
	}{
		{name: "resourceVersion", mutate: func(obj *storagepoolv1.StoragePool) { obj.ResourceVersion = "client-rv" }},
		{name: "deletionTimestamp", mutate: func(obj *storagepoolv1.StoragePool) { obj.DeletionTimestamp = "2026-06-09T00:00:00Z" }},
		{name: "finalizers", mutate: func(obj *storagepoolv1.StoragePool) {
			obj.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}
		}},
	}

	for _, tt := range serverOwnedCases {
		t.Run(tt.name, func(t *testing.T) {
			srv, st := newTestServer(t)
			obj := validStoragePool()
			if rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj); rec.Code != http.StatusCreated {
				t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
			}

			update := validStoragePool()
			tt.mutate(&update)
			rec := doApply(t, srv, metav1.KindStoragePool, update.Name, update)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("update status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if msg := decodeError(t, rec); msg == "" {
				t.Fatalf("expected non-empty error body")
			}

			raw := storedRaw(t, st, metav1.KindStoragePool, obj.Name)
			var stored storagepoolv1.StoragePool
			if err := json.Unmarshal(raw.Value, &stored); err != nil {
				t.Fatalf("decode stored StoragePool: %v", err)
			}
			if stored.Spec != obj.Spec {
				t.Fatalf("stored spec changed after rejected update: got %+v want %+v", stored.Spec, obj.Spec)
			}
		})
	}
}

func TestApplyUpdatePreservesServerOwnedMetadataForNonVM(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validStoragePool()
	if rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	created := storedStoragePool(t, st, obj.Name)
	created.ResourceVersion = "stored-rv"
	created.DeletionTimestamp = "2026-06-09T00:00:00Z"
	created.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown, metav1.Finalizer("example.com/other")}
	data, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("marshal stored StoragePool: %v", err)
	}
	if _, err := st.Put(context.Background(), storeKey(metav1.KindStoragePool, obj.Name), data, ""); err != nil {
		t.Fatalf("overwrite stored StoragePool: %v", err)
	}

	update := validStoragePool()
	rec := doApply(t, srv, metav1.KindStoragePool, update.Name, update)
	if rec.Code != http.StatusCreated {
		t.Fatalf("update status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedStoragePool(t, st, obj.Name)
	if stored.ResourceVersion != created.ResourceVersion {
		t.Fatalf("stored resourceVersion = %q, want %q", stored.ResourceVersion, created.ResourceVersion)
	}
	if stored.DeletionTimestamp != created.DeletionTimestamp {
		t.Fatalf("stored deletionTimestamp = %q, want %q", stored.DeletionTimestamp, created.DeletionTimestamp)
	}
	if len(stored.Finalizers) != len(created.Finalizers) {
		t.Fatalf("stored finalizers = %v, want %v", stored.Finalizers, created.Finalizers)
	}
	for i := range created.Finalizers {
		if stored.Finalizers[i] != created.Finalizers[i] {
			t.Fatalf("stored finalizers = %v, want %v", stored.Finalizers, created.Finalizers)
		}
	}
}

func TestApplyUpdateCorruptExistingEnvelopeReturnsInternalError(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validStoragePool()
	stored := obj
	stored.UID = ""
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal invalid stored StoragePool: %v", err)
	}
	if _, err := st.Put(context.Background(), storeKey(metav1.KindStoragePool, obj.Name), data, ""); err != nil {
		t.Fatalf("seed invalid stored StoragePool: %v", err)
	}

	rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("update status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestApplyNICAllocInjectsNodeTeardownFinalizer 验证：apply 一个 MAC 为空的 NIC 时，
// 走 applyNIC 的 WithAllocation 分配分支——该分支在闭包内对 *nic 重新 json.Marshal 后
// store.Put，是与直 put 结构不同的独立 marshal 路径。注入点位置若写错（例如 injectFinalizer
// 误挪到分配之后、或 MAC-empty 分支独立 marshal 时漏带 finalizer），落盘字节就会丢 finalizer。
// 本测试从 fake store 取持久化的原始字节 unmarshal，断言 finalizer 经分配闭包重新 marshal 后仍在。
func TestApplyNICAllocInjectsNodeTeardownFinalizer(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validNIC()
	if obj.Spec.MAC != "" {
		t.Fatalf("fixture precondition: NIC MAC must be empty (drives WithAllocation path)")
	}
	if len(obj.Finalizers) != 0 {
		t.Fatalf("fixture precondition: NIC Finalizers must be empty")
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

	// Sanity: we really took the allocation branch (an empty MAC got filled in).
	if stored.Spec.MAC == "" {
		t.Fatalf("stored NIC MAC is empty; allocation branch did not run")
	}

	want := []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	if len(stored.Finalizers) != len(want) || stored.Finalizers[0] != want[0] {
		t.Fatalf("stored finalizers = %v, want %v (lost across applyNIC re-marshal)", stored.Finalizers, want)
	}
}

func TestApplyNICUpdatePreservesExistingMACWhenBodyOmitsIt(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validNIC()
	if rec := doApply(t, srv, metav1.KindNIC, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	created := storedNIC(t, st, obj.Name)
	if created.Spec.MAC == "" {
		t.Fatalf("created NIC MAC is empty")
	}

	update := validNIC()
	update.Spec.MAC = ""
	rec := doApply(t, srv, metav1.KindNIC, update.Name, update)
	if rec.Code != http.StatusCreated {
		t.Fatalf("update status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	stored := storedNIC(t, st, obj.Name)
	if stored.Spec.MAC != created.Spec.MAC {
		t.Fatalf("stored MAC = %q, want preserved %q", stored.Spec.MAC, created.Spec.MAC)
	}
}

func TestApplyNICUpdateRejectsMACChange(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validNIC()
	if rec := doApply(t, srv, metav1.KindNIC, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	created := storedNIC(t, st, obj.Name)

	update := validNIC()
	update.Spec.MAC = "02:00:00:00:99:99"
	rec := doApply(t, srv, metav1.KindNIC, update.Name, update)
	if rec.Code != http.StatusConflict {
		t.Fatalf("update status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	stored := storedNIC(t, st, obj.Name)
	if stored.Spec.MAC != created.Spec.MAC {
		t.Fatalf("stored MAC = %q, want unchanged %q", stored.Spec.MAC, created.Spec.MAC)
	}
}

// TestApplyVMScheduleInjectsNodeTeardownFinalizer 验证：apply 一个无显式 NodeName 的 VM 时，
// 走 bindVM 调度分支——先由 scheduler 写 ObjectMeta.NodeName，再 put 持久化。注入点位置若写错
// （例如 injectFinalizer 误挪到 bindVM 之后、或调度改写 meta 时覆盖掉 finalizer），落盘就会丢 finalizer。
// 本测试从 fake store 取持久化的原始字节 unmarshal，断言调度写回 NodeName 后 finalizer 仍随对象落盘。
func TestApplyVMScheduleInjectsNodeTeardownFinalizer(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	if obj.NodeName != "" {
		t.Fatalf("fixture precondition: VM NodeName must be empty (drives bindVM schedule path)")
	}
	if len(obj.Finalizers) != 0 {
		t.Fatalf("fixture precondition: VM Finalizers must be empty")
	}

	rec := doApply(t, srv, metav1.KindVM, obj.Name, obj)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	raw := storedRaw(t, st, metav1.KindVM, obj.Name)
	var stored vmv1.VM
	if err := json.Unmarshal(raw.Value, &stored); err != nil {
		t.Fatalf("decode stored VM: %v", err)
	}

	// Sanity: we really took the schedule branch (an empty NodeName got bound).
	if stored.NodeName == "" {
		t.Fatalf("stored VM NodeName is empty; schedule branch did not run")
	}

	want := []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	if len(stored.Finalizers) != len(want) || stored.Finalizers[0] != want[0] {
		t.Fatalf("stored finalizers = %v, want %v (lost across bindVM schedule + put)", stored.Finalizers, want)
	}
}

func TestApplyVMCreateRejectsOffMissingPowerOffMode(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	obj.Spec.PowerState = vmv1.PowerStateOff
	obj.Spec.PowerOffMode = ""

	rec := doApply(t, srv, metav1.KindVM, obj.Name, obj)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
	if _, err := st.Get(context.Background(), storeKey(metav1.KindVM, obj.Name)); err == nil {
		t.Fatalf("rejected VM must not be stored")
	}
}

func TestApplyVMCreateAcceptsOnAndOffPowerStates(t *testing.T) {
	cases := []struct {
		powerState   vmv1.PowerState
		powerOffMode vmv1.PowerOffMode
	}{
		{vmv1.PowerStateOn, ""},
		{vmv1.PowerStateOff, vmv1.PowerOffModeAcpi},
	}
	for _, tc := range cases {
		t.Run(string(tc.powerState), func(t *testing.T) {
			srv, st := newTestServer(t)
			obj := validVM()
			obj.Name = "vm-" + string(tc.powerState)
			obj.UID = "uid-" + string(tc.powerState)
			obj.Spec.PowerState = tc.powerState
			obj.Spec.PowerOffMode = tc.powerOffMode

			rec := doApply(t, srv, metav1.KindVM, obj.Name, obj)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
			}

			stored := storedVM(t, st, obj.Name)
			if stored.Spec.PowerState != tc.powerState {
				t.Fatalf("stored powerState = %q, want %q", stored.Spec.PowerState, tc.powerState)
			}
			if stored.Spec.PowerOffMode != tc.powerOffMode {
				t.Fatalf("stored powerOffMode = %q, want %q", stored.Spec.PowerOffMode, tc.powerOffMode)
			}
		})
	}
}

func TestApplyVMUpdateAllowsOffPowerState(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	if rec := doApply(t, srv, metav1.KindVM, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	update := validVM()
	update.Spec.PowerState = vmv1.PowerStateOff
	update.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
	rec := doApply(t, srv, metav1.KindVM, update.Name, update)
	if rec.Code != http.StatusCreated {
		t.Fatalf("update status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedVM(t, st, obj.Name)
	if stored.Spec.PowerState != vmv1.PowerStateOff {
		t.Fatalf("stored powerState = %q, want %q", stored.Spec.PowerState, vmv1.PowerStateOff)
	}
	if stored.Spec.PowerOffMode != vmv1.PowerOffModeAcpi {
		t.Fatalf("stored powerOffMode = %q, want %q", stored.Spec.PowerOffMode, vmv1.PowerOffModeAcpi)
	}
}

func TestApplyRejectsImmutableVMArchUpdate(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	if rec := doApply(t, srv, metav1.KindVM, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	created := storedVM(t, st, obj.Name)

	update := validVM()
	update.Spec.Arch = "aarch64"
	rec := doApply(t, srv, metav1.KindVM, update.Name, update)
	if rec.Code != http.StatusConflict {
		t.Fatalf("update status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedVM(t, st, obj.Name)
	if stored.Spec.Arch != created.Spec.Arch {
		t.Fatalf("stored arch = %q, want unchanged %q", stored.Spec.Arch, created.Spec.Arch)
	}
}

func TestApplyAllowsColdMutableVMMemoryUpdate(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	if rec := doApply(t, srv, metav1.KindVM, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	// Knife 6 gate 1: a cold-mutable change (memoryMiB) is accepted only when the
	// submitted powerState is Off. The pre-Knife-6 contract ("cold-mutable always
	// accepted at write time") was overturned — a cold config change now requires
	// the VM be declared Off. This still proves cold-mutable fields ARE mutable
	// (the point of the test), now with the Off precondition the design requires.
	update := validVM()
	update.Spec.MemoryMiB = 4096
	update.Spec.PowerState = vmv1.PowerStateOff
	update.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
	rec := doApply(t, srv, metav1.KindVM, update.Name, update)
	if rec.Code != http.StatusCreated {
		t.Fatalf("update status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedVM(t, st, obj.Name)
	if stored.Spec.MemoryMiB != update.Spec.MemoryMiB {
		t.Fatalf("stored memoryMiB = %d, want %d", stored.Spec.MemoryMiB, update.Spec.MemoryMiB)
	}
}

func TestApplyRejectsVolumeCapacityDecrease(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	created := storedVolume(t, st, obj.Name)

	update := validVolume()
	update.Spec.CapacityBytes = created.Spec.CapacityBytes - 1
	rec := doApply(t, srv, metav1.KindVolume, update.Name, update)
	if rec.Code != http.StatusConflict {
		t.Fatalf("update status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedVolume(t, st, obj.Name)
	if stored.Spec.CapacityBytes != created.Spec.CapacityBytes {
		t.Fatalf("stored capacityBytes = %d, want unchanged %d", stored.Spec.CapacityBytes, created.Spec.CapacityBytes)
	}
}

func TestApplyVMUpdatePreservesExistingNodeNameWhenOmitted(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	if rec := doApply(t, srv, metav1.KindVM, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	created := storedVM(t, st, obj.Name)
	if created.NodeName == "" {
		t.Fatalf("fixture failed: created VM was not scheduled")
	}

	update := validVM()
	update.NodeName = ""
	update.Spec.PowerState = vmv1.PowerStateOff
	update.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
	rec := doApply(t, srv, metav1.KindVM, update.Name, update)
	if rec.Code != http.StatusCreated {
		t.Fatalf("update status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedVM(t, st, obj.Name)
	if stored.NodeName != created.NodeName {
		t.Fatalf("stored nodeName = %q, want preserved %q", stored.NodeName, created.NodeName)
	}
	if stored.Spec.PowerState != vmv1.PowerStateOff {
		t.Fatalf("stored powerState = %q, want %q", stored.Spec.PowerState, vmv1.PowerStateOff)
	}

	var resp vmv1.VM
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response VM: %v", err)
	}
	if resp.NodeName != created.NodeName {
		t.Fatalf("response nodeName = %q, want preserved %q", resp.NodeName, created.NodeName)
	}
}

func TestApplyVMUpdateAllowsExplicitSameNodeName(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	if rec := doApply(t, srv, metav1.KindVM, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	created := storedVM(t, st, obj.Name)

	update := validVM()
	update.NodeName = created.NodeName
	update.Spec.PowerState = vmv1.PowerStateOff
	update.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
	rec := doApply(t, srv, metav1.KindVM, update.Name, update)
	if rec.Code != http.StatusCreated {
		t.Fatalf("update status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedVM(t, st, obj.Name)
	if stored.NodeName != created.NodeName {
		t.Fatalf("stored nodeName = %q, want %q", stored.NodeName, created.NodeName)
	}
}

func TestApplyVMUpdateRejectsDifferentNodeNameWithConflict(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	if rec := doApply(t, srv, metav1.KindVM, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	created := storedVM(t, st, obj.Name)

	update := validVM()
	update.NodeName = "node-2"
	update.Spec.PowerState = vmv1.PowerStateOff
	update.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
	rec := doApply(t, srv, metav1.KindVM, update.Name, update)
	if rec.Code != http.StatusConflict {
		t.Fatalf("update status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}

	stored := storedVM(t, st, obj.Name)
	if stored.NodeName != created.NodeName {
		t.Fatalf("stored nodeName = %q, want unchanged %q", stored.NodeName, created.NodeName)
	}
	if stored.Spec.PowerState != created.Spec.PowerState {
		t.Fatalf("stored powerState = %q, want unchanged %q", stored.Spec.PowerState, created.Spec.PowerState)
	}
}

func TestApplyUpdateRejectsUIDChange(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validStoragePool()
	if rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	update := validStoragePool()
	update.UID = "uid-other"
	rec := doApply(t, srv, metav1.KindStoragePool, update.Name, update)
	if rec.Code != http.StatusConflict {
		t.Fatalf("update status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	raw := storedRaw(t, st, metav1.KindStoragePool, obj.Name)
	var stored storagepoolv1.StoragePool
	if err := json.Unmarshal(raw.Value, &stored); err != nil {
		t.Fatalf("decode stored StoragePool: %v", err)
	}
	if stored.UID != obj.UID {
		t.Fatalf("stored uid = %q, want unchanged %q", stored.UID, obj.UID)
	}
}

func TestApplyUpdateCorruptExistingNonVMReturnsInternalError(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validStoragePool()
	if _, err := st.Put(context.Background(), storeKey(metav1.KindStoragePool, obj.Name), []byte("{"), ""); err != nil {
		t.Fatalf("seed corrupt StoragePool: %v", err)
	}

	rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("update status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

func TestApplyVMUpdatePreservesServerOwnedMetadata(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	if rec := doApply(t, srv, metav1.KindVM, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	created := storedVM(t, st, obj.Name)
	created.ResourceVersion = "stored-rv"
	created.DeletionTimestamp = "2026-06-08T12:00:00Z"
	created.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown, metav1.Finalizer("example.com/other")}
	data, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("marshal stored VM: %v", err)
	}
	if _, err := st.Put(context.Background(), storeKey(metav1.KindVM, obj.Name), data, ""); err != nil {
		t.Fatalf("overwrite stored VM: %v", err)
	}

	update := validVM()
	update.Spec.PowerState = vmv1.PowerStateOff
	update.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
	update.Finalizers = nil
	update.DeletionTimestamp = ""
	update.ResourceVersion = ""
	rec := doApply(t, srv, metav1.KindVM, update.Name, update)
	if rec.Code != http.StatusCreated {
		t.Fatalf("update status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedVM(t, st, obj.Name)
	if stored.ResourceVersion != created.ResourceVersion {
		t.Fatalf("stored resourceVersion = %q, want preserved %q", stored.ResourceVersion, created.ResourceVersion)
	}
	if stored.DeletionTimestamp != created.DeletionTimestamp {
		t.Fatalf("stored deletionTimestamp = %q, want preserved %q", stored.DeletionTimestamp, created.DeletionTimestamp)
	}
	if len(stored.Finalizers) != len(created.Finalizers) {
		t.Fatalf("stored finalizers = %v, want preserved %v", stored.Finalizers, created.Finalizers)
	}
	for i := range created.Finalizers {
		if stored.Finalizers[i] != created.Finalizers[i] {
			t.Fatalf("stored finalizers = %v, want preserved %v", stored.Finalizers, created.Finalizers)
		}
	}
	if stored.Spec.PowerState != vmv1.PowerStateOff {
		t.Fatalf("stored powerState = %q, want %q", stored.Spec.PowerState, vmv1.PowerStateOff)
	}
}

func TestApplyVMUpdateCorruptExistingReturnsInternalError(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	if _, err := st.Put(context.Background(), storeKey(metav1.KindVM, obj.Name), []byte("{"), ""); err != nil {
		t.Fatalf("seed corrupt VM: %v", err)
	}

	obj.Spec.PowerState = vmv1.PowerStateOff
	obj.Spec.PowerOffMode = vmv1.PowerOffModeAcpi
	rec := doApply(t, srv, metav1.KindVM, obj.Name, obj)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("update status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestApplySnapshotInjectsNodeNameFromTargetVM 验证：apply 一个 Snapshot 时，apiserver
// 不接受用户提供的 nodeName，而是从 spec.vmRef 指向的目标 VM 派生 nodeName 并落盘——
// 快照必须跑在持有该 VM qcow2 文件的节点上（确定性派生，单一真相源）。本测试 seed 一个
// nodeName=node-1 的目标 VM，apply 指向它的 Snapshot，断言落盘 Snapshot.nodeName==node-1。
func TestApplySnapshotInjectsNodeNameFromTargetVM(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validSnapshot()
	if obj.NodeName != "" {
		t.Fatalf("fixture precondition: Snapshot NodeName must be empty (server-derived)")
	}

	rec := doApply(t, srv, metav1.KindSnapshot, obj.Name, obj)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	// The seeded target VM (seedSnapshotVMRef) is bound to node-1.
	target := storedVM(t, st, obj.Spec.VMRef)
	if target.NodeName == "" {
		t.Fatalf("fixture failed: seeded target VM has no nodeName")
	}

	stored := storedSnapshot(t, st, obj.Name)
	if stored.NodeName != target.NodeName {
		t.Fatalf("stored Snapshot nodeName = %q, want target VM nodeName %q", stored.NodeName, target.NodeName)
	}

	// The response body must reflect the derived nodeName too.
	var resp snapshotv1.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response Snapshot: %v", err)
	}
	if resp.NodeName != target.NodeName {
		t.Fatalf("response nodeName = %q, want %q", resp.NodeName, target.NodeName)
	}
}

// TestApplySnapshotReResolvesNodeNameOnUpdate 验证 I1 修复：Snapshot 的 nodeName 在
// create 和 update 两条路径上都重新解析。一次完全相同的 re-apply（Snapshot spec 不可变，
// 任何 re-apply 都分类为 update）不携带 nodeName，若只在 create 注入，update 会落空
// nodeName，node watch（?nodeName=）将停止路由它。本测试先 create 再 re-apply，断言
// 落盘 nodeName 仍稳定等于目标 VM 的 nodeName。
func TestApplySnapshotReResolvesNodeNameOnUpdate(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validSnapshot()
	if rec := doApply(t, srv, metav1.KindSnapshot, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	created := storedSnapshot(t, st, obj.Name)
	if created.NodeName == "" {
		t.Fatalf("created Snapshot nodeName is empty")
	}

	// Re-apply the identical Snapshot (an update) carrying no nodeName.
	update := validSnapshot()
	if update.NodeName != "" {
		t.Fatalf("fixture precondition: re-apply body must carry no nodeName")
	}
	rec := doApply(t, srv, metav1.KindSnapshot, update.Name, update)
	if rec.Code != http.StatusCreated {
		t.Fatalf("update status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedSnapshot(t, st, obj.Name)
	if stored.NodeName != created.NodeName {
		t.Fatalf("stored nodeName = %q, want re-resolved %q (I1: nodeName must persist on update)", stored.NodeName, created.NodeName)
	}
}

// TestApplySnapshotRejectsMissingVMReference 验证：apply 一个 vmRef 指向不存在 VM 的
// Snapshot → 400（admission ReferenceValidator 拒绝），且不落盘。
func TestApplySnapshotRejectsMissingVMReference(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validSnapshot()
	rec := doApplyWithoutReferenceSeeds(t, srv, metav1.KindSnapshot, obj.Name, obj)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
	if _, err := st.Get(context.Background(), storeKey(metav1.KindSnapshot, obj.Name)); err == nil {
		t.Fatalf("rejected Snapshot must not be stored")
	}
}

func storedSnapshot(t *testing.T, st store.Store, name string) snapshotv1.Snapshot {
	t.Helper()
	raw, err := st.Get(context.Background(), storeKey(metav1.KindSnapshot, name))
	if err != nil {
		t.Fatalf("get stored Snapshot %s: %v", name, err)
	}
	var stored snapshotv1.Snapshot
	if err := json.Unmarshal(raw.Value, &stored); err != nil {
		t.Fatalf("decode stored Snapshot %s: %v", name, err)
	}
	return stored
}

func storedVM(t *testing.T, st store.Store, name string) vmv1.VM {
	t.Helper()
	raw, err := st.Get(context.Background(), storeKey(metav1.KindVM, name))
	if err != nil {
		t.Fatalf("get stored VM %s: %v", name, err)
	}
	var stored vmv1.VM
	if err := json.Unmarshal(raw.Value, &stored); err != nil {
		t.Fatalf("decode stored VM %s: %v", name, err)
	}
	return stored
}

func storedStoragePool(t *testing.T, st store.Store, name string) storagepoolv1.StoragePool {
	t.Helper()
	raw, err := st.Get(context.Background(), storeKey(metav1.KindStoragePool, name))
	if err != nil {
		t.Fatalf("get stored StoragePool %s: %v", name, err)
	}
	var stored storagepoolv1.StoragePool
	if err := json.Unmarshal(raw.Value, &stored); err != nil {
		t.Fatalf("decode stored StoragePool %s: %v", name, err)
	}
	return stored
}

func storedNIC(t *testing.T, st store.Store, name string) nicv1.NIC {
	t.Helper()
	raw, err := st.Get(context.Background(), storeKey(metav1.KindNIC, name))
	if err != nil {
		t.Fatalf("get stored NIC %s: %v", name, err)
	}
	var stored nicv1.NIC
	if err := json.Unmarshal(raw.Value, &stored); err != nil {
		t.Fatalf("decode stored NIC %s: %v", name, err)
	}
	return stored
}

func storedVolume(t *testing.T, st store.Store, name string) volumev1.Volume {
	t.Helper()
	raw, err := st.Get(context.Background(), storeKey(metav1.KindVolume, name))
	if err != nil {
		t.Fatalf("get stored Volume %s: %v", name, err)
	}
	var stored volumev1.Volume
	if err := json.Unmarshal(raw.Value, &stored); err != nil {
		t.Fatalf("decode stored Volume %s: %v", name, err)
	}
	return stored
}

// TestApplyUpdatePreservesNodeOwnedStatus is the regression for the cold-resize
// bug found in e2e: an apply that changes spec (e.g. Volume.capacityBytes) must
// never clobber the node-owned status projection. The caller's manifest carries
// no status, so without preservation the stored status.phase=ready would reset
// to "" and the Volume controller would mis-route to the create path
// (ErrVolumeConflict). status is a PatchStatus-owned subresource (k8s
// spec/status separation + 上下一致): apply touches spec only.
func TestApplyUpdatePreservesNodeOwnedStatus(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	// Node reports a ready status through the PatchStatus subresource.
	reported := volumev1.VolumeStatus{
		Phase:      volumev1.VolumePhaseReady,
		VolumePath: "/var/lib/govirta/pool/pool-block/vm-e2e-001/vm-e2e-disk-0.qcow2",
	}
	statusBody, err := json.Marshal(reported)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if rec := doPatchStatus(t, srv, metav1.KindVolume, obj.Name, statusBody); rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// A spec-only apply update (grow capacity) that omits status must not reset
	// the node-reported ready status.
	created := storedVolume(t, st, obj.Name)
	update := validVolume()
	update.Spec.CapacityBytes = created.Spec.CapacityBytes * 2
	if rec := doApply(t, srv, metav1.KindVolume, update.Name, update); rec.Code != http.StatusCreated {
		t.Fatalf("update status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedVolume(t, st, obj.Name)
	if stored.Status.Phase != volumev1.VolumePhaseReady {
		t.Fatalf("stored status.phase = %q, want preserved %q", stored.Status.Phase, volumev1.VolumePhaseReady)
	}
	if stored.Status.VolumePath != reported.VolumePath {
		t.Fatalf("stored status.volumePath = %q, want preserved %q", stored.Status.VolumePath, reported.VolumePath)
	}
	if stored.Spec.CapacityBytes != update.Spec.CapacityBytes {
		t.Fatalf("stored spec.capacityBytes = %d, want updated %d", stored.Spec.CapacityBytes, update.Spec.CapacityBytes)
	}
}
