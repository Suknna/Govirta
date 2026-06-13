package admission

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"slices"

	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"
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
	if req.Operation != OperationUpdate && req.Operation != OperationReplace {
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
	case metav1.KindSnapshot:
		oldSnap, ok := oldObj.(snapshotv1.Snapshot)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("old object type %T is not Snapshot", req.OldObject))
		}
		newSnap, ok := newObj.(snapshotv1.Snapshot)
		if !ok {
			return Reject(v.Name(), ReasonInternal, fmt.Errorf("new object type %T is not Snapshot", req.NewObject))
		}
		return v.validateSnapshot(oldSnap, newSnap)
	case metav1.KindTask:
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("Task spec is internal-only and cannot be updated through public admission"))
	default:
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("unsupported kind %q", req.Kind))
	}
}

func (v FieldPolicyValidator) validateVM(oldVM, newVM vmv1.VM) error {
	if oldVM.Spec.Arch != newVM.Spec.Arch {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.arch is immutable for VM update: existing %q vs requested %q", oldVM.Spec.Arch, newVM.Spec.Arch))
	}
	// Gate 1: cold-mutable fields (memoryMiB/vcpus/volumeRefs/nicRefs) may only
	// change while the desired power intent is Off. We look exclusively at the
	// submitted newVM.Spec.PowerState; status is a live projection and never
	// drives admission.
	if vmColdMutableChanged(oldVM, newVM) && newVM.Spec.PowerState != vmv1.PowerStateOff {
		return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("cold config change requires powerState=Off, got %q", newVM.Spec.PowerState))
	}
	return nil
}

// vmColdMutableChanged reports whether any cold-mutable VM spec field differs
// between oldVM and newVM. Scalar fields use !=; slice fields use
// reflect.DeepEqual so that both membership and ordering changes count as drift.
func vmColdMutableChanged(oldVM, newVM vmv1.VM) bool {
	if oldVM.Spec.MemoryMiB != newVM.Spec.MemoryMiB {
		return true
	}
	if oldVM.Spec.VCPUs != newVM.Spec.VCPUs {
		return true
	}
	if !reflect.DeepEqual(oldVM.Spec.VolumeRefs, newVM.Spec.VolumeRefs) {
		return true
	}
	if !reflect.DeepEqual(oldVM.Spec.NICRefs, newVM.Spec.NICRefs) {
		return true
	}
	return false
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
	if oldSpec.BridgeName != newSpec.BridgeName {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.bridgeName is immutable for Network update: existing %q vs requested %q", oldSpec.BridgeName, newSpec.BridgeName))
	}
	if oldSpec.Subnet != newSpec.Subnet {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.subnet is immutable for Network update: existing %q vs requested %q", oldSpec.Subnet, newSpec.Subnet))
	}
	if oldSpec.GatewayCIDR != newSpec.GatewayCIDR {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.gatewayCIDR is immutable for Network update: existing %q vs requested %q", oldSpec.GatewayCIDR, newSpec.GatewayCIDR))
	}
	if oldSpec.DHCPRangeStart != newSpec.DHCPRangeStart {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.dhcpRangeStart is immutable for Network update: existing %q vs requested %q", oldSpec.DHCPRangeStart, newSpec.DHCPRangeStart))
	}
	if oldSpec.DHCPRangeEnd != newSpec.DHCPRangeEnd {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.dhcpRangeEnd is immutable for Network update: existing %q vs requested %q", oldSpec.DHCPRangeEnd, newSpec.DHCPRangeEnd))
	}
	if oldSpec.EgressInterface != newSpec.EgressInterface {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.egressInterface is immutable for Network update: existing %q vs requested %q", oldSpec.EgressInterface, newSpec.EgressInterface))
	}
	if oldSpec.LeaseSeconds != newSpec.LeaseSeconds {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.leaseSeconds is immutable for Network update: existing %d vs requested %d", oldSpec.LeaseSeconds, newSpec.LeaseSeconds))
	}
	if !slices.Equal(oldSpec.DNS, newSpec.DNS) {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.dns is immutable for Network update: existing %v vs requested %v", oldSpec.DNS, newSpec.DNS))
	}
	if !slices.Equal(oldSpec.Router, newSpec.Router) {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.router is immutable for Network update: existing %v vs requested %v", oldSpec.Router, newSpec.Router))
	}
	return nil
}

func (v FieldPolicyValidator) validateImage(imagev1.Image, imagev1.Image) error {
	return nil
}

type ImageSourceValidator struct {
	UploadPublicURL string
}

func (ImageSourceValidator) Name() string { return "ImageSourceValidator" }

func (v ImageSourceValidator) Validate(ctx context.Context, req Request) error {
	if req.Kind != metav1.KindImage || (req.Operation != OperationCreate && req.Operation != OperationUpdate && req.Operation != OperationReplace) {
		return nil
	}
	obj, err := normalizeObject(req.NewObject)
	if err != nil {
		return Reject(v.Name(), ReasonInternal, err)
	}
	image, ok := obj.(imagev1.Image)
	if !ok {
		return Reject(v.Name(), ReasonInternal, fmt.Errorf("new object type %T is not Image", req.NewObject))
	}
	spec := image.Spec
	switch spec.Source.Type {
	case imagev1.ImageSourceUpload:
		if v.UploadPublicURL == "" {
			return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("upload image source requires configured image store public URL"))
		}
		wantLocation := v.UploadPublicURL + "/apis/Image/" + image.Name + "/store/" + spec.Version
		if spec.Source.Location != wantLocation {
			return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("upload image source location %q must equal %q", spec.Source.Location, wantLocation))
		}
	case imagev1.ImageSourceHTTP:
		parsed, err := url.Parse(spec.Source.Location)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("http image source location must be an explicit http(s) URL"))
		}
	default:
		return Reject(v.Name(), ReasonBadRequest, fmt.Errorf("unsupported image source type %q", spec.Source.Type))
	}
	return nil
}

func (v FieldPolicyValidator) validateStoragePool(oldPool, newPool storagepoolv1.StoragePool) error {
	oldSpec := oldPool.Spec
	newSpec := newPool.Spec
	if oldSpec.Backend != newSpec.Backend {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.backend is immutable for StoragePool update: existing %q vs requested %q", oldSpec.Backend, newSpec.Backend))
	}
	if oldSpec.Type != newSpec.Type {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.type is immutable for StoragePool update: existing %q vs requested %q", oldSpec.Type, newSpec.Type))
	}
	if oldSpec.StorageRoot != newSpec.StorageRoot {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.storageRoot is immutable for StoragePool update: existing %q vs requested %q", oldSpec.StorageRoot, newSpec.StorageRoot))
	}
	if oldSpec.CapacityBytes != newSpec.CapacityBytes {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec.capacityBytes is immutable for StoragePool update: existing %d vs requested %d", oldSpec.CapacityBytes, newSpec.CapacityBytes))
	}
	return nil
}

// validateSnapshot enforces that a Snapshot's spec is fully immutable after
// creation. SnapshotSpec holds a single comparable string field (vmRef), so a
// struct == comparison is a sound whole-spec immutability check.
func (v FieldPolicyValidator) validateSnapshot(oldSnap, newSnap snapshotv1.Snapshot) error {
	if oldSnap.Spec != newSnap.Spec {
		return Reject(v.Name(), ReasonConflict, fmt.Errorf("spec is immutable for Snapshot update: existing %q vs requested %q", oldSnap.Spec.VMRef, newSnap.Spec.VMRef))
	}
	return nil
}
