package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestServer wires an httptest server with a mux mirroring the apiserver's
// /apis routes, so the client is exercised against the real path shapes it must
// speak in production rather than a hand-faked single handler.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// GET /apis/{kind}/{name}: "vm-a" exists, anything else 404s. This lets one
	// server cover both the hit and the ErrNotFound mapping.
	mux.HandleFunc("GET /apis/{kind}/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != "vm-a" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"VM","name":"vm-a"}`))
	})

	// GET /apis/{kind}: collection list returns a JSON array.
	mux.HandleFunc("GET /apis/{kind}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"vm-a"},{"name":"vm-b"}]`))
	})

	// PATCH /apis/{kind}/{name}/status: echoes the received body back so the test
	// can assert method+path+body all reached the server intact.
	mux.HandleFunc("PATCH /apis/{kind}/{name}/status", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClientGetHit(t *testing.T) {
	srv := newTestServer(t)
	c := New(srv.URL, srv.Client())

	got, err := c.Get(context.Background(), "VM", "vm-a")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	want := `{"kind":"VM","name":"vm-a"}`
	if string(got) != want {
		t.Fatalf("Get body = %q, want %q", got, want)
	}
}

func TestClientGetNotFound(t *testing.T) {
	srv := newTestServer(t)
	c := New(srv.URL, srv.Client())

	_, err := c.Get(context.Background(), "VM", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of missing object: got err %v, want ErrNotFound", err)
	}
}

func TestClientGetServerError(t *testing.T) {
	// A non-404 non-2xx must surface as a (non-ErrNotFound) wrapped error so the
	// caller does not mistake a transport/server failure for a missing object.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, srv.Client())

	_, err := c.Get(context.Background(), "VM", "vm-a")
	if err == nil {
		t.Fatal("Get against 500 returned nil error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("Get against 500 mapped to ErrNotFound: %v", err)
	}
}

func TestClientList(t *testing.T) {
	srv := newTestServer(t)
	c := New(srv.URL, srv.Client())

	got, err := c.List(context.Background(), "VM")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	want := `[{"name":"vm-a"},{"name":"vm-b"}]`
	if string(got) != want {
		t.Fatalf("List body = %q, want %q", got, want)
	}
}

func TestClientPatchStatus(t *testing.T) {
	// Capture the exact method, path, and body the server received, asserting the
	// client sent PATCH to /apis/<kind>/<name>/status with the status bytes verbatim.
	var (
		gotMethod string
		gotPath   string
		gotBody   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		gotBody = b
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, srv.Client())

	status := []byte(`{"status":{"phase":"Running"}}`)
	resp, err := c.PatchStatus(context.Background(), "VM", "vm-a", status)
	if err != nil {
		t.Fatalf("PatchStatus returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("PatchStatus method = %q, want PATCH", gotMethod)
	}
	if want := "/apis/VM/vm-a/status"; gotPath != want {
		t.Errorf("PatchStatus path = %q, want %q", gotPath, want)
	}
	if !bytes.Equal(gotBody, status) {
		t.Errorf("PatchStatus body = %q, want %q", gotBody, status)
	}
	if want := `{"ok":true}`; string(resp) != want {
		t.Errorf("PatchStatus response = %q, want %q", resp, want)
	}
}

func TestClientPatchStatusNon2xx(t *testing.T) {
	// A non-2xx PATCH must return an error carrying the server's body text so the
	// failure is diagnosable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected: bad status", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, srv.Client())

	_, err := c.PatchStatus(context.Background(), "VM", "vm-a", []byte(`{}`))
	if err == nil {
		t.Fatal("PatchStatus against 400 returned nil error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("rejected: bad status")) {
		t.Fatalf("PatchStatus error %q does not include server body text", err)
	}
}

func TestClientNilHTTPClientUsesDefault(t *testing.T) {
	// hc==nil is a transport knob, not a business default: New must fall back to
	// http.DefaultClient and still function against a live server.
	srv := newTestServer(t)
	c := New(srv.URL, nil)
	if c.http != http.DefaultClient {
		t.Fatal("New with nil hc did not adopt http.DefaultClient")
	}

	got, err := c.Get(context.Background(), "VM", "vm-a")
	if err != nil {
		t.Fatalf("Get with default client returned error: %v", err)
	}
	if want := `{"kind":"VM","name":"vm-a"}`; string(got) != want {
		t.Fatalf("Get body = %q, want %q", got, want)
	}
}

func TestClientRemoveFinalizer(t *testing.T) {
	// Capture the exact method, path, and body the server received, asserting the
	// client sent PATCH to /apis/<kind>/<name>/finalizers with the expected
	// {"remove":"<finalizer>"} body.
	var (
		gotMethod string
		gotPath   string
		gotBody   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		gotBody = b
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, srv.Client())

	const finalizer = "govirta.io/node-teardown"
	if err := c.RemoveFinalizer(context.Background(), "VM", "vm-a", finalizer); err != nil {
		t.Fatalf("RemoveFinalizer returned error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("RemoveFinalizer method = %q, want PATCH", gotMethod)
	}
	if want := "/apis/VM/vm-a/finalizers"; gotPath != want {
		t.Errorf("RemoveFinalizer path = %q, want %q", gotPath, want)
	}
	// Decode rather than string-compare so the assertion is robust to key
	// ordering / whitespace and proves the body is well-formed JSON.
	var decoded map[string]string
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("RemoveFinalizer body %q is not valid JSON: %v", gotBody, err)
	}
	if got, want := decoded["remove"], finalizer; got != want {
		t.Errorf("RemoveFinalizer body remove = %q, want %q", got, want)
	}
	if len(decoded) != 1 {
		t.Errorf("RemoveFinalizer body has %d keys, want exactly 1 (%v)", len(decoded), decoded)
	}
}

func TestClientRemoveFinalizerNon2xx(t *testing.T) {
	// A non-2xx PATCH must return an error carrying the status code (and the
	// server's body text) so the failure is diagnosable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such finalizer", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, srv.Client())

	err := c.RemoveFinalizer(context.Background(), "VM", "vm-a", "govirta.io/node-teardown")
	if err == nil {
		t.Fatal("RemoveFinalizer against 404 returned nil error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("404")) {
		t.Fatalf("RemoveFinalizer error %q does not include the status code", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("no such finalizer")) {
		t.Fatalf("RemoveFinalizer error %q does not include server body text", err)
	}
}
