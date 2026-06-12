package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
	"github.com/suknna/govirta/internal/controlplane/store"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// watchObjectField is the JSON key under which a watch event carries the raw
// stored object. The wire shape is one newline-delimited JSON object per event:
// {"type":"ADDED","object":{...}}, matching the k8s-style downstream event
// stream the node consumes.
type watchEventWire struct {
	Type   store.EventType `json:"type"`
	Object json.RawMessage `json:"object"`
}

// nodeNameSelector is the minimal projection the watch handler decodes from a
// stored object to learn which node it belongs to. nodeName lives in
// metadata.nodeName for every first-class kind (set by govirtctl for node-local
// resources and by the scheduler for VM). The store stays kind-agnostic: this is
// the only place nodeName routing is interpreted.
type nodeNameSelector struct {
	Metadata struct {
		NodeName string `json:"nodeName"`
	} `json:"metadata"`
}

// ListOrWatch dispatches GET /apis/{kind}: a request carrying watch=true opens a
// streaming watch, everything else is a plain collection list. The two share a
// route because ServeMux cannot discriminate on a query parameter.
func (s *Server) ListOrWatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("watch") == "true" {
		s.Watch(w, r)
		return
	}
	s.List(w, r)
}

// Watch streams store changes for a kind to a single node as chunked,
// newline-delimited JSON events. The flow is A-直通: it opens store.Watch on the
// kind's key prefix and forwards each event whose object's nodeName matches the
// required nodeName query parameter, filtering the rest out in Go.
//
// Pre-stream failures (missing nodeName, a ResponseWriter that cannot flush, or a
// store that refuses the watch) are reported as the uniform {"error": "..."}
// envelope with a 4xx/5xx status, because no bytes have been committed yet. Once
// the 200 status and headers are flushed the response is committed and any later
// error can only end the stream (return), never rewrite the status.
//
// The watch loop runs in this handler's own goroutine and returns the moment
// r.Context() is done (client disconnect) or the store closes the channel; no
// fire-and-forget goroutine is spawned, so a disconnected client cannot leak a
// watcher.
func (s *Server) Watch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	kind := metav1.Kind(r.PathValue("kind"))

	// nodeName is mandatory: a node dial always carries it, and a watch with no
	// node scope would fan every kind's changes to one node. Missing it is an
	// explicit 400 rather than a silent unfiltered stream.
	nodeName := r.URL.Query().Get("nodeName")
	if nodeName == "" {
		writeWatchError(ctx, w, badRequest(fmt.Errorf("apiserver: watch %s requires the nodeName query parameter", kind)))
		return
	}

	// Chunked streaming needs a Flusher; without one the node would only ever see
	// buffered bytes at close, defeating the live event stream.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeWatchError(ctx, w, internalErr(fmt.Errorf("apiserver: watch %s: response writer does not support flushing", kind)))
		return
	}

	// resourceVersion is the optional resume point: a reconnecting node passes the
	// last version it saw so the store replays objects newer than it as ADDED.
	startRevision := r.URL.Query().Get("resourceVersion")

	// Trailing slash scopes the prefix to this kind's collection so a kind whose
	// name prefixes another cannot bleed into the stream.
	prefix := admission.ListPrefix(kind)
	events, err := s.store.Watch(ctx, prefix, startRevision)
	if err != nil {
		writeWatchError(ctx, w, internalErr(fmt.Errorf("apiserver: open watch %s: %w", kind, err)))
		return
	}

	// Commit the stream: send 200 and flush the headers so the node observes the
	// watch is open before any event arrives. After this point the response is
	// committed and errors can only return.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	enc := json.NewEncoder(w)

	for {
		select {
		case <-ctx.Done():
			// Client disconnected (or the request was cancelled): the store's watch
			// goroutine observes the same ctx and tears itself down, so returning
			// here leaks nothing.
			return
		case ev, open := <-events:
			if !open {
				// Store closed the watch channel (store shutdown); end the stream.
				return
			}

			match, err := nodeNameMatches(ev.Object.Value, nodeName)
			if err != nil {
				// A stored object that does not decode for nodeName extraction is a
				// data problem, not a stream problem: log and skip it rather than
				// killing an otherwise-healthy stream.
				zerolog.Ctx(ctx).Error().Err(err).Str("key", ev.Object.Key).Msg("apiserver: watch nodeName decode")
				continue
			}
			if !match {
				continue
			}

			if err := writeWatchEvent(enc, ev); err != nil {
				// The connection is broken mid-write; recording and returning is the
				// only honest action — the response is already committed.
				zerolog.Ctx(ctx).Error().Err(err).Str("key", ev.Object.Key).Msg("apiserver: watch write event")
				return
			}
			flusher.Flush()
		}
	}
}

// nodeNameMatches reports whether the stored object's nodeName equals want. A
// DELETED event carries an empty Value (the object is gone), which cannot match
// any node and is filtered out. nodeName is read from metadata.nodeName, where
// every first-class kind carries it.
func nodeNameMatches(value []byte, want string) (bool, error) {
	if len(value) == 0 {
		return false, nil
	}
	var sel nodeNameSelector
	if err := json.Unmarshal(value, &sel); err != nil {
		return false, fmt.Errorf("apiserver: decode watched object nodeName: %w", err)
	}
	return sel.Metadata.NodeName == want, nil
}

// writeWatchEvent encodes one event as a single newline-terminated JSON line. The
// stored object bytes are forwarded verbatim via json.RawMessage so the body the
// node receives is byte-identical to what Apply persisted; json.Encoder.Encode
// appends the trailing newline that delimits events.
func writeWatchEvent(enc *json.Encoder, ev store.WatchEvent) error {
	obj := json.RawMessage(ev.Object.Value)
	if len(obj) > 0 {
		withResourceVersion, err := withWatchResourceVersion(obj, ev.Object.ResourceVersion)
		if err != nil {
			return err
		}
		obj = withResourceVersion
	}
	if len(obj) == 0 {
		// An empty object (e.g. a DELETED with no value that somehow reaches here)
		// must still be valid JSON on the wire.
		obj = json.RawMessage("null")
	}
	if err := enc.Encode(watchEventWire{Type: ev.Type, Object: obj}); err != nil {
		return fmt.Errorf("apiserver: encode watch event: %w", err)
	}
	return nil
}

func withWatchResourceVersion(obj json.RawMessage, rv string) (json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(obj, &envelope); err != nil {
		return nil, fmt.Errorf("apiserver: decode watch event object for resourceVersion: %w", err)
	}
	var metadata map[string]json.RawMessage
	if rawMeta, ok := envelope["metadata"]; ok && len(rawMeta) > 0 && string(rawMeta) != "null" {
		if err := json.Unmarshal(rawMeta, &metadata); err != nil {
			return nil, fmt.Errorf("apiserver: decode watch event metadata for resourceVersion: %w", err)
		}
	} else {
		metadata = make(map[string]json.RawMessage)
	}
	rvBytes, err := json.Marshal(rv)
	if err != nil {
		return nil, fmt.Errorf("apiserver: encode watch resourceVersion: %w", err)
	}
	metadata["resourceVersion"] = rvBytes
	metaBytes, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("apiserver: encode watch metadata with resourceVersion: %w", err)
	}
	envelope["metadata"] = metaBytes
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("apiserver: encode watch object with resourceVersion: %w", err)
	}
	return out, nil
}

// writeWatchError renders a pre-stream failure as the uniform error envelope. It
// is only valid before the 200 headers are committed; once streaming starts the
// handler returns instead of calling this.
func writeWatchError(ctx context.Context, w http.ResponseWriter, apiErr *apiError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(apiErr.code)
	if _, err := w.Write(errorBody(apiErr)); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write watch error response")
	}
}
