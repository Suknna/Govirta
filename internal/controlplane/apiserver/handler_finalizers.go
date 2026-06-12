package apiserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
	"github.com/suknna/govirta/internal/controlplane/store"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// handler_finalizers.go implements the exit phase of the finalizer two-phase
// delete: the finalizers sub-resource through which a node摘除自己的 finalizer
// once its live resources are torn down. It is the deliberate counterpart to the
// DELETE entry phase (handler_delete.go): DELETE only stamps deletionTimestamp,
// and the real delete (真删) happens here — and only here — when the last
// finalizer is removed from an object that has been marked for deletion.
//
// 最小权限：body 只支持 {"remove": "<finalizer>"}。两类保证强度不同，区分清楚：
//   - spec/status 是字节级物理透传保证：复用 metadataPatchObject 只解 metadata，
//     spec/status 以原始字节穿过 re-marshal，端点物理上无法改写它们。
//   - metadata 不是字节级物理保证：metadata 被 decodeMetadataPatchObject 完整解码再
//     re-marshal，模型层面能改任意 metadata 字段。它的最小权限来自“输入契约 + handler
//     行为”：请求体 finalizerPatch 只有 remove 一个字段，handler 也只动 Finalizers。
//     不要高估 metadata 侧的保证强度——它是契约约束，不是物理隔离。
//
// 收口的安全语义（关键）：清空 finalizer 触发真删，当且仅当对象带 deletionTimestamp
// （存在删除意图）。若对象没有 deletionTimestamp 却被清空了 finalizer，绝不真删——
// 那会误删一个仍然存活、只是恰好没有 finalizer 的对象。这条路径只回写。

// finalizersHandler binds the finalizers sub-resource verb. PATCH
// /apis/{kind}/{name}/finalizers removes one finalizer from an object and, when
// that empties the finalizer list on a deletion-marked object, performs the real
// delete. It shares the single /apis surface with apply/get/status/delete; the
// {kind}/{name}/finalizers path distinguishes it from the {kind}/{name}/status
// PATCH sub-resource (Go 1.22+ ServeMux routes on the full pattern).
func (s *Server) finalizersHandler(mux *http.ServeMux) {
	mux.HandleFunc("PATCH /apis/{kind}/{name}/finalizers", s.PatchFinalizers)
}

// PatchFinalizers removes the requested finalizer from the stored object via a
// read-modify-write, then either really deletes the object (last finalizer gone
// and deletion was requested) or writes the trimmed object back. On the delete
// branch it returns HTTP 200 with an empty body; on the write-back branch it
// returns HTTP 200 with the current object's raw JSON and its ResourceVersion in
// the X-Resource-Version header. A missing object maps to 404, a CAS conflict to
// 409, a malformed body to 400, and any other store failure to 5xx; on failure it
// writes the uniform {"error": "..."} envelope.
func (s *Server) PatchFinalizers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	raw, deleted, apiErr := s.patchFinalizers(ctx, r)
	if apiErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(apiErr.code)
		if _, err := w.Write(errorBody(apiErr)); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write finalizers error response")
		}
		return
	}

	// 真删收口：对象已消失，没有当前字节可回。返回 200 + 空 body。
	if deleted {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		return
	}

	// 回写：对象仍在（删除中但还剩其它 finalizer）。返回当前对象字节 + 新版本号。
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(resourceVersionHeader, raw.ResourceVersion)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(raw.Value); err != nil {
		// The response is already committed; record the write failure rather than
		// silently discard it.
		zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write finalizers response")
	}
}

// patchFinalizers performs the read-modify-write finalizer removal and decides
// the outcome. It returns (raw, true, nil) when the object was really deleted
// (its finalizer list emptied while a deletionTimestamp marked it for deletion),
// (raw, false, nil) when the trimmed object was written back, or a classified
// *apiError. ctx is threaded into every store call end to end.
//
// 透传保护：通过 metadataPatchObject 只解 metadata，spec/status 以原始字节穿过，
// 移除 finalizer 物理上无法改写 spec/status。CAS 用读到的 ResourceVersion 作前置，
// 与并发 apply/status/delete 写竞争时返回 409 让调用者重试，而非盲目覆盖更新的版本。
func (s *Server) patchFinalizers(ctx context.Context, r *http.Request) (store.RawObject, bool, *apiError) {
	kind := metav1.Kind(r.PathValue("kind"))
	name := r.PathValue("name")
	key := storeKey(kind, name)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return store.RawObject{}, false, badRequest(fmt.Errorf("apiserver: read finalizers body: %w", err))
	}
	patch, err := admission.DecodeFinalizerPatch(body)
	if err != nil {
		return store.RawObject{}, false, badRequest(fmt.Errorf("apiserver: decode finalizers patch: %w", err))
	}
	bodyReq := admission.Request{
		Operation:   admission.OperationFinalizersPatch,
		Subresource: admission.SubresourceFinalizers,
		Kind:        kind,
		Name:        name,
		NewRaw:      body,
		NewObject:   patch,
	}
	if err := admission.FinalizersPatchBodyChain().Validate(ctx, bodyReq); err != nil {
		return store.RawObject{}, false, admissionToAPIError(err)
	}

	raw, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.RawObject{}, false, notFound(fmt.Errorf("apiserver: finalizers %s/%s: %w", kind, name, err))
		}
		return store.RawObject{}, false, internalErr(fmt.Errorf("apiserver: finalizers get %s/%s: %w", kind, name, err))
	}

	targetReq := bodyReq
	targetReq.OldRaw = raw.Value
	if err := admission.FinalizersPatchTargetChain().Validate(ctx, targetReq); err != nil {
		return store.RawObject{}, false, admissionToAPIError(err)
	}

	// Decode only the metadata, keeping the rest of the object as opaque bytes so
	// spec/status are physically preserved on write-back (see metadataPatchObject).
	obj, err := decodeMetadataPatchObject(raw.Value)
	if err != nil {
		return store.RawObject{}, false, internalErr(fmt.Errorf("apiserver: decode %s/%s for finalizers: %w", kind, name, err))
	}

	// Remove the whitelisted node-teardown finalizer. Removing that finalizer when
	// it is already absent remains an idempotent no-op for deleting objects; any
	// non-whitelisted finalizer was rejected by admission before this point.
	obj.Metadata.Finalizers = slices.DeleteFunc(obj.Metadata.Finalizers, func(f metav1.Finalizer) bool {
		return f == patch.Remove
	})

	// 收口判定：finalizer 清空 且 对象带 deletionTimestamp（存在删除意图）→ 真删。
	// 这是真删唯一发生的地方：DELETE 只打戳，等所有 finalizer 拆净才在此收口。
	// 真删同样以本次读取到的 ResourceVersion 为 CAS 条件；若 Get→DeleteIfVersion
	// 之间发生并发写，返回 409 让调用者重试，避免盲删更新后的对象。
	if len(obj.Metadata.Finalizers) == 0 && obj.Metadata.DeletionTimestamp != "" {
		if err := s.store.DeleteIfVersion(ctx, key, raw.ResourceVersion); err != nil {
			if errors.Is(err, store.ErrRevisionConflict) {
				return store.RawObject{}, false, conflictErr(fmt.Errorf("apiserver: finalizers delete %s/%s: %w", kind, name, err))
			}
			return store.RawObject{}, false, internalErr(fmt.Errorf("apiserver: finalizers delete %s/%s: %w", kind, name, err))
		}
		return store.RawObject{}, true, nil
	}

	// 还剩 finalizer：删除尚未收口，单纯回写缩短后的列表。没有 deletionTimestamp
	// 的对象已被 FinalizersPatchTargetChain 拒绝，不能到达这里。
	newValue, err := obj.marshal()
	if err != nil {
		return store.RawObject{}, false, internalErr(fmt.Errorf("apiserver: re-encode %s/%s for finalizers: %w", kind, name, err))
	}

	// CAS against the version we read: a concurrent apply/status/delete write
	// between Get and here makes this fail with ErrRevisionConflict, surfaced as
	// 409 so the caller retries rather than blindly clobbering the newer revision.
	out, err := s.store.Put(ctx, key, newValue, raw.ResourceVersion)
	if err != nil {
		if errors.Is(err, store.ErrRevisionConflict) {
			return store.RawObject{}, false, conflictErr(fmt.Errorf("apiserver: finalizers %s/%s: %w", kind, name, err))
		}
		return store.RawObject{}, false, internalErr(fmt.Errorf("apiserver: finalizers put %s/%s: %w", kind, name, err))
	}
	return out, false, nil
}
