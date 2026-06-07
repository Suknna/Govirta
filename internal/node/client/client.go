// Package client is the node's HTTP client to the master apiserver. It speaks
// the project's own /apis surface (the same routes apiserver.Handler registers:
// GET /apis/{kind}/{name}, GET /apis/{kind}, PATCH /apis/{kind}/{name}/status)
// and stays kind-agnostic — every method returns raw JSON bytes so the caller
// (a controller) decodes the concrete apis object itself. This mirrors the
// controller framework's contract (raw Event.Object bytes) and keeps the client
// free of any pkg/apis dependency.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ErrNotFound is returned by Get when the apiserver answers 404 for the named
// object. It is a sentinel (not a formatted error) so callers can branch with
// errors.Is — dependency gating relies on distinguishing "not there yet" from a
// transport failure.
var ErrNotFound = errors.New("node/client: object not found")

// Client talks to one master apiserver over HTTP. baseURL is the apiserver's
// scheme://host[:port] root (no trailing /apis); http is the transport.
type Client struct {
	baseURL string
	http    *http.Client
}

// New constructs a Client against an explicit master baseURL. baseURL is
// mandatory — the package never defaults a master address, since pointing a node
// at the wrong control plane silently is worse than failing loudly.
//
// hc is a behavior knob, not a business default: when nil, http.DefaultClient is
// used so callers that do not care about transport tuning need not build one.
// Callers that need custom timeouts or transports pass their own.
func New(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: baseURL, http: hc}
}

// Get reads a single object by kind and name. On 404 it returns ErrNotFound so
// dependency gating can treat a missing object distinctly; on any other non-2xx
// it returns a wrapped error carrying the status. The returned bytes are the
// object's raw JSON, left for the caller to decode.
func (c *Client) Get(ctx context.Context, kind, name string) (out []byte, err error) {
	path := "/apis/" + url.PathEscape(kind) + "/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("node/client: build get request for %s/%s: %w", kind, name, err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("node/client: get %s/%s: %w", kind, name, err)
	}
	// Named returns + bare-style returns so a body-close failure joins into err on
	// every path, including success (项目铁律: 不吞 close 错误). A plain
	// `return body, nil` would discard the deferred close error.
	defer closeBody(resp.Body, &err)

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("node/client: get %s/%s: %w", kind, name, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError("get", kind, name, resp)
	}

	out, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("node/client: read get %s/%s body: %w", kind, name, err)
	}
	return out, err
}

// List reads all objects of a kind. The returned bytes are the raw JSON array
// the apiserver emits, left for the caller to decode. A non-2xx status yields a
// wrapped error.
func (c *Client) List(ctx context.Context, kind string) (out []byte, err error) {
	path := "/apis/" + url.PathEscape(kind)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("node/client: build list request for %s: %w", kind, err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("node/client: list %s: %w", kind, err)
	}
	// Named returns so a body-close failure joins into err on the success path too.
	defer closeBody(resp.Body, &err)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError("list", kind, "", resp)
	}

	out, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("node/client: read list %s body: %w", kind, err)
	}
	return out, err
}

// PatchStatus reports an object's status up to the master by PATCHing the
// /status sub-resource. status is the raw JSON patch body (the client never
// constructs it). It returns the apiserver's response bytes on success and a
// wrapped error — including the response body text for diagnosis — on any non-2xx.
func (c *Client) PatchStatus(ctx context.Context, kind, name string, status []byte) (out []byte, err error) {
	path := "/apis/" + url.PathEscape(kind) + "/" + url.PathEscape(name) + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+path, bytes.NewReader(status))
	if err != nil {
		return nil, fmt.Errorf("node/client: build patch-status request for %s/%s: %w", kind, name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("node/client: patch status %s/%s: %w", kind, name, err)
	}
	// Named returns so a body-close failure joins into err on the success path too.
	defer closeBody(resp.Body, &err)

	out, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("node/client: read patch-status %s/%s body: %w", kind, name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("node/client: patch status %s/%s: unexpected status %d: %s", kind, name, resp.StatusCode, bytes.TrimSpace(out))
	}
	return out, err
}

// RemoveFinalizer drops one finalizer from an object by PATCHing the
// /finalizers sub-resource with body {"remove":"<finalizer>"}. The node calls
// this after tearing down its live resources so the master can let the object's
// deletion proceed. It returns nil on a 2xx and a wrapped error — including the
// response body text for diagnosis — on any non-2xx. The response body carries
// nothing the node needs, but it is still drained so the underlying connection
// can be reused, and a read failure is propagated.
func (c *Client) RemoveFinalizer(ctx context.Context, kind, name, finalizer string) (err error) {
	body, err := json.Marshal(map[string]string{"remove": finalizer})
	if err != nil {
		return fmt.Errorf("node/client: marshal remove-finalizer body for %s/%s: %w", kind, name, err)
	}

	path := "/apis/" + url.PathEscape(kind) + "/" + url.PathEscape(name) + "/finalizers"
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("node/client: build remove-finalizer request for %s/%s: %w", kind, name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("node/client: remove finalizer %s/%s: %w", kind, name, err)
	}
	// Named returns so a body-close failure joins into err on the success path too.
	defer closeBody(resp.Body, &err)

	// Drain the body even though the node ignores it: an undrained body defeats
	// connection reuse. A read failure must still propagate.
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("node/client: read remove-finalizer %s/%s body: %w", kind, name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("node/client: remove finalizer %s/%s: unexpected status %d: %s", kind, name, resp.StatusCode, bytes.TrimSpace(out))
	}
	return err
}

// statusError builds the wrapped error for a non-2xx response, including the
// response body text so the caller sees the server's explanation. name is "" for
// list (collection) requests and is omitted from the message in that case.
func statusError(op, kind, name string, resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	target := kind
	if name != "" {
		target = kind + "/" + name
	}
	return fmt.Errorf("node/client: %s %s: unexpected status %d: %s", op, target, resp.StatusCode, bytes.TrimSpace(body))
}

// closeBody closes the response body, joining any close error into the caller's
// returned error via a *error out-parameter. A close failure must not be silently
// dropped (项目铁律: 不吞 close 错误), but it also must not mask a successful read,
// so it is joined only when set through the deferred assignment.
func closeBody(body io.ReadCloser, errp *error) {
	if cerr := body.Close(); cerr != nil {
		*errp = errors.Join(*errp, fmt.Errorf("node/client: close response body: %w", cerr))
	}
}
