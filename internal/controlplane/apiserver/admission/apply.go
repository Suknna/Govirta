package admission

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

// EnvelopeValidator validates the shared API envelope before handler-owned
// mutations run. It rejects caller attempts to set or change server-owned
// metadata while still allowing update bodies to omit those fields.
type EnvelopeValidator struct{}

func (EnvelopeValidator) Name() string { return "EnvelopeValidator" }

func (v EnvelopeValidator) Validate(ctx context.Context, req Request) error {
	meta, err := Metadata(req.NewObject)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, err)
	}
	typeMeta, err := TypeMeta(req.NewObject)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, err)
	}
	if typeMeta.APIVersion != metav1.APIGroupVersion {
		return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("apiVersion %q must be %q", typeMeta.APIVersion, metav1.APIGroupVersion))
	}
	if typeMeta.Kind != req.Kind {
		return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("kind %q must match URL kind %q", typeMeta.Kind, req.Kind))
	}
	if meta.Name != req.Name {
		return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("metadata.name %q must match URL name %q", meta.Name, req.Name))
	}
	if err := meta.Validate(); err != nil {
		return Reject(v.Name(), ReasonBadRequest, err)
	}

	switch req.Operation {
	case OperationCreate:
		if meta.ResourceVersion != "" {
			return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("resourceVersion is server-owned on create"))
		}
		if meta.DeletionTimestamp != "" {
			return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("deletionTimestamp is server-owned on create"))
		}
		if len(meta.Finalizers) != 0 {
			return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("finalizers are server-owned on create"))
		}
	case OperationUpdate:
		oldMeta, err := Metadata(req.OldObject)
		if err != nil {
			return Reject(v.Name(), ReasonInternal, err)
		}
		if oldMeta.UID != meta.UID {
			return Reject(v.Name(), ReasonConflict, fmt.Errorf("uid is immutable: existing %q vs requested %q", oldMeta.UID, meta.UID))
		}
		if meta.ResourceVersion != "" && meta.ResourceVersion != oldMeta.ResourceVersion {
			return Reject(v.Name(), ReasonConflict, fmt.Errorf("resourceVersion is server-owned: existing %q vs requested %q", oldMeta.ResourceVersion, meta.ResourceVersion))
		}
		if meta.DeletionTimestamp != "" && meta.DeletionTimestamp != oldMeta.DeletionTimestamp {
			return Reject(v.Name(), ReasonConflict, fmt.Errorf("deletionTimestamp is server-owned: existing %q vs requested %q", oldMeta.DeletionTimestamp, meta.DeletionTimestamp))
		}
		if len(meta.Finalizers) != 0 && !slices.Equal(meta.Finalizers, oldMeta.Finalizers) {
			return Reject(v.Name(), ReasonConflict, fmt.Errorf("finalizers are server-owned: existing %v vs requested %v", oldMeta.Finalizers, meta.Finalizers))
		}
	}
	return nil
}

// SpecValidator runs the per-kind API spec Validate contract and apply-only
// Network range self-consistency checks.
type SpecValidator struct{}

func (SpecValidator) Name() string { return "SpecValidator" }

func (v SpecValidator) Validate(ctx context.Context, req Request) error {
	spec, err := Spec(req.NewObject)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, err)
	}
	if err := spec.Validate(); err != nil {
		return Reject(v.Name(), ReasonBadRequest, err)
	}
	obj, err := normalizeObject(req.NewObject)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, err)
	}
	if network, ok := obj.(networkv1.Network); ok {
		if err := validateNetworkSpecAdmission(network.Spec); err != nil {
			return Reject(v.Name(), ReasonBadRequest, err)
		}
	}
	return nil
}

// ApplyOperationValidator ensures create/update classification and old-object
// context agree. Mismatch indicates apiserver/store plumbing drift, not a user
// input problem.
type ApplyOperationValidator struct{}

func (ApplyOperationValidator) Name() string { return "ApplyOperationValidator" }

func (v ApplyOperationValidator) Validate(ctx context.Context, req Request) error {
	switch req.Operation {
	case OperationCreate:
		if len(req.OldRaw) != 0 || req.OldObject != nil {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("create request must not include an old object"))
		}
	case OperationUpdate:
		if len(req.OldRaw) == 0 || req.OldObject == nil {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("update request must include an old object"))
		}
	default:
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("unsupported apply operation %q", req.Operation))
	}
	return nil
}

// VMPowerStateValidator enforces VM apply rules that depend on create/update
// context but are not field immutability policy.
type VMPowerStateValidator struct{}

func (VMPowerStateValidator) Name() string { return "VMPowerStateValidator" }

func (v VMPowerStateValidator) Validate(ctx context.Context, req Request) error {
	obj, err := normalizeObject(req.NewObject)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, err)
	}
	vm, ok := obj.(vmv1.VM)
	if !ok {
		return nil
	}
	if req.Operation == OperationCreate && vm.Spec.PowerState == vmv1.PowerStateShutdown {
		return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("%w: powerState Shutdown is only valid for VM updates", vmv1.ErrInvalidSpec))
	}
	if req.Operation != OperationUpdate || vm.NodeName == "" {
		return nil
	}
	oldObj, err := normalizeObject(req.OldObject)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, err)
	}
	oldVM, ok := oldObj.(vmv1.VM)
	if !ok {
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("old object type %T is not VM", req.OldObject))
	}
	if oldVM.NodeName != "" && vm.NodeName != oldVM.NodeName {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("nodeName is immutable for VM update: existing %q vs requested %q", oldVM.NodeName, vm.NodeName))
	}
	return nil
}

// NICFinalMACValidator validates the final NIC object after handler-owned MAC
// allocation. It is intentionally a post-apply check so an empty submitted MAC
// can still be accepted and filled by the apiserver.
type NICFinalMACValidator struct{}

func (NICFinalMACValidator) Name() string { return "NICFinalMACValidator" }

func (v NICFinalMACValidator) Validate(ctx context.Context, req Request) error {
	obj, err := normalizeObject(req.NewObject)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, err)
	}
	nic, ok := obj.(nicv1.NIC)
	if !ok {
		return nil
	}
	if err := nic.Spec.Validate(); err != nil {
		return Reject(v.Name(), ReasonBadRequest, err)
	}
	if nic.Spec.MAC == "" {
		return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("%w: final mac is required", nicv1.ErrInvalidSpec))
	}
	return nil
}

func validateNetworkSpecAdmission(spec networkv1.NetworkSpec) error {
	if spec.LeaseSeconds < 0 {
		return fmt.Errorf("%w: leaseSeconds must be non-negative, got %d", networkv1.ErrInvalidSpec, spec.LeaseSeconds)
	}
	subnet, err := netip.ParsePrefix(spec.Subnet)
	if err != nil {
		return fmt.Errorf("%w: subnet %q: %w", networkv1.ErrInvalidSpec, spec.Subnet, err)
	}
	gateway, err := netip.ParsePrefix(spec.GatewayCIDR)
	if err != nil {
		return fmt.Errorf("%w: gatewayCIDR %q: %w", networkv1.ErrInvalidSpec, spec.GatewayCIDR, err)
	}
	start, err := netip.ParseAddr(spec.DHCPRangeStart)
	if err != nil {
		return fmt.Errorf("%w: dhcpRangeStart %q: %w", networkv1.ErrInvalidSpec, spec.DHCPRangeStart, err)
	}
	end, err := netip.ParseAddr(spec.DHCPRangeEnd)
	if err != nil {
		return fmt.Errorf("%w: dhcpRangeEnd %q: %w", networkv1.ErrInvalidSpec, spec.DHCPRangeEnd, err)
	}
	if start.Compare(end) > 0 {
		return fmt.Errorf("%w: dhcpRangeStart %s is after dhcpRangeEnd %s", networkv1.ErrInvalidSpec, start, end)
	}
	if !subnet.Contains(gateway.Addr()) {
		return fmt.Errorf("%w: gateway %s not in subnet %s", networkv1.ErrInvalidSpec, gateway.Addr(), subnet)
	}
	if !subnet.Contains(start) {
		return fmt.Errorf("%w: dhcpRangeStart %s not in subnet %s", networkv1.ErrInvalidSpec, start, subnet)
	}
	if !subnet.Contains(end) {
		return fmt.Errorf("%w: dhcpRangeEnd %s not in subnet %s", networkv1.ErrInvalidSpec, end, subnet)
	}
	return nil
}
