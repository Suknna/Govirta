package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/internal/vmm"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

type fakeColdReader struct {
	phase vmm.Phase
	err   error
}

func (f fakeColdReader) Status(ctx context.Context, uuid string) (vmm.VM, error) {
	if f.err != nil {
		return vmm.VM{}, f.err
	}
	return vmm.VM{Phase: f.phase}, nil
}

func TestVMIsCold(t *testing.T) {
	vm := vmv1.VM{}
	vm.Name = "vm-x"
	vm.UID = "uid-x"

	cases := []struct {
		name     string
		reader   fakeColdReader
		wantCold bool
		wantErr  bool
	}{
		{"stopped is cold", fakeColdReader{phase: vmm.PhaseStopped}, true, false},
		{"defined is cold", fakeColdReader{phase: vmm.PhaseDefined}, true, false},
		{"runtime absent is cold", fakeColdReader{err: vmm.ErrNotFound}, true, false},
		{"running not cold", fakeColdReader{phase: vmm.PhaseRunning}, false, false},
		{"failed not cold", fakeColdReader{phase: vmm.PhaseFailed}, false, false},
		{"read error propagates", fakeColdReader{err: errors.New("boom")}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cold, err := vmIsCold(context.Background(), tc.reader, vm)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if cold != tc.wantCold {
				t.Fatalf("cold = %v, want %v", cold, tc.wantCold)
			}
		})
	}
}
