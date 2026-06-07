// Package govirtctl implements the Govirta control-plane CLI: a kind-agnostic
// manifest tool that submits full resource objects to the master apiserver and
// reads them back. It deliberately holds no schema knowledge beyond locating the
// kind and name inside an object's envelope — every behavior-affecting field is
// supplied by the operator in the manifest (显式优于隐式), never inferred here.
package govirtctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrNotFound is returned by Get when the master has no object of that kind/name.
var ErrNotFound = errors.New("govirtctl: object not found")

// ErrReferenced is returned by Delete when the master refuses (409) because the
// object is still referenced by another object (finalizer protection). The
// wrapping error carries the apiserver's "still referenced by <Kind>/<name>" text.
var ErrReferenced = errors.New("govirtctl: object still referenced")

// resourceVersionHeader mirrors the apiserver's X-Resource-Version response
// header (internal/controlplane/apiserver/handler_get.go).
const resourceVersionHeader = "X-Resource-Version"

// Client talks to one master apiserver root over HTTP. It is the only place
// govirtctl encodes the apiserver route contract.
type Client struct {
	baseURL string
	hc      *http.Client
}

// NewClient builds a Client for baseURL (scheme://host[:port]). hc may be nil,
// in which case http.DefaultClient is used.
func NewClient(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: baseURL, hc: hc}
}

// Apply submits body as the full object for kind/name via POST
// /apis/{kind}/{name}. It returns the master's stored object bytes on success,
// or an error carrying the apiserver {"error":...} envelope on a non-2xx reply.
func (c *Client) Apply(ctx context.Context, kind, name string, body []byte) (_ []byte, err error) {
	url := fmt.Sprintf("%s/apis/%s/%s", c.baseURL, kind, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("govirtctl: build apply request for %s/%s: %w", kind, name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("govirtctl: apply %s/%s: %w", kind, name, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("govirtctl: close apply response body: %w", cerr)
		}
	}()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("govirtctl: read apply response for %s/%s: %w", kind, name, readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("govirtctl: apply %s/%s: master returned %d: %s", kind, name, resp.StatusCode, errorMessage(respBody))
	}
	return respBody, nil
}

// Get fetches kind/name via GET /apis/{kind}/{name}. It returns the object bytes
// and the X-Resource-Version header value, or ErrNotFound on a 404.
func (c *Client) Get(ctx context.Context, kind, name string) (_ []byte, _ string, err error) {
	url := fmt.Sprintf("%s/apis/%s/%s", c.baseURL, kind, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("govirtctl: build get request for %s/%s: %w", kind, name, err)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("govirtctl: get %s/%s: %w", kind, name, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("govirtctl: close get response body: %w", cerr)
		}
	}()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, "", fmt.Errorf("govirtctl: read get response for %s/%s: %w", kind, name, readErr)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", fmt.Errorf("govirtctl: get %s/%s: %w", kind, name, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("govirtctl: get %s/%s: master returned %d: %s", kind, name, resp.StatusCode, errorMessage(respBody))
	}
	return respBody, resp.Header.Get(resourceVersionHeader), nil
}

// Delete issues DELETE /apis/{kind}/{name} to the master. It maps the apiserver's
// finalizer-two-phase responses: 202 Accepted → nil (deletion accepted, async teardown);
// 404 → ErrNotFound (wrapped); 409 → ErrReferenced (wrapped, body carries "still referenced by ...").
func (c *Client) Delete(ctx context.Context, kind, name string) (err error) {
	url := fmt.Sprintf("%s/apis/%s/%s", c.baseURL, kind, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("govirtctl: build delete request for %s/%s: %w", kind, name, err)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("govirtctl: delete %s/%s: %w", kind, name, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("govirtctl: close delete response body: %w", cerr)
		}
	}()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("govirtctl: read delete response for %s/%s: %w", kind, name, readErr)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("govirtctl: delete %s/%s: %w", kind, name, ErrNotFound)
	}
	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("govirtctl: delete %s/%s: %w: %s", kind, name, ErrReferenced, errorMessage(respBody))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("govirtctl: delete %s/%s: master returned %d: %s", kind, name, resp.StatusCode, errorMessage(respBody))
	}
	return nil
}

// errorMessage extracts the apiserver {"error":"..."} message, falling back to
// the raw body when it is not the expected envelope.
func errorMessage(body []byte) string {
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error != "" {
		return env.Error
	}
	return string(body)
}
