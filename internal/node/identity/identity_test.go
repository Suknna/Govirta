package identity

import (
	"strings"
	"testing"

	"github.com/suknna/govirta/pkg/hostnet/firewall"
	"github.com/suknna/govirta/pkg/hostnet/link"
)

func TestIdentityNetworkDeterministic(t *testing.T) {
	const name = "tenant-a-net"

	first := DeriveNetworkIdentity(name)
	second := DeriveNetworkIdentity(name)

	if first != second {
		t.Fatalf("DeriveNetworkIdentity(%q) is not deterministic:\nfirst:  %+v\nsecond: %+v", name, first, second)
	}

	if first.FirewallTable != firewall.TableName("govirta") {
		t.Errorf("FirewallTable = %q, want %q", first.FirewallTable, "govirta")
	}
	if want := firewall.ChainName("gv-masq-" + name); first.MasqueradeChain != want {
		t.Errorf("MasqueradeChain = %q, want %q", first.MasqueradeChain, want)
	}
	if want := firewall.ChainName("gv-fwd-" + name); first.ForwardChain != want {
		t.Errorf("ForwardChain = %q, want %q", first.ForwardChain, want)
	}
	if want := firewall.RuleOwner("govirta/" + name); first.RuleOwner != want {
		t.Errorf("RuleOwner = %q, want %q", first.RuleOwner, want)
	}
}

func TestIdentityNetworkPriorities(t *testing.T) {
	got := DeriveNetworkIdentity("net")

	wantMasq := firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT)
	if got.MasqueradePriority != wantMasq {
		t.Errorf("MasqueradePriority = %+v, want %+v", got.MasqueradePriority, wantMasq)
	}
	if !got.MasqueradePriority.Set {
		t.Error("MasqueradePriority.Set = false, want true")
	}

	wantFwd := firewall.ExplicitPriority(0, firewall.PriorityNameForwardFilter)
	if got.ForwardPriority != wantFwd {
		t.Errorf("ForwardPriority = %+v, want %+v", got.ForwardPriority, wantFwd)
	}
	if !got.ForwardPriority.Set {
		t.Error("ForwardPriority.Set = false, want true")
	}
}

func TestIdentityNetworkUnique(t *testing.T) {
	a := DeriveNetworkIdentity("alpha")
	b := DeriveNetworkIdentity("beta")

	if a.MasqueradeChain == b.MasqueradeChain {
		t.Errorf("distinct networks share MasqueradeChain %q", a.MasqueradeChain)
	}
	if a.ForwardChain == b.ForwardChain {
		t.Errorf("distinct networks share ForwardChain %q", a.ForwardChain)
	}
	if a.RuleOwner == b.RuleOwner {
		t.Errorf("distinct networks share RuleOwner %q", a.RuleOwner)
	}

	// The single project-owned table is intentionally shared across networks.
	if a.FirewallTable != b.FirewallTable {
		t.Errorf("FirewallTable differs across networks: %q vs %q", a.FirewallTable, b.FirewallTable)
	}
}

func TestIdentityNICDeterministic(t *testing.T) {
	const (
		uid = "vm-0123456789abcdef"
		idx = 0
	)

	first := DeriveNICIdentity(uid, idx)
	second := DeriveNICIdentity(uid, idx)

	if first != second {
		t.Fatalf("DeriveNICIdentity(%q, %d) is not deterministic:\nfirst:  %+v\nsecond: %+v", uid, idx, first, second)
	}

	if first.AntiSpoofTable != firewall.TableName("govirta") {
		t.Errorf("AntiSpoofTable = %q, want %q", first.AntiSpoofTable, "govirta")
	}
	if want := firewall.ChainName("gv-as-" + string(first.TapName)); first.AntiSpoofChain != want {
		t.Errorf("AntiSpoofChain = %q, want %q", first.AntiSpoofChain, want)
	}
	if first.VNetHeader != link.VNetHeaderEnabled {
		t.Errorf("VNetHeader = %q, want %q", first.VNetHeader, link.VNetHeaderEnabled)
	}
}

func TestIdentityNICPriority(t *testing.T) {
	got := DeriveNICIdentity("vm-1", 0)

	want := firewall.ExplicitPriority(-200, firewall.PriorityNameBridgeFilter)
	if got.AntiSpoofPriority != want {
		t.Errorf("AntiSpoofPriority = %+v, want %+v", got.AntiSpoofPriority, want)
	}
	if !got.AntiSpoofPriority.Set {
		t.Error("AntiSpoofPriority.Set = false, want true")
	}
}

func TestIdentityNICUniquePerVM(t *testing.T) {
	a := DeriveNICIdentity("vm-aaaa", 0)
	b := DeriveNICIdentity("vm-bbbb", 0)

	if a.TapName == b.TapName {
		t.Errorf("distinct VMs share TapName %q", a.TapName)
	}
	if a.AntiSpoofChain == b.AntiSpoofChain {
		t.Errorf("distinct VMs share AntiSpoofChain %q", a.AntiSpoofChain)
	}
}

func TestIdentityNICUniquePerIndex(t *testing.T) {
	zero := DeriveNICIdentity("vm-shared", 0)
	one := DeriveNICIdentity("vm-shared", 1)

	if zero.TapName == one.TapName {
		t.Errorf("distinct NIC indices share TapName %q", zero.TapName)
	}
	if zero.AntiSpoofChain == one.AntiSpoofChain {
		t.Errorf("distinct NIC indices share AntiSpoofChain %q", zero.AntiSpoofChain)
	}
}

func TestIdentityNICTapNameLength(t *testing.T) {
	cases := []struct {
		name     string
		vmUID    string
		nicIndex int
	}{
		{name: "short uid", vmUID: "a", nicIndex: 0},
		{name: "typical uuid", vmUID: "550e8400-e29b-41d4-a716-446655440000", nicIndex: 3},
		{name: "very long uid", vmUID: strings.Repeat("vm-identifier-", 64), nicIndex: 7},
		{name: "multi digit index", vmUID: "vm-x", nicIndex: 99},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveNICIdentity(tc.vmUID, tc.nicIndex)
			if l := len(string(got.TapName)); l > link.MaxInterfaceNameLength {
				t.Errorf("TapName %q length = %d, want <= %d", got.TapName, l, link.MaxInterfaceNameLength)
			}
		})
	}
}

func TestIdentityNICTapNameShape(t *testing.T) {
	got := DeriveNICIdentity("vm-shape", 2)

	tap := string(got.TapName)
	if !strings.HasPrefix(tap, "gv") {
		t.Errorf("TapName %q does not start with %q", tap, "gv")
	}
	if !strings.HasSuffix(tap, ".2") {
		t.Errorf("TapName %q does not end with NIC index suffix %q", tap, ".2")
	}
}
