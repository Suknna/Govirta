package firmware

import "testing"

func TestBIOSArgReturnsPath(t *testing.T) {
	const path = "/usr/share/edk2/aarch64/QEMU_EFI.fd"

	arg, err := (BIOS{Path: path}).Arg()
	if err != nil {
		t.Fatalf("Arg() returned error: %v", err)
	}
	if arg != path {
		t.Fatalf("Arg() = %q, want %q", arg, path)
	}
}

func TestBIOSArgRejectsEmptyPath(t *testing.T) {
	_, err := (BIOS{}).Arg()
	if err == nil {
		t.Fatal("Arg() returned nil error")
	}
}

func TestBIOSArgRejectsLeadingDashPath(t *testing.T) {
	_, err := (BIOS{Path: "-option-like"}).Arg()
	if err == nil {
		t.Fatal("Arg() returned nil error")
	}
}
