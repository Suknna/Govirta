package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/node/controller"
)

// WatchSource is the node's streaming implementation of controller.EventSource.
// It opens GET /apis/{kind}?watch=true against the master, consumes the
// newline-delimited JSON event stream apiserver.Watch emits, and translates each
// wire event into a controller.Event pushed onto an owned channel. It stays
// kind-agnostic: the object bytes are forwarded verbatim and only metadata.name /
// metadata.resourceVersion are projected out for the framework's Key/resume cursor.
type WatchSource struct {
	baseURL  string
	http     *http.Client
	nodeName string
}

// NewWatchSource constructs a WatchSource against an explicit master baseURL,
// scoped to a single node. baseURL and nodeName are mandatory and never
// defaulted: a watch with the wrong control-plane address or an empty node scope
// would silently mis-route, which is worse than failing to build a URL loudly.
//
// hc is a transport knob, not a business default: when nil, http.DefaultClient is
// used, matching New so callers that do not tune transport need not build one.
func NewWatchSource(baseURL string, hc *http.Client, nodeName string) *WatchSource {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &WatchSource{baseURL: baseURL, http: hc, nodeName: nodeName}
}

// watchEventWire is the on-the-wire event shape apiserver.writeWatchEvent emits:
// one newline-terminated JSON object per event. Object is forwarded verbatim as
// raw JSON so the bytes the controller decodes are byte-identical to what the
// master persisted.
type watchEventWire struct {
	Type   controller.EventType `json:"type"`
	Object json.RawMessage      `json:"object"`
}

// objectMeta is the minimal projection pulled from a watched object: name becomes
// the Event.Key (dedup identity) and resourceVersion becomes the resume cursor.
// Everything else in the object is left for the controller to decode from
// Event.Object.
type objectMeta struct {
	Metadata struct {
		Name            string `json:"name"`
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
}

// Watch opens a streaming watch for kind, resuming after startRevision (empty
// means "from the current state", matching the master's resourceVersion
// semantics). It dials GET /apis/{kind}?watch=true&nodeName=..&resourceVersion=..
// and, on a 200, spawns a goroutine that decodes the newline-delimited JSON
// stream and forwards translated controller.Events on the returned channel.
//
// The goroutine is owned by ctx: it exits and closes the channel when ctx is
// cancelled or the stream ends (server hangs up / decode EOF), so there is no
// fire-and-forget watcher to leak. A non-200 response is drained and returned as
// a wrapped error before any goroutine starts.
func (s *WatchSource) Watch(ctx context.Context, kind, startRevision string) (<-chan controller.Event, error) {
	q := url.Values{}
	q.Set("watch", "true")
	q.Set("nodeName", s.nodeName)
	// resourceVersion is always sent: empty is a valid resume point ("from
	// current") that the master's watch handler reads the same as an absent param.
	q.Set("resourceVersion", startRevision)

	reqURL := s.baseURL + "/apis/" + url.PathEscape(kind) + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("node/client: build watch request for %s: %w", kind, err)
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("node/client: watch %s: %w", kind, err)
	}
	if resp.StatusCode != http.StatusOK {
		// Drain the body for the server's explanation, then close it: nothing has
		// been streamed yet, so this is a clean pre-stream failure.
		body, _ := io.ReadAll(resp.Body)
		if cerr := resp.Body.Close(); cerr != nil {
			return nil, fmt.Errorf("node/client: watch %s: unexpected status %d (and close body: %v): %s", kind, resp.StatusCode, cerr, body)
		}
		return nil, fmt.Errorf("node/client: watch %s: unexpected status %d: %s", kind, resp.StatusCode, body)
	}

	out := make(chan controller.Event)
	go s.stream(ctx, kind, resp.Body, out)
	return out, nil
}

// stream decodes the newline-delimited JSON event stream and forwards translated
// events on out until the stream ends or ctx is cancelled. It owns body and out:
// both are released here (body closed, out closed) so the caller's only handle is
// the receive channel. A body-close error is logged rather than dropped (项目铁律:
// 不吞 close 错误); there is no return value to join it into, and the loop has
// already ended by the time close runs.
func (s *WatchSource) stream(ctx context.Context, kind string, body io.ReadCloser, out chan<- controller.Event) {
	defer close(out)
	defer func() {
		if cerr := body.Close(); cerr != nil {
			zerolog.Ctx(ctx).Error().Err(cerr).Str("kind", kind).Msg("node/client: close watch body")
		}
	}()

	dec := json.NewDecoder(body)
	for {
		var wire watchEventWire
		if err := dec.Decode(&wire); err != nil {
			// io.EOF is the normal end (server hung up); any other decode error
			// means the stream broke. Either way the stream is over: log non-EOF
			// for diagnosis and return so the manager's feeder can reconnect.
			if err != io.EOF && ctx.Err() == nil {
				zerolog.Ctx(ctx).Error().Err(err).Str("kind", kind).Msg("node/client: decode watch event")
			}
			return
		}

		var meta objectMeta
		// The object should always decode for metadata; if it does not, the event
		// is unusable for dedup/resume, so skip it rather than push a keyless event.
		if len(wire.Object) > 0 {
			if err := json.Unmarshal(wire.Object, &meta); err != nil {
				zerolog.Ctx(ctx).Error().Err(err).Str("kind", kind).Msg("node/client: decode watch object metadata")
				continue
			}
		}

		ev := controller.Event{
			Type:            wire.Type,
			Key:             meta.Metadata.Name,
			ResourceVersion: meta.Metadata.ResourceVersion,
			Object:          wire.Object,
		}

		select {
		case <-ctx.Done():
			return
		case out <- ev:
		}
	}
}
