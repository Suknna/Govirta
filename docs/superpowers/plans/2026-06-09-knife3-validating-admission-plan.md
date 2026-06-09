# Knife 3 Validating Admission Controller Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the internal Validating Admission Controller framework and route every etcd-mutating apiserver write path through business validators.

**Architecture:** Add `internal/controlplane/apiserver/admission` as the apiserver-owned validating admission boundary. Handlers continue to parse HTTP, perform existing mutations, and write the store; admission owns validating policy for Apply, DELETE, status PATCH, and finalizers PATCH. Apply uses pre-mutation validation for user intent and post-mutation validation for final object contracts such as allocated NIC MAC.

**Tech Stack:** Go 1.26, stdlib `encoding/json`/`errors`/`slices`, existing `store.Store`, existing `pkg/apis/*/v1alpha1` contracts, existing apiserver fake/etcd test seams.

---

## File structure and line-count forecast

Current notable file sizes:

- `internal/controlplane/apiserver/handler_apply.go`: 418 lines. It is near the 500-line soft limit; this plan moves validation out instead of adding more policy here.
- `internal/controlplane/apiserver/handler_apply_test.go`: 586 lines. Tests are already over the soft limit but below the 800-line hard limit; new admission-package tests should avoid expanding this file heavily.
- `internal/controlplane/apiserver/reference_guard.go`: 219 lines. Its logic will move into admission reference validators; the file can be deleted or reduced to a thin wrapper during migration.
- API `types.go` files are all below 150 lines; adding status `Validate()` methods keeps them below 200.

New files:

- `internal/controlplane/apiserver/admission/request.go` — `Operation`, `Subresource`, `Request`, store key helpers.
- `internal/controlplane/apiserver/admission/error.go` — structured admission errors and reason helpers.
- `internal/controlplane/apiserver/admission/chain.go` — `Validator`, `Chain`, chain execution.
- `internal/controlplane/apiserver/admission/registry.go` — chain constructor functions as each validator group lands.
- `internal/controlplane/apiserver/admission/object.go` — typed object helpers, metadata/spec/status extraction.
- `internal/controlplane/apiserver/admission/apply.go` — envelope/spec/operation/power/final MAC validators.
- `internal/controlplane/apiserver/admission/fields.go` — immutable/cold-mutable field policy validators.
- `internal/controlplane/apiserver/admission/references.go` — apply/delete reference validators and reference projections.
- `internal/controlplane/apiserver/admission/status.go` — bare status PATCH validators.
- `internal/controlplane/apiserver/admission/finalizers.go` — finalizers PATCH validators.
- `internal/controlplane/apiserver/admission/*_test.go` — focused unit tests per validator group.

Existing files to modify:

- `pkg/apis/{storagepool,image,network,nic,volume,vm}/v1alpha1/types.go` — add phase/status validation contracts.
- `internal/controlplane/apiserver/handler_apply.go` — build admission requests and call pre/post mutation chains.
- `internal/controlplane/apiserver/apply_admission.go` — delete or shrink old VM-only admission helpers after migration.
- `internal/controlplane/apiserver/handler_delete.go` — call delete admission chain instead of direct guard.
- `internal/controlplane/apiserver/handler_status.go` — validate bare status body through admission before CAS merge.
- `internal/controlplane/apiserver/handler_finalizers.go` — validate single-field remove request through admission before mutation.
- Existing apiserver tests — update expected error paths only where admission changes the responsible layer; preserve HTTP semantics.

---

### Task 1: API status validation contracts

**Files:**
- Modify: `pkg/apis/storagepool/v1alpha1/types.go`
- Modify: `pkg/apis/image/v1alpha1/types.go`
- Modify: `pkg/apis/network/v1alpha1/types.go`
- Modify: `pkg/apis/nic/v1alpha1/types.go`
- Modify: `pkg/apis/volume/v1alpha1/types.go`
- Modify: `pkg/apis/vm/v1alpha1/types.go`
- Test: corresponding `types_test.go` files

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: every Status type has a validating contract that status PATCH admission can call.

Acceptance evidence:
- `go test -count=1 ./pkg/apis/...` passes.
- Invalid status phases are rejected for all six resource kinds.
- VM status rejects invalid `ObservedPowerState` and invalid `PowerTransition`.

- [ ] **Step 2: Add `Valid()` methods for phase enums that do not have one**

Add methods equivalent to the following in each package:

```go
func (p PoolPhase) Valid() bool {
	switch p {
	case PoolPhasePending, PoolPhaseReady, PoolPhaseFailed:
		return true
	default:
		return false
	}
}
```

Use each package's actual phase constants:

- `storagepool`: `PoolPhasePending`, `PoolPhaseReady`, `PoolPhaseFailed`
- `image`: `ImagePhasePending`, `ImagePhaseReady`, `ImagePhaseDeleting`, `ImagePhaseFailed`
- `network`: `NetworkPhasePending`, `NetworkPhaseReady`, `NetworkPhaseFailed`
- `nic`: `NICPhasePending`, `NICPhaseReady`, `NICPhaseFailed`
- `volume`: `VolumePhasePending`, `VolumePhaseReady`, `VolumePhaseFailed`
- `vm`: `VMPhaseDefined`, `VMPhaseStarting`, `VMPhaseRunning`, `VMPhaseStopping`, `VMPhaseStopped`, `VMPhaseFailed`

- [ ] **Step 3: Add status validation errors and `Validate()` methods**

In each package add `ErrInvalidStatus` beside `ErrInvalidSpec`, then implement status validation.

Example for StoragePool:

```go
var ErrInvalidStatus = errors.New("storagepool: invalid status")

func (s StoragePoolStatus) Validate() error {
	if !s.Phase.Valid() {
		return fmt.Errorf("%w: phase %q", ErrInvalidStatus, s.Phase)
	}
	return nil
}
```

VM status must validate all three machine fields:

```go
var ErrInvalidStatus = errors.New("vm: invalid status")

func (s VMStatus) Validate() error {
	if !s.Phase.Valid() {
		return fmt.Errorf("%w: phase %q", ErrInvalidStatus, s.Phase)
	}
	if !s.ObservedPowerState.Valid() {
		return fmt.Errorf("%w: observedPowerState %q", ErrInvalidStatus, s.ObservedPowerState)
	}
	if !s.PowerTransition.Valid() {
		return fmt.Errorf("%w: powerTransition %q", ErrInvalidStatus, s.PowerTransition)
	}
	return nil
}
```

`Message` fields require no validation; empty message is valid.

- [ ] **Step 4: Add API tests**

Add tests with exact names and assertions. Example for StoragePool:

```go
func TestStoragePoolStatusValidateAcceptsKnownPhase(t *testing.T) {
	status := StoragePoolStatus{Phase: PoolPhaseReady}
	if err := status.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestStoragePoolStatusValidateRejectsUnknownPhase(t *testing.T) {
	status := StoragePoolStatus{Phase: PoolPhase("bogus")}
	err := status.Validate()
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
	}
}
```

Create equivalent tests for Image, Network, NIC, and Volume using each package's phase type and `ErrInvalidStatus`. For VM, the known-phase acceptance test must set all required machine fields:

```go
func TestVMStatusValidateAcceptsKnownPhase(t *testing.T) {
	status := VMStatus{Phase: VMPhaseRunning, ObservedPowerState: ObservedPowerStateOn, PowerTransition: PowerTransitionNone}
	if err := status.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestVMStatusValidateRejectsUnknownPhase(t *testing.T) {
	status := VMStatus{Phase: VMPhase("bogus"), ObservedPowerState: ObservedPowerStateOn, PowerTransition: PowerTransitionNone}
	err := status.Validate()
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
	}
}
```

For VM also add:

```go
func TestVMStatusValidateRejectsUnknownObservedPowerState(t *testing.T) {
	status := VMStatus{Phase: VMPhaseRunning, ObservedPowerState: ObservedPowerState("bogus"), PowerTransition: PowerTransitionNone}
	err := status.Validate()
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
	}
}

func TestVMStatusValidateRejectsUnknownPowerTransition(t *testing.T) {
	status := VMStatus{Phase: VMPhaseRunning, ObservedPowerState: ObservedPowerStateOn, PowerTransition: PowerTransition("bogus")}
	err := status.Validate()
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("Validate() error = %v, want ErrInvalidStatus", err)
	}
}
```

Each rejection test must use `errors.Is(err, ErrInvalidStatus)`.

- [ ] **Step 5: Verify and commit**

Run:

```bash
gofmt -w pkg/apis/*/v1alpha1/types.go pkg/apis/*/v1alpha1/*_test.go
go test -count=1 ./pkg/apis/...
```

Expected: PASS.

Commit:

```bash
git add pkg/apis
git commit -m "feat(apis): validate resource status contracts"
```

---

### Task 2: Admission core package

**Files:**
- Create: `internal/controlplane/apiserver/admission/request.go`
- Create: `internal/controlplane/apiserver/admission/error.go`
- Create: `internal/controlplane/apiserver/admission/chain.go`
- Create: `internal/controlplane/apiserver/admission/registry.go`
- Test: `internal/controlplane/apiserver/admission/chain_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: create the reusable validating admission framework with structured errors and ordered chains.

Acceptance evidence:
- `go test -count=1 ./internal/controlplane/apiserver/admission` passes.
- Chain executes validators in order and short-circuits on the first admission error.
- Error exposes reason, validator name, and wrapped cause.

- [ ] **Step 2: Implement `request.go`**

Create:

```go
package admission

import (
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

type Operation string

const (
	OperationCreate          Operation = "Create"
	OperationUpdate          Operation = "Update"
	OperationDelete          Operation = "Delete"
	OperationStatusPatch     Operation = "StatusPatch"
	OperationFinalizersPatch Operation = "FinalizersPatch"
)

type Subresource string

const (
	SubresourceNone       Subresource = ""
	SubresourceStatus     Subresource = "status"
	SubresourceFinalizers Subresource = "finalizers"
)

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

func StoreKey(kind metav1.Kind, name string) string {
	return fmt.Sprintf("/govirta/%s/%s", kind, name)
}

func ListPrefix(kind metav1.Kind) string {
	return fmt.Sprintf("/govirta/%s/", kind)
}
```

- [ ] **Step 3: Implement `error.go`**

Create:

```go
package admission

import "fmt"

type ErrorReason string

const (
	ReasonBadRequest ErrorReason = "BadRequest"
	ReasonConflict   ErrorReason = "Conflict"
	ReasonInternal   ErrorReason = "Internal"
)

type Error struct {
	Validator string
	Reason    ErrorReason
	Err       error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("admission %s rejected request: %v", e.Validator, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func Reject(validator string, reason ErrorReason, err error) *Error {
	return &Error{Validator: validator, Reason: reason, Err: err}
}
```

- [ ] **Step 4: Implement `chain.go`**

Create:

```go
package admission

import (
	"context"
	"fmt"
)

type Validator interface {
	Name() string
	Validate(ctx context.Context, req Request) error
}

type Chain struct {
	validators []Validator
}

func NewChain(validators ...Validator) Chain {
	return Chain{validators: append([]Validator(nil), validators...)}
}

func (c Chain) Validate(ctx context.Context, req Request) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("admission: context done: %w", err)
	}
	for _, v := range c.validators {
		if err := v.Validate(ctx, req); err != nil {
			if _, ok := err.(*Error); ok {
				return err
			}
			return Reject(v.Name(), ReasonInternal, err)
		}
	}
	return nil
}
```

- [ ] **Step 5: Implement initial `registry.go`**

Create chain constructor functions even before all validators exist. Task 2 returns empty chains except for the validators already defined in this task; later tasks update these constructors in place.

```go
package admission

func PreApplyChain(st StoreReader) Chain {
	return NewChain()
}

func PostApplyChain() Chain {
	return NewChain()
}

func DeleteChain(st StoreReader) Chain {
	return NewChain()
}

func StatusPatchChain() Chain {
	return NewChain()
}

func FinalizersPatchChain() Chain {
	return NewChain()
}
```

`StoreReader` is introduced in Task 6. To keep Task 2 compiling, define it now in `request.go`:

```go
type StoreReader interface {
	Get(ctx context.Context, key string) (store.RawObject, error)
	List(ctx context.Context, prefix string) ([]store.RawObject, error)
}
```

Add `context` and `github.com/suknna/govirta/internal/controlplane/store` imports to `request.go`.

- [ ] **Step 6: Add chain tests**

Test names:

- `TestChainRunsValidatorsInOrder`
- `TestChainShortCircuitsOnAdmissionError`
- `TestChainWrapsPlainValidatorErrorAsInternal`
- `TestChainStopsWhenContextIsCanceled`

Use a fake validator:

```go
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
```

- [ ] **Step 7: Verify and commit**

Run:

```bash
gofmt -w internal/controlplane/apiserver/admission
go test -count=1 ./internal/controlplane/apiserver/admission
```

Expected: PASS.

Commit:

```bash
git add internal/controlplane/apiserver/admission
git commit -m "feat(apiserver): add validating admission core"
```

---

### Task 3: Admission object helpers and status interfaces

**Files:**
- Create: `internal/controlplane/apiserver/admission/object.go`
- Test: `internal/controlplane/apiserver/admission/object_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: validators can read metadata/spec/status from typed API objects without duplicating type switches in every validator.

Acceptance evidence:
- All six resource kinds return the expected metadata and spec validator.
- Status validation helper rejects wrong typed objects.

- [ ] **Step 2: Implement typed interfaces and helpers**

Create `object.go` with:

```go
package admission

import (
	"fmt"

	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	networkv1 "github.com/suknna/govirta/pkg/apis/network/v1alpha1"
	nicv1 "github.com/suknna/govirta/pkg/apis/nic/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

type specValidator interface { Validate() error }
type statusValidator interface { Validate() error }

func Metadata(obj any) (metav1.ObjectMeta, error) {
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
	default:
		return metav1.ObjectMeta{}, fmt.Errorf("unsupported object type %T", obj)
	}
}

func TypeMeta(obj any) (metav1.TypeMeta, error) {
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
	default:
		return metav1.TypeMeta{}, fmt.Errorf("unsupported object type %T", obj)
	}
}

func Spec(obj any) (specValidator, error) {
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
	default:
		return nil, fmt.Errorf("unsupported object type %T", obj)
	}
}
```

Add `Status(obj any) (statusValidator, error)` with a switch that accepts both full resource objects and raw status structs. It must return `o.Status` for full objects and the value itself for `storagepoolv1.StoragePoolStatus`, `imagev1.ImageStatus`, `volumev1.VolumeStatus`, `networkv1.NetworkStatus`, `nicv1.NICStatus`, and `vmv1.VMStatus`. This lets status PATCH admission use the bare Status object as `Request.NewObject`.

- [ ] **Step 3: Add object helper tests**

Use one valid object per kind and assert:

- `Metadata(obj).Name` equals fixture name.
- `TypeMeta(obj).Kind` equals fixture kind.
- `Spec(obj).Validate()` succeeds.
- `Status(obj).Validate()` succeeds after setting all required status fields. For VM this means phase plus `ObservedPowerStateOn` and `PowerTransitionNone`.
- Unknown object type returns an error.

- [ ] **Step 4: Verify and commit**

Run:

```bash
gofmt -w internal/controlplane/apiserver/admission
go test -count=1 ./internal/controlplane/apiserver/admission
```

Expected: PASS.

Commit:

```bash
git add internal/controlplane/apiserver/admission
git commit -m "feat(admission): add typed object helpers"
```

---

### Task 4: Apply pre/post mutation admission integration

**Files:**
- Create: `internal/controlplane/apiserver/admission/apply.go`
- Modify: `internal/controlplane/apiserver/handler_apply.go`
- Modify: `internal/controlplane/apiserver/apply_admission.go`
- Test: `internal/controlplane/apiserver/admission/apply_test.go`
- Test: `internal/controlplane/apiserver/handler_apply_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: apply create/update flows through pre-mutation admission before existing mutations and through post-mutation admission before store write.

Acceptance evidence:
- Existing apply tests still pass.
- Create with missing `metadata.uid` returns 400 through admission.
- Create with user-provided finalizers returns 400 before handler injects defaults.
- NIC with empty MAC is accepted pre-mutation, allocated by current handler, then validated post-mutation.

- [ ] **Step 2: Implement apply validators**

In `admission/apply.go` implement validators:

```go
type EnvelopeValidator struct{}
func (EnvelopeValidator) Name() string { return "EnvelopeValidator" }
func (EnvelopeValidator) Validate(ctx context.Context, req Request) error {
	meta, err := Metadata(req.NewObject)
	if err != nil { return Reject("EnvelopeValidator", ReasonInternal, err) }
	tm, err := TypeMeta(req.NewObject)
	if err != nil { return Reject("EnvelopeValidator", ReasonInternal, err) }
	if tm.Kind != req.Kind || tm.APIVersion != metav1.APIGroupVersion {
		return Reject("EnvelopeValidator", ReasonBadRequest, fmt.Errorf("typeMeta mismatch"))
	}
	if meta.Name != req.Name {
		return Reject("EnvelopeValidator", ReasonBadRequest, fmt.Errorf("metadata.name %q does not match path %q", meta.Name, req.Name))
	}
	if req.Operation == OperationUpdate {
		oldMeta, err := Metadata(req.OldObject)
		if err != nil { return Reject("EnvelopeValidator", ReasonInternal, err) }
		if oldMeta.UID != meta.UID {
			return Reject("EnvelopeValidator", ReasonConflict, fmt.Errorf("metadata.uid is immutable"))
		}
	}
	if err := meta.Validate(); err != nil {
		return Reject("EnvelopeValidator", ReasonBadRequest, err)
	}
	if len(meta.Finalizers) > 0 {
		return Reject("EnvelopeValidator", ReasonBadRequest, fmt.Errorf("metadata.finalizers are server-owned"))
	}
	if meta.DeletionTimestamp != "" || meta.ResourceVersion != "" {
		return Reject("EnvelopeValidator", ReasonBadRequest, fmt.Errorf("server-owned metadata fields must be empty"))
	}
	return nil
}
```

Implement `SpecValidator`, `ApplyOperationValidator`, `VMPowerStateValidator`, and `NICFinalMACValidator`. `NICFinalMACValidator` must accept only NIC objects and call `nic.Spec.Validate()` after MAC allocation. `VMPowerStateValidator` maps nodeName mismatch on update to `ReasonConflict`, not `ReasonBadRequest`.

Update `registry.go` in the same task:

```go
func PreApplyChain(st StoreReader) Chain {
	return NewChain(EnvelopeValidator{}, SpecValidator{}, ApplyOperationValidator{}, VMPowerStateValidator{})
}

func PostApplyChain() Chain {
	return NewChain(NICFinalMACValidator{})
}
```

- [ ] **Step 3: Add all-kind apply request building**

Generalize the current VM-only `classifyApply` path. Every kind must read the target key, classify create/update, and decode the old typed object on update before invoking admission.

Define a helper in apiserver package:

```go
type applyAdmissionInput struct {
	operation admission.Operation
	oldRaw    []byte
	oldObject any
}

func (s *Server) buildApplyAdmissionInput(ctx context.Context, kind metav1.Kind, key string) (applyAdmissionInput, *apiError) {
	raw, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return applyAdmissionInput{operation: admission.OperationCreate}, nil
		}
		return applyAdmissionInput{}, internalErr(fmt.Errorf("apiserver: classify apply %q: %w", key, err))
	}
	oldObj, decErr := decodeObjectByKind(kind, raw.Value)
	if decErr != nil {
		return applyAdmissionInput{}, internalErr(fmt.Errorf("apiserver: decode existing %s for apply: %w", kind, decErr))
	}
	return applyAdmissionInput{operation: admission.OperationUpdate, oldRaw: raw.Value, oldObject: oldObj}, nil
}
```

`decodeObjectByKind(kind, raw)` must switch all six kinds and return typed objects. Unknown kind remains the existing handler-level `ErrUnknownKind` mapping.

- [ ] **Step 4: Wire handler pre/post admission**

In `handler_apply.go`, after decoding and before mutation call:

```go
if err := admission.PreApplyChain(s.store).Validate(ctx, req); err != nil {
	return nil, admissionToAPIError(err)
}
```

After existing mutation and before store write call a post-mutation chain for final object validation:

```go
if err := admission.PostApplyChain().Validate(ctx, postReq); err != nil {
	return nil, admissionToAPIError(err)
}
```

`admissionToAPIError` lives in apiserver package and maps `*admission.Error` reasons to existing `badRequest` / `conflictErr` / `internalErr`.

- [ ] **Step 5: Preserve existing mutation semantics**

Keep these behaviors in handler code:

- `injectFinalizer` after pre-admission.
- `applyNIC` MAC allocation.
- `applyVM` scheduling and server-owned metadata preservation.

If post-admission rejects a NIC after MAC allocation, the object must not be stored. This is safe because MAC allocation's `WithAllocation` commit closure should only store after validation; move the post-admission call inside that closure before `store.Put`.

NIC allocation closure must follow this order:

```go
err := s.alloc.WithAllocation(ctx, func(hw net.HardwareAddr) error {
	nic.Spec.MAC = hw.String()
	postReq := admission.Request{Operation: input.operation, Kind: metav1.KindNIC, Name: nic.Name, OldRaw: input.oldRaw, OldObject: input.oldObject, NewObject: *nic}
	if err := admission.PostApplyChain().Validate(ctx, postReq); err != nil {
		return err
	}
	data, err := json.Marshal(*nic)
	if err != nil { return fmt.Errorf("apiserver: marshal NIC: %w", err) }
	raw, err = s.store.Put(ctx, key, data, "")
	return err
})
```

If the closure returns `*admission.Error`, map it through `admissionToAPIError` rather than `internalErr`.

- [ ] **Step 6: Add tests**

Admission package tests:

- `TestEnvelopeValidatorRejectsUserFinalizersOnCreate`
- `TestSpecValidatorRunsAPISpecValidate`
- `TestVMPowerStateValidatorRejectsCreateShutdown`
- `TestVMPowerStateValidatorAllowsUpdateShutdown`
- `TestNICFinalMACValidatorRejectsInvalidAllocatedMAC`

Apiserver handler tests:

- update existing `TestApplyVMCreateRejectsShutdownPowerState` to assert admission error body is non-empty.
- add `TestApplyRejectsUserProvidedFinalizers`.
- add `TestApplyUpdateRejectsUIDChange`.
- add `TestApplyVMUpdateRejectsDifferentNodeNameWithConflict` and assert HTTP 409.
- add `TestApplyUpdateCorruptExistingReturnsInternalErrorForAllKinds` or one representative non-VM kind plus the existing VM case.
- keep `TestApplyVMUpdatePreservesServerOwnedMetadata` passing.

- [ ] **Step 7: Verify and commit**

Run:

```bash
gofmt -w internal/controlplane/apiserver
go test -count=1 ./internal/controlplane/apiserver/admission ./internal/controlplane/apiserver
```

Expected: PASS.

Commit:

```bash
git add internal/controlplane/apiserver
git commit -m "feat(apiserver): route apply through validating admission"
```

---

### Task 5: Apply update field grading validators

**Files:**
- Create: `internal/controlplane/apiserver/admission/fields.go`
- Test: `internal/controlplane/apiserver/admission/fields_test.go`
- Test: `internal/controlplane/apiserver/handler_apply_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: update rejects immutable field changes, accepts cold-mutable field changes, and enforces cold-mutable direction rules without checking runtime stopped state.

Acceptance evidence:
- VM `arch` update returns 409.
- VM `memoryMiB` update is accepted.
- Volume `capacityBytes` decrease returns 409.
- Volume `capacityBytes` increase is accepted by apiserver.

- [ ] **Step 2: Implement `FieldPolicyValidator`**

Use explicit per-kind comparisons, not reflection-based policy maps.

Required behavior:

```go
type FieldPolicyValidator struct{}
func (FieldPolicyValidator) Name() string { return "FieldPolicyValidator" }
func (FieldPolicyValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationUpdate { return nil }
	switch oldObj := req.OldObject.(type) {
	case vmv1.VM:
		newObj, ok := req.NewObject.(vmv1.VM)
		if !ok { return Reject("FieldPolicyValidator", ReasonInternal, fmt.Errorf("new object type %T", req.NewObject)) }
		if oldObj.Spec.Arch != newObj.Spec.Arch {
			return Reject("FieldPolicyValidator", ReasonConflict, fmt.Errorf("VM.spec.arch is immutable"))
		}
	case volumev1.Volume:
		newObj, ok := req.NewObject.(volumev1.Volume)
		if !ok { return Reject("FieldPolicyValidator", ReasonInternal, fmt.Errorf("new object type %T", req.NewObject)) }
		if oldObj.Spec.PoolRef != newObj.Spec.PoolRef { return Reject("FieldPolicyValidator", ReasonConflict, fmt.Errorf("Volume.spec.poolRef is immutable")) }
		if oldObj.Spec.VMRef != newObj.Spec.VMRef { return Reject("FieldPolicyValidator", ReasonConflict, fmt.Errorf("Volume.spec.vmRef is immutable")) }
		if oldObj.Spec.VMName != newObj.Spec.VMName { return Reject("FieldPolicyValidator", ReasonConflict, fmt.Errorf("Volume.spec.vmName is immutable")) }
		if oldObj.Spec.DiskIndex != newObj.Spec.DiskIndex { return Reject("FieldPolicyValidator", ReasonConflict, fmt.Errorf("Volume.spec.diskIndex is immutable")) }
		if oldObj.Spec.Role != newObj.Spec.Role { return Reject("FieldPolicyValidator", ReasonConflict, fmt.Errorf("Volume.spec.role is immutable")) }
		if oldObj.Spec.ImageRef != newObj.Spec.ImageRef { return Reject("FieldPolicyValidator", ReasonConflict, fmt.Errorf("Volume.spec.imageRef is immutable")) }
		if oldObj.Spec.ImageFilePoolRef != newObj.Spec.ImageFilePoolRef { return Reject("FieldPolicyValidator", ReasonConflict, fmt.Errorf("Volume.spec.imageFilePoolRef is immutable")) }
		if newObj.Spec.CapacityBytes < oldObj.Spec.CapacityBytes { return Reject("FieldPolicyValidator", ReasonConflict, fmt.Errorf("Volume.spec.capacityBytes cannot decrease")) }
	}
	return nil
}
```

Per-kind rules:

- VM immutable: `arch`; cold-mutable accepted: `memoryMiB`, `vcpus`, `volumeRefs`, `nicRefs`; live-mutable accepted: `powerState`.
- Volume immutable: `poolRef`, `vmRef`, `vmName`, `diskIndex`, `role`, `imageRef`, `imageFilePoolRef`; `capacityBytes` may increase only.
- NIC immutable: `networkRef`, `vmRef`, `mac`; `ip` and `hostname` are immutable for this knife as part of NIC network identity.
- Network/Image/StoragePool: entire spec immutable.

Update `PreApplyChain` to include the field policy validator after VM power validation:

```go
func PreApplyChain(st StoreReader) Chain {
	return NewChain(EnvelopeValidator{}, SpecValidator{}, ApplyOperationValidator{}, VMPowerStateValidator{}, FieldPolicyValidator{})
}
```

- [ ] **Step 3: Add tests**

Admission unit tests:

- `TestFieldPolicyRejectsVMArchChange`
- `TestFieldPolicyAllowsVMColdMutableChanges`
- `TestFieldPolicyRejectsVolumePoolRefChange`
- `TestFieldPolicyRejectsVolumeCapacityDecrease`
- `TestFieldPolicyAllowsVolumeCapacityIncrease`
- `TestFieldPolicyRejectsNICMACChange`
- `TestFieldPolicyRejectsNetworkSpecChange`
- `TestFieldPolicyRejectsImageSpecChange`
- `TestFieldPolicyRejectsStoragePoolSpecChange`

Handler tests:

- `TestApplyRejectsImmutableVMArchUpdate`
- `TestApplyAllowsColdMutableVMMemoryUpdate`
- `TestApplyRejectsVolumeCapacityDecrease`

- [ ] **Step 4: Verify and commit**

Run:

```bash
gofmt -w internal/controlplane/apiserver
go test -count=1 ./internal/controlplane/apiserver/admission ./internal/controlplane/apiserver
```

Expected: PASS.

Commit:

```bash
git add internal/controlplane/apiserver
git commit -m "feat(admission): validate update field policies"
```

---

### Task 6: Apply reference integrity validators

**Files:**
- Create: `internal/controlplane/apiserver/admission/references.go`
- Test: `internal/controlplane/apiserver/admission/references_test.go`
- Test: `internal/controlplane/apiserver/handler_apply_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: apply rejects missing references and references to deletion-marked objects, closing the Knife 1 deletion window.

Acceptance evidence:
- Applying a NIC referencing a deletion-marked Network returns 409.
- Applying a VM referencing a missing Volume returns 400.
- Applying a Volume referencing a missing VM UID returns 400.

- [ ] **Step 2: Implement reference helpers using StoreReader**

`StoreReader` was defined in Task 2 so registry constructors compile. In `references.go`, use that interface and add reference helpers:

```go
type ReferenceValidator struct { Store StoreReader }
func (v ReferenceValidator) Name() string { return "ReferenceValidator" }
```

Implement helpers:

```go
func loadMetadata(ctx context.Context, st StoreReader, kind metav1.Kind, name string) (metav1.ObjectMeta, bool, error)
func findVMByUID(ctx context.Context, st StoreReader, uid string) (metav1.ObjectMeta, bool, error)
```

`loadMetadata` returns `(meta,false,nil)` for `store.ErrNotFound`; other store errors wrap as internal.

- [ ] **Step 3: Implement reference checks**

Reference checks:

- `Volume`: `poolRef` StoragePool by name; root volume `imageRef` Image by name; root volume `imageFilePoolRef` StoragePool by name; `vmRef` VM by UID via list.
- `NIC`: `networkRef` Network by name; `vmRef` VM by UID via list.
- `VM`: every `volumeRefs` Volume by name; every `nicRefs` NIC by name.
- `Image`: `filePoolRef` StoragePool by name.
- `StoragePool` and `Network`: no upstream references to validate.

Missing reference → `ReasonBadRequest`. Reference with `DeletionTimestamp != ""` → `ReasonConflict`.

Update `PreApplyChain` so reference validation runs after field policy validation:

```go
func PreApplyChain(st StoreReader) Chain {
	return NewChain(EnvelopeValidator{}, SpecValidator{}, ApplyOperationValidator{}, VMPowerStateValidator{}, FieldPolicyValidator{}, ReferenceValidator{Store: st})
}
```

- [ ] **Step 4: Add tests**

Admission tests:

- `TestReferenceValidatorRejectsMissingVMVolumeRef`
- `TestReferenceValidatorRejectsDeletingNetworkRef`
- `TestReferenceValidatorRejectsMissingVolumeVMUID`
- `TestReferenceValidatorAllowsReadyReferenceGraph`

Handler tests:

- apply a Network, stamp its deletionTimestamp directly in fake store, then apply NIC referencing it; expect 409.
- apply VM referencing missing Volume; expect 400.

- [ ] **Step 5: Verify and commit**

Run:

```bash
gofmt -w internal/controlplane/apiserver
go test -count=1 ./internal/controlplane/apiserver/admission ./internal/controlplane/apiserver
```

Expected: PASS.

Commit:

```bash
git add internal/controlplane/apiserver
git commit -m "feat(admission): reject invalid object references"
```

---

### Task 7: DELETE admission and reverse reference migration

**Files:**
- Modify/Create: `internal/controlplane/apiserver/admission/references.go`
- Modify: `internal/controlplane/apiserver/handler_delete.go`
- Modify/Delete: `internal/controlplane/apiserver/reference_guard.go`
- Test: `internal/controlplane/apiserver/admission/references_test.go`
- Test: `internal/controlplane/apiserver/handler_delete_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: DELETE uses admission for protective reverse-reference validation, including VM references by UID.

Acceptance evidence:
- Deleting a VM referenced by Volume/NIC `vmRef` returns 409.
- Existing delete guard tests still pass.
- `reference_guard.go` is removed or no longer owns policy.

- [ ] **Step 2: Implement `DeleteReferenceValidator`**

Use the same projections currently in `reference_guard.go`, moved into admission. Add VM reverse refs that Knife 1 intentionally skipped:

```go
case metav1.KindVM:
	// list Volume and NIC; compare spec.vmRef to the VM object's UID
```

For delete request, the handler must pass the old typed object in `OldObject` so the VM validator has the VM UID.

Update `DeleteChain`:

```go
func DeleteChain(st StoreReader) Chain {
	return NewChain(DeleteReferenceValidator{Store: st})
}
```

- [ ] **Step 3: Wire delete handler**

In `handler_delete.go`, replace `s.guardNotReferenced(ctx, kind, name)` with:

```go
req := admission.Request{
	Operation: admission.OperationDelete,
	Kind: kind,
	Name: name,
	OldRaw: raw.Value,
	OldObject: oldTypedObject,
}
if err := admission.DeleteChain(s.store).Validate(ctx, req); err != nil {
	return admissionToAPIError(err)
}
```

The handler still owns 404 for missing object, deletionTimestamp stamping, CAS Put, and finalizers-empty delete.

- [ ] **Step 4: Add tests**

Admission tests:

- `TestDeleteReferenceValidatorRejectsStoragePoolReferencedByImageFilePool`
- `TestDeleteReferenceValidatorRejectsVMReferencedByVolumeUID`
- `TestDeleteReferenceValidatorRejectsVMReferencedByNICUID`
- `TestDeleteReferenceValidatorAllowsUnreferencedVM`

Handler tests:

- `TestDeleteRejectsVMReferencedByVolume`
- existing StoragePool/Image/Network/Volume/NIC delete guard tests continue passing.

- [ ] **Step 5: Verify and commit**

Run:

```bash
gofmt -w internal/controlplane/apiserver
go test -count=1 ./internal/controlplane/apiserver/admission ./internal/controlplane/apiserver
```

Expected: PASS.

Commit:

```bash
git add internal/controlplane/apiserver
git commit -m "feat(admission): validate delete references"
```

---

### Task 8: status PATCH admission

**Files:**
- Create: `internal/controlplane/apiserver/admission/status.go`
- Modify: `internal/controlplane/apiserver/handler_status.go`
- Test: `internal/controlplane/apiserver/admission/status_test.go`
- Test: `internal/controlplane/apiserver/handler_status_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: status PATCH accepts only bare valid Status JSON and rejects malformed phase/power values or full-object bodies.

Acceptance evidence:
- `PATCH /status` with `{"phase":"bogus"}` returns 400.
- `PATCH /status` with `{"spec":{},"status":{"phase":"ready"}}` returns 400.
- Valid node status reports still pass.

- [ ] **Step 2: Implement status admission validator**

In `status.go`:

```go
type StatusPatchValidator struct{}
func (StatusPatchValidator) Name() string { return "StatusPatchValidator" }
func (StatusPatchValidator) Validate(ctx context.Context, req Request) error {
	if req.Operation != OperationStatusPatch { return nil }
	if looksLikeFullObject(req.NewRaw) {
		return Reject("StatusPatchValidator", ReasonBadRequest, fmt.Errorf("status patch body must be bare status JSON"))
	}
	status, err := Status(req.NewObject)
	if err != nil { return Reject("StatusPatchValidator", ReasonInternal, err) }
	if err := status.Validate(); err != nil {
		return Reject("StatusPatchValidator", ReasonBadRequest, err)
	}
	meta, err := Metadata(req.OldObject)
	if err != nil { return Reject("StatusPatchValidator", ReasonInternal, err) }
	if meta.DeletionTimestamp != "" && len(meta.Finalizers) == 0 {
		return Reject("StatusPatchValidator", ReasonConflict, fmt.Errorf("object is finalizing"))
	}
	return nil
}
```

`looksLikeFullObject` should unmarshal into `map[string]json.RawMessage` and return true when `spec`, `metadata`, `apiVersion`, or `kind` keys are present.

Update `StatusPatchChain`:

```go
func StatusPatchChain() Chain {
	return NewChain(StatusPatchValidator{})
}
```

- [ ] **Step 3: Wire status handler**

Before `mergeStatus`, decode `body` into the kind-specific Status object and construct request:

```go
req := admission.Request{
	Operation: admission.OperationStatusPatch,
	Subresource: admission.SubresourceStatus,
	Kind: kind,
	Name: name,
	OldRaw: raw.Value,
	OldObject: storedTypedObject,
	NewRaw: body,
	NewObject: decodedStatus,
}
```

`decodedStatus` is the bare typed Status struct for the kind, such as `vmv1.VMStatus` or `volumev1.VolumeStatus`. `admission.Status()` from Task 3 must support bare Status structs.

- [ ] **Step 4: Add tests**

Admission tests:

- `TestStatusPatchValidatorAcceptsBareVMStatus`
- `TestStatusPatchValidatorRejectsFullObjectBody`
- `TestStatusPatchValidatorRejectsInvalidPhase`
- `TestStatusPatchValidatorRejectsInvalidVMPowerFields`

Handler tests:

- `TestPatchStatusRejectsInvalidVMPhase`
- `TestPatchStatusRejectsFullObjectBody`
- existing status CAS tests continue passing.

- [ ] **Step 5: Verify and commit**

Run:

```bash
gofmt -w internal/controlplane/apiserver
go test -count=1 ./internal/controlplane/apiserver/admission ./internal/controlplane/apiserver
```

Expected: PASS.

Commit:

```bash
git add internal/controlplane/apiserver
git commit -m "feat(admission): validate status patches"
```

---

### Task 9: finalizers PATCH admission

**Files:**
- Create: `internal/controlplane/apiserver/admission/finalizers.go`
- Modify: `internal/controlplane/apiserver/handler_finalizers.go`
- Test: `internal/controlplane/apiserver/admission/finalizers_test.go`
- Test: `internal/controlplane/apiserver/handler_finalizers_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: finalizers PATCH only removes the whitelisted node teardown finalizer from deletion-marked objects.

Acceptance evidence:
- Removing `FinalizerNodeTeardown` from deletion-marked object is accepted.
- Removing non-whitelisted finalizer returns 409.
- Removing from object without deletionTimestamp returns 409.
- Body with unknown fields returns 400.

- [ ] **Step 2: Implement strict remove body decoder**

In admission, define:

```go
type FinalizerRemoveRequest struct {
	Remove metav1.Finalizer `json:"remove"`
}

func DecodeFinalizerRemove(body []byte) (FinalizerRemoveRequest, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req FinalizerRemoveRequest
	if err := dec.Decode(&req); err != nil { return FinalizerRemoveRequest{}, err }
	// Reject trailing JSON tokens (e.g. `{"remove":"x"} {}`): a strict patch
	// shape must be exactly one object, so a second Decode must report io.EOF.
	if err := dec.Decode(&struct{}{}); err != io.EOF { return FinalizerRemoveRequest{}, fmt.Errorf("finalizers patch body must be a single JSON object") }
	if req.Remove == "" { return FinalizerRemoveRequest{}, fmt.Errorf("remove is required") }
	return req, nil
}
```

- [ ] **Step 3: Implement validator**

```go
type FinalizersPatchValidator struct{}
func (FinalizersPatchValidator) Name() string { return "FinalizersPatchValidator" }
func (FinalizersPatchValidator) Validate(ctx context.Context, req Request) error {
	patch, ok := req.NewObject.(FinalizerRemoveRequest)
	if !ok { return Reject("FinalizersPatchValidator", ReasonInternal, fmt.Errorf("new object type %T", req.NewObject)) }
	if patch.Remove != metav1.FinalizerNodeTeardown {
		return Reject("FinalizersPatchValidator", ReasonConflict, fmt.Errorf("finalizer %q is not removable", patch.Remove))
	}
	meta, err := Metadata(req.OldObject)
	if err != nil { return Reject("FinalizersPatchValidator", ReasonInternal, err) }
	if meta.DeletionTimestamp == "" {
		return Reject("FinalizersPatchValidator", ReasonConflict, fmt.Errorf("object is not deletion-marked"))
	}
	return nil
}
```

Update `FinalizersPatchChain`:

```go
func FinalizersPatchChain() Chain {
	return NewChain(FinalizersPatchValidator{})
}
```

- [ ] **Step 4: Wire handler**

Use `admission.DecodeFinalizerRemove(body)` instead of local loose `json.Unmarshal`. Construct request with `OldObject` decoded from stored bytes and `NewObject` set to the remove request.

The handler still owns actual slice removal, CAS write, and final store.Delete when finalizers drain.

- [ ] **Step 5: Add tests**

Admission tests:

- `TestFinalizersPatchValidatorAllowsNodeTeardownOnDeletingObject`
- `TestFinalizersPatchValidatorRejectsNonWhitelistedFinalizer`
- `TestFinalizersPatchValidatorRejectsObjectWithoutDeletionTimestamp`
- `TestDecodeFinalizerRemoveRejectsUnknownFields`

Handler tests:

- `TestPatchFinalizersRejectsUnknownField`
- `TestPatchFinalizersRejectsNonWhitelistedFinalizer`
- `TestPatchFinalizersRejectsNotDeletingObject`

- [ ] **Step 6: Verify and commit**

Run:

```bash
gofmt -w internal/controlplane/apiserver
go test -count=1 ./internal/controlplane/apiserver/admission ./internal/controlplane/apiserver
```

Expected: PASS.

Commit:

```bash
git add internal/controlplane/apiserver
git commit -m "feat(admission): validate finalizer patches"
```

---

### Task 10: Handler cleanup and behavior drift review

**Files:**
- Modify: `internal/controlplane/apiserver/handler_apply.go`
- Modify: `internal/controlplane/apiserver/apply_admission.go`
- Modify/Delete: `internal/controlplane/apiserver/reference_guard.go`
- Modify: `internal/controlplane/apiserver/*_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: remove duplicated old validating logic from handlers after admission owns it, without changing mutation/store semantics.

Acceptance evidence:
- No handler directly calls `XxxSpec.Validate()` except through admission.
- `apply_admission.go` contains only mutation helpers or is removed.
- Existing handler tests pass with the same HTTP status codes.

- [ ] **Step 2: Delete or shrink old helpers**

Remove obsolete functions from `handler_apply.go` if they are fully replaced:

- `specValidator`
- `validateObject`
- `requireName`
- `validateNetworkAdmission` if moved into admission Spec/Network validator

Keep mutation helpers:

- `injectFinalizer`
- `applyNIC`
- `bindVM`
- `preserveVMUpdateMetadata` unless deliberately kept as VM mutation helper

- [ ] **Step 3: Confirm no policy duplication**

Use content search (the project's Grep tool, or `rg`) for these patterns scoped to `internal/controlplane/apiserver`:

- `.Spec.Validate`
- `validateObject`
- `requireName`
- `validateNetworkAdmission`

Expected: direct validating policy appears only under `internal/controlplane/apiserver/admission` or in tests.

- [ ] **Step 4: Run handler regression suite**

Run:

```bash
go test -count=1 ./internal/controlplane/apiserver
go test -race -count=1 ./internal/controlplane/apiserver/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/controlplane/apiserver
git commit -m "refactor(apiserver): centralize validating policy in admission"
```

---

### Task 11: Full verification and final review

**Files:**
- Review all changed files.

- [ ] **Step 1: Inspect final diff**

Run:

```bash
git status --short
git diff --stat main...HEAD
git diff --check main...HEAD
```

Expected: clean status except intended branch changes; diff check has no output.

- [ ] **Step 2: Run local CI equivalent**

Run:

```bash
scripts/verify.sh
```

Expected: PASS.

- [ ] **Step 3: Run race tests**

Run:

```bash
go test -race ./internal/controlplane/apiserver/...
go test -race ./...
```

Expected: PASS.

- [ ] **Step 4: Run real distributed e2e**

Run:

```bash
scripts/e2e.sh full
```

Expected: `TestDistributedSpineClosure` PASS. Existing Off→On→Shutdown→Off and reverse teardown behavior must remain unchanged.

- [ ] **Step 5: Spec coverage checklist**

Confirm each spec requirement has implementation evidence:

- all mutating entrypoints call admission;
- Apply includes spec validation;
- Apply has pre/post mutation validating stages;
- immutable updates reject;
- cold-mutable updates are accepted by apiserver;
- references to missing/deleting objects reject with the specified reason;
- DELETE guard lives in admission;
- status PATCH validates bare Status JSON;
- finalizers PATCH enforces whitelist and deletion precondition;
- no Mutating Admission framework exists.

- [ ] **Step 6: Request final code review**

Dispatch a code-review subagent with base SHA and HEAD SHA. Fix Critical/Important findings before merge.

- [ ] **Step 7: Commit any validation fixes**

If final review requires fixes, commit them with focused messages such as:

```bash
git commit -m "fix(admission): reject stale finalizer patch shapes"
```

---

## Execution notes

- Execute in an isolated worktree.
- Recommended mode: subagent-driven development, one fresh subagent per task, with main-agent review after every commit.
- Do not introduce a Mutating Admission framework in this knife.
- Do not implement Volume resize, VM argv rebuild, hardware add/remove execution, or Job spec.
- If a task finds a real spec mismatch, stop and bring it back for design correction instead of silently changing the product contract.
