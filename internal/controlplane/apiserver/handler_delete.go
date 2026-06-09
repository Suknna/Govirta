package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
	"github.com/suknna/govirta/internal/controlplane/store"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// handler_delete.go implements the entry phase of the finalizer two-phase
// delete. A DELETE never tears down live resources or removes the object: it
// only stamps metadata.deletionTimestamp (signalling "deletion requested") and
// guarantees the node-teardown finalizer is present. The node then drains its
// live resources and removes the finalizer; the apiserver performs the real
// delete only once finalizers is empty — that真删 lives in the finalizers
// endpoint (Task 6), deliberately not here.
//
// 两个核心正确性点：
//   - 反向引用扫描在"首次打戳"（状态2）和"重复删除"（状态3）两条 DELETE 入口路径都跑，
//     被下游引用的对象一律拒绝删除。但"已打 deletionTimestamp 的删除中对象"到"真删"之间
//     仍存在一个窗口：期间若有新对象引用这个删除中对象，当前实现不会在真删时拦截——
//     真删发生时（finalizers 端点判定 finalizers==[] && deletionTimestamp!=""）node 侧
//     已拆净 live 资源（正是拆净才摘的最后一个 finalizer），此刻若因"又被引用"而拒绝真删，
//     对象会变成僵尸：finalizers 已空、live 资源已拆、却永远卡在 etcd，引用它的新对象指向
//     一个 live 资源已不存在的壳，比悬挂引用更糟。所以 finalizers 端点真删时不能也不会再做
//     引用守卫。关闭该窗口的正确位置是 apply 准入侧：apply 的 ReferenceValidator 已拒绝任何
//     引用带 deletionTimestamp 的删除中对象的新建/更新请求，使删除中对象不会获得新的下游引用。
//   - 打戳走 CAS（store.Put 带读到的 ResourceVersion），防止与并发 apply/status
//     写互相覆盖：若期间对象被改写，CAS 失败，让客户端重试而非盲目覆盖。

// deleteHandler binds the delete verb. DELETE /apis/{kind}/{name} requests
// deletion of one object, sharing the single /apis surface with apply/get/status.
func (s *Server) deleteHandler(mux *http.ServeMux) {
	mux.HandleFunc("DELETE /apis/{kind}/{name}", s.Delete)
}

// Delete runs the finalizer-delete entry state machine and writes the result.
// On success it returns HTTP 202 Accepted (deletion accepted/in progress, not
// yet complete — the object still exists until its finalizers drain). On
// failure it writes the uniform {"error": "..."} envelope with a 4xx/5xx code.
func (s *Server) Delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	apiErr := s.delete(ctx, r)
	if apiErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(apiErr.code)
		if _, err := w.Write(errorBody(apiErr)); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write delete error response")
		}
		return
	}

	// 202 Accepted: the delete was accepted and the object is now in the
	// deleting state (deletionTimestamp set), but it is not gone yet — its
	// finalizers must drain first. This mirrors k8s DELETE-with-finalizers.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
}

// delete is the kind-dispatched delete state machine. It resolves kind/name,
// reads the object, and either stamps deletionTimestamp (first delete) or
// returns idempotently (already deleting), guarding both paths with a reverse-
// reference scan run through the admission DeleteChain. ctx is threaded into
// every store and admission call end to end.
func (s *Server) delete(ctx context.Context, r *http.Request) *apiError {
	kind := metav1.Kind(r.PathValue("kind"))
	name := r.PathValue("name")
	key := storeKey(kind, name)

	raw, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound(fmt.Errorf("apiserver: delete %s/%s: %w", kind, name, err))
		}
		return internalErr(fmt.Errorf("apiserver: delete get %s/%s: %w", kind, name, err))
	}

	// Decode only the metadata, keeping the rest of the object as opaque bytes so
	// spec/status are physically preserved on write-back (see metadataPatchObject).
	obj, err := decodeMetadataPatchObject(raw.Value)
	if err != nil {
		return internalErr(fmt.Errorf("apiserver: decode %s/%s for delete: %w", kind, name, err))
	}

	// 反向引用保护经 admission DeleteChain：被下游引用（作为前置依赖）则拒绝（409），强制
	// 调用者先删依赖对象。本检查在"首次打戳"（状态2）和"重复删除/删除进行中"（状态3）两条
	// 入口路径上同样运行——状态3 仍重扫是额外一层防护，拒绝在删除窗口内 out-of-band 冒出的
	// 新引用。VM 是所有权树顶点、无反向依赖边，可直接删除（Volume.vmRef/NIC.vmRef 是归属
	// 回指针，不是 VM 的删除前置依赖，仅在 apply 侧由 ReferenceValidator 约束）。
	admissionReq := admission.Request{
		Operation: admission.OperationDelete,
		Kind:      kind,
		Name:      name,
	}
	if err := admission.DeleteChain(s.store).Validate(ctx, admissionReq); err != nil {
		return admissionToAPIError(err)
	}

	// 状态3：已带 deletionTimestamp（重复删除 / 删除进行中）。不重复打戳，幂等返回 202，
	// 不刷新时间戳（保留首次删除请求的时刻）。引用守卫已在上方统一跑过。
	if obj.Metadata.DeletionTimestamp != "" {
		return nil
	}

	// 状态2：首次删除。打戳：deletionTimestamp = 当前 UTC RFC3339，并防御性确保
	// node-teardown finalizer 在列表里（理论上 apply 时 admission 已注入，这里兜底防止
	// 漏注入即被删导致 node 侧 live 资源无人拆除的泄漏）。
	obj.Metadata.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)
	ensureFinalizer(&obj.Metadata)

	newValue, err := obj.marshal()
	if err != nil {
		return internalErr(fmt.Errorf("apiserver: re-encode %s/%s for delete: %w", kind, name, err))
	}

	// CAS against the version we read: a concurrent apply/status write between Get
	// and here makes this fail with ErrRevisionConflict, surfaced as 409 so the
	// caller retries rather than blindly clobbering the newer revision.
	if _, err := s.store.Put(ctx, key, newValue, raw.ResourceVersion); err != nil {
		if errors.Is(err, store.ErrRevisionConflict) {
			return conflictErr(fmt.Errorf("apiserver: delete %s/%s: %w", kind, name, err))
		}
		return internalErr(fmt.Errorf("apiserver: delete put %s/%s: %w", kind, name, err))
	}

	return nil
}

// ensureFinalizer guarantees the node-teardown finalizer is present without
// disturbing any other finalizers already on the object. Unlike apply's
// injectFinalizer (which only injects into an empty list), this defends an
// in-progress delete: the object must never lose its teardown guard, even if a
// future caller added other finalizers alongside it.
func ensureFinalizer(meta *metav1.ObjectMeta) {
	if !slices.Contains(meta.Finalizers, metav1.FinalizerNodeTeardown) {
		meta.Finalizers = append(meta.Finalizers, metav1.FinalizerNodeTeardown)
	}
}
