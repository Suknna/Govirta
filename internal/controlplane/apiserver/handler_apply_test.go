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

func TestApplyVMCreateRejectsShutdownPowerState(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	obj.Spec.PowerState = vmv1.PowerStateShutdown

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
	for _, powerState := range []vmv1.PowerState{vmv1.PowerStateOn, vmv1.PowerStateOff} {
		t.Run(string(powerState), func(t *testing.T) {
			srv, st := newTestServer(t)
			obj := validVM()
			obj.Name = "vm-" + string(powerState)
			obj.UID = "uid-" + string(powerState)
			obj.Spec.PowerState = powerState

			rec := doApply(t, srv, metav1.KindVM, obj.Name, obj)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
			}

			stored := storedVM(t, st, obj.Name)
			if stored.Spec.PowerState != powerState {
				t.Fatalf("stored powerState = %q, want %q", stored.Spec.PowerState, powerState)
			}
		})
	}
}

func TestApplyVMUpdateAllowsShutdownPowerState(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	obj.Spec.PowerState = vmv1.PowerStateOff
	if rec := doApply(t, srv, metav1.KindVM, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	update := validVM()
	update.Spec.PowerState = vmv1.PowerStateShutdown
	rec := doApply(t, srv, metav1.KindVM, update.Name, update)
	if rec.Code != http.StatusCreated {
		t.Fatalf("update status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedVM(t, st, obj.Name)
	if stored.Spec.PowerState != vmv1.PowerStateShutdown {
		t.Fatalf("stored powerState = %q, want %q", stored.Spec.PowerState, vmv1.PowerStateShutdown)
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

	update := validVM()
	update.Spec.MemoryMiB = 4096
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
	update.Spec.PowerState = vmv1.PowerStateShutdown
	rec := doApply(t, srv, metav1.KindVM, update.Name, update)
	if rec.Code != http.StatusCreated {
		t.Fatalf("update status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	stored := storedVM(t, st, obj.Name)
	if stored.NodeName != created.NodeName {
		t.Fatalf("stored nodeName = %q, want preserved %q", stored.NodeName, created.NodeName)
	}
	if stored.Spec.PowerState != vmv1.PowerStateShutdown {
		t.Fatalf("stored powerState = %q, want %q", stored.Spec.PowerState, vmv1.PowerStateShutdown)
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
	update.Spec.PowerState = vmv1.PowerStateShutdown
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
	update.Spec.PowerState = vmv1.PowerStateShutdown
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
	update.Spec.PowerState = vmv1.PowerStateShutdown
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
	if stored.Spec.PowerState != vmv1.PowerStateShutdown {
		t.Fatalf("stored powerState = %q, want %q", stored.Spec.PowerState, vmv1.PowerStateShutdown)
	}
}

func TestApplyVMUpdateCorruptExistingReturnsInternalError(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validVM()
	if _, err := st.Put(context.Background(), storeKey(metav1.KindVM, obj.Name), []byte("{"), ""); err != nil {
		t.Fatalf("seed corrupt VM: %v", err)
	}

	obj.Spec.PowerState = vmv1.PowerStateShutdown
	rec := doApply(t, srv, metav1.KindVM, obj.Name, obj)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("update status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
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
