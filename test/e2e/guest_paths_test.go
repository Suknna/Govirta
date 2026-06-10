//go:build e2e

package e2e

import (
	"path/filepath"
	"testing"

	"github.com/suknna/govirta/internal/node/identity"
)

func TestGuestQcowPath(t *testing.T) {
	// storageRoot 复用权威常量；want 独立硬编码，常量本身写错时仍能抓到。
	got := guestQcowPath(guestBlockStorageRoot, "pool-block", "vm-e2e-001", "vm-e2e", 0)
	want := "/var/lib/govirta/block/pool/pool-block/vm-e2e-001/vm-e2e-disk-0.qcow2"
	if got != want {
		t.Fatalf("guestQcowPath = %q, want %q", got, want)
	}
}

func TestGuestTAPNameMatchesIdentity(t *testing.T) {
	// 等值断言：钉死封装确实委托给 identity.DeriveNICIdentity().TapName。
	got := guestTAPName("vm-e2e-001", 0)
	want := string(identity.DeriveNICIdentity("vm-e2e-001", 0).TapName)
	if got != want {
		t.Fatalf("guestTAPName = %q, want %q", got, want)
	}
}

func TestGuestAntiSpoofChainMatchesIdentity(t *testing.T) {
	got := guestAntiSpoofChain("vm-e2e-001", 0)
	want := string(identity.DeriveNICIdentity("vm-e2e-001", 0).AntiSpoofChain)
	if got != want {
		t.Fatalf("guestAntiSpoofChain = %q, want %q", got, want)
	}
}

func TestGuestMasqueradeChainMatchesIdentity(t *testing.T) {
	got := guestMasqueradeChain("net-e2e")
	want := string(identity.DeriveNetworkIdentity("net-e2e").MasqueradeChain)
	if got != want {
		t.Fatalf("guestMasqueradeChain = %q, want %q", got, want)
	}
}

func TestGuestForwardChainMatchesIdentity(t *testing.T) {
	got := guestForwardChain("net-e2e")
	want := string(identity.DeriveNetworkIdentity("net-e2e").ForwardChain)
	if got != want {
		t.Fatalf("guestForwardChain = %q, want %q", got, want)
	}
}

func TestGuestRuntimeDir(t *testing.T) {
	got := guestRuntimeDir("vm-e2e-001")
	want := filepath.Join(guestRuntimeRoot, "vm-e2e-001")
	if got != want {
		t.Fatalf("guestRuntimeDir = %q, want %q", got, want)
	}
}
