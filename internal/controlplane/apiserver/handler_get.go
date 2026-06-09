package apiserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
	"github.com/suknna/govirta/internal/controlplane/store"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// resourceVersionHeader carries the store-assigned ResourceVersion of a single
// fetched object. We expose it as a response header rather than re-wrapping the
// stored JSON: the get path is a pass-through that hands back the exact bytes the
// store holds (already valid resource JSON), so the body stays byte-identical to
// what Apply persisted and this handler remains kind-agnostic — it never decodes
// the object to splice a version field in. List has no single version to carry,
// so it does not set this header.
const resourceVersionHeader = "X-Resource-Version"

// getHandler binds the read verbs. GET /apis/{kind}/{name} fetches one object and
// GET /apis/{kind} lists a kind's collection. Both are straight pass-throughs to
// the store with no caching, so a read always reflects the latest committed write.
func (s *Server) getHandler(mux *http.ServeMux) {
	mux.HandleFunc("GET /apis/{kind}/{name}", s.Get)
	// GET /apis/{kind} is list-or-watch: a watch=true query opens a streaming
	// watch, otherwise it lists the collection (ServeMux cannot route on a query).
	mux.HandleFunc("GET /apis/{kind}", s.ListOrWatch)
}

// Get fetches a single object by kind/name and writes its raw stored JSON with
// HTTP 200, attaching the object's ResourceVersion as the X-Resource-Version
// header. A missing object maps to 404; any other store failure maps to 5xx. On
// failure it writes the uniform {"error": "..."} envelope.
func (s *Server) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	raw, apiErr := s.get(ctx, r)
	if apiErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(apiErr.code)
		if _, err := w.Write(errorBody(apiErr)); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write get error response")
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(resourceVersionHeader, raw.ResourceVersion)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(raw.Value); err != nil {
		// The response is already committed; record the write failure rather than
		// silently discard it.
		zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write get response")
	}
}

// get resolves the kind/name path to a store key and fetches it, classifying
// ErrNotFound as 404 and every other store error as 5xx. It does not decode the
// object: the stored bytes are returned verbatim to the caller.
func (s *Server) get(ctx context.Context, r *http.Request) (store.RawObject, *apiError) {
	kind := metav1.Kind(r.PathValue("kind"))
	name := r.PathValue("name")

	raw, err := s.store.Get(ctx, storeKey(kind, name))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.RawObject{}, notFound(fmt.Errorf("apiserver: get %s/%s: %w", kind, name, err))
		}
		return store.RawObject{}, internalErr(fmt.Errorf("apiserver: get %s/%s: %w", kind, name, err))
	}
	return raw, nil
}

// List returns all objects of a kind as a JSON array with HTTP 200, or the
// uniform {"error": "..."} envelope on failure. The array is empty ("[]", never
// "null") when the collection holds nothing.
func (s *Server) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	body, apiErr := s.list(ctx, r)
	code := http.StatusOK
	if apiErr != nil {
		code = apiErr.code
		body = errorBody(apiErr)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if _, err := w.Write(body); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write list response")
	}
}

// list fetches every object under the kind's key prefix and assembles their raw
// JSON values into a single JSON array. store.List returns objects sorted by key,
// and keys are /govirta/<kind>/<name>, so the array is sorted by name. The stored
// values are already valid JSON objects, so they are spliced directly between the
// array brackets without decoding (keeping the handler kind-agnostic). An empty
// collection yields "[]", satisfying the contract that the array is never "null".
func (s *Server) list(ctx context.Context, r *http.Request) ([]byte, *apiError) {
	kind := metav1.Kind(r.PathValue("kind"))

	// Trailing slash scopes the prefix to this kind's collection so a kind whose
	// name prefixes another cannot bleed into the result.
	raws, err := s.store.List(ctx, admission.ListPrefix(kind))
	if err != nil {
		return nil, internalErr(fmt.Errorf("apiserver: list %s: %w", kind, err))
	}

	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, raw := range raws {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(raw.Value)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}
