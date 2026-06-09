package apiserver

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store/fake"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// doDelete submits DELETE /apis/{kind}/{name} through the server's handler and
// returns the recorded response.
func doDelete(t *testing.T, srv *Server, kind metav1.Kind, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/apis/"+string(kind)+"/"+name, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// storedMeta fetches the persisted object for kind/name and decodes only its
// metadata, so a test can assert deletionTimestamp/finalizers without pinning
// the concrete kind type.
func storedMeta(t *testing.T, st *fake.Store, kind metav1.Kind, name string) metav1.ObjectMeta {
	t.Helper()
	raw := storedRaw(t, st, kind, name)
	var obj struct {
		Metadata metav1.ObjectMeta `json:"metadata"`
	}
	if err := json.Unmarshal(raw.Value, &obj); err != nil {
		t.Fatalf("decode stored %s/%s metadata: %v", kind, name, err)
	}
	return obj.Metadata
}

// TestDeleteMissingObjectReturns404 covers the state-machine entry: a DELETE of
// an object that was never created resolves to store.ErrNotFound -> 404.
func TestDeleteMissingObjectReturns404(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := doDelete(t, srv, metav1.KindVolume, "nonexistent")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

// TestDeleteReferencedObjectReturns409 covers state2 reference protection: a
// Volume still referenced by a VM cannot be deleted; the 409 message must name
// the referencing object so the caller knows what to remove first.
func TestDeleteReferencedObjectReturns409(t *testing.T) {
	srv, st := newTestServer(t)

	// Seed a Volume and a VM whose volumeRefs names it. validVM().Spec.VolumeRefs
	// is ["vol-a"] and validVolume().Name is "vol-a", so the VM references it.
	vol := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, vol.Name, vol); rec.Code != http.StatusCreated {
		t.Fatalf("seed volume apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	vm := validVM()
	if rec := doApply(t, srv, metav1.KindVM, vm.Name, vm); rec.Code != http.StatusCreated {
		t.Fatalf("seed vm apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	rec := doDelete(t, srv, metav1.KindVolume, vol.Name)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	msg := decodeError(t, rec)
	if !strings.Contains(msg, "still referenced by") {
		t.Fatalf("error %q does not explain the reference", msg)
	}
	if want := refIdentity(metav1.KindVM, vm.Name); !strings.Contains(msg, want) {
		t.Fatalf("error %q does not name the referencing object %q", msg, want)
	}

	// The object must NOT have been stamped: a rejected delete leaves it intact.
	meta := storedMeta(t, st, metav1.KindVolume, vol.Name)
	if meta.DeletionTimestamp != "" {
		t.Fatalf("deletionTimestamp = %q, want empty (rejected delete must not stamp)", meta.DeletionTimestamp)
	}
}

// TestDeleteUnreferencedStampsTimestampPreservesSpec covers the state2 happy
// path: an unreferenced object is accepted (202), and afterward it still exists
// carrying a non-empty RFC3339 deletionTimestamp, its finalizer, and a spec that
// is byte-for-byte unchanged (the delete handler never rewrites spec/status).
func TestDeleteUnreferencedStampsTimestampPreservesSpec(t *testing.T) {
	srv, st := newTestServer(t)

	// Seed a Volume with no referencing VM, so the reference guard passes.
	vol := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, vol.Name, vol); rec.Code != http.StatusCreated {
		t.Fatalf("seed volume apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	before := storedRaw(t, st, metav1.KindVolume, vol.Name)

	rec := doDelete(t, srv, metav1.KindVolume, vol.Name)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	// The object must still exist (not really deleted in this phase), stamped and
	// finalizer-guarded.
	after := storedRaw(t, st, metav1.KindVolume, vol.Name)
	meta := storedMeta(t, st, metav1.KindVolume, vol.Name)
	if meta.DeletionTimestamp == "" {
		t.Fatalf("deletionTimestamp empty after delete; want a non-empty RFC3339 stamp")
	}
	if _, err := time.Parse(time.RFC3339, meta.DeletionTimestamp); err != nil {
		t.Fatalf("deletionTimestamp %q is not RFC3339: %v", meta.DeletionTimestamp, err)
	}
	if !slices.Contains(meta.Finalizers, metav1.FinalizerNodeTeardown) {
		t.Fatalf("finalizers = %v, want to contain %q", meta.Finalizers, metav1.FinalizerNodeTeardown)
	}

	// CAS must have bumped the revision (a real conditional write happened).
	if after.ResourceVersion == before.ResourceVersion {
		t.Fatalf("ResourceVersion unchanged %q; stamping must write a new revision", after.ResourceVersion)
	}

	// Spec must be byte-for-byte the seeded spec: delete never touches spec.
	var storedVol volumev1.Volume
	if err := json.Unmarshal(after.Value, &storedVol); err != nil {
		t.Fatalf("decode stored Volume: %v", err)
	}
	if storedVol.Spec != vol.Spec {
		t.Fatalf("spec changed: got %+v, want %+v", storedVol.Spec, vol.Spec)
	}

	// metadata round-trip: the delete path decodes metadata into the typed
	// ObjectMeta and re-marshals it, so identity fields outside deletionTimestamp/
	// finalizers must survive the stamp untouched (guards against silent drop if
	// the ObjectMeta model ever drifts from the stored metadata shape).
	if storedVol.UID != vol.UID {
		t.Fatalf("metadata.uid changed: got %q, want %q", storedVol.UID, vol.UID)
	}
	if storedVol.Name != vol.Name {
		t.Fatalf("metadata.name changed: got %q, want %q", storedVol.Name, vol.Name)
	}
}

// TestDeleteRepeatedIsIdempotent covers state3: a second DELETE of an
// already-deleting object returns 202 without refreshing the timestamp (the
// first deletion request's instant is preserved) and without bumping the
// revision (no redundant write).
func TestDeleteRepeatedIsIdempotent(t *testing.T) {
	srv, st := newTestServer(t)

	vol := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, vol.Name, vol); rec.Code != http.StatusCreated {
		t.Fatalf("seed volume apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	if rec := doDelete(t, srv, metav1.KindVolume, vol.Name); rec.Code != http.StatusAccepted {
		t.Fatalf("first delete = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	first := storedRaw(t, st, metav1.KindVolume, vol.Name)
	firstMeta := storedMeta(t, st, metav1.KindVolume, vol.Name)

	if rec := doDelete(t, srv, metav1.KindVolume, vol.Name); rec.Code != http.StatusAccepted {
		t.Fatalf("repeat delete = %d, want 202 (idempotent); body=%s", rec.Code, rec.Body.String())
	}
	second := storedRaw(t, st, metav1.KindVolume, vol.Name)
	secondMeta := storedMeta(t, st, metav1.KindVolume, vol.Name)

	if secondMeta.DeletionTimestamp != firstMeta.DeletionTimestamp {
		t.Fatalf("deletionTimestamp refreshed on repeat: first %q, second %q (must preserve original)",
			firstMeta.DeletionTimestamp, secondMeta.DeletionTimestamp)
	}
	if second.ResourceVersion != first.ResourceVersion {
		t.Fatalf("ResourceVersion bumped on idempotent repeat: first %q, second %q (no redundant write expected)",
			first.ResourceVersion, second.ResourceVersion)
	}
}

// TestApplyCannotReferenceStampedObject covers the apply-side guard that closes
// the former delete window: after an object is stamped, a new object cannot start
// referencing it, so no fresh downstream reference can appear before finalizers
// drain.
func TestApplyCannotReferenceStampedObject(t *testing.T) {
	srv, _ := newTestServer(t)

	vol := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, vol.Name, vol); rec.Code != http.StatusCreated {
		t.Fatalf("seed volume apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	// First delete: no referencing VM yet, so it stamps successfully (202).
	if rec := doDelete(t, srv, metav1.KindVolume, vol.Name); rec.Code != http.StatusAccepted {
		t.Fatalf("first delete = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	// A new VM now tries to reference the deleting Volume. Apply admission must
	// reject it before it can create the former stamp-to-finalize window.
	vm := validVM()
	if rec := doApplyWithoutReferenceSeeds(t, srv, metav1.KindVM, vm.Name, vm); rec.Code != http.StatusConflict {
		t.Fatalf("vm apply = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	rec := doDelete(t, srv, metav1.KindVolume, vol.Name)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (failed apply must not create a fresh reference); body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestDeleteRepeatedRescansReferences covers the state3 guard: even after an
// object is already stamped (deletionTimestamp set), a repeated DELETE must
// re-run the reverse-reference scan and reject with 409 if a downstream
// reference exists. Apply admission now blocks new references to a deleting
// object, so this race is closed at the front door; but the state3 guard is a
// defense-in-depth backstop against any reference that appears out-of-band
// (legacy data, direct store write, future code path). We seed the referencing
// VM directly into the store — bypassing apply admission — to exercise exactly
// that backstop.
func TestDeleteRepeatedRescansReferences(t *testing.T) {
	srv, st := newTestServer(t)

	vol := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, vol.Name, vol); rec.Code != http.StatusCreated {
		t.Fatalf("seed volume apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	// First delete: no referencing VM yet, so it stamps successfully (202).
	if rec := doDelete(t, srv, metav1.KindVolume, vol.Name); rec.Code != http.StatusAccepted {
		t.Fatalf("first delete = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	// A VM referencing the deleting Volume appears out-of-band (apply admission
	// would have rejected it, so we write it straight to the store). validVM's
	// Spec.VolumeRefs is ["vol-a"] and validVolume().Name is "vol-a".
	vm := validVM()
	seedStoreObject(t, st, metav1.KindVM, vm.Name, vm)

	// Repeated DELETE must re-scan references and reject: the object is still
	// referenced, so it cannot proceed toward finalize.
	rec := doDelete(t, srv, metav1.KindVolume, vol.Name)
	if rec.Code != http.StatusConflict {
		t.Fatalf("repeat delete = %d, want 409 (state3 must re-scan references); body=%s",
			rec.Code, rec.Body.String())
	}
	if want := refIdentity(metav1.KindVM, vm.Name); !strings.Contains(decodeError(t, rec), want) {
		t.Fatalf("error does not name the referencing object %q", want)
	}
}

// TestDeleteConcurrentWriteReturns409 covers the state2 CAS branch: stamping
// deletionTimestamp is a conditional Put against the ResourceVersion read in
// the same request. If a concurrent apply/status write bumps the revision
// between Get and Put, store.ErrRevisionConflict must surface as 409 so the
// caller retries rather than clobbering the newer revision. We reuse
// stalePatchStore (from handler_status_test.go, same package) to force the
// conditional Put to fail deterministically.
func TestDeleteConcurrentWriteReturns409(t *testing.T) {
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
	// ErrRevisionConflict — exactly the delete-stamp CAS. Unconditional seeds
	// (apply) pass through, so the Volume is created normally.
	wrapped := &stalePatchStore{Store: st, failsRemaining: 1}
	srv := NewServer(wrapped, alloc, scheduler.NewNoopScheduler(), []string{"node-1"}, "")

	// Seed an unreferenced Volume so the reference guard passes and the handler
	// reaches the stamping CAS Put.
	vol := validVolume()
	if rec := doApply(t, srv, metav1.KindVolume, vol.Name, vol); rec.Code != http.StatusCreated {
		t.Fatalf("seed volume apply = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	rec := doDelete(t, srv, metav1.KindVolume, vol.Name)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (CAS conflict on stamp must surface as 409); body=%s",
			rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
	if wrapped.failsRemaining != 0 {
		t.Fatalf("forced CAS conflict not consumed; failsRemaining = %d", wrapped.failsRemaining)
	}
}
