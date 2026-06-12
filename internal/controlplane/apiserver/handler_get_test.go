package apiserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
)

// doGet issues GET against path through the server's handler and returns the
// recorded response.
func doGet(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestGetHitReturnsStoredObject(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validStoragePool()
	if rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("seed apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	rec := doGet(t, srv, "/apis/"+string(metav1.KindStoragePool)+"/"+obj.Name)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// The body must decode back to the same object while carrying the store
	// ResourceVersion injected by the response path.
	var got storagepoolv1.StoragePool
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Name != obj.Name {
		t.Fatalf("response name = %q, want %q", got.Name, obj.Name)
	}
	if got.Spec.StorageRoot != obj.Spec.StorageRoot {
		t.Fatalf("response storageRoot = %q, want %q", got.Spec.StorageRoot, obj.Spec.StorageRoot)
	}

	hv := rec.Header().Get(resourceVersionHeader)
	if hv == "" {
		t.Fatalf("%s header is empty; expected store-assigned ResourceVersion", resourceVersionHeader)
	}
	if got.ResourceVersion == "" {
		t.Fatal("response body metadata.resourceVersion is empty")
	}
	if got.ResourceVersion != hv {
		t.Fatalf("body resourceVersion = %q, want header %q", got.ResourceVersion, hv)
	}

	var stored storagepoolv1.StoragePool
	if err := json.Unmarshal(storedRaw(t, st, metav1.KindStoragePool, obj.Name).Value, &stored); err != nil {
		t.Fatalf("decode stored raw: %v", err)
	}
	if stored.ResourceVersion != "" {
		t.Fatalf("stored raw metadata.resourceVersion = %q, want empty", stored.ResourceVersion)
	}
}

func TestGetBodyResourceVersionMatchesHeaderWithoutMutatingStore(t *testing.T) {
	srv, st := newTestServer(t)

	obj := validStoragePool()
	if rec := doApply(t, srv, metav1.KindStoragePool, obj.Name, obj); rec.Code != http.StatusCreated {
		t.Fatalf("seed apply status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	before := storedRaw(t, st, metav1.KindStoragePool, obj.Name)
	rec := doGet(t, srv, "/apis/"+string(metav1.KindStoragePool)+"/"+obj.Name)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	after := storedRaw(t, st, metav1.KindStoragePool, obj.Name)

	var got storagepoolv1.StoragePool
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if hv := rec.Header().Get(resourceVersionHeader); got.ResourceVersion != hv || hv == "" {
		t.Fatalf("body resourceVersion = %q, header = %q; want same non-empty value", got.ResourceVersion, hv)
	}
	if !bytes.Equal(before.Value, after.Value) {
		t.Fatalf("stored raw changed after GET: before=%s after=%s", before.Value, after.Value)
	}
	if bytes.Contains(after.Value, []byte(`"resourceVersion"`)) {
		t.Fatalf("stored raw unexpectedly contains resourceVersion: %s", after.Value)
	}
}

func TestGetResourceVersionInjectionErrorReturns500(t *testing.T) {
	srv, st := newTestServer(t)

	name := "pool-malformed-metadata"
	if _, err := st.Put(
		t.Context(),
		storeKey(metav1.KindStoragePool, name),
		[]byte(`{"apiVersion":"govirta.io/v1alpha1","kind":"StoragePool","metadata":"invalid"}`),
		"",
	); err != nil {
		t.Fatalf("seed malformed metadata object: %v", err)
	}

	rec := doGet(t, srv, "/apis/"+string(metav1.KindStoragePool)+"/"+name)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

func TestGetResourceVersionInjectionPreservesLargeJSONNumbers(t *testing.T) {
	srv, st := newTestServer(t)

	name := "pool-large-capacity"
	largeCapacity := json.RawMessage(`9223372036854775807`)
	if _, err := st.Put(
		t.Context(),
		storeKey(metav1.KindStoragePool, name),
		[]byte(`{"apiVersion":"govirta.io/v1alpha1","kind":"StoragePool","metadata":{"name":"pool-large-capacity","uid":"uid-pool-large-capacity"},"spec":{"backend":"localfile","type":"file","storageRoot":"/var/lib/govirta/pool-large-capacity","capacityBytes":9223372036854775807}}`),
		"",
	); err != nil {
		t.Fatalf("seed large capacity object: %v", err)
	}

	rec := doGet(t, srv, "/apis/"+string(metav1.KindStoragePool)+"/"+name)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
		Spec     struct {
			CapacityBytes json.RawMessage `json:"capacityBytes"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response raw messages: %v", err)
	}
	if got := string(body.Spec.CapacityBytes); got != string(largeCapacity) {
		t.Fatalf("capacityBytes raw JSON = %s, want %s", got, largeCapacity)
	}
	if bytes.Contains(body.Spec.CapacityBytes, []byte("e")) || bytes.Contains(body.Spec.CapacityBytes, []byte("E")) {
		t.Fatalf("capacityBytes used scientific notation: %s", body.Spec.CapacityBytes)
	}
	if len(body.Metadata["resourceVersion"]) == 0 {
		t.Fatalf("metadata.resourceVersion was not injected: %s", rec.Body.String())
	}
}

func TestGetMissingReturns404(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := doGet(t, srv, "/apis/"+string(metav1.KindStoragePool)+"/nonexistent")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeError(t, rec); msg == "" {
		t.Fatalf("expected non-empty error body")
	}
}

func TestGetListEmptyReturnsEmptyArray(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := doGet(t, srv, "/apis/"+string(metav1.KindStoragePool))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Hard requirement: an empty collection is "[]", never "null".
	if body := bytes.TrimSpace(rec.Body.Bytes()); !bytes.Equal(body, []byte("[]")) {
		t.Fatalf("empty list body = %q, want %q", body, "[]")
	}

	var arr []storagepoolv1.StoragePool
	if err := json.Unmarshal(rec.Body.Bytes(), &arr); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(arr) != 0 {
		t.Fatalf("empty list len = %d, want 0", len(arr))
	}
}

func TestGetListReturnsSortedByName(t *testing.T) {
	srv, _ := newTestServer(t)

	// Seed out of order; store.List sorts by key (/govirta/<kind>/<name>), so the
	// response array must come back ordered by name regardless of insertion order.
	names := []string{"pool-c", "pool-a", "pool-b"}
	for _, n := range names {
		obj := validStoragePool()
		obj.Name = n
		obj.UID = "uid-" + n
		if rec := doApply(t, srv, metav1.KindStoragePool, n, obj); rec.Code != http.StatusCreated {
			t.Fatalf("seed apply %q status = %d, want 201; body=%s", n, rec.Code, rec.Body.String())
		}
	}

	rec := doGet(t, srv, "/apis/"+string(metav1.KindStoragePool))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var arr []storagepoolv1.StoragePool
	if err := json.Unmarshal(rec.Body.Bytes(), &arr); err != nil {
		t.Fatalf("decode list: %v", err)
	}

	want := []string{"pool-a", "pool-b", "pool-c"}
	if len(arr) != len(want) {
		t.Fatalf("list len = %d, want %d", len(arr), len(want))
	}
	for i, w := range want {
		if arr[i].Name != w {
			t.Fatalf("list[%d].Name = %q, want %q", i, arr[i].Name, w)
		}
	}
}
