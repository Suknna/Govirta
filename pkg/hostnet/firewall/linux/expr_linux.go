//go:build linux

package linux

import (
	"bytes"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
	"github.com/suknna/govirta/pkg/hostnet/firewall/firewallerr"
	"golang.org/x/sys/unix"
)

type endpointGuardKind string

const (
	userDataMagicKey   = "govirta-firewall-rule"
	userDataMagicValue = "v1"
	userDataOwnerKey   = "govirta-owner"
	userDataPurposeKey = "govirta-purpose"
	userDataGuardKey   = "govirta-guard"

	guardMasquerade endpointGuardKind = "masquerade"
	guardEtherMAC   endpointGuardKind = "ether-mac"
	guardIPv4       endpointGuardKind = "ipv4"
	guardARPMAC     endpointGuardKind = "arp-mac"
	guardARPIPv4    endpointGuardKind = "arp-ipv4"

	etherTypeIPv4 uint16 = 0x0800
	etherTypeARP  uint16 = 0x0806

	regMatch    uint32 = unix.NFT_REG_1
	regProtocol uint32 = unix.NFT_REG_2
	regMask     uint32 = unix.NFT_REG_3

	interfaceNameLen = 16
)

func masqueradeExprs(summary firewall.MasqueradeSummary) []expr.Any {
	masked := summary.GuestCIDR.Masked()
	addr := masked.Addr().As4()
	mask := prefixMask(summary.GuestCIDR.Bits(), 4)
	return []expr.Any{
		&expr.Payload{OperationType: expr.PayloadLoad, DestRegister: regMatch, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
		&expr.Bitwise{SourceRegister: regMatch, DestRegister: regMask, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMask, Data: addr[:]},
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: regMatch},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMatch, Data: interfaceNameData(summary.EgressInterfaceName)},
		&expr.Masq{},
	}
}

func endpointEtherMACDropExprs(summary firewall.EndpointAntiSpoofingSummary) []expr.Any {
	return endpointDropExprsForBridge(summary.BridgeName, summary.TapName, expr.PayloadBaseLLHeader, 6, []byte(summary.MAC))
}

func endpointIPv4DropExprs(summary firewall.EndpointAntiSpoofingSummary) []expr.Any {
	addr := summary.IPv4.As4()
	return endpointProtocolDropExprs(summary.BridgeName, summary.TapName, etherTypeIPv4, expr.PayloadBaseNetworkHeader, 12, addr[:])
}

func endpointARPMACDropExprs(summary firewall.EndpointAntiSpoofingSummary) []expr.Any {
	return endpointProtocolDropExprs(summary.BridgeName, summary.TapName, etherTypeARP, expr.PayloadBaseNetworkHeader, 8, []byte(summary.MAC))
}

func endpointARPIPv4DropExprs(summary firewall.EndpointAntiSpoofingSummary) []expr.Any {
	addr := summary.IPv4.As4()
	return endpointProtocolDropExprs(summary.BridgeName, summary.TapName, etherTypeARP, expr.PayloadBaseNetworkHeader, 14, addr[:])
}

func endpointDropExprsForBridge(bridge firewall.InterfaceName, tap firewall.InterfaceName, base expr.PayloadBase, offset uint32, data []byte) []expr.Any {
	exprs := endpointInterfaceExprs(bridge, tap)
	exprs = append(exprs,
		&expr.Payload{OperationType: expr.PayloadLoad, DestRegister: regMatch, Base: base, Offset: offset, Len: uint32(len(data))},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: regMatch, Data: data},
		&expr.Verdict{Kind: expr.VerdictDrop},
	)
	return exprs
}

func endpointProtocolDropExprs(bridge firewall.InterfaceName, tap firewall.InterfaceName, etherType uint16, base expr.PayloadBase, offset uint32, data []byte) []expr.Any {
	exprs := endpointInterfaceExprs(bridge, tap)
	exprs = append(exprs,
		&expr.Meta{Key: expr.MetaKeyPROTOCOL, Register: regProtocol},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regProtocol, Data: etherTypeData(etherType)},
		&expr.Payload{OperationType: expr.PayloadLoad, DestRegister: regMatch, Base: base, Offset: offset, Len: uint32(len(data))},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: regMatch, Data: data},
		&expr.Verdict{Kind: expr.VerdictDrop},
	)
	return exprs
}

// endpointInterfaceExprs binds the bridge-family forward-hook interface match.
// In the kernel bridge forward hook, MetaKeyIIFNAME is the frame's receive port
// (the guest TAP) and MetaKeyBRIIIFNAME is the bridge the port is enslaved to.
// The guard therefore matches iifname==TAP AND ibrname==bridge; binding these
// the other way round makes the conjunction unsatisfiable on real guest frames,
// silently disabling every anti-spoofing DROP that follows.
func endpointInterfaceExprs(bridge firewall.InterfaceName, tap firewall.InterfaceName) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: regMatch},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMatch, Data: interfaceNameData(tap)},
		&expr.Meta{Key: expr.MetaKeyBRIIIFNAME, Register: regMatch},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMatch, Data: interfaceNameData(bridge)},
	}
}

type observedRuleDetail struct {
	info  firewall.RuleInfo
	guard endpointGuardKind
}

func observedRuleDetailFor(table *nftables.Table, chain *nftables.Chain, rule *nftables.Rule) (observedRuleDetail, bool, error) {
	metadata, ok, err := parseRuleUserData(rule.UserData)
	if err != nil || !ok {
		return observedRuleDetail{}, ok, err
	}

	family, ok := firewallFamily(table.Family)
	if !ok {
		return observedRuleDetail{}, true, invalidObservedState("unsupported table family %d", table.Family)
	}

	info := firewall.RuleInfo{
		Ref: firewall.RuleRef{
			Owner:     metadata.owner,
			Purpose:   metadata.purpose,
			Family:    family,
			TableName: firewall.TableName(table.Name),
			ChainName: firewall.ChainName(chain.Name),
			Handle:    firewall.RuleHandle(rule.Handle),
		},
	}

	switch metadata.purpose {
	case firewall.RulePurposeMasquerade:
		if metadata.guard != guardMasquerade {
			return observedRuleDetail{}, true, invalidObservedState("masquerade rule has guard %q", metadata.guard)
		}
		summary, err := parseMasquerade(rule.Exprs, chain)
		if err != nil {
			return observedRuleDetail{}, true, err
		}
		info.Summary.Masquerade = summary
	case firewall.RulePurposeEndpointAntiSpoofing:
		summary, err := parseEndpointAntiSpoofing(metadata.guard, rule.Exprs, chain)
		if err != nil {
			return observedRuleDetail{}, true, err
		}
		info.Summary.EndpointAntiSpoofing = summary
	case firewall.RulePurposeForwardAccept:
		summary, err := parseForwardAccept(metadata.guard, rule.Exprs, chain)
		if err != nil {
			return observedRuleDetail{}, true, err
		}
		info.Summary.ForwardAccept = summary
	default:
		return observedRuleDetail{}, true, invalidObservedState("unsupported rule purpose %q", metadata.purpose)
	}

	return observedRuleDetail{info: info, guard: metadata.guard}, true, nil
}

type ruleUserData struct {
	owner   firewall.RuleOwner
	purpose firewall.RulePurpose
	guard   endpointGuardKind
}

func userDataForRule(owner firewall.RuleOwner, purpose firewall.RulePurpose, guard endpointGuardKind) []byte {
	return []byte(fmt.Sprintf("%s=%s;%s=%s;%s=%s;%s=%s", userDataMagicKey, userDataMagicValue, userDataOwnerKey, owner, userDataPurposeKey, purpose, userDataGuardKey, guard))
}

func parseRuleUserData(data []byte) (ruleUserData, bool, error) {
	if len(data) == 0 {
		return ruleUserData{}, false, nil
	}
	fields := map[string]string{}
	for _, part := range strings.Split(string(data), ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		fields[key] = value
	}
	owner, hasOwner := fields[userDataOwnerKey]
	purpose, hasPurpose := fields[userDataPurposeKey]
	guard, hasGuard := fields[userDataGuardKey]
	magic, hasMagic := fields[userDataMagicKey]
	if !hasMagic || magic != userDataMagicValue {
		return ruleUserData{}, false, nil
	}
	if !hasOwner || !hasPurpose || !hasGuard || owner == "" || purpose == "" || guard == "" {
		return ruleUserData{}, true, invalidObservedState("incomplete Govirta rule metadata")
	}
	metadata := ruleUserData{owner: firewall.RuleOwner(owner), purpose: firewall.RulePurpose(purpose), guard: endpointGuardKind(guard)}
	if err := validateObservedRuleUserData(metadata); err != nil {
		return ruleUserData{}, true, err
	}
	return metadata, true, nil
}

func validateObservedRuleUserData(metadata ruleUserData) error {
	if err := validateSafeName("owner", string(metadata.owner)); err != nil {
		return invalidObservedState("invalid Govirta rule owner")
	}
	if err := validatePurpose(metadata.purpose); err != nil {
		return invalidObservedState("invalid Govirta rule purpose")
	}
	switch metadata.purpose {
	case firewall.RulePurposeMasquerade:
		if metadata.guard != guardMasquerade {
			return invalidObservedState("masquerade rule has guard %q", metadata.guard)
		}
	case firewall.RulePurposeEndpointAntiSpoofing:
		switch metadata.guard {
		case guardEtherMAC, guardIPv4, guardARPMAC, guardARPIPv4:
			return nil
		default:
			return invalidObservedState("endpoint anti-spoofing rule has guard %q", metadata.guard)
		}
	case firewall.RulePurposeForwardAccept:
		switch metadata.guard {
		case guardForwardEgress, guardForwardReturn:
			return nil
		default:
			return invalidObservedState("forward-accept rule has guard %q", metadata.guard)
		}
	}
	return nil
}

func parseMasquerade(exprs []expr.Any, chain *nftables.Chain) (*firewall.MasqueradeSummary, error) {
	if len(exprs) != 6 {
		return nil, invalidObservedState("masquerade expression count is %d", len(exprs))
	}
	payload, ok := exprs[0].(*expr.Payload)
	if !ok || payload.OperationType != expr.PayloadLoad || payload.DestRegister != regMatch || payload.Base != expr.PayloadBaseNetworkHeader || payload.Offset != 12 || payload.Len != 4 {
		return nil, invalidObservedState("masquerade source payload expression is invalid")
	}
	bitwise, ok := exprs[1].(*expr.Bitwise)
	if !ok || bitwise.SourceRegister != regMatch || bitwise.DestRegister != regMask || bitwise.Len != 4 || len(bitwise.Mask) != 4 || !bytes.Equal(bitwise.Xor, []byte{0, 0, 0, 0}) {
		return nil, invalidObservedState("masquerade mask expression is invalid")
	}
	cmpCIDR, ok := exprs[2].(*expr.Cmp)
	if !ok || cmpCIDR.Op != expr.CmpOpEq || cmpCIDR.Register != regMask || len(cmpCIDR.Data) != 4 {
		return nil, invalidObservedState("masquerade CIDR comparison is invalid")
	}
	meta, ok := exprs[3].(*expr.Meta)
	if !ok || meta.Key != expr.MetaKeyOIFNAME || meta.Register != regMatch || meta.SourceRegister {
		return nil, invalidObservedState("masquerade output interface expression is invalid")
	}
	cmpIface, ok := exprs[4].(*expr.Cmp)
	if !ok || cmpIface.Op != expr.CmpOpEq || cmpIface.Register != regMatch {
		return nil, invalidObservedState("masquerade output interface comparison is invalid")
	}
	masq, ok := exprs[5].(*expr.Masq)
	if !ok || masq.ToPorts || masq.Random || masq.FullyRandom || masq.Persistent {
		return nil, invalidObservedState("masquerade action expression is invalid")
	}
	bits, ok := maskBits(bitwise.Mask)
	if !ok {
		return nil, invalidObservedState("masquerade CIDR mask is not contiguous")
	}
	addr := netip.AddrFrom4(bytesTo4(cmpCIDR.Data))
	return &firewall.MasqueradeSummary{
		GuestCIDR:           netip.PrefixFrom(addr, bits).Masked(),
		EgressInterfaceName: firewall.InterfaceName(interfaceNameFromData(cmpIface.Data)),
		Priority:            priorityFromChain(chain, firewall.PriorityNameSrcNAT),
	}, nil
}

func parseEndpointAntiSpoofing(guard endpointGuardKind, exprs []expr.Any, chain *nftables.Chain) (*firewall.EndpointAntiSpoofingSummary, error) {
	summary, err := parseEndpointDropExprs(exprs)
	if err != nil {
		return nil, err
	}
	if err := validateEndpointGuardPayload(guard, exprs); err != nil {
		return nil, err
	}
	summary.Priority = priorityFromChain(chain, firewall.PriorityNameBridgeFilter)

	switch guard {
	case guardEtherMAC, guardARPMAC:
		if len(summary.MAC) != 6 {
			return nil, invalidObservedState("endpoint MAC guard has invalid MAC")
		}
	case guardIPv4, guardARPIPv4:
		if !summary.IPv4.IsValid() || !summary.IPv4.Is4() {
			return nil, invalidObservedState("endpoint IPv4 guard has invalid IPv4")
		}
	default:
		return nil, invalidObservedState("unsupported endpoint guard %q", guard)
	}
	return summary, nil
}

func validateEndpointGuardPayload(guard endpointGuardKind, exprs []expr.Any) error {
	payloadIndex := 4
	if guard != guardEtherMAC {
		if err := validateEndpointGuardProtocol(guard, exprs); err != nil {
			return err
		}
		payloadIndex = 6
	}
	payload, ok := exprs[payloadIndex].(*expr.Payload)
	if !ok {
		return invalidObservedState("endpoint payload expression is invalid")
	}
	var wantBase expr.PayloadBase
	var wantOffset uint32
	var wantLen uint32
	switch guard {
	case guardEtherMAC:
		wantBase, wantOffset, wantLen = expr.PayloadBaseLLHeader, 6, 6
	case guardIPv4:
		wantBase, wantOffset, wantLen = expr.PayloadBaseNetworkHeader, 12, 4
	case guardARPMAC:
		wantBase, wantOffset, wantLen = expr.PayloadBaseNetworkHeader, 8, 6
	case guardARPIPv4:
		wantBase, wantOffset, wantLen = expr.PayloadBaseNetworkHeader, 14, 4
	default:
		return invalidObservedState("unsupported endpoint guard %q", guard)
	}
	if payload.Base != wantBase || payload.Offset != wantOffset || payload.Len != wantLen {
		return invalidObservedState("endpoint guard %q payload does not match metadata", guard)
	}
	return nil
}

func validateEndpointGuardProtocol(guard endpointGuardKind, exprs []expr.Any) error {
	meta, ok := exprs[4].(*expr.Meta)
	if !ok || meta.Key != expr.MetaKeyPROTOCOL || meta.Register != regProtocol || meta.SourceRegister {
		return invalidObservedState("endpoint protocol expression is invalid")
	}
	cmpProtocol, ok := exprs[5].(*expr.Cmp)
	if !ok || cmpProtocol.Op != expr.CmpOpEq || cmpProtocol.Register != regProtocol {
		return invalidObservedState("endpoint protocol comparison is invalid")
	}
	var wantEtherType uint16
	switch guard {
	case guardIPv4:
		wantEtherType = etherTypeIPv4
	case guardARPMAC, guardARPIPv4:
		wantEtherType = etherTypeARP
	default:
		return invalidObservedState("endpoint guard %q must not have protocol guard", guard)
	}
	if !bytes.Equal(cmpProtocol.Data, etherTypeData(wantEtherType)) {
		return invalidObservedState("endpoint guard %q EtherType does not match metadata", guard)
	}
	return nil
}

func parseEndpointDropExprs(exprs []expr.Any) (*firewall.EndpointAntiSpoofingSummary, error) {
	if len(exprs) != 7 && len(exprs) != 9 {
		return nil, invalidObservedState("endpoint guard expression count is %d", len(exprs))
	}
	// Mirror endpointInterfaceExprs: exprs[0] (iifname) binds the receive port
	// (the TAP) and exprs[2] (ibrname) binds the bridge.
	meta, ok := exprs[0].(*expr.Meta)
	if !ok || meta.Key != expr.MetaKeyIIFNAME || meta.Register != regMatch || meta.SourceRegister {
		return nil, invalidObservedState("endpoint TAP (iifname) expression is invalid")
	}
	cmpTap, ok := exprs[1].(*expr.Cmp)
	if !ok || cmpTap.Op != expr.CmpOpEq || cmpTap.Register != regMatch {
		return nil, invalidObservedState("endpoint TAP (iifname) comparison is invalid")
	}
	meta, ok = exprs[2].(*expr.Meta)
	if !ok || meta.Key != expr.MetaKeyBRIIIFNAME || meta.Register != regMatch || meta.SourceRegister {
		return nil, invalidObservedState("endpoint bridge (ibrname) expression is invalid")
	}
	cmpBridge, ok := exprs[3].(*expr.Cmp)
	if !ok || cmpBridge.Op != expr.CmpOpEq || cmpBridge.Register != regMatch {
		return nil, invalidObservedState("endpoint bridge (ibrname) comparison is invalid")
	}
	payloadIndex := len(exprs) - 3
	payload, ok := exprs[payloadIndex].(*expr.Payload)
	if !ok || payload.OperationType != expr.PayloadLoad || payload.DestRegister != regMatch {
		return nil, invalidObservedState("endpoint payload expression is invalid")
	}
	cmpValue, ok := exprs[payloadIndex+1].(*expr.Cmp)
	if !ok || cmpValue.Op != expr.CmpOpNeq || cmpValue.Register != regMatch || uint32(len(cmpValue.Data)) != payload.Len {
		return nil, invalidObservedState("endpoint payload comparison is invalid")
	}
	verdict, ok := exprs[payloadIndex+2].(*expr.Verdict)
	if !ok || verdict.Kind != expr.VerdictDrop || verdict.Chain != "" {
		return nil, invalidObservedState("endpoint drop verdict is invalid")
	}

	summary := &firewall.EndpointAntiSpoofingSummary{
		BridgeName: firewall.InterfaceName(interfaceNameFromData(cmpBridge.Data)),
		TapName:    firewall.InterfaceName(interfaceNameFromData(cmpTap.Data)),
	}
	switch len(cmpValue.Data) {
	case 4:
		summary.IPv4 = netip.AddrFrom4(bytesTo4(cmpValue.Data))
	case 6:
		summary.MAC = net.HardwareAddr(append([]byte(nil), cmpValue.Data...))
	default:
		return nil, invalidObservedState("endpoint payload length %d is unsupported", len(cmpValue.Data))
	}
	return summary, nil
}

func priorityFromChain(chain *nftables.Chain, name firewall.PriorityName) firewall.Priority {
	if chain.Priority == nil {
		return firewall.Priority{Name: name}
	}
	return firewall.ExplicitPriority(int(*chain.Priority), name)
}

func prefixMask(bits int, size int) []byte {
	mask := make([]byte, size)
	for i := 0; i < bits && i < size*8; i++ {
		mask[i/8] |= 1 << uint(7-(i%8))
	}
	return mask
}

func maskBits(mask []byte) (int, bool) {
	bits := 0
	seenZero := false
	for _, b := range mask {
		for shift := 7; shift >= 0; shift-- {
			set := b&(1<<uint(shift)) != 0
			if set && seenZero {
				return 0, false
			}
			if set {
				bits++
			} else {
				seenZero = true
			}
		}
	}
	return bits, true
}

func interfaceNameData(name firewall.InterfaceName) []byte {
	data := make([]byte, interfaceNameLen)
	copy(data, string(name))
	return data
}

func etherTypeData(etherType uint16) []byte {
	return []byte{byte(etherType >> 8), byte(etherType)}
}

func interfaceNameFromData(data []byte) string {
	return string(bytes.TrimRight(data, "\x00"))
}

func invalidObservedState(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{firewallerr.ErrInvalidObservedState}, args...)...)
}

func bytesTo4(data []byte) [4]byte {
	var out [4]byte
	copy(out[:], data)
	return out
}
