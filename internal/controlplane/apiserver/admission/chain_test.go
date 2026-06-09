package admission

import (
	"context"
	"errors"
	"reflect"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

type recordingValidator struct {
	name string
	seen *[]string
	err  error
}

func (v recordingValidator) Name() string { return v.name }

func (v recordingValidator) Validate(ctx context.Context, req Request) error {
	*v.seen = append(*v.seen, v.name)
	return v.err
}

type cancelingValidator struct {
	name   string
	seen   *[]string
	cancel context.CancelFunc
}

func (v cancelingValidator) Name() string { return v.name }

func (v cancelingValidator) Validate(ctx context.Context, req Request) error {
	*v.seen = append(*v.seen, v.name)
	v.cancel()
	return nil
}

func TestChainRunsValidatorsInOrder(t *testing.T) {
	seen := []string{}
	chain := NewChain(
		recordingValidator{name: "first", seen: &seen},
		recordingValidator{name: "second", seen: &seen},
		recordingValidator{name: "third", seen: &seen},
	)

	if err := chain.Validate(context.Background(), Request{}); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}

	want := []string{"first", "second", "third"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("validators ran in order %v, want %v", seen, want)
	}
}

func TestChainShortCircuitsOnAdmissionError(t *testing.T) {
	cause := errors.New("invalid object")
	rejection := Reject("second", ReasonBadRequest, cause)
	seen := []string{}
	chain := NewChain(
		recordingValidator{name: "first", seen: &seen},
		recordingValidator{name: "second", seen: &seen, err: rejection},
		recordingValidator{name: "third", seen: &seen},
	)

	err := chain.Validate(context.Background(), Request{})
	if err != rejection {
		t.Fatalf("Validate() error = %v, want admission rejection", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Validate() error = %v, want wrapped cause %v", err, cause)
	}

	want := []string{"first", "second"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("validators ran in order %v, want %v", seen, want)
	}
}

func TestChainWrapsPlainValidatorErrorAsInternal(t *testing.T) {
	cause := errors.New("unexpected validator failure")
	seen := []string{}
	chain := NewChain(recordingValidator{name: "plain", seen: &seen, err: cause})

	err := chain.Validate(context.Background(), Request{})
	var admissionErr *Error
	if !errors.As(err, &admissionErr) {
		t.Fatalf("Validate() error = %v, want *Error", err)
	}
	if admissionErr.Validator != "plain" {
		t.Fatalf("Validator = %q, want plain", admissionErr.Validator)
	}
	if admissionErr.Reason != ReasonInternal {
		t.Fatalf("Reason = %q, want %q", admissionErr.Reason, ReasonInternal)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Validate() error = %v, want wrapped cause %v", err, cause)
	}
}

func TestChainStopsWhenContextIsCanceled(t *testing.T) {
	seen := []string{}
	chain := NewChain(recordingValidator{name: "never", seen: &seen})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := chain.Validate(ctx, Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate() error = %v, want context.Canceled", err)
	}
	if len(seen) != 0 {
		t.Fatalf("validators ran %v, want none", seen)
	}
}

func TestChainStopsWhenContextIsCanceledBetweenValidators(t *testing.T) {
	seen := []string{}
	ctx, cancel := context.WithCancel(context.Background())
	chain := NewChain(
		cancelingValidator{name: "cancel", seen: &seen, cancel: cancel},
		recordingValidator{name: "never", seen: &seen},
	)

	err := chain.Validate(ctx, Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate() error = %v, want context.Canceled", err)
	}
	want := []string{"cancel"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("validators ran %v, want %v", seen, want)
	}
}

func TestStoreKeyAndListPrefixExactStrings(t *testing.T) {
	if got := StoreKey(metav1.KindVM, "vm-a"); got != "/govirta/VM/vm-a" {
		t.Fatalf("StoreKey() = %q, want /govirta/VM/vm-a", got)
	}
	if got := ListPrefix(metav1.KindNIC); got != "/govirta/NIC/" {
		t.Fatalf("ListPrefix() = %q, want /govirta/NIC/", got)
	}
}
