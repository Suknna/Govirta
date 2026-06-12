package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane/apiserver/admission"
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

// Replace updates an existing resource only when the submitted
// metadata.resourceVersion matches the store's current version.
func (s *Server) Replace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, apiErr := s.replace(ctx, r)
	if apiErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(apiErr.code)
		if _, err := w.Write(errorBody(apiErr)); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write replace error response")
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write replace response")
	}
}

func (s *Server) replace(ctx context.Context, r *http.Request) ([]byte, *apiError) {
	kind := metav1.Kind(r.PathValue("kind"))
	name := r.PathValue("name")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, badRequest(fmt.Errorf("apiserver: read replace request body: %w", err))
	}

	obj, req, expectedVersion, aerr := s.decodeAndAdmitReplace(ctx, kind, name, body)
	if aerr != nil {
		return nil, aerr
	}

	switch o := obj.(type) {
	case storagepoolv1.StoragePool:
		if aerr := preserveUpdateObjectMeta(req, &o.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		o.ResourceVersion = ""
		return s.putReplaceResponse(ctx, storeKey(kind, o.Name), o, req, expectedVersion)
	case imagev1.Image:
		if aerr := preserveUpdateObjectMeta(req, &o.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		o.ResourceVersion = ""
		return s.putReplaceResponse(ctx, storeKey(kind, o.Name), o, req, expectedVersion)
	case volumev1.Volume:
		if aerr := preserveUpdateObjectMeta(req, &o.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		o.ResourceVersion = ""
		return s.putReplaceResponse(ctx, storeKey(kind, o.Name), o, req, expectedVersion)
	case networkv1.Network:
		if aerr := preserveUpdateObjectMeta(req, &o.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		o.ResourceVersion = ""
		return s.putReplaceResponse(ctx, storeKey(kind, o.Name), o, req, expectedVersion)
	case nicv1.NIC:
		if aerr := preserveUpdateObjectMeta(req, &o.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		o.ResourceVersion = ""
		if req.Operation == admission.OperationReplace && o.Spec.MAC == "" {
			oldNIC, ok := req.OldObject.(nicv1.NIC)
			if !ok {
				return nil, internalErr(fmt.Errorf("apiserver: existing object for NIC %q has type %T", o.Name, req.OldObject))
			}
			o.Spec.MAC = oldNIC.Spec.MAC
		}
		return s.putReplaceResponse(ctx, storeKey(kind, o.Name), o, req, expectedVersion)
	case vmv1.VM:
		if aerr := preserveVMUpdateMetadata(req.OldObject, &o); aerr != nil {
			return nil, aerr
		}
		o.ResourceVersion = ""
		return s.putReplaceResponse(ctx, storeKey(kind, o.Name), o, req, expectedVersion)
	case snapshotv1.Snapshot:
		if aerr := preserveUpdateObjectMeta(req, &o.ObjectMeta); aerr != nil {
			return nil, aerr
		}
		o.ResourceVersion = ""
		node, aerr := s.resolveVMNodeName(ctx, o.Spec.VMRef)
		if aerr != nil {
			return nil, aerr
		}
		o.NodeName = node
		return s.putReplaceResponse(ctx, storeKey(kind, o.Name), o, req, expectedVersion)
	default:
		return nil, notFound(fmt.Errorf("%w: %q", ErrUnknownKind, kind))
	}
}

func (s *Server) decodeAndAdmitReplace(ctx context.Context, kind metav1.Kind, name string, body []byte) (any, admission.Request, string, *apiError) {
	obj, err := decodeObjectByKind(kind, body)
	if err != nil {
		if errors.Is(err, ErrUnknownKind) {
			return nil, admission.Request{}, "", notFound(fmt.Errorf("%w: %q", ErrUnknownKind, kind))
		}
		return nil, admission.Request{}, "", badRequest(fmt.Errorf("apiserver: decode replace %s: %w", kind, err))
	}

	key := storeKey(kind, name)
	raw, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, admission.Request{}, "", notFound(fmt.Errorf("apiserver: replace target %s/%s: %w", kind, name, err))
		}
		return nil, admission.Request{}, "", internalErr(fmt.Errorf("apiserver: read replace target %s/%s: %w", kind, name, err))
	}
	oldObj, err := decodeObjectByKind(kind, raw.Value)
	if err != nil {
		return nil, admission.Request{}, "", internalErr(fmt.Errorf("apiserver: decode existing %s %q: %w", kind, key, err))
	}
	if err := validateStoredObjectEnvelope(kind, name, oldObj); err != nil {
		return nil, admission.Request{}, "", internalErr(fmt.Errorf("apiserver: invalid existing %s %q: %w", kind, key, err))
	}

	req := admission.Request{
		Operation: admission.OperationReplace,
		Kind:      kind,
		Name:      name,
		OldRaw:    raw.Value,
		NewRaw:    body,
		OldObject: oldObj,
		NewObject: obj,
	}
	if err := admission.PreReplaceChain(s.store).Validate(ctx, req); err != nil {
		return nil, admission.Request{}, "", admissionToAPIError(err)
	}
	submittedMeta, err := admission.Metadata(obj)
	if err != nil {
		return nil, admission.Request{}, "", internalErr(fmt.Errorf("apiserver: replace submitted metadata for %s/%s: %w", kind, name, err))
	}
	preserved, aerr := preserveUpdateStatus(req, obj)
	if aerr != nil {
		return nil, admission.Request{}, "", aerr
	}
	req.NewObject = preserved
	return preserved, req, submittedMeta.ResourceVersion, nil
}

func (s *Server) putReplaceResponse(ctx context.Context, key string, obj any, req admission.Request, expectedVersion string) ([]byte, *apiError) {
	if err := validatePostReplace(ctx, obj, req); err != nil {
		return nil, admissionToAPIError(err)
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, internalErr(fmt.Errorf("apiserver: marshal replace object: %w", err))
	}
	raw, err := s.store.Put(ctx, key, data, expectedVersion)
	if err != nil {
		if errors.Is(err, store.ErrRevisionConflict) {
			return nil, conflictErr(fmt.Errorf("apiserver: replace %q: %w", key, err))
		}
		return nil, internalErr(fmt.Errorf("apiserver: replace store put %q: %w", key, err))
	}
	body, err := withBodyResourceVersion(data, raw.ResourceVersion)
	if err != nil {
		return nil, internalErr(fmt.Errorf("apiserver: replace response resourceVersion: %w", err))
	}
	return body, nil
}

func validatePostReplace(ctx context.Context, obj any, req admission.Request) error {
	req.NewObject = obj
	data, err := json.Marshal(obj)
	if err != nil {
		return admission.Reject("PostReplaceMarshal", admission.ReasonInternal, fmt.Errorf("apiserver: marshal final replace object: %w", err))
	}
	req.NewRaw = data
	return admission.PostReplaceChain().Validate(ctx, req)
}
