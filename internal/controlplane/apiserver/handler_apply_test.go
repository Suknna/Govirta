package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

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

// TestApplyPreservesExistingFinalizers 验证：apply 一个已带 finalizer 的对象时，
// apiserver 不覆盖原值（有条件注入只在 Finalizers 为空时生效）。
// 这与 MAC/调度的有条件注入一致，为未来多 finalizer 场景留口。
func TestApplyPreservesExistingFinalizers(t *testing.T) {
	srv, st := newTestServer(t)

	const existing = metav1.Finalizer("govirta.io/custom")
	obj := validStoragePool()
	obj.Finalizers = []metav1.Finalizer{existing}

	rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	raw := storedRaw(t, st, metav1.KindStoragePool, obj.Name)
	var stored storagepoolv1.StoragePool
	if err := json.Unmarshal(raw.Value, &stored); err != nil {
		t.Fatalf("decode stored StoragePool: %v", err)
	}

	if len(stored.Finalizers) != 1 || stored.Finalizers[0] != existing {
		t.Fatalf("stored finalizers = %v, want %v (existing must be preserved as-is)", stored.Finalizers, []metav1.Finalizer{existing})
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
