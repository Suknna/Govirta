// Package admission defines the apiserver validating admission boundary.
package admission

import (
	"context"
	"fmt"

	"github.com/suknna/govirta/internal/controlplane/store"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// Operation identifies the apiserver write path being admitted.
type Operation string

const (
	OperationCreate          Operation = "Create"
	OperationUpdate          Operation = "Update"
	OperationReplace         Operation = "Replace"
	OperationDelete          Operation = "Delete"
	OperationStatusPatch     Operation = "StatusPatch"
	OperationFinalizersPatch Operation = "FinalizersPatch"
)

// Subresource identifies the object subresource being admitted.
type Subresource string

const (
	SubresourceNone       Subresource = ""
	SubresourceStatus     Subresource = "status"
	SubresourceFinalizers Subresource = "finalizers"
)

// Request carries the raw and typed objects needed by admission validators.
type Request struct {
	Operation   Operation
	Subresource Subresource
	Kind        metav1.Kind
	Name        string

	OldRaw []byte
	NewRaw []byte

	OldObject any
	NewObject any
}

// StoreReader is the read-only store surface available to admission validators.
type StoreReader interface {
	Get(ctx context.Context, key string) (store.RawObject, error)
	List(ctx context.Context, prefix string) ([]store.RawObject, error)
}

// StoreKey returns the canonical persistence key for a resource identity.
func StoreKey(kind metav1.Kind, name string) string {
	return fmt.Sprintf("/govirta/%s/%s", kind, name)
}

// ListPrefix returns the canonical persistence prefix for all resources of kind.
func ListPrefix(kind metav1.Kind) string {
	return fmt.Sprintf("/govirta/%s/", kind)
}
