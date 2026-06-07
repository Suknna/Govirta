package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/rs/zerolog"
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
//   - 反向引用扫描在"首次打戳"和"重复删除"两条路径都跑。状态3 的重扫提供额外一层防护，
//     但它只在客户端主动发起二次 DELETE 时才触发，可能永不发生——真删前的权威引用校验
//     在 finalizers 端点（Task 6），那里才是关闭"打戳到真删之间被新引用"竞态窗口的权威防线。
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
// reference scan. ctx is threaded into every store and guard call end to end.
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

	// 状态3：已带 deletionTimestamp（重复删除 / 删除进行中）。不重复打戳，但仍重扫
	// 反向引用作为额外一层防护——注意这条路径只在客户端主动二次 DELETE 时才走到，
	// 真删前的权威引用校验在 finalizers 端点（Task 6）。
	if obj.Metadata.DeletionTimestamp != "" {
		if apiErr := s.guardNotReferenced(ctx, kind, name); apiErr != nil {
			return apiErr
		}
		// 幂等：已在删除中，直接返回 202，不刷新时间戳（保留首次删除请求的时刻）。
		return nil
	}

	// 状态2：首次删除。先做反向引用保护：被下游引用则拒绝，强制调用者先删依赖对象。
	if apiErr := s.guardNotReferenced(ctx, kind, name); apiErr != nil {
		return apiErr
	}

	// 打戳：deletionTimestamp = 当前 UTC RFC3339，并防御性确保 node-teardown
	// finalizer 在列表里（理论上 apply 时 admission 已注入，这里兜底防止漏注入即被删
	// 导致 node 侧 live 资源无人拆除的泄漏）。
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

// guardNotReferenced runs the reverse-reference scan and maps its outcome to an
// *apiError, or nil when the object is reference-clear. A live reference is a
// 409 whose message names the referencing object ("still referenced by
// <Kind>/<name>") so the caller knows what to remove first. An unknown kind is a
// caller-facing 404 (the kind collection does not exist); any other guard
// failure (a store list / decode error) is a 5xx — errors 向上传播，从不吞掉。
func (s *Server) guardNotReferenced(ctx context.Context, kind metav1.Kind, name string) *apiError {
	referencedBy, referenced, err := s.referenceGuard(ctx, kind, name)
	if err != nil {
		if errors.Is(err, ErrUnknownKind) {
			return notFound(err)
		}
		return internalErr(fmt.Errorf("apiserver: reference guard %s/%s: %w", kind, name, err))
	}
	if referenced {
		return conflictErr(fmt.Errorf("apiserver: cannot delete %s/%s: still referenced by %s", kind, name, referencedBy))
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
