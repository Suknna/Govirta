// Package v1alpha1 defines the shared API envelope for Govirta first-class
// resource objects. It is the project's bottom contract layer and depends only
// on the Go standard library; it never imports internal/ or pkg/hostnet.
package v1alpha1

import (
	"errors"
	"fmt"
)

// APIGroupVersion is the apiVersion string carried by every Govirta API object.
const APIGroupVersion = "govirta.io/v1alpha1"

// Kind names a Govirta API object kind. State-machine-like discriminator, so it
// is a dedicated type with named constants (项目铁律：no bare string).
type Kind string

const (
	// KindStoragePool identifies a StoragePool object.
	KindStoragePool Kind = "StoragePool"
	// KindImage identifies an Image object.
	KindImage Kind = "Image"
	// KindVolume identifies a Volume object.
	KindVolume Kind = "Volume"
	// KindNetwork identifies a Network object.
	KindNetwork Kind = "Network"
	// KindNIC identifies a NIC object.
	KindNIC Kind = "NIC"
	// KindVM identifies a VM object.
	KindVM Kind = "VM"
	// KindSnapshot identifies a Snapshot object (whole-VM cold snapshot).
	KindSnapshot Kind = "Snapshot"
	// KindTask identifies an internal Task object.
	KindTask Kind = "Task"
)

// Finalizer 是阻止对象在 live 资源拆净前被真正删除的守卫标识。
// 状态机式标识符，专用类型 + 命名常量（项目铁律：no bare string）。
type Finalizer string

const (
	// FinalizerNodeTeardown 表示该对象有 node 侧 live 资源待拆除；
	// node 拆净后通过 finalizers 端点摘除它，apiserver 见 finalizers 清空才真删。
	FinalizerNodeTeardown Finalizer = "govirta.io/node-teardown"
)

// ErrInvalidObjectMeta is returned when required identity fields are missing.
var ErrInvalidObjectMeta = errors.New("apis: invalid object metadata")

// TypeMeta carries the apiVersion + kind discriminator shared by all objects.
type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       Kind   `json:"kind"`
}

// ObjectMeta carries identity and routing metadata shared by all objects.
//
// UID and Name are caller-provided (一等公民判据 + 显式铁律); the API layer never
// generates them. ResourceVersion mirrors the etcd revision and is written by
// the store, not the caller. NodeName is set by govirtctl for node-local
// resources and by the scheduler for VM; node watch filters on it.
type ObjectMeta struct {
	Name            string            `json:"name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	NodeName        string            `json:"nodeName,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	// DeletionTimestamp 非空表示对象处于删除中（RFC3339）；空=未删。
	DeletionTimestamp string `json:"deletionTimestamp,omitempty"`
	// Finalizers 是拆净前阻止真删的守卫列表；空且 DeletionTimestamp 非空时 apiserver 真删。
	Finalizers []Finalizer `json:"finalizers,omitempty"`
}

// Validate reports whether the caller-provided identity fields are present.
// ResourceVersion is intentionally not required: the store assigns it.
func (m ObjectMeta) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidObjectMeta)
	}
	if m.UID == "" {
		return fmt.Errorf("%w: uid is required", ErrInvalidObjectMeta)
	}
	return nil
}
