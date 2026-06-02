//go:build linux

package linux

import (
	"bytes"
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
	summary := firewall.MasqueradeSummary{GuestCIDR: netip.MustParsePrefix("203.0.113.0/24"), EgressInterfaceName: "eth2", Priority: firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT)}
	fh.rules = append(fh.rules,
		&nftables.Rule{Table: objects.natTable, Chain: objects.natChain, Handle: 99},
		&nftables.Rule{Table: objects.natTable, Chain: objects.natChain, Handle: 100, Exprs: masqueradeExprs(summary), UserData: []byte("govirta-owner=vm-3;govirta-purpose=masquerade;govirta-guard=masquerade")},
		&nftables.Rule{Table: objects.natTable, Chain: objects.natChain, Handle: 101, Exprs: masqueradeExprs(summary), UserData: []byte("govirta-purpose=masquerade")},
		&nftables.Rule{Table: objects.natTable, Chain: objects.natChain, Handle: 102, Exprs: masqueradeExprs(summary), UserData: []byte("other-key=value")},
	)

	infos, err := NewManagerWithHandle(fh).ListRules(context.Background(), validRuleFilter())
	if err != nil {
		t.Fatalf("ListRules error = %v", err)
	}

	for _, info := range infos {
		switch ruleRefForInfo(info).Handle {
		case 99, 100, 101, 102:
			t.Fatalf("ListRules returned non-Govirta rule: %+v", info)
		}
	}
}

func TestListRulesReturnsInvalidObservedStateForBrokenGovirtaUserData(t *testing.T) {
	cases := []struct {
		name     string
		userData []byte
	}{
		{name: "magic missing owner", userData: []byte("govirta-firewall-rule=v1;govirta-purpose=masquerade;govirta-guard=masquerade")},
		{name: "magic missing purpose", userData: []byte("govirta-firewall-rule=v1;govirta-owner=vm-3;govirta-guard=masquerade")},
		{name: "magic missing guard", userData: []byte("govirta-firewall-rule=v1;govirta-owner=vm-3;govirta-purpose=masquerade")},
		{name: "magic illegal owner", userData: []byte("govirta-firewall-rule=v1;govirta-owner=bad/name;govirta-purpose=masquerade;govirta-guard=masquerade")},
		{name: "magic illegal purpose", userData: []byte("govirta-firewall-rule=v1;govirta-owner=vm-3;govirta-purpose=unsupported;govirta-guard=masquerade")},
		{name: "magic illegal guard", userData: []byte("govirta-firewall-rule=v1;govirta-owner=vm-3;govirta-purpose=endpoint-anti-spoofing;govirta-guard=unsupported")},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fh, objects := seededRuleHandle()
			summary := firewall.MasqueradeSummary{GuestCIDR: netip.MustParsePrefix("203.0.113.0/24"), EgressInterfaceName: "eth2", Priority: firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT)}
			fh.rules = append(fh.rules, &nftables.Rule{Table: objects.natTable, Chain: objects.natChain, Handle: 99, Exprs: masqueradeExprs(summary), UserData: tt.userData})

			_, err := NewManagerWithHandle(fh).ListRules(context.Background(), validRuleFilter())
			if !errors.Is(err, firewallerr.ErrInvalidObservedState) {
				t.Fatalf("ListRules error = %v, want %v", err, firewallerr.ErrInvalidObservedState)
			}
		})
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
	ref := endpointRef("vm-1", 5)

	info, err := NewManagerWithHandle(fh).GetRule(context.Background(), firewall.RuleQuery{Ref: ref})
	if err != nil {
		t.Fatalf("GetRule error = %v", err)
	}
	if got := ruleRefForInfo(info); got != ref {
		t.Fatalf("GetRule ref = %+v, want %+v", got, ref)
	}
	if info.Summary.EndpointAntiSpoofing == nil || info.Summary.EndpointAntiSpoofing.BridgeName != "br0" || info.Summary.EndpointAntiSpoofing.TapName != "tap0" || info.Summary.EndpointAntiSpoofing.MAC.String() != "02:00:00:00:00:01" || info.Summary.EndpointAntiSpoofing.IPv4 != netip.MustParseAddr("192.0.2.10") {
		t.Fatalf("GetRule summary = %+v, want merged endpoint bridge br0 TAP tap0 MAC 02:00:00:00:00:01 IPv4 192.0.2.10", info.Summary)
	}
}

func TestGetRuleRejectsCrossProtocolEndpointExpression(t *testing.T) {
	cases := []struct {
		name       string
		metadata   endpointGuardKind
		actualType uint16
	}{
		{name: "IPv4 metadata with ARP EtherType", metadata: guardIPv4, actualType: etherTypeARP},
		{name: "ARP MAC metadata with IPv4 EtherType", metadata: guardARPMAC, actualType: etherTypeIPv4},
		{name: "ARP IPv4 metadata with IPv4 EtherType", metadata: guardARPIPv4, actualType: etherTypeIPv4},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fh, objects := seededRuleHandle()
			summary := firewall.EndpointAntiSpoofingSummary{BridgeName: "br0", TapName: "tap2", MAC: net.HardwareAddr{0x02, 0, 0, 0, 0, 0x02}, IPv4: netip.MustParseAddr("192.0.2.12")}
			var payload []byte
			var offset uint32
			switch tt.metadata {
			case guardIPv4:
				addr := summary.IPv4.As4()
				payload, offset = addr[:], 12
			case guardARPMAC:
				payload, offset = []byte(summary.MAC), 8
			case guardARPIPv4:
				addr := summary.IPv4.As4()
				payload, offset = addr[:], 14
			}
			rule := &nftables.Rule{
				Table:    objects.bridgeTable,
				Chain:    objects.bridgeChain,
				Handle:   22,
				Exprs:    endpointProtocolDropExprs(summary.BridgeName, summary.TapName, tt.actualType, expr.PayloadBaseNetworkHeader, offset, payload),
				UserData: userDataForRule("vm-3", firewall.RulePurposeEndpointAntiSpoofing, tt.metadata),
			}
			fh.rules = append(fh.rules, rule)

			_, err := NewManagerWithHandle(fh).GetRule(context.Background(), firewall.RuleQuery{Ref: endpointRef("vm-3", 22)})
			if !errors.Is(err, firewallerr.ErrInvalidObservedState) {
				t.Fatalf("GetRule error = %v, want %v", err, firewallerr.ErrInvalidObservedState)
			}
		})
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

	if err := manager.DeleteEndpointAntiSpoofing(context.Background(), endpointRef("vm-1", 5)); err != nil {
		t.Fatalf("DeleteEndpointAntiSpoofing error = %v", err)
	}

	infos, err := manager.ListRules(context.Background(), validRuleFilter())
	if err != nil {
		t.Fatalf("ListRules after delete error = %v", err)
	}
	got := refsFromInfos(infos)
	want := []firewall.RuleRef{
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
		endpointRule(bridgeTable, bridgeChain, "vm-1", 5, guardEtherMAC, "tap0", net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}, netip.Addr{}),
		endpointRule(bridgeTable, bridgeChain, "vm-1", 7, guardIPv4, "tap0", nil, netip.MustParseAddr("192.0.2.10")),
		endpointRule(bridgeTable, bridgeChain, "vm-1", 8, guardARPMAC, "tap0", net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}, netip.Addr{}),
		endpointRule(bridgeTable, bridgeChain, "vm-1", 9, guardARPIPv4, "tap0", nil, netip.MustParseAddr("192.0.2.10")),
		endpointRule(bridgeTable, bridgeChain, "vm-2", 3, guardEtherMAC, "tap1", net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02}, netip.Addr{}),
		endpointRule(bridgeTable, bridgeChain, "vm-2", 4, guardIPv4, "tap1", nil, netip.MustParseAddr("192.0.2.11")),
		endpointRule(bridgeTable, bridgeChain, "vm-2", 6, guardARPMAC, "tap1", net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02}, netip.Addr{}),
		endpointRule(bridgeTable, bridgeChain, "vm-2", 11, guardARPIPv4, "tap1", nil, netip.MustParseAddr("192.0.2.11")),
	}
	return fh, seededObjects{natTable: natTable, natChain: natChain, bridgeTable: bridgeTable, bridgeChain: bridgeChain}
}

func masqueradeRule(table *nftables.Table, chain *nftables.Chain, owner firewall.RuleOwner, handle uint64, cidr netip.Prefix, iface firewall.InterfaceName) *nftables.Rule {
	summary := firewall.MasqueradeSummary{GuestCIDR: cidr, EgressInterfaceName: iface, Priority: firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT)}
	return &nftables.Rule{
		Table:    table,
		Chain:    chain,
		Handle:   handle,
		Exprs:    masqueradeExprs(summary),
		UserData: userDataForRule(owner, firewall.RulePurposeMasquerade, guardMasquerade),
	}
}

func endpointRule(table *nftables.Table, chain *nftables.Chain, owner firewall.RuleOwner, handle uint64, guard endpointGuardKind, tap firewall.InterfaceName, mac net.HardwareAddr, ip netip.Addr) *nftables.Rule {
	summary := firewall.EndpointAntiSpoofingSummary{BridgeName: "br0", TapName: tap, MAC: mac, IPv4: ip, Priority: firewall.ExplicitPriority(-200, firewall.PriorityNameBridgeFilter)}
	var exprs []expr.Any
	switch guard {
	case guardEtherMAC:
		exprs = endpointEtherMACDropExprs(summary)
	case guardIPv4:
		exprs = endpointIPv4DropExprs(summary)
	case guardARPMAC:
		exprs = endpointARPMACDropExprs(summary)
	case guardARPIPv4:
		exprs = endpointARPIPv4DropExprs(summary)
	}
	return &nftables.Rule{
		Table:    table,
		Chain:    chain,
		Handle:   handle,
		Exprs:    exprs,
		UserData: userDataForRule(owner, firewall.RulePurposeEndpointAntiSpoofing, guard),
	}
}

func TestEndpointProtocolBuildersIncludeEtherTypeGuard(t *testing.T) {
	summary := firewall.EndpointAntiSpoofingSummary{BridgeName: "br0", TapName: "tap0", MAC: net.HardwareAddr{0x02, 0, 0, 0, 0, 1}, IPv4: netip.MustParseAddr("192.0.2.10")}
	cases := []struct {
		name      string
		exprs     []expr.Any
		etherType uint16
	}{
		{name: "IPv4", exprs: endpointIPv4DropExprs(summary), etherType: etherTypeIPv4},
		{name: "ARP MAC", exprs: endpointARPMACDropExprs(summary), etherType: etherTypeARP},
		{name: "ARP IPv4", exprs: endpointARPIPv4DropExprs(summary), etherType: etherTypeARP},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.exprs) != 9 {
				t.Fatalf("expr count = %d, want 9", len(tt.exprs))
			}
			meta, ok := tt.exprs[4].(*expr.Meta)
			if !ok || meta.Key != expr.MetaKeyPROTOCOL || meta.Register != regProtocol {
				t.Fatalf("protocol expr = %#v, want MetaKeyPROTOCOL in regProtocol", tt.exprs[4])
			}
			cmp, ok := tt.exprs[5].(*expr.Cmp)
			if !ok || cmp.Op != expr.CmpOpEq || cmp.Register != regProtocol || !bytes.Equal(cmp.Data, etherTypeData(tt.etherType)) {
				t.Fatalf("protocol comparison = %#v, want EtherType %#04x", tt.exprs[5], tt.etherType)
			}
		})
	}
}

func TestEndpointEtherMACBuilderHasNoEtherTypeGuard(t *testing.T) {
	summary := firewall.EndpointAntiSpoofingSummary{BridgeName: "br0", TapName: "tap0", MAC: net.HardwareAddr{0x02, 0, 0, 0, 0, 1}}
	exprs := endpointEtherMACDropExprs(summary)
	if len(exprs) != 7 {
		t.Fatalf("expr count = %d, want 7", len(exprs))
	}
	for _, expression := range exprs {
		meta, ok := expression.(*expr.Meta)
		if ok && meta.Key == expr.MetaKeyPROTOCOL {
			t.Fatalf("ether MAC guard unexpectedly includes protocol expression")
		}
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
