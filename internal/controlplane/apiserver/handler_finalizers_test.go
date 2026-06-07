package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// doPatchFinalizers submits {"remove": <remove>} to PATCH
// /apis/{kind}/{name}/finalizers through the server's handler and returns the
// recorded response.
func doPatchFinalizers(t *testing.T, srv *Server, kind metav1.Kind, name string, remove metav1.Finalizer) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(finalizerPatch{Remove: remove})
	if err != nil {
		t.Fatalf("marshal finalizer patch: %v", err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/apis/"+string(kind)+"/"+name+"/finalizers", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// stampDeletionTimestamp drives a DELETE through the handler so the seeded object
// carries a real deletionTimestamp (the first phase of the two-phase delete),
// putting it into the deleting state the finalizers endpoint then收口.
func stampDeletionTimestamp(t *testing.T, srv *Server, kind metav1.Kind, name string) {
	t.Helper()
	rec := doDelete(t, srv, kind, name)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("seed delete (stamp) = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
}

// TestPatchFinalizersRemovesLastWithDeletionTimestampReallyDeletes covers the
// real-delete收口: an object marked for deletion (deletionTimestamp set) whose
// last finalizer is removed must physically disappear from the store. The
// handler returns 200 with an empty body, and a follow-up store.Get returns
// ErrNotFound — the object is gone, not merely trimmed.
func TestPatchFinalizersRemovesLastWithDeletionTimestampReallyDeletes(t *testing.T) {
	srv, st := newTestServer(t)

	// Seed a Volume (apply injects the node-teardown finalizer), then DELETE it so
	// it carries a deletionTimestamp and the teardown finalizer — exactly the
	// deleting state a node would摘 its finalizer from.
	vol := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, vol.Name, vol); rec.Code != http.StatusCreated {
		t.Fatalf("seed volume apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	stampDeletionTimestamp(t, srv, metav1.KindVolume, vol.Name)

	// Sanity: the object is in the deleting state with exactly the teardown
	// finalizer, so removing it empties the list.
	meta := storedMeta(t, st, metav1.KindVolume, vol.Name)
	if meta.DeletionTimestamp == "" {
		t.Fatalf("precondition: deletionTimestamp empty; want a stamp before finalizer removal")
	}
	if !slices.Contains(meta.Finalizers, metav1.FinalizerNodeTeardown) {
		t.Fatalf("precondition: finalizers = %v, want to contain %q", meta.Finalizers, metav1.FinalizerNodeTeardown)
	}

	rec := doPatchFinalizers(t, srv, metav1.KindVolume, vol.Name, metav1.FinalizerNodeTeardown)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// 真删收口: the response body is empty (the object no longer exists to return).
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty (real delete returns no object)", rec.Body.String())
	}

	// The object must be physically gone — a real store.Delete, not a trim.
	if _, err := st.Get(context.Background(), storeKey(metav1.KindVolume, vol.Name)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("store.Get after finalizer-empty delete: err = %v, want ErrNotFound (object must be really deleted)", err)
	}
}

// TestPatchFinalizersRemovesOneKeepsOthers covers the trim-and-write-back path:
// removing one finalizer while others remain leaves the object in the store with
// a shortened finalizer list, and spec/status carried through byte-for-byte (the
// finalizers handler physically cannot rewrite spec via the pass-through model).
func TestPatchFinalizersRemovesOneKeepsOthers(t *testing.T) {
	srv, st := newTestServer(t)

	// Seed a Volume carrying two finalizers (the default node-teardown plus an
	// extra one) and a deletionTimestamp, so removing one still leaves the other.
	const extra metav1.Finalizer = "govirta.io/extra-guard"
	vol := validVolume()
	vol.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown, extra}
	vol.DeletionTimestamp = "2024-01-01T00:00:00Z"
	seedRaw, err := json.Marshal(vol)
	if err != nil {
		t.Fatalf("marshal seed volume: %v", err)
	}
	if _, err := st.Put(context.Background(), storeKey(metav1.KindVolume, vol.Name), seedRaw, ""); err != nil {
		t.Fatalf("seed volume put: %v", err)
	}

	rec := doPatchFinalizers(t, srv, metav1.KindVolume, vol.Name, metav1.FinalizerNodeTeardown)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// The object still exists; only the named finalizer was removed.
	after := storedRaw(t, st, metav1.KindVolume, vol.Name)
	var stored volumev1.Volume
	if err := json.Unmarshal(after.Value, &stored); err != nil {
		t.Fatalf("decode stored Volume: %v", err)
	}
	if slices.Contains(stored.Finalizers, metav1.FinalizerNodeTeardown) {
		t.Fatalf("finalizers = %v, removed finalizer %q still present", stored.Finalizers, metav1.FinalizerNodeTeardown)
	}
	if !slices.Contains(stored.Finalizers, extra) {
		t.Fatalf("finalizers = %v, want to still contain %q (only the named one removed)", stored.Finalizers, extra)
	}
	if len(stored.Finalizers) != 1 {
		t.Fatalf("finalizers = %v, want exactly one remaining", stored.Finalizers)
	}
	// deletionTimestamp must survive: it is metadata the removal must not disturb.
	if stored.DeletionTimestamp != vol.DeletionTimestamp {
		t.Fatalf("deletionTimestamp = %q, want %q (unchanged)", stored.DeletionTimestamp, vol.DeletionTimestamp)
	}
	// Spec must be byte-for-byte the seeded spec: finalizers patch never touches spec.
	if stored.Spec != vol.Spec {
		t.Fatalf("spec changed: got %+v, want %+v", stored.Spec, vol.Spec)
	}

	// The response body is the freshly stored object: same trimmed finalizers,
	// same spec.
	var resp volumev1.Volume
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response Volume: %v", err)
	}
	if len(resp.Finalizers) != 1 || resp.Finalizers[0] != extra {
		t.Fatalf("response finalizers = %v, want [%q]", resp.Finalizers, extra)
	}
	if resp.Spec != vol.Spec {
		t.Fatalf("response spec changed: got %+v, want %+v", resp.Spec, vol.Spec)
	}
}

// TestPatchFinalizersRemoveAbsentIsIdempotent covers the idempotent no-op: removing
// a finalizer the object does not carry changes nothing — the object stays, its
// finalizer list is unchanged, and the call still succeeds (a node retrying its
// finalizer摘除 must converge regardless of whether a prior attempt already landed).
func TestPatchFinalizersRemoveAbsentIsIdempotent(t *testing.T) {
	srv, st := newTestServer(t)

	vol := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, vol.Name, vol); rec.Code != http.StatusCreated {
		t.Fatalf("seed volume apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	before := storedMeta(t, st, metav1.KindVolume, vol.Name)

	const absent metav1.Finalizer = "govirta.io/not-present"
	rec := doPatchFinalizers(t, srv, metav1.KindVolume, vol.Name, absent)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// The object must still exist with its finalizer list unchanged.
	after := storedMeta(t, st, metav1.KindVolume, vol.Name)
	if !slices.Equal(after.Finalizers, before.Finalizers) {
		t.Fatalf("finalizers changed: got %v, want %v (removing an absent finalizer is a no-op)", after.Finalizers, before.Finalizers)
	}
}

// TestPatchFinalizersEmptyWithoutDeletionTimestampDoesNotDelete is the key safety
// case: an object with NO deletionTimestamp (no deletion intent) whose finalizer
// list is emptied must NOT be really deleted — it is a live object that merely
// happens to have no finalizers, and真删 would be a wrong removal. The handler
// only writes the trimmed (now empty) finalizer list back; the object survives.
func TestPatchFinalizersEmptyWithoutDeletionTimestampDoesNotDelete(t *testing.T) {
	srv, st := newTestServer(t)

	// Seed a live Volume (apply, so NO deletionTimestamp) carrying only the
	// node-teardown finalizer. Removing it empties the list — but with no deletion
	// intent, the object must stay.
	vol := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, vol.Name, vol); rec.Code != http.StatusCreated {
		t.Fatalf("seed volume apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	// Precondition: no deletion intent.
	if meta := storedMeta(t, st, metav1.KindVolume, vol.Name); meta.DeletionTimestamp != "" {
		t.Fatalf("precondition: deletionTimestamp = %q, want empty (live object)", meta.DeletionTimestamp)
	}

	rec := doPatchFinalizers(t, srv, metav1.KindVolume, vol.Name, metav1.FinalizerNodeTeardown)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Write-back path: the response carries the current object, not an empty body.
	if rec.Body.Len() == 0 {
		t.Fatalf("body empty; want the written-back object (no real delete without deletion intent)")
	}

	// The object MUST still exist — emptying finalizers on a non-deleting object
	// must never trigger真删.
	after, err := st.Get(context.Background(), storeKey(metav1.KindVolume, vol.Name))
	if err != nil {
		t.Fatalf("store.Get after finalizer removal: err = %v, want the object to survive (no deletion intent)", err)
	}
	var stored volumev1.Volume
	if err := json.Unmarshal(after.Value, &stored); err != nil {
		t.Fatalf("decode stored Volume: %v", err)
	}
	if len(stored.Finalizers) != 0 {
		t.Fatalf("finalizers = %v, want empty (the named finalizer was removed)", stored.Finalizers)
	}
	if stored.DeletionTimestamp != "" {
		t.Fatalf("deletionTimestamp = %q, want still empty (write-back must not invent deletion intent)", stored.DeletionTimestamp)
	}
	// Spec preserved byte-for-byte through the write-back.
	if stored.Spec != vol.Spec {
		t.Fatalf("spec changed: got %+v, want %+v", stored.Spec, vol.Spec)
	}
}

// TestPatchFinalizersConcurrentWriteReturns409 covers the CAS branch: the
// write-back is a conditional Put against the ResourceVersion read in the same
// request. If a concurrent write bumps the revision between Get and Put,
// store.ErrRevisionConflict must surface as 409 so the caller retries rather than
// clobbering the newer revision. We reuse stalePatchStore (from
// handler_status_test.go, same package) to force the conditional Put to fail.
func TestPatchFinalizersConcurrentWriteReturns409(t *testing.T) {
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

	// Wrap so the first conditional (expectedVersion != "") Put fails with
	// ErrRevisionConflict — exactly the finalizers write-back CAS. Unconditional
	// seeds (apply) pass through, so the Volume is created normally.
	wrapped := &stalePatchStore{Store: st, failsRemaining: 1}
	srv := NewServer(wrapped, alloc, scheduler.NewNoopScheduler(), []string{"node-1"}, "")

	// Seed a Volume carrying two finalizers, so removing one leaves the list
	// non-empty and the handler reaches the write-back CAS Put (rather than the
	// real-delete branch).
	const extra metav1.Finalizer = "govirta.io/extra-guard"
	vol := validVolume()
	vol.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown, extra}
	vol.DeletionTimestamp = "2024-01-01T00:00:00Z"
	seedRaw, err := json.Marshal(vol)
	if err != nil {
		t.Fatalf("marshal seed volume: %v", err)
	}
	if _, err := st.Put(context.Background(), storeKey(metav1.KindVolume, vol.Name), seedRaw, ""); err != nil {
		t.Fatalf("seed volume put: %v", err)
	}

	rec := doPatchFinalizers(t, srv, metav1.KindVolume, vol.Name, metav1.FinalizerNodeTeardown)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (CAS conflict on write-back must surface as 409); body=%s",
			rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
	if wrapped.failsRemaining != 0 {
		t.Fatalf("forced CAS conflict not consumed; failsRemaining = %d", wrapped.failsRemaining)
	}
}

// TestPatchFinalizersMissingObjectReturns404 covers the 404 error branch: a
// PATCH of the finalizers sub-resource on an object that was never created
// resolves to store.ErrNotFound -> 404, with the uniform {"error": "..."}
// envelope carrying a non-empty message.
// 缺失对象（store.ErrNotFound）必须映射为 404，并带非空 error body。
func TestPatchFinalizersMissingObjectReturns404(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := doPatchFinalizers(t, srv, metav1.KindVolume, "nonexistent", metav1.FinalizerNodeTeardown)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

// TestPatchFinalizersMissingRemoveFieldReturns400 covers the required-field
// validation branch: a PATCH body whose "remove" field is empty (absent, or the
// empty string) must be rejected with 400 before any store access, with the
// uniform {"error": "..."} envelope carrying a non-empty message.
// 请求体缺 remove 字段（或 remove 为空串）直接触发必填校验 -> 400 + 非空 error body。
func TestPatchFinalizersMissingRemoveFieldReturns400(t *testing.T) {
	srv, st := newTestServer(t)

	// Seed a live Volume so the failure is unambiguously the required-field
	// check rather than a missing object — the validation rejects the body
	// before the store is ever read.
	vol := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, vol.Name, vol); rec.Code != http.StatusCreated {
		t.Fatalf("seed volume apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	// Empty remove (the zero value of finalizerPatch) trips the required-field
	// guard: patch.Remove == "" -> 400.
	rec := doPatchFinalizers(t, srv, metav1.KindVolume, vol.Name, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}

	// The required-field check must short-circuit before touching the store:
	// the seeded object stays intact with its finalizers untouched.
	after := storedMeta(t, st, metav1.KindVolume, vol.Name)
	if !slices.Contains(after.Finalizers, metav1.FinalizerNodeTeardown) {
		t.Fatalf("finalizers = %v, want still to contain %q (a rejected 400 must not mutate the object)", after.Finalizers, metav1.FinalizerNodeTeardown)
	}
}
