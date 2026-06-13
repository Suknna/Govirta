package govirtctl

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientApplyReturnsStoredObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/apis/StoragePool/pool-a" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"kind":"StoragePool","metadata":{"name":"pool-a"},"status":{"phase":"pending"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	got, err := c.Apply(context.Background(), "StoragePool", "pool-a", []byte(`{"kind":"StoragePool"}`))
	if err != nil {
		t.Fatalf("Apply error = %v, want nil", err)
	}
	if len(got) == 0 {
		t.Fatal("Apply returned empty body, want stored object")
	}
}

func TestClientApplySurfacesErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"name mismatch"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	_, err := c.Apply(context.Background(), "VM", "vm-a", []byte(`{}`))
	if err == nil {
		t.Fatal("Apply error = nil, want non-2xx error")
	}
	if got := err.Error(); !contains(got, "name mismatch") {
		t.Fatalf("Apply error = %q, want it to carry the envelope message", got)
	}
}

func TestClientReplaceUsesPutAndReturnsStoredObject(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kind":"VM","metadata":{"name":"vm-a","resourceVersion":"2"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	got, err := c.Replace(context.Background(), "VM", "vm-a", []byte(`{"kind":"VM","metadata":{"name":"vm-a","resourceVersion":"1"}}`))
	if err != nil {
		t.Fatalf("Replace error = %v, want nil", err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("server saw method %q, want PUT", gotMethod)
	}
	if gotPath != "/apis/VM/vm-a" {
		t.Fatalf("server saw path %q, want /apis/VM/vm-a", gotPath)
	}
	if gotContentType != "application/json" {
		t.Fatalf("server saw Content-Type %q, want application/json", gotContentType)
	}
	if len(got) == 0 {
		t.Fatal("Replace returned empty body, want stored object")
	}
}

func TestClientReplaceSurfacesConflictEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected method %s, want PUT", r.Method)
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"resource version conflict"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	_, err := c.Replace(context.Background(), "VM", "vm-a", []byte(`{"kind":"VM"}`))
	if err == nil {
		t.Fatal("Replace error = nil, want non-2xx error")
	}
	if got := err.Error(); !contains(got, "master returned 409") || !contains(got, "resource version conflict") {
		t.Fatalf("Replace error = %q, want it to carry status and envelope message", got)
	}
}

func TestClientGetReturnsObjectAndResourceVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(resourceVersionHeader, "42")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kind":"VM","status":{"phase":"running"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	body, rv, err := c.Get(context.Background(), "VM", "vm-a")
	if err != nil {
		t.Fatalf("Get error = %v, want nil", err)
	}
	if rv != "42" {
		t.Fatalf("Get resourceVersion = %q, want 42", rv)
	}
	if len(body) == 0 {
		t.Fatal("Get returned empty body")
	}
}

func TestClientGetNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	_, _, err := c.Get(context.Background(), "VM", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

func TestClientDeleteAcceptedReturnsNil(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	if err := c.Delete(context.Background(), "VM", "vm-a"); err != nil {
		t.Fatalf("Delete error = %v, want nil", err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("server saw method %q, want DELETE", gotMethod)
	}
	if gotPath != "/apis/VM/vm-a" {
		t.Fatalf("server saw path %q, want /apis/VM/vm-a", gotPath)
	}
}

func TestClientDeleteNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	err := c.Delete(context.Background(), "VM", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete error = %v, want ErrNotFound", err)
	}
}

func TestClientDeleteReferencedCarriesBody(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"still referenced by VM/vm-a"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	err := c.Delete(context.Background(), "StoragePool", "pool-a")
	if !errors.Is(err, ErrReferenced) {
		t.Fatalf("Delete error = %v, want ErrReferenced", err)
	}
	if got := err.Error(); !contains(got, "still referenced by VM/vm-a") {
		t.Fatalf("Delete error = %q, want it to carry the referenced-by body text", got)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("server saw method %q, want DELETE", gotMethod)
	}
}

func TestClientDeleteServerErrorIsGeneric(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	err := c.Delete(context.Background(), "VM", "vm-a")
	if err == nil {
		t.Fatal("Delete error = nil, want non-2xx error")
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrReferenced) {
		t.Fatalf("Delete error = %v, want a generic error (not a sentinel)", err)
	}
	if got := err.Error(); !contains(got, "boom") {
		t.Fatalf("Delete error = %q, want it to carry the envelope message", got)
	}
}

func TestClientUploadImageSendsExplicitQueryFields(t *testing.T) {
	var gotMethod, gotPath, gotUID, gotFormat, gotSHA, gotSize, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotUID = r.URL.Query().Get("uid")
		gotFormat = r.URL.Query().Get("format")
		gotSHA = r.URL.Query().Get("sha256")
		gotSize = r.URL.Query().Get("declaredSizeBytes")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		gotBody = string(body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"kind":"Image","metadata":{"name":"img-a"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, srv.Client())
	_, err := c.UploadImage(context.Background(), "img-a", "uid-img-a", "v1", "qcow2", 5, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", strings.NewReader("bytes"))
	if err != nil {
		t.Fatalf("UploadImage error = %v, want nil", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/apis/Image/img-a/store/v1" {
		t.Fatalf("server saw %s %s, want PUT /apis/Image/img-a/store/v1", gotMethod, gotPath)
	}
	if gotUID != "uid-img-a" || gotFormat != "qcow2" || gotSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || gotSize != "5" {
		t.Fatalf("query uid=%q format=%q sha=%q size=%q, want explicit upload fields", gotUID, gotFormat, gotSHA, gotSize)
	}
	if gotBody != "bytes" {
		t.Fatalf("body = %q, want bytes", gotBody)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
