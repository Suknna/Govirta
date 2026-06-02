//go:build linux

package linux

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/google/nftables"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

func TestEnsureEndpointAntiSpoofingCreatesBridgeChainFourGuardsAndFlushes(t *testing.T) {
	fh := &fakeHandle{}
	info, err := NewManagerWithHandle(fh).EnsureEndpointAntiSpoofing(context.Background(), taskEndpointAntiSpoofingSpec())
	if err != nil {
		t.Fatalf("EnsureEndpointAntiSpoofing error = %v", err)
	}

	if len(fh.tables) != 1 || fh.tables[0].Family != nftables.TableFamilyBridge || fh.tables[0].Name != "gv_filter" {
		t.Fatalf("tables = %+v, want one bridge gv_filter table", fh.tables)
	}
	if len(fh.chains) != 1 {
		t.Fatalf("chains = %+v, want one chain", fh.chains)
	}
	chain := fh.chains[0]
	if chain.Name != "forward" || chain.Type != nftables.ChainTypeFilter || chain.Hooknum != nftables.ChainHookForward || chain.Priority == nil || int(*chain.Priority) != -200 {
		t.Fatalf("chain = %+v, want forward filter base chain at bridge filter priority", chain)
	}
	if len(fh.rules) != 4 {
		t.Fatalf("rules = %d, want four endpoint guard rules", len(fh.rules))
	}
	assertEndpointGuards(t, fh.rules, map[endpointGuardKind]bool{guardEtherMAC: true, guardIPv4: true, guardARPMAC: true, guardARPIPv4: true})
	if !containsCall(fh.calls, "Flush") {
		t.Fatalf("calls = %v, want Flush", fh.calls)
	}

	wantRef := firewall.RuleRef{Owner: "govirta-test", Purpose: firewall.RulePurposeEndpointAntiSpoofing, Family: firewall.TableFamilyBridge, TableName: "gv_filter", ChainName: "forward", Handle: 1}
	if info.Ref != wantRef {
		t.Fatalf("RuleInfo.Ref = %+v, want %+v", info.Ref, wantRef)
	}
	assertEndpointSummary(t, info, taskEndpointAntiSpoofingSpec())
}

func TestEnsureEndpointAntiSpoofingListReturnsOneLogicalGroup(t *testing.T) {
	fh := &fakeHandle{}
	manager := NewManagerWithHandle(fh)
	if _, err := manager.EnsureEndpointAntiSpoofing(context.Background(), taskEndpointAntiSpoofingSpec()); err != nil {
		t.Fatalf("EnsureEndpointAntiSpoofing error = %v", err)
	}

	infos, err := manager.ListRules(context.Background(), firewall.RuleFilter{
		Owner:   firewall.FilterOwner("govirta-test"),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeEndpointAntiSpoofing),
		Family:  firewall.FilterFamily(firewall.TableFamilyBridge),
		Table:   firewall.FilterTable("gv_filter"),
		Chain:   firewall.FilterChain("forward"),
	})
	if err != nil {
		t.Fatalf("ListRules error = %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("ListRules returned %d rules, want one logical endpoint group", len(infos))
	}
	assertEndpointSummary(t, infos[0], taskEndpointAntiSpoofingSpec())
}

func TestEnsureEndpointAntiSpoofingIsIdempotent(t *testing.T) {
	fh := &fakeHandle{}
	manager := NewManagerWithHandle(fh)
	first, err := manager.EnsureEndpointAntiSpoofing(context.Background(), taskEndpointAntiSpoofingSpec())
	if err != nil {
		t.Fatalf("first EnsureEndpointAntiSpoofing error = %v", err)
	}
	fh.calls = nil

	second, err := manager.EnsureEndpointAntiSpoofing(context.Background(), taskEndpointAntiSpoofingSpec())
	if err != nil {
		t.Fatalf("second EnsureEndpointAntiSpoofing error = %v", err)
	}
	if first.Ref != second.Ref || !ruleSummaryMatchesDesired(second.Summary, first.Summary) {
		t.Fatalf("second RuleInfo = %+v, want equivalent to first %+v", second, first)
	}
	if len(fh.rules) != 4 {
		t.Fatalf("rule count = %d, want 4", len(fh.rules))
	}
	if containsCall(fh.calls, "AddRule:bridge:gv_filter:forward") || containsCall(fh.calls, "Flush") {
		t.Fatalf("calls = %v, want no mutation on idempotent ensure", fh.calls)
	}
}

func TestEnsureEndpointAntiSpoofingConflictsOnDifferentMAC(t *testing.T) {
	fh := &fakeHandle{}
	manager := NewManagerWithHandle(fh)
	if _, err := manager.EnsureEndpointAntiSpoofing(context.Background(), taskEndpointAntiSpoofingSpec()); err != nil {
		t.Fatalf("initial EnsureEndpointAntiSpoofing error = %v", err)
	}

	spec := taskEndpointAntiSpoofingSpec()
	spec.MAC = net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x20, 0x02}
	_, err := manager.EnsureEndpointAntiSpoofing(context.Background(), spec)
	if !errors.Is(err, firewallerr.ErrConflict) {
		t.Fatalf("EnsureEndpointAntiSpoofing error = %v, want %v", err, firewallerr.ErrConflict)
	}
}

func TestEnsureEndpointAntiSpoofingConflictsOnDifferentIPv4(t *testing.T) {
	fh := &fakeHandle{}
	manager := NewManagerWithHandle(fh)
	if _, err := manager.EnsureEndpointAntiSpoofing(context.Background(), taskEndpointAntiSpoofingSpec()); err != nil {
		t.Fatalf("initial EnsureEndpointAntiSpoofing error = %v", err)
	}

	spec := taskEndpointAntiSpoofingSpec()
	spec.IPv4 = netip.MustParseAddr("192.168.100.11")
	_, err := manager.EnsureEndpointAntiSpoofing(context.Background(), spec)
	if !errors.Is(err, firewallerr.ErrConflict) {
		t.Fatalf("EnsureEndpointAntiSpoofing error = %v, want %v", err, firewallerr.ErrConflict)
	}
}

func TestEnsureEndpointAntiSpoofingRecreatesOnlyMissingGuard(t *testing.T) {
	fh := &fakeHandle{}
	manager := NewManagerWithHandle(fh)
	if _, err := manager.EnsureEndpointAntiSpoofing(context.Background(), taskEndpointAntiSpoofingSpec()); err != nil {
		t.Fatalf("initial EnsureEndpointAntiSpoofing error = %v", err)
	}
	fh.rules = fh.rules[:3]
	fh.calls = nil

	if _, err := manager.EnsureEndpointAntiSpoofing(context.Background(), taskEndpointAntiSpoofingSpec()); err != nil {
		t.Fatalf("repair EnsureEndpointAntiSpoofing error = %v", err)
	}
	if len(fh.rules) != 4 {
		t.Fatalf("rule count = %d, want repaired four guards", len(fh.rules))
	}
	addCount := 0
	for _, call := range fh.calls {
		if call == "AddRule:bridge:gv_filter:forward" {
			addCount++
		}
	}
	if addCount != 1 {
		t.Fatalf("AddRule count = %d, want 1 for missing guard repair; calls = %v", addCount, fh.calls)
	}
}

func TestDeleteEndpointAntiSpoofingRemovesGroupAndLeavesOtherEndpoint(t *testing.T) {
	fh := &fakeHandle{}
	manager := NewManagerWithHandle(fh)
	first, err := manager.EnsureEndpointAntiSpoofing(context.Background(), taskEndpointAntiSpoofingSpec())
	if err != nil {
		t.Fatalf("first EnsureEndpointAntiSpoofing error = %v", err)
	}
	other := taskEndpointAntiSpoofingSpec()
	other.TapName = "gv-tap1"
	other.MAC = net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x10, 0x02}
	other.IPv4 = netip.MustParseAddr("192.168.100.11")
	if _, err := manager.EnsureEndpointAntiSpoofing(context.Background(), other); err != nil {
		t.Fatalf("other EnsureEndpointAntiSpoofing error = %v", err)
	}

	if err := manager.DeleteEndpointAntiSpoofing(context.Background(), first.Ref); err != nil {
		t.Fatalf("DeleteEndpointAntiSpoofing error = %v", err)
	}
	infos, err := manager.ListRules(context.Background(), firewall.RuleFilter{
		Owner:   firewall.FilterOwner("govirta-test"),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeEndpointAntiSpoofing),
		Family:  firewall.FilterFamily(firewall.TableFamilyBridge),
		Table:   firewall.FilterTable("gv_filter"),
		Chain:   firewall.FilterChain("forward"),
	})
	if err != nil {
		t.Fatalf("ListRules after delete error = %v", err)
	}
	if len(infos) != 1 || infos[0].Summary.EndpointAntiSpoofing == nil || infos[0].Summary.EndpointAntiSpoofing.TapName != "gv-tap1" {
		t.Fatalf("remaining endpoint infos = %+v, want only gv-tap1", infos)
	}
}

func TestEnsureEndpointAntiSpoofingCanceledContextRecordsNoHandleCalls(t *testing.T) {
	fh := &fakeHandle{}
	_, err := NewManagerWithHandle(fh).EnsureEndpointAntiSpoofing(canceledContext(), taskEndpointAntiSpoofingSpec())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureEndpointAntiSpoofing error = %v, want %v", err, context.Canceled)
	}
	assertNoHandleCalls(t, fh)
}

func taskEndpointAntiSpoofingSpec() firewall.EndpointAntiSpoofingSpec {
	return firewall.EndpointAntiSpoofingSpec{
		TableName:  "gv_filter",
		ChainName:  "forward",
		RuleOwner:  "govirta-test",
		BridgeName: "govirta0",
		TapName:    "gv-tap0",
		MAC:        net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x10, 0x01},
		IPv4:       netip.MustParseAddr("192.168.100.10"),
		Priority:   firewall.ExplicitPriority(-200, firewall.PriorityNameBridgeFilter),
	}
}

func assertEndpointGuards(t *testing.T, rules []*nftables.Rule, want map[endpointGuardKind]bool) {
	t.Helper()
	got := map[endpointGuardKind]bool{}
	for _, rule := range rules {
		metadata, ok, err := parseRuleUserData(rule.UserData)
		if err != nil || !ok {
			t.Fatalf("parseRuleUserData(%q) = metadata=%+v ok=%v err=%v", string(rule.UserData), metadata, ok, err)
		}
		got[metadata.guard] = true
	}
	for guard := range want {
		if !got[guard] {
			t.Fatalf("guard %q missing from rules; got=%v", guard, got)
		}
	}
}

func assertEndpointSummary(t *testing.T, info firewall.RuleInfo, spec firewall.EndpointAntiSpoofingSpec) {
	t.Helper()
	summary := info.Summary.EndpointAntiSpoofing
	if info.Ref.Purpose != firewall.RulePurposeEndpointAntiSpoofing || summary == nil {
		t.Fatalf("RuleInfo = %+v, want endpoint anti-spoofing summary", info)
	}
	if summary.BridgeName != spec.BridgeName || summary.TapName != spec.TapName || summary.MAC.String() != spec.MAC.String() || summary.IPv4 != spec.IPv4 || summary.Priority != spec.Priority {
		t.Fatalf("EndpointAntiSpoofingSummary = %+v, want bridge=%s tap=%s mac=%s ip=%s priority=%+v", summary, spec.BridgeName, spec.TapName, spec.MAC, spec.IPv4, spec.Priority)
	}
}
