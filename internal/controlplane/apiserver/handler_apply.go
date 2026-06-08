// Package apiserver implements the Govirta control-plane HTTP surface. It is the
// admission boundary between caller-submitted resource JSON and the raw
// store.Store: it decodes a submitted object by its kind, runs the apis-layer
// Validate() contracts, applies admission-time mutations/checks that are the
// apiserver's responsibility (NIC MAC allocation, Network range self-consistency),
// and persists the object. The store remains kind-agnostic; all kind dispatch
// lives here. This package never imports or mutates pkg/apis types — it only
// decodes into them and calls their public Validate() methods.
package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// ErrUnknownKind is returned when the {kind} path segment does not name one of
// the six first-class Govirta resource kinds. It maps to HTTP 404 (the named
// resource collection does not exist).
var ErrUnknownKind = errors.New("apiserver: unknown resource kind")

// ErrNameMismatch is returned when the {name} path segment disagrees with the
// submitted object's metadata.name. The URL identity and the body identity must
// agree so a write cannot silently land under a key the caller did not address.
var ErrNameMismatch = errors.New("apiserver: path name does not match metadata.name")

// ErrNetworkAdmission is returned when a Network spec passes its own apis-layer
// Validate() (every field is individually well-formed) but is not internally
// self-consistent: a negative lease, an inverted DHCP range, or a gateway/DHCP
// address outside the declared subnet. This cross-field check is the apiserver's
// admission responsibility and is deliberately not pushed down into apis/node.
var ErrNetworkAdmission = errors.New("apiserver: network admission failed")

// The Server struct, NewServer constructor, and Handler() router live in
// server.go alongside the Run(ctx) lifecycle; this file holds the apply pipeline
// that Server.Apply dispatches into. All methods thread ctx from the inbound
// request end to end.

// Apply decodes a submitted resource object, validates it, applies kind-specific
// admission (NIC MAC allocation, Network range checks), persists it, and writes
// the stored object (carrying its assigned ResourceVersion) back with HTTP 201.
// On any failure it writes a {"error": "..."} body with a 4xx/5xx status.
func (s *Server) Apply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	body, apiErr := s.apply(ctx, r)
	code := http.StatusCreated
	if apiErr != nil {
		code = apiErr.code
		body = errorBody(apiErr)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if _, err := w.Write(body); err != nil {
		// The response is already committed; the only honest action left is to
		// record the write failure rather than silently discard it.
		zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write apply response")
	}
}

// apply performs the kind-dispatched decode/validate/admit/store pipeline and
// returns the response body for a successful apply, or a classified *apiError.
func (s *Server) apply(ctx context.Context, r *http.Request) ([]byte, *apiError) {
	kind := metav1.Kind(r.PathValue("kind"))
	name := r.PathValue("name")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, badRequest(fmt.Errorf("apiserver: read request body: %w", err))
	}

	switch kind {
	case metav1.KindStoragePool:
		var obj storagepoolv1.StoragePool
		if err := json.Unmarshal(body, &obj); err != nil {
			return nil, badRequest(fmt.Errorf("apiserver: decode StoragePool: %w", err))
		}
		if err := validateObject(obj.ObjectMeta, obj.Spec); err != nil {
			return nil, badRequest(err)
		}
		if err := requireName(name, obj.Name); err != nil {
			return nil, badRequest(err)
		}
		injectFinalizer(&obj.ObjectMeta)
		raw, aerr := s.put(ctx, storeKey(kind, obj.Name), obj)
		if aerr != nil {
			return nil, aerr
		}
		obj.ResourceVersion = raw.ResourceVersion
		return marshalResponse(obj)

	case metav1.KindImage:
		var obj imagev1.Image
		if err := json.Unmarshal(body, &obj); err != nil {
			return nil, badRequest(fmt.Errorf("apiserver: decode Image: %w", err))
		}
		if err := validateObject(obj.ObjectMeta, obj.Spec); err != nil {
			return nil, badRequest(err)
		}
		if err := requireName(name, obj.Name); err != nil {
			return nil, badRequest(err)
		}
		injectFinalizer(&obj.ObjectMeta)
		raw, aerr := s.put(ctx, storeKey(kind, obj.Name), obj)
		if aerr != nil {
			return nil, aerr
		}
		obj.ResourceVersion = raw.ResourceVersion
		return marshalResponse(obj)

	case metav1.KindVolume:
		var obj volumev1.Volume
		if err := json.Unmarshal(body, &obj); err != nil {
			return nil, badRequest(fmt.Errorf("apiserver: decode Volume: %w", err))
		}
		if err := validateObject(obj.ObjectMeta, obj.Spec); err != nil {
			return nil, badRequest(err)
		}
		if err := requireName(name, obj.Name); err != nil {
			return nil, badRequest(err)
		}
		injectFinalizer(&obj.ObjectMeta)
		raw, aerr := s.put(ctx, storeKey(kind, obj.Name), obj)
		if aerr != nil {
			return nil, aerr
		}
		obj.ResourceVersion = raw.ResourceVersion
		return marshalResponse(obj)

	case metav1.KindNetwork:
		var obj networkv1.Network
		if err := json.Unmarshal(body, &obj); err != nil {
			return nil, badRequest(fmt.Errorf("apiserver: decode Network: %w", err))
		}
		if err := validateObject(obj.ObjectMeta, obj.Spec); err != nil {
			return nil, badRequest(err)
		}
		if err := requireName(name, obj.Name); err != nil {
			return nil, badRequest(err)
		}
		// Admission-time cross-field self-consistency, beyond per-field Validate().
		if err := validateNetworkAdmission(obj.Spec); err != nil {
			return nil, badRequest(err)
		}
		injectFinalizer(&obj.ObjectMeta)
		raw, aerr := s.put(ctx, storeKey(kind, obj.Name), obj)
		if aerr != nil {
			return nil, aerr
		}
		obj.ResourceVersion = raw.ResourceVersion
		return marshalResponse(obj)

	case metav1.KindNIC:
		var obj nicv1.NIC
		if err := json.Unmarshal(body, &obj); err != nil {
			return nil, badRequest(fmt.Errorf("apiserver: decode NIC: %w", err))
		}
		if err := validateObject(obj.ObjectMeta, obj.Spec); err != nil {
			return nil, badRequest(err)
		}
		if err := requireName(name, obj.Name); err != nil {
			return nil, badRequest(err)
		}
		injectFinalizer(&obj.ObjectMeta)
		raw, aerr := s.applyNIC(ctx, storeKey(kind, obj.Name), &obj)
		if aerr != nil {
			return nil, aerr
		}
		obj.ResourceVersion = raw.ResourceVersion
		return marshalResponse(obj)

	case metav1.KindVM:
		var obj vmv1.VM
		if err := json.Unmarshal(body, &obj); err != nil {
			return nil, badRequest(fmt.Errorf("apiserver: decode VM: %w", err))
		}
		if err := validateObject(obj.ObjectMeta, obj.Spec); err != nil {
			return nil, badRequest(err)
		}
		if err := requireName(name, obj.Name); err != nil {
			return nil, badRequest(err)
		}
		injectFinalizer(&obj.ObjectMeta)
		raw, aerr := s.applyVM(ctx, storeKey(kind, obj.Name), &obj)
		if aerr != nil {
			return nil, aerr
		}
		obj.ResourceVersion = raw.ResourceVersion
		return marshalResponse(obj)

	default:
		return nil, notFound(fmt.Errorf("%w: %q", ErrUnknownKind, kind))
	}
}

// injectFinalizer 在对象持久化前注入默认 node-teardown finalizer（仅当 Finalizers 为空），
// 与 MAC/调度的 admission 注入同模式：落 etcd 的对象一定带 finalizer，消除"未加 finalizer 就被删"
// 的泄漏竞态。有条件注入（为空才注入）为未来多 finalizer 留口。
func injectFinalizer(meta *metav1.ObjectMeta) {
	if len(meta.Finalizers) == 0 {
		meta.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	}
}

// applyNIC persists a NIC, allocating a platform MAC when Spec.MAC is empty. The
// allocation and the NIC Put happen atomically inside WithAllocation so the
// chosen MAC cannot be claimed by a concurrent apply between selection and write.
// A non-empty submitted MAC is preserved as-is (already validated by Validate()).
func (s *Server) applyNIC(ctx context.Context, key string, nic *nicv1.NIC) (store.RawObject, *apiError) {
	if nic.Spec.MAC != "" {
		return s.put(ctx, key, *nic)
	}

	var raw store.RawObject
	err := s.alloc.WithAllocation(ctx, func(hw net.HardwareAddr) error {
		nic.Spec.MAC = hw.String()
		data, mErr := json.Marshal(*nic)
		if mErr != nil {
			return fmt.Errorf("apiserver: marshal NIC: %w", mErr)
		}
		r, pErr := s.store.Put(ctx, key, data, "")
		if pErr != nil {
			return fmt.Errorf("apiserver: store NIC: %w", pErr)
		}
		raw = r
		return nil
	})
	if err != nil {
		if errors.Is(err, mac.ErrMACPoolExhausted) {
			return store.RawObject{}, conflictErr(err)
		}
		return store.RawObject{}, internalErr(err)
	}
	return raw, nil
}

// bindVM places a VM onto a node when it carries no explicit binding, writing the
// scheduler's choice into ObjectMeta.NodeName so the persisted object is routed to
// that node by the watch filter. A VM that already names a node is left untouched
// (caller-pinned placement). An empty candidate set surfaces as ErrNoNodes, which
// is a transient capacity condition the caller should retry against, so it maps to
// 503 rather than a 4xx; any other scheduler failure (including ctx cancellation)
// maps to 5xx. ctx is threaded into Schedule end to end.
func (s *Server) bindVM(ctx context.Context, vm *vmv1.VM) *apiError {
	if vm.NodeName != "" {
		return nil
	}
	node, err := s.sched.Schedule(ctx, *vm, s.nodeNames)
	if err != nil {
		if errors.Is(err, scheduler.ErrNoNodes) {
			return unavailable(fmt.Errorf("apiserver: schedule VM %q: %w", vm.Name, err))
		}
		return internalErr(fmt.Errorf("apiserver: schedule VM %q: %w", vm.Name, err))
	}
	vm.NodeName = node
	return nil
}

// put marshals obj and writes it unconditionally (empty expectedVersion), mapping
// marshal/store failures to 5xx. The returned RawObject carries the store-assigned
// ResourceVersion.
func (s *Server) put(ctx context.Context, key string, obj any) (store.RawObject, *apiError) {
	data, err := json.Marshal(obj)
	if err != nil {
		return store.RawObject{}, internalErr(fmt.Errorf("apiserver: marshal object: %w", err))
	}
	raw, err := s.store.Put(ctx, key, data, "")
	if err != nil {
		return store.RawObject{}, internalErr(fmt.Errorf("apiserver: store put %q: %w", key, err))
	}
	return raw, nil
}

// specValidator is the subset of the apis Spec contract the apiserver depends on:
// each resource Spec exposes Validate() to self-check its fields.
type specValidator interface {
	Validate() error
}

// validateObject runs both halves of the apis-layer identity+spec contract and
// joins their failures, because there is no object-level Validate(): meta and
// spec validate independently and a caller may violate both at once.
func validateObject(meta metav1.ObjectMeta, spec specValidator) error {
	return errors.Join(meta.Validate(), spec.Validate())
}

// requireName enforces that the URL identity matches the body identity. metaName
// is guaranteed non-empty here because validateObject already rejected an empty
// name with a 400.
func requireName(pathName, metaName string) error {
	if pathName != metaName {
		return fmt.Errorf("%w: path %q vs metadata.name %q", ErrNameMismatch, pathName, metaName)
	}
	return nil
}

// validateNetworkAdmission checks cross-field self-consistency that per-field
// Validate() cannot: a non-negative lease, an ordered DHCP range, and gateway +
// DHCP bounds contained in the declared subnet. Addresses are re-parsed (they
// already passed Validate) and any parse failure is propagated rather than
// ignored, so a future Validate change cannot let a malformed value slip through.
func validateNetworkAdmission(spec networkv1.NetworkSpec) error {
	if spec.LeaseSeconds < 0 {
		return fmt.Errorf("%w: leaseSeconds must be non-negative, got %d", ErrNetworkAdmission, spec.LeaseSeconds)
	}

	subnet, err := netip.ParsePrefix(spec.Subnet)
	if err != nil {
		return fmt.Errorf("%w: subnet %q: %w", ErrNetworkAdmission, spec.Subnet, err)
	}
	gateway, err := netip.ParsePrefix(spec.GatewayCIDR)
	if err != nil {
		return fmt.Errorf("%w: gatewayCIDR %q: %w", ErrNetworkAdmission, spec.GatewayCIDR, err)
	}
	start, err := netip.ParseAddr(spec.DHCPRangeStart)
	if err != nil {
		return fmt.Errorf("%w: dhcpRangeStart %q: %w", ErrNetworkAdmission, spec.DHCPRangeStart, err)
	}
	end, err := netip.ParseAddr(spec.DHCPRangeEnd)
	if err != nil {
		return fmt.Errorf("%w: dhcpRangeEnd %q: %w", ErrNetworkAdmission, spec.DHCPRangeEnd, err)
	}

	if start.Compare(end) > 0 {
		return fmt.Errorf("%w: dhcpRangeStart %s is after dhcpRangeEnd %s", ErrNetworkAdmission, start, end)
	}

	gw := gateway.Addr()
	if !subnet.Contains(gw) {
		return fmt.Errorf("%w: gateway %s not in subnet %s", ErrNetworkAdmission, gw, subnet)
	}
	if !subnet.Contains(start) {
		return fmt.Errorf("%w: dhcpRangeStart %s not in subnet %s", ErrNetworkAdmission, start, subnet)
	}
	if !subnet.Contains(end) {
		return fmt.Errorf("%w: dhcpRangeEnd %s not in subnet %s", ErrNetworkAdmission, end, subnet)
	}
	return nil
}

// storeKey builds the store key /govirta/<kind>/<name>. This matches the prefix
// the MAC allocator lists (/govirta/NIC/), so NIC writes are visible to occupancy
// derivation.
func storeKey(kind metav1.Kind, name string) string {
	return fmt.Sprintf("/govirta/%s/%s", kind, name)
}

// marshalResponse serializes the stored object for the response, mapping a
// marshal failure to 5xx.
func marshalResponse(obj any) ([]byte, *apiError) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, internalErr(fmt.Errorf("apiserver: marshal response: %w", err))
	}
	return data, nil
}

// errorResponse is the uniform error envelope written for every 4xx/5xx.
type errorResponse struct {
	Error string `json:"error"`
}

// errorBody renders an *apiError as the {"error": "..."} envelope, falling back
// to a static payload if even that tiny marshal fails (which cannot happen for a
// string field, but is handled rather than ignored).
func errorBody(e *apiError) []byte {
	data, err := json.Marshal(errorResponse{Error: e.err.Error()})
	if err != nil {
		return []byte(`{"error":"apiserver: internal error"}`)
	}
	return data
}

// apiError pairs a wrapped cause with the HTTP status it maps to, so the pipeline
// can classify failures (4xx vs 5xx) at the point they occur and Apply can render
// them uniformly.
type apiError struct {
	code int
	err  error
}

// Error implements error.
func (e *apiError) Error() string { return e.err.Error() }

// Unwrap exposes the underlying cause for errors.Is/As inspection in tests.
func (e *apiError) Unwrap() error { return e.err }

func badRequest(err error) *apiError  { return &apiError{code: http.StatusBadRequest, err: err} }
func notFound(err error) *apiError    { return &apiError{code: http.StatusNotFound, err: err} }
func conflictErr(err error) *apiError { return &apiError{code: http.StatusConflict, err: err} }
func internalErr(err error) *apiError {
	return &apiError{code: http.StatusInternalServerError, err: err}
}

// unavailable maps a transient capacity condition (no schedulable node yet) to
// 503: the request was well-formed, so it is not a 4xx, and the caller should
// retry once a node registers rather than treat it as a permanent server fault.
func unavailable(err error) *apiError {
	return &apiError{code: http.StatusServiceUnavailable, err: err}
}
