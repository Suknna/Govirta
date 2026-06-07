package govirtctl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
