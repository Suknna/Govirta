package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
	"github.com/suknna/govirta/internal/controlplane/store"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// statusRetryLimit bounds the read-modify-write retry loop on a CAS conflict.
// A status PATCH reads the current object, splices in the new .status, and
// writes it back conditioned on the version it read. If a concurrent write
// (an apply, or another status report) lands in between, the store returns
// ErrRevisionConflict; we re-read and retry rather than surfacing the 409 to
// the node, because a node's status report is an idempotent up-reconcile of
// observed reality and should converge once the spec write settles. The limit
// keeps a pathological write storm from looping forever — after exhausting it
// we return 409 so the caller can back off and retry.
const statusRetryLimit = 3

// statusHandler binds the status sub-resource verb. PATCH
// /apis/{kind}/{name}/status accepts a node's reported status and merges it
// into the stored object's .status field, leaving .spec and every other field
// untouched. This is the up-reconcile direction (node reality -> master record):
// the master only records observed status here and never lets a status report
// rewrite spec.
func (s *Server) statusHandler(mux *http.ServeMux) {
	mux.HandleFunc("PATCH /apis/{kind}/{name}/status", s.PatchStatus)
}

// PatchStatus merges the request body into the stored object's .status field and
// writes it back, returning the updated object's raw JSON with HTTP 200 and its
// new ResourceVersion in the X-Resource-Version header. A missing object maps to
// 404; an unresolvable version conflict maps to 409; any other store failure maps
// to 5xx. On failure it writes the uniform {"error": "..."} envelope.
func (s *Server) PatchStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	raw, apiErr := s.patchStatus(ctx, r)
	if apiErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(apiErr.code)
		if _, err := w.Write(errorBody(apiErr)); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write status error response")
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(resourceVersionHeader, raw.ResourceVersion)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(raw.Value); err != nil {
		// The response is already committed; record the write failure rather than
		// silently discard it.
		zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write status response")
	}
}

// patchStatus performs the read-modify-write status merge. It is deliberately
// kind-agnostic: it parses the stored object only as far as map[string]json.RawMessage,
// replaces the "status" key with the request body verbatim, and re-marshals. spec
// and all other fields are carried through as their original bytes, so this handler
// physically cannot rewrite spec and never needs to know any of the six concrete
// types. The write is a compare-and-swap against the version that was read; on
// ErrRevisionConflict it re-reads and retries up to statusRetryLimit times.
func (s *Server) patchStatus(ctx context.Context, r *http.Request) (store.RawObject, *apiError) {
	kind := metav1.Kind(r.PathValue("kind"))
	name := r.PathValue("name")
	key := storeKey(kind, name)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return store.RawObject{}, badRequest(fmt.Errorf("apiserver: read status body: %w", err))
	}
	// The reported status must be valid JSON: it becomes the object's .status
	// value. Validating here keeps a malformed body from corrupting the stored
	// object (re-marshal would otherwise embed raw bytes).
	if !json.Valid(body) {
		return store.RawObject{}, badRequest(fmt.Errorf("apiserver: status body is not valid JSON"))
	}

	// Validating admission for the bare status subresource: it checks the body
	// shape (a bare status object, not a full envelope), decodes + validates the
	// per-kind Status enums, and gates on the target object's lifecycle. It never
	// mutates; the read-modify-write merge below still owns the write.
	req := admission.Request{
		Operation:   admission.OperationStatusPatch,
		Subresource: admission.SubresourceStatus,
		Kind:        kind,
		Name:        name,
		NewRaw:      body,
	}
	if err := admission.StatusPatchChain(s.store).Validate(ctx, req); err != nil {
		return store.RawObject{}, admissionToAPIError(err)
	}

	var lastConflict error
	for attempt := 0; attempt < statusRetryLimit; attempt++ {
		raw, gErr := s.store.Get(ctx, key)
		if gErr != nil {
			if errors.Is(gErr, store.ErrNotFound) {
				return store.RawObject{}, notFound(fmt.Errorf("apiserver: status %s/%s: %w", kind, name, gErr))
			}
			return store.RawObject{}, internalErr(fmt.Errorf("apiserver: status get %s/%s: %w", kind, name, gErr))
		}

		currentReq := req
		currentReq.OldRaw = raw.Value
		if err := (admission.TargetObjectValidator{}).Validate(ctx, currentReq); err != nil {
			return store.RawObject{}, admissionToAPIError(err)
		}

		merged, mErr := mergeStatus(raw.Value, body)
		if mErr != nil {
			return store.RawObject{}, internalErr(fmt.Errorf("apiserver: status merge %s/%s: %w", kind, name, mErr))
		}

		// CAS against the version we just read: if a concurrent write changed the
		// object since the Get, the store rejects this and we loop to re-read.
		out, pErr := s.store.Put(ctx, key, merged, raw.ResourceVersion)
		if pErr == nil {
			return out, nil
		}
		if errors.Is(pErr, store.ErrRevisionConflict) {
			lastConflict = pErr
			continue
		}
		return store.RawObject{}, internalErr(fmt.Errorf("apiserver: status put %s/%s: %w", kind, name, pErr))
	}

	// Retries exhausted: the object kept changing under us. Surface 409 so the
	// caller can back off rather than spinning the loop indefinitely here.
	return store.RawObject{}, conflictErr(fmt.Errorf("apiserver: status %s/%s: %w", kind, name, lastConflict))
}

// mergeStatus replaces the "status" key of a stored object's JSON with the
// reported status bytes, preserving every other top-level key as its original
// bytes. It parses the object only as a map of raw messages, so spec and the rest
// pass through untouched and unparsed — the handler stays oblivious to the six
// concrete resource types, mirroring the get/list pass-through design. The
// reported status is assigned verbatim (already verified valid JSON by the caller).
func mergeStatus(object, reportedStatus []byte) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(object, &fields); err != nil {
		return nil, fmt.Errorf("decode stored object: %w", err)
	}
	fields["status"] = json.RawMessage(reportedStatus)
	merged, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode merged object: %w", err)
	}
	return merged, nil
}
