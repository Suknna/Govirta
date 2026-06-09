package admission

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/controlplane/store"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

func TestPatchShapeValidatorAcceptsBareStatusObject(t *testing.T) {
	body := []byte(`{"phase":"ready","message":"ok"}`)
	err := PatchShapeValidator{}.Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindStoragePool,
		Name:      "pool-a",
		NewRaw:    body,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestPatchShapeValidatorRejectsEnvelopeKeys(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "apiVersion", body: `{"apiVersion":"govirta.io/v1alpha1","phase":"ready"}`},
		{name: "kind", body: `{"kind":"StoragePool","phase":"ready"}`},
		{name: "metadata", body: `{"metadata":{"name":"pool-a"},"phase":"ready"}`},
		{name: "spec", body: `{"spec":{},"phase":"ready"}`},
		{name: "status", body: `{"status":{"phase":"ready"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := PatchShapeValidator{}.Validate(context.Background(), Request{
				Operation: OperationStatusPatch,
				Kind:      metav1.KindStoragePool,
				Name:      "pool-a",
				NewRaw:    []byte(tt.body),
			})
			assertAdmissionReason(t, err, ReasonBadRequest)
		})
	}
}

func TestPatchShapeValidatorRejectsNonObjectJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "array", body: `[{"phase":"ready"}]`},
		{name: "string", body: `"ready"`},
		{name: "number", body: `1`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := PatchShapeValidator{}.Validate(context.Background(), Request{
				Operation: OperationStatusPatch,
				Kind:      metav1.KindStoragePool,
				Name:      "pool-a",
				NewRaw:    []byte(tt.body),
			})
			assertAdmissionReason(t, err, ReasonBadRequest)
		})
	}
}

func TestStatusTypeValidatorAcceptsAllKnownStatuses(t *testing.T) {
	tests := []struct {
		name   string
		kind   metav1.Kind
		status any
	}{
		{name: "storagepool", kind: metav1.KindStoragePool, status: storagepoolv1.StoragePoolStatus{Phase: storagepoolv1.PoolPhaseReady}},
		{name: "image", kind: metav1.KindImage, status: imagev1.ImageStatus{Phase: imagev1.ImagePhaseReady}},
		{name: "volume", kind: metav1.KindVolume, status: volumev1.VolumeStatus{Phase: volumev1.VolumePhaseReady}},
		{name: "network", kind: metav1.KindNetwork, status: networkv1.NetworkStatus{Phase: networkv1.NetworkPhaseReady}},
		{name: "nic", kind: metav1.KindNIC, status: nicv1.NICStatus{Phase: nicv1.NICPhaseReady}},
		{name: "vm", kind: metav1.KindVM, status: vmv1.VMStatus{Phase: vmv1.VMPhaseRunning, ObservedPowerState: vmv1.ObservedPowerStateOn, PowerTransition: vmv1.PowerTransitionNone}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.status)
			if err != nil {
				t.Fatalf("marshal status: %v", err)
			}
			err = StatusTypeValidator{}.Validate(context.Background(), Request{
				Operation: OperationStatusPatch,
				Kind:      tt.kind,
				Name:      tt.name,
				NewRaw:    raw,
			})
			if err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestStatusTypeValidatorRejectsInvalidPhase(t *testing.T) {
	body := []byte(`{"phase":"unknown"}`)
	err := StatusTypeValidator{}.Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindStoragePool,
		Name:      "pool-a",
		NewRaw:    body,
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
}

func TestStatusTypeValidatorRejectsInvalidVMPowerEnums(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "observedPowerState", body: `{"phase":"running","observedPowerState":"Maybe","powerTransition":"None"}`},
		{name: "powerTransition", body: `{"phase":"running","observedPowerState":"On","powerTransition":"Teleporting"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := StatusTypeValidator{}.Validate(context.Background(), Request{
				Operation: OperationStatusPatch,
				Kind:      metav1.KindVM,
				Name:      "vm-a",
				NewRaw:    []byte(tt.body),
			})
			assertAdmissionReason(t, err, ReasonBadRequest)
		})
	}
}

func TestStatusTypeValidatorRejectsDecodeFailure(t *testing.T) {
	err := StatusTypeValidator{}.Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindStoragePool,
		Name:      "pool-a",
		NewRaw:    []byte(`{"phase":`),
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
}

func TestStatusTypeValidatorRejectsUnknownFields(t *testing.T) {
	err := StatusTypeValidator{}.Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindVM,
		Name:      "vm-a",
		NewRaw:    []byte(`{"phase":"running","observedPowerState":"On","powerTransition":"None","unexpected":true}`),
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
}

func TestStatusTypeValidatorRejectsTrailingJSONValues(t *testing.T) {
	err := StatusTypeValidator{}.Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindStoragePool,
		Name:      "pool-a",
		NewRaw:    []byte(`{"phase":"ready"} {"phase":"failed"}`),
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
}

func TestTargetObjectValidatorAllowsMissingObject(t *testing.T) {
	st := newReferenceTestStore(t)
	err := TargetObjectValidator{Store: st}.Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindVM,
		Name:      "missing",
	})
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil so handler returns 404", err)
	}
}

func TestTargetObjectValidatorRejectsDeletingObjectWithNoFinalizers(t *testing.T) {
	st := newReferenceTestStore(t)
	vm := validAdmissionVM()
	vm.DeletionTimestamp = "2026-06-09T00:00:00Z"
	seedReferenceObject(t, st, vm)

	err := TargetObjectValidator{Store: st}.Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindVM,
		Name:      vm.Name,
	})
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestTargetObjectValidatorRejectsDeletingOldRawWithNoFinalizers(t *testing.T) {
	vm := validAdmissionVM()
	vm.DeletionTimestamp = "2026-06-09T00:00:00Z"
	vm.Finalizers = nil
	raw, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("marshal vm: %v", err)
	}

	err = TargetObjectValidator{}.Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindVM,
		Name:      vm.Name,
		OldRaw:    raw,
	})
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestTargetObjectValidatorAllowsDeletingObjectWithFinalizers(t *testing.T) {
	st := newReferenceTestStore(t)
	vm := validAdmissionVM()
	vm.DeletionTimestamp = "2026-06-09T00:00:00Z"
	vm.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	seedReferenceObject(t, st, vm)

	err := TargetObjectValidator{Store: st}.Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindVM,
		Name:      vm.Name,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestTargetObjectValidatorAllowsNormalObject(t *testing.T) {
	st := newReferenceTestStore(t)
	vm := validAdmissionVM()
	seedReferenceObject(t, st, vm)

	err := TargetObjectValidator{Store: st}.Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindVM,
		Name:      vm.Name,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestTargetObjectValidatorStoreErrorIsInternal(t *testing.T) {
	errRead := errors.New("store read failed")
	err := TargetObjectValidator{Store: statusFailingStore{getErr: errRead}}.Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindVM,
		Name:      "vm-a",
	})
	assertAdmissionReason(t, err, ReasonInternal)
}

func TestStatusPatchChainRunsShapeBeforeType(t *testing.T) {
	err := StatusPatchChain(newReferenceTestStore(t)).Validate(context.Background(), Request{
		Operation: OperationStatusPatch,
		Kind:      metav1.KindVM,
		Name:      "vm-a",
		NewRaw:    []byte(`{"status":{"phase":"running"}}`),
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
	var admissionErr *Error
	if !errors.As(err, &admissionErr) {
		t.Fatalf("Validate() error = %v, want admission error", err)
	}
	if admissionErr.Validator != "PatchShapeValidator" {
		t.Fatalf("Validator = %q, want PatchShapeValidator", admissionErr.Validator)
	}
}

type statusFailingStore struct {
	getErr error
}

func (s statusFailingStore) Get(ctx context.Context, key string) (store.RawObject, error) {
	return store.RawObject{}, s.getErr
}

func (s statusFailingStore) List(ctx context.Context, prefix string) ([]store.RawObject, error) {
	return nil, nil
}
