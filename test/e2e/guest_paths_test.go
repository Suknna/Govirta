//go:build e2e

package e2e

import "testing"

func TestGuestQcowPath(t *testing.T) {
	got := guestQcowPath("/var/lib/govirta/block", "pool-block", "vm-e2e-001", "vm-e2e", 0)
	want := "/var/lib/govirta/block/pool/pool-block/vm-e2e-001/vm-e2e-disk-0.qcow2"
	if got != want {
		t.Fatalf("guestQcowPath = %q, want %q", got, want)
	}
}

func TestGuestTAPNameMatchesIdentity(t *testing.T) {
	// 复用 identity 包，验证封装调用对了（identity 自身有派生正确性单测）。
	got := guestTAPName("vm-e2e-001", 0)
	if got == "" || got[:2] != "gv" {
		t.Fatalf("guestTAPName = %q, want gv-prefixed name", got)
	}
}
