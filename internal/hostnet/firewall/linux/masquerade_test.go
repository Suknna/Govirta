//go:build linux

package linux

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/google/nftables"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

func TestEnsureMasqueradeCreatesTableChainRuleAndFlushes(t *testing.T) {
	fh := &fakeHandle{}
	info, err := NewManagerWithHandle(fh).EnsureMasquerade(context.Background(), taskMasqueradeSpec())
	if err != nil {
		t.Fatalf("EnsureMasquerade error = %v", err)
	}

	if len(fh.tables) != 1 || fh.tables[0].Family != nftables.TableFamilyIPv4 || fh.tables[0].Name != "gv_nat" {
		t.Fatalf("tables = %+v, want one IPv4 gv_nat table", fh.tables)
	}
	if len(fh.chains) != 1 {
		t.Fatalf("chains = %+v, want one chain", fh.chains)
	}
	chain := fh.chains[0]
	if chain.Name != "postrouting" || chain.Type != nftables.ChainTypeNAT || chain.Hooknum != nftables.ChainHookPostrouting || chain.Priority == nil || int(*chain.Priority) != 100 {
		t.Fatalf("chain = %+v, want postrouting NAT base chain at srcnat priority", chain)
	}
	if len(fh.rules) != 1 {
		t.Fatalf("rules = %+v, want one rule", fh.rules)
	}
	assertCallSequence(t, fh.calls, []string{
		"GetTables",
		"GetChains",
		"GetTables",
		"AddTable:ip:gv_nat",
		"GetChains",
		"AddChain:ip:gv_nat:postrouting",
		"AddRule:ip:gv_nat:postrouting",
		"Flush",
		"GetTables",
		"GetChains",
		"GetRules:ip:gv_nat:postrouting",
	})

	wantRef := firewall.RuleRef{Owner: "govirta-test", Purpose: firewall.RulePurposeMasquerade, Family: firewall.TableFamilyIPv4, TableName: "gv_nat", ChainName: "postrouting", Handle: 1}
	if got := info.Ref; got != wantRef {
		t.Fatalf("RuleInfo.Ref = %+v, want %+v", got, wantRef)
	}
	wantSummary := &firewall.MasqueradeSummary{GuestCIDR: netip.MustParsePrefix("192.168.100.0/24"), EgressInterfaceName: "eth0", Priority: firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT)}
	if info.Summary.Masquerade == nil || *info.Summary.Masquerade != *wantSummary {
		t.Fatalf("RuleInfo.Summary.Masquerade = %+v, want %+v", info.Summary.Masquerade, wantSummary)
	}
}

func TestEnsureMasqueradeIsIdempotent(t *testing.T) {
	fh := &fakeHandle{}
	manager := NewManagerWithHandle(fh)
	first, err := manager.EnsureMasquerade(context.Background(), taskMasqueradeSpec())
	if err != nil {
		t.Fatalf("first EnsureMasquerade error = %v", err)
	}
	fh.calls = nil

	second, err := manager.EnsureMasquerade(context.Background(), taskMasqueradeSpec())
	if err != nil {
		t.Fatalf("second EnsureMasquerade error = %v", err)
	}
	if first.Ref != second.Ref || first.Summary.Masquerade == nil || second.Summary.Masquerade == nil || *first.Summary.Masquerade != *second.Summary.Masquerade {
		t.Fatalf("second RuleInfo = %+v, want equivalent to first %+v", second, first)
	}
	if len(fh.rules) != 1 {
		t.Fatalf("rule count = %d, want 1", len(fh.rules))
	}
	assertCallSequence(t, fh.calls, []string{"GetTables", "GetChains", "GetRules:ip:gv_nat:postrouting"})
}

func TestEnsureMasqueradeConflictsOnDifferentGuestCIDR(t *testing.T) {
	fh := &fakeHandle{}
	manager := NewManagerWithHandle(fh)
	if _, err := manager.EnsureMasquerade(context.Background(), taskMasqueradeSpec()); err != nil {
		t.Fatalf("initial EnsureMasquerade error = %v", err)
	}
	fh.calls = nil

	spec := taskMasqueradeSpec()
	spec.GuestCIDR = netip.MustParsePrefix("192.168.101.0/24")
	_, err := manager.EnsureMasquerade(context.Background(), spec)
	if !errors.Is(err, firewallerr.ErrConflict) {
		t.Fatalf("EnsureMasquerade error = %v, want %v", err, firewallerr.ErrConflict)
	}
	if len(fh.rules) != 1 {
		t.Fatalf("rule count = %d, want 1", len(fh.rules))
	}
	assertCallSequence(t, fh.calls, []string{"GetTables", "GetChains", "GetRules:ip:gv_nat:postrouting"})
}

func TestEnsureMasqueradeConflictsOnDifferentEgressInterface(t *testing.T) {
	fh := &fakeHandle{}
	manager := NewManagerWithHandle(fh)
	if _, err := manager.EnsureMasquerade(context.Background(), taskMasqueradeSpec()); err != nil {
		t.Fatalf("initial EnsureMasquerade error = %v", err)
	}
	fh.calls = nil

	spec := taskMasqueradeSpec()
	spec.EgressInterfaceName = "eth1"
	_, err := manager.EnsureMasquerade(context.Background(), spec)
	if !errors.Is(err, firewallerr.ErrConflict) {
		t.Fatalf("EnsureMasquerade error = %v, want %v", err, firewallerr.ErrConflict)
	}
	if len(fh.rules) != 1 {
		t.Fatalf("rule count = %d, want 1", len(fh.rules))
	}
	assertCallSequence(t, fh.calls, []string{"GetTables", "GetChains", "GetRules:ip:gv_nat:postrouting"})
}

func TestEnsureMasqueradeReturnsFlushFailure(t *testing.T) {
	flushErr := errors.New("flush failed")
	fh := &fakeHandle{failures: map[string]error{"Flush": flushErr}}

	_, err := NewManagerWithHandle(fh).EnsureMasquerade(context.Background(), taskMasqueradeSpec())
	if !errors.Is(err, flushErr) {
		t.Fatalf("EnsureMasquerade error = %v, want flush cause %v", err, flushErr)
	}
	if !containsCall(fh.calls, "Flush") {
		t.Fatalf("calls = %v, want Flush", fh.calls)
	}
}

func TestEnsureMasqueradeCanceledContextRecordsNoHandleCalls(t *testing.T) {
	fh := &fakeHandle{}
	_, err := NewManagerWithHandle(fh).EnsureMasquerade(canceledContext(), taskMasqueradeSpec())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureMasquerade error = %v, want %v", err, context.Canceled)
	}
	assertNoHandleCalls(t, fh)
}

func taskMasqueradeSpec() firewall.MasqueradeSpec {
	return firewall.MasqueradeSpec{
		TableName:           "gv_nat",
		ChainName:           "postrouting",
		RuleOwner:           "govirta-test",
		GuestCIDR:           netip.MustParsePrefix("192.168.100.0/24"),
		EgressInterfaceName: "eth0",
		Priority:            firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT),
	}
}

func assertCallSequence(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
