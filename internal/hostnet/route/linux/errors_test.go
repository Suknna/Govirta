//go:build linux

package linux

import (
	"errors"
	"syscall"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
	"github.com/vishvananda/netlink"
)

func TestTranslateErrorMapsLinuxErrors(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class error
	}{
		{name: "permission", err: syscall.EPERM, class: routeerr.ErrPermission},
		{name: "already exists", err: syscall.EEXIST, class: routeerr.ErrAlreadyExists},
		{name: "not found", err: syscall.ESRCH, class: routeerr.ErrNotFound},
		{name: "dump interrupted", err: netlink.ErrDumpInterrupted, class: routeerr.ErrIncompleteList},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := translateError("test operation", tt.err)
			if !errors.Is(err, tt.class) {
				t.Fatalf("translateError error = %v, want class %v", err, tt.class)
			}
			if !errors.Is(err, tt.err) {
				t.Fatalf("translateError error = %v, want preserved cause %v", err, tt.err)
			}
		})
	}
}
