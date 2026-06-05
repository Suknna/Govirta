package argv

import (
	"errors"
	"testing"

	imgexec "github.com/suknna/govirta/pkg/virt/qemuimg/internal/exec"
)

func TestPathOperandAcceptsNormalPath(t *testing.T) {
	got, err := PathOperand("path", "disk.qcow2")
	if err != nil {
		t.Fatalf("PathOperand() error = %v", err)
	}
	if got != "disk.qcow2" {
		t.Fatalf("PathOperand() = %q", got)
	}
}

func TestPathOperandRejectsBlankPath(t *testing.T) {
	_, err := PathOperand("path", " \t\n")
	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("PathOperand() error = %v, want ErrInvalidRequest", err)
	}
}

func TestPathOperandRejectsLeadingDash(t *testing.T) {
	_, err := PathOperand("path", "--help")
	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("PathOperand() error = %v, want ErrInvalidRequest", err)
	}
}
