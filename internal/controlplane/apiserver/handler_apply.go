// Package apiserver implements the Govirta control-plane HTTP surface. It is the
// admission boundary between caller-submitted resource JSON and the raw
// store.Store: it decodes a submitted object by its kind, runs validating
// admission, applies apiserver-owned mutations (finalizer injection, NIC MAC
// allocation, VM node binding, server-owned metadata preservation), and persists
// the object. The store remains kind-agnostic; all kind dispatch lives here.
package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// ErrUnknownKind is returned when the {kind} path segment does not name one of
// the six first-class Govirta resource kinds. It maps to HTTP 404 (the named
// resource collection does not exist).
var ErrUnknownKind = errors.New("apiserver: unknown resource kind")

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
	case metav1.KindTask:
		return nil, forbidden(fmt.Errorf("apiserver: Task is internal and cannot be applied through the public API"))

	case metav1.KindStoragePool:
		obj, req, aerr := s.decodeAndAdmitApply(ctx, kind, name, body)
		if aerr != nil {
			return nil, aerr
		}
		pool := obj.(storagepoolv1.StoragePool)
		injectFinalizer(&pool.ObjectMeta)
		if aerr := preserveUpdateObjectMeta(req, &pool.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		raw, aerr := s.putWithPostAdmission(ctx, storeKey(kind, pool.Name), pool, req)
		if aerr != nil {
			return nil, aerr
		}
		pool.ResourceVersion = raw.ResourceVersion
		return marshalResponse(pool)

	case metav1.KindImage:
		obj, req, aerr := s.decodeAndAdmitApply(ctx, kind, name, body)
		if aerr != nil {
			return nil, aerr
		}
		image := obj.(imagev1.Image)
		injectImageFinalizer(&image.ObjectMeta)
		if aerr := preserveUpdateObjectMeta(req, &image.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		raw, aerr := s.putWithPostAdmission(ctx, storeKey(kind, image.Name), image, req)
		if aerr != nil {
			return nil, aerr
		}
		image.ResourceVersion = raw.ResourceVersion
		return marshalResponse(image)

	case metav1.KindVolume:
		obj, req, aerr := s.decodeAndAdmitApply(ctx, kind, name, body)
		if aerr != nil {
			return nil, aerr
		}
		volume := obj.(volumev1.Volume)
		injectFinalizer(&volume.ObjectMeta)
		if aerr := preserveUpdateObjectMeta(req, &volume.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		raw, aerr := s.putWithPostAdmission(ctx, storeKey(kind, volume.Name), volume, req)
		if aerr != nil {
			return nil, aerr
		}
		volume.ResourceVersion = raw.ResourceVersion
		return marshalResponse(volume)

	case metav1.KindNetwork:
		obj, req, aerr := s.decodeAndAdmitApply(ctx, kind, name, body)
		if aerr != nil {
			return nil, aerr
		}
		network := obj.(networkv1.Network)
		injectFinalizer(&network.ObjectMeta)
		if aerr := preserveUpdateObjectMeta(req, &network.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		raw, aerr := s.putWithPostAdmission(ctx, storeKey(kind, network.Name), network, req)
		if aerr != nil {
			return nil, aerr
		}
		network.ResourceVersion = raw.ResourceVersion
		return marshalResponse(network)

	case metav1.KindNIC:
		obj, req, aerr := s.decodeAndAdmitApply(ctx, kind, name, body)
		if aerr != nil {
			return nil, aerr
		}
		nic := obj.(nicv1.NIC)
		injectFinalizer(&nic.ObjectMeta)
		if aerr := preserveUpdateObjectMeta(req, &nic.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		raw, aerr := s.applyNIC(ctx, storeKey(kind, nic.Name), &nic, req)
		if aerr != nil {
			return nil, aerr
		}
		nic.ResourceVersion = raw.ResourceVersion
		return marshalResponse(nic)

	case metav1.KindVM:
		obj, req, aerr := s.decodeAndAdmitApply(ctx, kind, name, body)
		if aerr != nil {
			return nil, aerr
		}
		vm := obj.(vmv1.VM)
		injectFinalizer(&vm.ObjectMeta)
		raw, aerr := s.applyVM(ctx, storeKey(kind, vm.Name), &vm, req)
		if aerr != nil {
			return nil, aerr
		}
		vm.ResourceVersion = raw.ResourceVersion
		return marshalResponse(vm)

	case metav1.KindSnapshot:
		obj, req, aerr := s.decodeAndAdmitApply(ctx, kind, name, body)
		if aerr != nil {
			return nil, aerr
		}
		snap := obj.(snapshotv1.Snapshot)
		injectFinalizer(&snap.ObjectMeta)
		if aerr := preserveUpdateObjectMeta(req, &snap.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		raw, aerr := s.applySnapshot(ctx, storeKey(kind, snap.Name), &snap, req)
		if aerr != nil {
			return nil, aerr
		}
		snap.ResourceVersion = raw.ResourceVersion
		return marshalResponse(snap)

	default:
		return nil, notFound(fmt.Errorf("%w: %q", ErrUnknownKind, kind))
	}
}

func (s *Server) decodeAndAdmitApply(ctx context.Context, kind metav1.Kind, name string, body []byte) (any, admission.Request, *apiError) {
	obj, err := decodeObjectByKind(kind, body)
	if err != nil {
		return nil, admission.Request{}, badRequest(fmt.Errorf("apiserver: decode %s: %w", kind, err))
	}
	key := storeKey(kind, name)
	op, oldRaw, oldObj, err := s.classifyApply(ctx, kind, name, key)
	if err != nil {
		return nil, admission.Request{}, internalErr(err)
	}
	req := admission.Request{
		Operation: op,
		Kind:      kind,
		Name:      name,
		NewRaw:    body,
		NewObject: obj,
		OldObject: oldObj,
	}
	if len(oldRaw.Value) != 0 {
		req.OldRaw = oldRaw.Value
	}
	if err := admission.PreApplyChain(s.store, s.imageStorePublicURL).Validate(ctx, req); err != nil {
		return nil, admission.Request{}, admissionToAPIError(err)
	}

	// status 是 node 经 PatchStatus 子资源独占的 live 投影（上下一致 + k8s
	// spec/status 子资源分离）。一次 apply 改的是 spec，绝不能动 status——但
	// 调用方提交的 manifest 通常不带 status，decode 后 status 是零值。若直接落库
	// 会把已存在对象的 status 清空（如 Volume.status.phase=ready → ""），控制器
	// 据 status 分流的逻辑就会误判（Volume 误走创建路径 → ErrVolumeConflict）。
	// 故 update 时从旧对象保留 status，create 时保持调用方提交的零值 status。
	preserved, aerr := preserveUpdateStatus(req, obj)
	if aerr != nil {
		return nil, admission.Request{}, aerr
	}
	req.NewObject = preserved
	return preserved, req, nil
}

// preserveUpdateStatus copies the stored object's status onto the submitted
// object on update, so an apply that changes spec never clobbers the
// node-owned status projection. On create it returns obj unchanged (the
// caller-submitted zero-value status stands). All seven kinds carry a typed
// Status field; the type switch mirrors decodeObjectByKind.
func preserveUpdateStatus(req admission.Request, obj any) (any, *apiError) {
	if req.Operation != admission.OperationUpdate && req.Operation != admission.OperationReplace {
		return obj, nil
	}
	mismatch := func() *apiError {
		return internalErr(fmt.Errorf("apiserver: existing %s %q has type %T, want %T", req.Kind, req.Name, req.OldObject, obj))
	}
	switch newObj := obj.(type) {
	case storagepoolv1.StoragePool:
		old, ok := req.OldObject.(storagepoolv1.StoragePool)
		if !ok {
			return nil, mismatch()
		}
		newObj.Status = old.Status
		return newObj, nil
	case imagev1.Image:
		old, ok := req.OldObject.(imagev1.Image)
		if !ok {
			return nil, mismatch()
		}
		if imageContentIdentityChanged(old, newObj) {
			newObj.Status = imagev1.ImageStatus{Phase: imagev1.ImagePhasePending}
			return newObj, nil
		}
		newObj.Status = old.Status
		return newObj, nil
	case volumev1.Volume:
		old, ok := req.OldObject.(volumev1.Volume)
		if !ok {
			return nil, mismatch()
		}
		newObj.Status = old.Status
		return newObj, nil
	case networkv1.Network:
		old, ok := req.OldObject.(networkv1.Network)
		if !ok {
			return nil, mismatch()
		}
		newObj.Status = old.Status
		return newObj, nil
	case nicv1.NIC:
		old, ok := req.OldObject.(nicv1.NIC)
		if !ok {
			return nil, mismatch()
		}
		newObj.Status = old.Status
		return newObj, nil
	case vmv1.VM:
		old, ok := req.OldObject.(vmv1.VM)
		if !ok {
			return nil, mismatch()
		}
		newObj.Status = old.Status
		return newObj, nil
	case snapshotv1.Snapshot:
		old, ok := req.OldObject.(snapshotv1.Snapshot)
		if !ok {
			return nil, mismatch()
		}
		newObj.Status = old.Status
		return newObj, nil
	default:
		return nil, internalErr(fmt.Errorf("apiserver: preserve status for unsupported kind %q (%T)", req.Kind, obj))
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

func injectImageFinalizer(meta *metav1.ObjectMeta) {
	if len(meta.Finalizers) == 0 {
		meta.Finalizers = []metav1.Finalizer{metav1.FinalizerImageCache}
	}
}

func imageContentIdentityChanged(oldImage, newImage imagev1.Image) bool {
	oldSpec := oldImage.Spec
	newSpec := newImage.Spec
	return oldSpec.Source != newSpec.Source || oldSpec.Format != newSpec.Format || oldSpec.Version != newSpec.Version || oldSpec.DeclaredSizeBytes != newSpec.DeclaredSizeBytes || oldSpec.SHA256 != newSpec.SHA256
}

// applyNIC persists a NIC, allocating a platform MAC when Spec.MAC is empty. The
// allocation and the NIC Put happen atomically inside WithAllocation so the
// chosen MAC cannot be claimed by a concurrent apply between selection and write.
// A non-empty submitted MAC is preserved as-is (already validated by Validate()).
func (s *Server) applyNIC(ctx context.Context, key string, nic *nicv1.NIC, req admission.Request) (store.RawObject, *apiError) {
	if req.Operation == admission.OperationUpdate || req.Operation == admission.OperationReplace {
		oldNIC, ok := req.OldObject.(nicv1.NIC)
		if !ok {
			return store.RawObject{}, internalErr(fmt.Errorf("apiserver: existing object for NIC %q has type %T", nic.Name, req.OldObject))
		}
		// An update body that omits the MAC inherits the existing one — this is a
		// handler mutation, not validation. Rejecting an explicit MAC *change* is
		// validating policy that FieldPolicyValidator already enforces in the
		// PreApplyChain (mac is immutable), which runs before this handler, so a
		// changed MAC never reaches here.
		if nic.Spec.MAC == "" {
			nic.Spec.MAC = oldNIC.Spec.MAC
		}
	}

	if nic.Spec.MAC != "" {
		return s.putWithPostAdmission(ctx, key, *nic, req)
	}

	var raw store.RawObject
	err := s.alloc.WithAllocation(ctx, func(hw net.HardwareAddr) error {
		nic.Spec.MAC = hw.String()
		if err := validatePostApply(ctx, *nic, req); err != nil {
			return err
		}
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
		var admissionErr *admission.Error
		if errors.As(err, &admissionErr) {
			return store.RawObject{}, admissionToAPIError(err)
		}
		if errors.Is(err, mac.ErrMACPoolExhausted) {
			return store.RawObject{}, conflictErr(err)
		}
		return store.RawObject{}, internalErr(err)
	}
	return raw, nil
}

// applySnapshot resolves the Snapshot's nodeName from its target VM (the snapshot
// must run on the node that holds the VM's qcow2 files) and persists it. This is
// the third admission mutation precedent after NIC MAC allocation and VM
// scheduling: the user never supplies a Snapshot nodeName — it is a deterministic
// derivation of the target VM's placement (single source of truth).
//
// nodeName is re-resolved on BOTH create and update. preserveUpdateObjectMeta
// preserves resourceVersion/deletionTimestamp/finalizers but NOT nodeName, and
// EnvelopeValidator does not treat nodeName as server-owned — so an identical
// re-apply (Snapshot spec is fully immutable, so any re-apply classifies as
// update) carrying no nodeName would otherwise persist an empty nodeName and the
// node watch (?nodeName=) would stop routing it. Re-resolving on update keeps the
// stored nodeName stable (a snapshot cannot migrate; the target VM's nodeName is
// itself immutable post-bind). The vmRef VM is proven to exist by
// ReferenceValidator on both create and update.
func (s *Server) applySnapshot(ctx context.Context, key string, snap *snapshotv1.Snapshot, req admission.Request) (store.RawObject, *apiError) {
	node, aerr := s.resolveVMNodeName(ctx, snap.Spec.VMRef)
	if aerr != nil {
		return store.RawObject{}, aerr
	}
	snap.NodeName = node
	return s.putWithPostAdmission(ctx, key, *snap, req)
}

// resolveVMNodeName reads the VM named ref and returns its metadata.nodeName.
// ReferenceValidator already proved the VM exists and is not deleting, so a
// missing VM here is an internal inconsistency (500). An empty VM nodeName is
// also internal: a stored VM is always scheduled (bindVM) before it lands.
func (s *Server) resolveVMNodeName(ctx context.Context, vmName string) (string, *apiError) {
	raw, err := s.store.Get(ctx, storeKey(metav1.KindVM, vmName))
	if err != nil {
		return "", internalErr(fmt.Errorf("apiserver: resolve snapshot target VM %q nodeName: %w", vmName, err))
	}
	var vm vmv1.VM
	if err := json.Unmarshal(raw.Value, &vm); err != nil {
		return "", internalErr(fmt.Errorf("apiserver: decode snapshot target VM %q: %w", vmName, err))
	}
	if vm.NodeName == "" {
		return "", internalErr(fmt.Errorf("apiserver: snapshot target VM %q has no nodeName", vmName))
	}
	return vm.NodeName, nil
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

func (s *Server) putWithPostAdmission(ctx context.Context, key string, obj any, req admission.Request) (store.RawObject, *apiError) {
	if err := validatePostApply(ctx, obj, req); err != nil {
		return store.RawObject{}, admissionToAPIError(err)
	}
	return s.put(ctx, key, obj)
}

func validatePostApply(ctx context.Context, obj any, req admission.Request) error {
	req.NewObject = obj
	data, err := json.Marshal(obj)
	if err != nil {
		return admission.Reject("PostApplyMarshal", admission.ReasonInternal, fmt.Errorf("apiserver: marshal final object: %w", err))
	}
	req.NewRaw = data
	return admission.PostApplyChain().Validate(ctx, req)
}

func admissionToAPIError(err error) *apiError {
	var admissionErr *admission.Error
	if !errors.As(err, &admissionErr) {
		return internalErr(err)
	}
	switch admissionErr.Reason {
	case admission.ReasonBadRequest:
		return badRequest(err)
	case admission.ReasonConflict:
		return conflictErr(err)
	case admission.ReasonInternal:
		return internalErr(err)
	default:
		return internalErr(err)
	}
}

// storeKey delegates to the admission package's canonical key helper so the
// HTTP write path and admission reference validators cannot drift apart.
func storeKey(kind metav1.Kind, name string) string {
	return admission.StoreKey(kind, name)
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
func forbidden(err error) *apiError   { return &apiError{code: http.StatusForbidden, err: err} }
func internalErr(err error) *apiError {
	return &apiError{code: http.StatusInternalServerError, err: err}
}

// unavailable maps a transient capacity condition (no schedulable node yet) to
// 503: the request was well-formed, so it is not a 4xx, and the caller should
// retry once a node registers rather than treat it as a permanent server fault.
func unavailable(err error) *apiError {
	return &apiError{code: http.StatusServiceUnavailable, err: err}
}
