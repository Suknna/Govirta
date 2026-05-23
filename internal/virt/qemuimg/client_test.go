package qemuimg

import (
	"errors"
	"testing"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

func TestNewClientDefaultsBinary(t *testing.T) {
	client := NewClient(Config{})

	if got := client.QCOW2().Binary(); got != "qemu-img" {
		t.Fatalf("QCOW2().Binary() = %q, want %q", got, "qemu-img")
	}
}

func TestNewClientUsesConfiguredBinary(t *testing.T) {
	client := NewClient(Config{Binary: "/usr/bin/qemu-img"})

	if got := client.QCOW2().Binary(); got != "/usr/bin/qemu-img" {
		t.Fatalf("QCOW2().Binary() = %q, want %q", got, "/usr/bin/qemu-img")
	}
}

func TestErrInvalidRequestAliasesExecBoundary(t *testing.T) {
	err := imgexec.InvalidRequest("path is required")

	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("errors.Is(%v, qemuimg.ErrInvalidRequest) = false, want true", err)
	}
}

func TestQCOW2ReturnsCommandBuilders(t *testing.T) {
	qcow2 := NewClient(Config{}).QCOW2()

	if qcow2.Create() == nil {
		t.Fatalf("Create() = nil, want builder")
	}
	if qcow2.Info() == nil {
		t.Fatalf("Info() = nil, want builder")
	}
	if qcow2.Convert() == nil {
		t.Fatalf("Convert() = nil, want builder")
	}
	if qcow2.Snapshot() == nil {
		t.Fatalf("Snapshot() = nil, want builder")
	}
	if qcow2.Check() == nil {
		t.Fatalf("Check() = nil, want builder")
	}
	if qcow2.Remove() == nil {
		t.Fatalf("Remove() = nil, want builder")
	}
}
