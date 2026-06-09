package admission

import (
	"context"
	"encoding/json"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

func TestDecodeFinalizerPatchRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown", body: `{"remove":"govirta.io/node-teardown","extra":true}`},
		{name: "trailing", body: `{"remove":"govirta.io/node-teardown"} {"remove":"govirta.io/node-teardown"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeFinalizerPatch([]byte(tt.body)); err == nil {
				t.Fatalf("DecodeFinalizerPatch() error = nil, want error")
			}
		})
	}
}

func TestFinalizersPatchShapeValidatorRejectsMissingRemove(t *testing.T) {
	err := FinalizersPatchShapeValidator{}.Validate(context.Background(), Request{
		Operation: OperationFinalizersPatch,
		Kind:      metav1.KindVolume,
		Name:      "vol-a",
		NewRaw:    []byte(`{}`),
	})
	assertAdmissionReason(t, err, ReasonBadRequest)
}

func TestWhitelistFinalizerValidatorRejectsNonNodeTeardown(t *testing.T) {
	err := WhitelistFinalizerValidator{}.Validate(context.Background(), Request{
		Operation: OperationFinalizersPatch,
		Kind:      metav1.KindVolume,
		Name:      "vol-a",
		NewRaw:    []byte(`{"remove":"govirta.io/other"}`),
	})
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestFinalizerDeletionPreconditionValidatorRequiresDeletionTimestamp(t *testing.T) {
	vol := validAdmissionVolume()
	raw, err := json.Marshal(vol)
	if err != nil {
		t.Fatalf("marshal volume: %v", err)
	}

	err = FinalizerDeletionPreconditionValidator{}.Validate(context.Background(), Request{
		Operation: OperationFinalizersPatch,
		Kind:      metav1.KindVolume,
		Name:      vol.Name,
		OldRaw:    raw,
		NewRaw:    []byte(`{"remove":"govirta.io/node-teardown"}`),
	})
	assertAdmissionReason(t, err, ReasonConflict)
}

func TestFinalizersPatchChainAllowsWhitelistedDeletingObject(t *testing.T) {
	vol := validAdmissionVolume()
	vol.DeletionTimestamp = "2026-06-09T00:00:00Z"
	vol.Finalizers = []metav1.Finalizer{metav1.FinalizerNodeTeardown}
	raw, err := json.Marshal(vol)
	if err != nil {
		t.Fatalf("marshal volume: %v", err)
	}

	err = FinalizersPatchChain().Validate(context.Background(), Request{
		Operation: OperationFinalizersPatch,
		Kind:      metav1.KindVolume,
		Name:      vol.Name,
		OldRaw:    raw,
		NewRaw:    []byte(`{"remove":"govirta.io/node-teardown"}`),
	})
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}
