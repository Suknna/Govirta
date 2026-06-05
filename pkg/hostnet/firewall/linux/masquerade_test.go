//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"testing"

	"github.com/google/nftables"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
	"github.com/suknna/govirta/pkg/hostnet/firewall/firewallerr"
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

// Issue 2: more than one Govirta-owned rule for the same
// owner/purpose/family/table/chain is inconsistent managed state. The ensure
// path must report conflict and must not auto-delete any rule.
func TestEnsureMasqueradeConflictsOnDuplicateManagedRules(t *testing.T) {
	table := &nftables.Table{Family: nftables.TableFamilyIPv4, Name: "gv_nat"}
	priority := nftables.ChainPriority(100)
	chain := &nftables.Chain{Table: table, Name: "postrouting", Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPostrouting, Priority: &priority}
	fh := &fakeHandle{
		tables: []*nftables.Table{table},
		chains: []*nftables.Chain{chain},
		rules: []*nftables.Rule{
			masqueradeRule(table, chain, "govirta-test", 1, netip.MustParsePrefix("192.168.100.0/24"), "eth0"),
			masqueradeRule(table, chain, "govirta-test", 2, netip.MustParsePrefix("192.168.100.0/24"), "eth0"),
		},
	}

	_, err := NewManagerWithHandle(fh).EnsureMasquerade(context.Background(), taskMasqueradeSpec())
	if !errors.Is(err, firewallerr.ErrConflict) {
		t.Fatalf("EnsureMasquerade error = %v, want %v", err, firewallerr.ErrConflict)
	}
	if len(fh.rules) != 2 {
		t.Fatalf("rule count = %d, want 2 (ensure path must not auto-delete)", len(fh.rules))
	}
	if containsCall(fh.calls, "AddRule:ip:gv_nat:postrouting") || containsCall(fh.calls, "Flush") {
		t.Fatalf("calls = %v, want no mutation on duplicate conflict", fh.calls)
	}
}

// Issue 1: an existing chain with the requested name but wrong base-chain
// semantics (type/hook/priority) must not be silently reused.
func TestEnsureMasqueradeConflictsWithIncompatibleExistingChain(t *testing.T) {
	srcnat := nftables.ChainPriority(100)
	wrong := nftables.ChainPriority(0)
	cases := []struct {
		name      string
		buildHook func() *nftables.ChainHook
		typ       nftables.ChainType
		priority  *nftables.ChainPriority
	}{
		{name: "wrong type", buildHook: func() *nftables.ChainHook { return nftables.ChainHookPostrouting }, typ: nftables.ChainTypeFilter, priority: &srcnat},
		{name: "wrong hook", buildHook: func() *nftables.ChainHook { return nftables.ChainHookForward }, typ: nftables.ChainTypeNAT, priority: &srcnat},
		{name: "nil hook", buildHook: func() *nftables.ChainHook { return nil }, typ: nftables.ChainTypeNAT, priority: &srcnat},
		{name: "wrong priority", buildHook: func() *nftables.ChainHook { return nftables.ChainHookPostrouting }, typ: nftables.ChainTypeNAT, priority: &wrong},
		{name: "nil priority", buildHook: func() *nftables.ChainHook { return nftables.ChainHookPostrouting }, typ: nftables.ChainTypeNAT, priority: nil},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			table := &nftables.Table{Family: nftables.TableFamilyIPv4, Name: "gv_nat"}
			chain := &nftables.Chain{Table: table, Name: "postrouting", Type: tt.typ, Hooknum: tt.buildHook(), Priority: tt.priority}
			fh := &fakeHandle{tables: []*nftables.Table{table}, chains: []*nftables.Chain{chain}}

			_, err := NewManagerWithHandle(fh).EnsureMasquerade(context.Background(), taskMasqueradeSpec())
			if !errors.Is(err, firewallerr.ErrConflict) {
				t.Fatalf("EnsureMasquerade error = %v, want %v", err, firewallerr.ErrConflict)
			}
			if len(fh.rules) != 0 {
				t.Fatalf("rule count = %d, want 0 (no rule added on chain conflict)", len(fh.rules))
			}
			if containsCall(fh.calls, "AddChain:ip:gv_nat:postrouting") || containsCall(fh.calls, "Flush") {
				t.Fatalf("calls = %v, want no chain creation or flush on chain conflict", fh.calls)
			}
		})
	}
}

// Issue 3: an observation read that fails after a successful Flush must
// propagate as invalid observed state and must not claim success.
func TestEnsureMasqueradePropagatesPostFlushObservationFailure(t *testing.T) {
	observeErr := errors.New("observation read failed")
	fh := &fakeHandle{failures: map[string]error{"GetRules": observeErr}}

	_, err := NewManagerWithHandle(fh).EnsureMasquerade(context.Background(), taskMasqueradeSpec())
	if !errors.Is(err, observeErr) {
		t.Fatalf("EnsureMasquerade error = %v, want observation cause %v", err, observeErr)
	}
	if !errors.Is(err, firewallerr.ErrInvalidObservedState) {
		t.Fatalf("EnsureMasquerade error = %v, want %v", err, firewallerr.ErrInvalidObservedState)
	}
	if !containsCall(fh.calls, "Flush") {
		t.Fatalf("calls = %v, want Flush before observation failure", fh.calls)
	}
}

// Issue 3: a successful Flush whose effect is not observable afterward must
// return invalid observed state rather than a false success.
func TestEnsureMasqueradeReturnsInvalidObservedStateWhenRuleNotObserved(t *testing.T) {
	fh := &fakeHandle{}
	h := &ruleDroppingHandle{fakeHandle: fh}

	_, err := NewManagerWithHandle(h).EnsureMasquerade(context.Background(), taskMasqueradeSpec())
	if !errors.Is(err, firewallerr.ErrInvalidObservedState) {
		t.Fatalf("EnsureMasquerade error = %v, want %v", err, firewallerr.ErrInvalidObservedState)
	}
	if !containsCall(fh.calls, "Flush") {
		t.Fatalf("calls = %v, want Flush before observation", fh.calls)
	}
}

// ruleDroppingHandle simulates a handle that accepts and flushes a rule add but
// whose effect is not visible to subsequent observation reads.
type ruleDroppingHandle struct {
	*fakeHandle
}

func (h *ruleDroppingHandle) AddRule(rule *nftables.Rule) *nftables.Rule {
	h.record(fmt.Sprintf("AddRule:%s:%s:%s", nftFamilyName(rule.Table.Family), rule.Table.Name, rule.Chain.Name))
	return rule
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
