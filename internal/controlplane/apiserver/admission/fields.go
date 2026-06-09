package admission

import (
	"context"
	"fmt"
	"slices"

	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// FieldPolicyValidator enforces apply update field mutability policy. It only
// compares the submitted object to the stored object; live VM power state and
// cold-operation execution are node-controller responsibilities, not admission.
type FieldPolicyValidator struct{}

func (FieldPolicyValidator) Name() string { return "FieldPolicyValidator" }

func (v FieldPolicyValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationUpdate {
		return nil
	}

	oldObj, err := normalizeObject(req.OldObject)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, err)
	}
	newObj, err := normalizeObject(req.NewObject)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, err)
	}

	switch req.Kind {
	case metav1.KindVM:
		oldVM, ok := oldObj.(vmv1.VM)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("old object type %T is not VM", req.OldObject))
		}
		newVM, ok := newObj.(vmv1.VM)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("new object type %T is not VM", req.NewObject))
		}
		return v.validateVM(oldVM, newVM)
	case metav1.KindVolume:
		oldVolume, ok := oldObj.(volumev1.Volume)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("old object type %T is not Volume", req.OldObject))
		}
		newVolume, ok := newObj.(volumev1.Volume)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("new object type %T is not Volume", req.NewObject))
		}
		return v.validateVolume(oldVolume, newVolume)
	case metav1.KindNIC:
		oldNIC, ok := oldObj.(nicv1.NIC)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("old object type %T is not NIC", req.OldObject))
		}
		newNIC, ok := newObj.(nicv1.NIC)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("new object type %T is not NIC", req.NewObject))
		}
		return v.validateNIC(oldNIC, newNIC)
	case metav1.KindNetwork:
		oldNetwork, ok := oldObj.(networkv1.Network)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("old object type %T is not Network", req.OldObject))
		}
		newNetwork, ok := newObj.(networkv1.Network)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("new object type %T is not Network", req.NewObject))
		}
		return v.validateNetwork(oldNetwork, newNetwork)
	case metav1.KindImage:
		oldImage, ok := oldObj.(imagev1.Image)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("old object type %T is not Image", req.OldObject))
		}
		newImage, ok := newObj.(imagev1.Image)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("new object type %T is not Image", req.NewObject))
		}
		return v.validateImage(oldImage, newImage)
	case metav1.KindStoragePool:
		oldPool, ok := oldObj.(storagepoolv1.StoragePool)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("old object type %T is not StoragePool", req.OldObject))
		}
		newPool, ok := newObj.(storagepoolv1.StoragePool)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("new object type %T is not StoragePool", req.NewObject))
		}
		return v.validateStoragePool(oldPool, newPool)
	default:
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("unsupported kind %q", req.Kind))
	}
}

func (v FieldPolicyValidator) validateVM(oldVM, newVM vmv1.VM) error {
	if oldVM.Spec.Arch != newVM.Spec.Arch {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.arch is immutable for VM update: existing %q vs requested %q", oldVM.Spec.Arch, newVM.Spec.Arch))
	}
	return nil
}

func (v FieldPolicyValidator) validateVolume(oldVolume, newVolume volumev1.Volume) error {
	oldSpec := oldVolume.Spec
	newSpec := newVolume.Spec
	if oldSpec.PoolRef != newSpec.PoolRef {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.poolRef is immutable for Volume update: existing %q vs requested %q", oldSpec.PoolRef, newSpec.PoolRef))
	}
	if oldSpec.VMRef != newSpec.VMRef {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.vmRef is immutable for Volume update: existing %q vs requested %q", oldSpec.VMRef, newSpec.VMRef))
	}
	if oldSpec.VMName != newSpec.VMName {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.vmName is immutable for Volume update: existing %q vs requested %q", oldSpec.VMName, newSpec.VMName))
	}
	if oldSpec.DiskIndex != newSpec.DiskIndex {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.diskIndex is immutable for Volume update: existing %d vs requested %d", oldSpec.DiskIndex, newSpec.DiskIndex))
	}
	if oldSpec.Role != newSpec.Role {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.role is immutable for Volume update: existing %q vs requested %q", oldSpec.Role, newSpec.Role))
	}
	if oldSpec.ImageRef != newSpec.ImageRef {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.imageRef is immutable for Volume update: existing %q vs requested %q", oldSpec.ImageRef, newSpec.ImageRef))
	}
	if oldSpec.ImageFilePoolRef != newSpec.ImageFilePoolRef {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.imageFilePoolRef is immutable for Volume update: existing %q vs requested %q", oldSpec.ImageFilePoolRef, newSpec.ImageFilePoolRef))
	}
	if newSpec.CapacityBytes < oldSpec.CapacityBytes {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.capacityBytes cannot decrease for Volume update: existing %d vs requested %d", oldSpec.CapacityBytes, newSpec.CapacityBytes))
	}
	return nil
}

func (v FieldPolicyValidator) validateNIC(oldNIC, newNIC nicv1.NIC) error {
	oldSpec := oldNIC.Spec
	newSpec := newNIC.Spec
	if oldSpec.NetworkRef != newSpec.NetworkRef {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.networkRef is immutable for NIC update: existing %q vs requested %q", oldSpec.NetworkRef, newSpec.NetworkRef))
	}
	if oldSpec.VMRef != newSpec.VMRef {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.vmRef is immutable for NIC update: existing %q vs requested %q", oldSpec.VMRef, newSpec.VMRef))
	}
	if newSpec.MAC != "" && oldSpec.MAC != newSpec.MAC {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.mac is immutable for NIC update: existing %q vs requested %q", oldSpec.MAC, newSpec.MAC))
	}
	if oldSpec.IP != newSpec.IP {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.ip is immutable for NIC update: existing %q vs requested %q", oldSpec.IP, newSpec.IP))
	}
	if oldSpec.Hostname != newSpec.Hostname {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.hostname is immutable for NIC update: existing %q vs requested %q", oldSpec.Hostname, newSpec.Hostname))
	}
	return nil
}

func (v FieldPolicyValidator) validateNetwork(oldNetwork, newNetwork networkv1.Network) error {
	oldSpec := oldNetwork.Spec
	newSpec := newNetwork.Spec
	if oldSpec.BridgeName != newSpec.BridgeName ||
		oldSpec.Subnet != newSpec.Subnet ||
		oldSpec.GatewayCIDR != newSpec.GatewayCIDR ||
		oldSpec.DHCPRangeStart != newSpec.DHCPRangeStart ||
		oldSpec.DHCPRangeEnd != newSpec.DHCPRangeEnd ||
		oldSpec.EgressInterface != newSpec.EgressInterface ||
		oldSpec.LeaseSeconds != newSpec.LeaseSeconds ||
		!slices.Equal(oldSpec.DNS, newSpec.DNS) ||
		!slices.Equal(oldSpec.Router, newSpec.Router) {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec is immutable for Network update"))
	}
	return nil
}

func (v FieldPolicyValidator) validateImage(oldImage, newImage imagev1.Image) error {
	oldSpec := oldImage.Spec
	newSpec := newImage.Spec
	if oldSpec.FilePoolRef != newSpec.FilePoolRef ||
		oldSpec.Source.Type != newSpec.Source.Type ||
		oldSpec.Source.Location != newSpec.Source.Location ||
		oldSpec.Format != newSpec.Format ||
		oldSpec.DeclaredSizeBytes != newSpec.DeclaredSizeBytes {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec is immutable for Image update"))
	}
	return nil
}

func (v FieldPolicyValidator) validateStoragePool(oldPool, newPool storagepoolv1.StoragePool) error {
	oldSpec := oldPool.Spec
	newSpec := newPool.Spec
	if oldSpec.Backend != newSpec.Backend ||
		oldSpec.Type != newSpec.Type ||
		oldSpec.StorageRoot != newSpec.StorageRoot ||
		oldSpec.CapacityBytes != newSpec.CapacityBytes {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec is immutable for StoragePool update"))
	}
	return nil
}
