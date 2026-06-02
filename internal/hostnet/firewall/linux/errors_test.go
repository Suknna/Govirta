//go:build linux

package linux

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

func TestTranslateErrorMapsPermissionAndPreservesCause(t *testing.T) {
	err := translateError("flush nftables", os.ErrPermission)
	if !errors.Is(err, firewallerr.ErrPermission) {
		t.Fatalf("translateError error = %v, want class %v", err, firewallerr.ErrPermission)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("translateError error = %v, want preserved cause %v", err, os.ErrPermission)
	}
}

func TestTranslateErrorMapsNotExistAndPreservesCause(t *testing.T) {
	err := translateError("get nftables rule", os.ErrNotExist)
	if !errors.Is(err, firewallerr.ErrNotFound) {
		t.Fatalf("translateError error = %v, want class %v", err, firewallerr.ErrNotFound)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("translateError error = %v, want preserved cause %v", err, os.ErrNotExist)
	}
}

func TestTranslateErrorKeepsFirewallConflictDetectable(t *testing.T) {
	err := translateError("add nftables rule", firewallerr.ErrConflict)
	if !errors.Is(err, firewallerr.ErrConflict) {
		t.Fatalf("translateError error = %v, want class %v", err, firewallerr.ErrConflict)
	}
}

func TestTranslateErrorDoesNotMisclassifyUnknownError(t *testing.T) {
	cause := fmt.Errorf("kernel rejected nftables batch")
	err := translateError("flush nftables", cause)
	if !errors.Is(err, cause) {
		t.Fatalf("translateError error = %v, want preserved cause %v", err, cause)
	}
	for _, class := range []error{
		firewallerr.ErrInvalidRequest,
		firewallerr.ErrInvalidObservedState,
		firewallerr.ErrNotFound,
		firewallerr.ErrAlreadyExists,
		firewallerr.ErrConflict,
		firewallerr.ErrPermission,
		firewallerr.ErrIncompleteList,
		firewallerr.ErrUnsupported,
	} {
		if errors.Is(err, class) {
			t.Fatalf("translateError error = %v, unexpectedly matched class %v", err, class)
		}
	}
}
