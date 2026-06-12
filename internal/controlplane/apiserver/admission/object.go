package admission

import (
	"fmt"

	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	snapshotv1 "github.com/suknna/govirta/pkg/apis/snapshot/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

type specValidator interface {
	Validate() error
}

type statusValidator interface {
	Validate() error
}

func Metadata(obj any) (metav1.ObjectMeta, error) {
	obj, err := normalizeObject(obj)
	if err != nil {
		return metav1.ObjectMeta{}, err
	}
	switch o := obj.(type) {
	case storagepoolv1.StoragePool:
		return o.ObjectMeta, nil
	case imagev1.Image:
		return o.ObjectMeta, nil
	case volumev1.Volume:
		return o.ObjectMeta, nil
	case networkv1.Network:
		return o.ObjectMeta, nil
	case nicv1.NIC:
		return o.ObjectMeta, nil
	case vmv1.VM:
		return o.ObjectMeta, nil
	case snapshotv1.Snapshot:
		return o.ObjectMeta, nil
	case taskv1.Task:
		return o.ObjectMeta, nil
	default:
		return metav1.ObjectMeta{}, fmt.Errorf("unsupported object type %T", obj)
	}
}

func TypeMeta(obj any) (metav1.TypeMeta, error) {
	obj, err := normalizeObject(obj)
	if err != nil {
		return metav1.TypeMeta{}, err
	}
	switch o := obj.(type) {
	case storagepoolv1.StoragePool:
		return o.TypeMeta, nil
	case imagev1.Image:
		return o.TypeMeta, nil
	case volumev1.Volume:
		return o.TypeMeta, nil
	case networkv1.Network:
		return o.TypeMeta, nil
	case nicv1.NIC:
		return o.TypeMeta, nil
	case vmv1.VM:
		return o.TypeMeta, nil
	case snapshotv1.Snapshot:
		return o.TypeMeta, nil
	case taskv1.Task:
		return o.TypeMeta, nil
	default:
		return metav1.TypeMeta{}, fmt.Errorf("unsupported object type %T", obj)
	}
}

func Spec(obj any) (specValidator, error) {
	obj, err := normalizeObject(obj)
	if err != nil {
		return nil, err
	}
	switch o := obj.(type) {
	case storagepoolv1.StoragePool:
		return o.Spec, nil
	case imagev1.Image:
		return o.Spec, nil
	case volumev1.Volume:
		return o.Spec, nil
	case networkv1.Network:
		return o.Spec, nil
	case nicv1.NIC:
		return o.Spec, nil
	case vmv1.VM:
		return o.Spec, nil
	case snapshotv1.Snapshot:
		return o.Spec, nil
	case taskv1.Task:
		return o.Spec, nil
	default:
		return nil, fmt.Errorf("unsupported object type %T", obj)
	}
}

func Status(obj any) (statusValidator, error) {
	obj, err := normalizeObject(obj)
	if err != nil {
		return nil, err
	}
	switch o := obj.(type) {
	case storagepoolv1.StoragePool:
		return o.Status, nil
	case imagev1.Image:
		return o.Status, nil
	case volumev1.Volume:
		return o.Status, nil
	case networkv1.Network:
		return o.Status, nil
	case nicv1.NIC:
		return o.Status, nil
	case vmv1.VM:
		return o.Status, nil
	case snapshotv1.Snapshot:
		return o.Status, nil
	case taskv1.Task:
		return o.Status, nil
	case storagepoolv1.StoragePoolStatus:
		return o, nil
	case imagev1.ImageStatus:
		return o, nil
	case volumev1.VolumeStatus:
		return o, nil
	case networkv1.NetworkStatus:
		return o, nil
	case nicv1.NICStatus:
		return o, nil
	case vmv1.VMStatus:
		return o, nil
	case snapshotv1.SnapshotStatus:
		return o, nil
	case taskv1.TaskStatus:
		return o, nil
	default:
		return nil, fmt.Errorf("unsupported object type %T", obj)
	}
}

func normalizeObject(obj any) (any, error) {
	switch o := obj.(type) {
	case *storagepoolv1.StoragePool:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *imagev1.Image:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *volumev1.Volume:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *networkv1.Network:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *nicv1.NIC:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *vmv1.VM:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *snapshotv1.Snapshot:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *taskv1.Task:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *storagepoolv1.StoragePoolStatus:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *imagev1.ImageStatus:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *volumev1.VolumeStatus:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *networkv1.NetworkStatus:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *nicv1.NICStatus:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *vmv1.VMStatus:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *snapshotv1.SnapshotStatus:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	case *taskv1.TaskStatus:
		if o == nil {
			return nil, fmt.Errorf("unsupported nil object type %T", obj)
		}
		return *o, nil
	default:
		return obj, nil
	}
}
