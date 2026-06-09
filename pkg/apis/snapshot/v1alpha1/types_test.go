package v1alpha1

import (
	"encoding/json"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

func TestSnapshotSpecValidate(t *testing.T) {
	if err := (SnapshotSpec{VMRef: "vm-a"}).Validate(); err != nil {
		t.Fatalf("valid spec: %v", err)
	}
	if err := (SnapshotSpec{}).Validate(); err == nil {
		t.Fatal("empty vmRef must be rejected")
	}
}

func TestSnapshotPhaseValid(t *testing.T) {
	for _, p := range []SnapshotPhase{SnapshotPhasePending, SnapshotPhaseReady, SnapshotPhaseDeleting, SnapshotPhaseFailed} {
		if !p.Valid() {
			t.Fatalf("%q must be valid", p)
		}
	}
	if SnapshotPhase("bogus").Valid() {
		t.Fatal("bogus phase must be invalid")
	}
}

func TestDiskSnapshotStateValid(t *testing.T) {
	if !DiskSnapshotStateCreated.Valid() || !DiskSnapshotStateFailed.Valid() {
		t.Fatal("known states must be valid")
	}
	if DiskSnapshotState("bogus").Valid() {
		t.Fatal("bogus state must be invalid")
	}
}

func TestSnapshotStatusValidate(t *testing.T) {
	ok := SnapshotStatus{Phase: SnapshotPhaseReady, DiskSnapshots: []DiskSnapshotResult{{VolumeRef: "v0", Result: DiskSnapshotStateCreated}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid status: %v", err)
	}
	if err := (SnapshotStatus{Phase: "bogus"}).Validate(); err == nil {
		t.Fatal("bogus phase must be rejected")
	}
	bad := SnapshotStatus{Phase: SnapshotPhaseReady, DiskSnapshots: []DiskSnapshotResult{{VolumeRef: "v0", Result: "bogus"}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("bogus disk result must be rejected")
	}
}

func TestSnapshotJSONRoundTrip(t *testing.T) {
	in := Snapshot{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindSnapshot},
		ObjectMeta: metav1.ObjectMeta{Name: "snap-a", UID: "snap-a-001"},
		Spec:       SnapshotSpec{VMRef: "vm-a"},
		Status:     SnapshotStatus{Phase: SnapshotPhaseReady, DiskSnapshots: []DiskSnapshotResult{{VolumeRef: "v0", Result: DiskSnapshotStateCreated}}},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Snapshot
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Kind != metav1.KindSnapshot || out.Spec.VMRef != "vm-a" || out.Status.Phase != SnapshotPhaseReady {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}
