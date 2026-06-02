//go:build linux

package linux

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

func TestListRulesIgnoresNonGovirtaRules(t *testing.T) {
	fh, objects := seededRuleHandle()
	fh.rules = append(fh.rules, &nftables.Rule{Table: objects.natTable, Chain: objects.natChain, Handle: 99})

	infos, err := NewManagerWithHandle(fh).ListRules(context.Background(), validRuleFilter())
	if err != nil {
		t.Fatalf("ListRules error = %v", err)
	}

	for _, info := range infos {
		if ruleRefForInfo(info).Handle == 99 {
			t.Fatalf("ListRules returned non-Govirta rule: %+v", info)
		}
	}
}

func TestListRulesReturnsSortedGovirtaRules(t *testing.T) {
	fh, _ := seededRuleHandle()

	infos, err := NewManagerWithHandle(fh).ListRules(context.Background(), validRuleFilter())
	if err != nil {
		t.Fatalf("ListRules error = %v", err)
	}

	got := refsFromInfos(infos)
	want := []firewall.RuleRef{
		endpointRef("vm-1", 5),
		endpointRef("vm-1", 7),
		endpointRef("vm-2", 3),
		masqueradeRef("vm-1", 10),
		masqueradeRef("vm-2", 2),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListRules refs = %+v, want %+v", got, want)
	}
}

func TestListRulesFilterByOwnerPurposeFamilyTableChain(t *testing.T) {
	fh, _ := seededRuleHandle()
	filter := firewall.RuleFilter{
		Owner:   firewall.FilterOwner("vm-1"),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeMasquerade),
		Family:  firewall.FilterFamily(firewall.TableFamilyIPv4),
		Table:   firewall.FilterTable("govirta_nat"),
		Chain:   firewall.FilterChain("postrouting"),
	}

	infos, err := NewManagerWithHandle(fh).ListRules(context.Background(), filter)
	if err != nil {
		t.Fatalf("ListRules error = %v", err)
	}

	got := refsFromInfos(infos)
	want := []firewall.RuleRef{masqueradeRef("vm-1", 10)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListRules refs = %+v, want %+v", got, want)
	}
}

func TestGetRuleReturnsRuleMatchingCompactRef(t *testing.T) {
	fh, _ := seededRuleHandle()
	ref := endpointRef("vm-1", 7)

	info, err := NewManagerWithHandle(fh).GetRule(context.Background(), firewall.RuleQuery{Ref: ref})
	if err != nil {
		t.Fatalf("GetRule error = %v", err)
	}
	if got := ruleRefForInfo(info); got != ref {
		t.Fatalf("GetRule ref = %+v, want %+v", got, ref)
	}
	if info.Summary.EndpointAntiSpoofing == nil || info.Summary.EndpointAntiSpoofing.BridgeName != "br0" || info.Summary.EndpointAntiSpoofing.TapName != "tap0" {
		t.Fatalf("GetRule summary = %+v, want endpoint bridge br0 and TAP tap0", info.Summary)
	}
}

func TestGetRuleMissingHandleReturnsNotFound(t *testing.T) {
	fh, _ := seededRuleHandle()
	ref := masqueradeRef("vm-1", 404)

	_, err := NewManagerWithHandle(fh).GetRule(context.Background(), firewall.RuleQuery{Ref: ref})
	if !errors.Is(err, firewallerr.ErrNotFound) {
		t.Fatalf("GetRule error = %v, want %v", err, firewallerr.ErrNotFound)
	}
}

func TestDeleteMasqueradeMissingRuleIsSuccess(t *testing.T) {
	fh, _ := seededRuleHandle()

	err := NewManagerWithHandle(fh).DeleteMasquerade(context.Background(), masqueradeRef("vm-1", 404))
	if err != nil {
		t.Fatalf("DeleteMasquerade error = %v", err)
	}
}

func TestDeleteEndpointAntiSpoofingRemovesOnlyMatchingRule(t *testing.T) {
	fh, _ := seededRuleHandle()
	manager := NewManagerWithHandle(fh)

	if err := manager.DeleteEndpointAntiSpoofing(context.Background(), endpointRef("vm-1", 7)); err != nil {
		t.Fatalf("DeleteEndpointAntiSpoofing error = %v", err)
	}

	infos, err := manager.ListRules(context.Background(), validRuleFilter())
	if err != nil {
		t.Fatalf("ListRules after delete error = %v", err)
	}
	got := refsFromInfos(infos)
	want := []firewall.RuleRef{
		endpointRef("vm-1", 5),
		endpointRef("vm-2", 3),
		masqueradeRef("vm-1", 10),
		masqueradeRef("vm-2", 2),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remaining refs = %+v, want %+v", got, want)
	}
}

type seededObjects struct {
	natTable    *nftables.Table
	natChain    *nftables.Chain
	bridgeTable *nftables.Table
	bridgeChain *nftables.Chain
}

func seededRuleHandle() (*fakeHandle, seededObjects) {
	natTable := &nftables.Table{Family: nftables.TableFamilyIPv4, Name: "govirta_nat"}
	natPriority := nftables.ChainPriority(100)
	natChain := &nftables.Chain{Table: natTable, Name: "postrouting", Priority: &natPriority}
	bridgeTable := &nftables.Table{Family: nftables.TableFamilyBridge, Name: "govirta_bridge"}
	bridgePriority := nftables.ChainPriority(-200)
	bridgeChain := &nftables.Chain{Table: bridgeTable, Name: "forward", Priority: &bridgePriority}

	fh := &fakeHandle{
		tables: []*nftables.Table{natTable, bridgeTable},
		chains: []*nftables.Chain{natChain, bridgeChain},
	}
	fh.rules = []*nftables.Rule{
		masqueradeRule(natTable, natChain, "vm-1", 10, netip.MustParsePrefix("192.0.2.0/24"), "eth0"),
		masqueradeRule(natTable, natChain, "vm-2", 2, netip.MustParsePrefix("198.51.100.0/24"), "eth1"),
		endpointRule(bridgeTable, bridgeChain, "vm-1", 7, guardIPv4, "tap0", nil, netip.MustParseAddr("192.0.2.10")),
		endpointRule(bridgeTable, bridgeChain, "vm-1", 5, guardEtherMAC, "tap0", net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}, netip.Addr{}),
		endpointRule(bridgeTable, bridgeChain, "vm-2", 3, guardARPIPv4, "tap1", nil, netip.MustParseAddr("192.0.2.11")),
	}
	return fh, seededObjects{natTable: natTable, natChain: natChain, bridgeTable: bridgeTable, bridgeChain: bridgeChain}
}

func masqueradeRule(table *nftables.Table, chain *nftables.Chain, owner firewall.RuleOwner, handle uint64, cidr netip.Prefix, iface firewall.InterfaceName) *nftables.Rule {
	summary := firewall.MasqueradeSummary{GuestCIDR: cidr, EgressInterfaceName: iface, Priority: firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT)}
	return &nftables.Rule{
		Table:    table,
		Chain:    chain,
		Handle:   handle,
		Exprs:    masqueradeExprs(summary, owner),
		UserData: userDataForRule(owner, firewall.RulePurposeMasquerade, guardMasquerade),
	}
}

func endpointRule(table *nftables.Table, chain *nftables.Chain, owner firewall.RuleOwner, handle uint64, guard endpointGuardKind, tap firewall.InterfaceName, mac net.HardwareAddr, ip netip.Addr) *nftables.Rule {
	summary := firewall.EndpointAntiSpoofingSummary{BridgeName: "br0", TapName: tap, MAC: mac, IPv4: ip, Priority: firewall.ExplicitPriority(-200, firewall.PriorityNameBridgeFilter)}
	var exprs []expr.Any
	switch guard {
	case guardEtherMAC:
		exprs = endpointEtherMACDropExprs(summary, owner)
	case guardIPv4:
		exprs = endpointIPv4DropExprs(summary, owner)
	case guardARPMAC:
		exprs = endpointARPMACDropExprs(summary, owner)
	case guardARPIPv4:
		exprs = endpointARPIPv4DropExprs(summary, owner)
	}
	return &nftables.Rule{
		Table:    table,
		Chain:    chain,
		Handle:   handle,
		Exprs:    exprs,
		UserData: userDataForRule(owner, firewall.RulePurposeEndpointAntiSpoofing, guard),
	}
}

func masqueradeRef(owner firewall.RuleOwner, handle firewall.RuleHandle) firewall.RuleRef {
	return firewall.RuleRef{Owner: owner, Purpose: firewall.RulePurposeMasquerade, Family: firewall.TableFamilyIPv4, TableName: "govirta_nat", ChainName: "postrouting", Handle: handle}
}

func endpointRef(owner firewall.RuleOwner, handle firewall.RuleHandle) firewall.RuleRef {
	return firewall.RuleRef{Owner: owner, Purpose: firewall.RulePurposeEndpointAntiSpoofing, Family: firewall.TableFamilyBridge, TableName: "govirta_bridge", ChainName: "forward", Handle: handle}
}

func refsFromInfos(infos []firewall.RuleInfo) []firewall.RuleRef {
	refs := make([]firewall.RuleRef, 0, len(infos))
	for _, info := range infos {
		refs = append(refs, ruleRefForInfo(info))
	}
	return refs
}
