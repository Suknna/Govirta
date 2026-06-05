//go:build linux

package linux

import (
	"bytes"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/suknna/govirta/pkg/hostnet/firewall"
)

// forwardEgressAcceptExprs builds "ip saddr {GuestCIDR} oifname {egress} accept".
//
// It mirrors masqueradeExprs source-CIDR and output-interface matching, then
// accepts instead of masquerading.
func forwardEgressAcceptExprs(summary firewall.ForwardAcceptSummary) []expr.Any {
	masked := summary.GuestCIDR.Masked()
	addr := masked.Addr().As4()
	mask := prefixMask(summary.GuestCIDR.Bits(), 4)
	return []expr.Any{
		&expr.Payload{OperationType: expr.PayloadLoad, DestRegister: regMatch, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
		&expr.Bitwise{SourceRegister: regMatch, DestRegister: regMask, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMask, Data: addr[:]},
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: regMatch},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMatch, Data: interfaceNameData(summary.EgressInterfaceName)},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

// forwardReturnAcceptExprs builds
// "ip daddr {GuestCIDR} iifname {egress} ct state established,related accept".
//
// The conntrack state is loaded into a register, bitwise-masked with the
// established|related bits, then compared not-equal to zero, matching the
// canonical nftables stateful-return pattern.
func forwardReturnAcceptExprs(summary firewall.ForwardAcceptSummary) []expr.Any {
	masked := summary.GuestCIDR.Masked()
	addr := masked.Addr().As4()
	mask := prefixMask(summary.GuestCIDR.Bits(), 4)
	stateMask := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	return []expr.Any{
		&expr.Payload{OperationType: expr.PayloadLoad, DestRegister: regMatch, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Bitwise{SourceRegister: regMatch, DestRegister: regMask, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMask, Data: addr[:]},
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: regMatch},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regMatch, Data: interfaceNameData(summary.EgressInterfaceName)},
		&expr.Ct{Register: regProtocol, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: regProtocol, DestRegister: regProtocol, Len: 4, Mask: stateMask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: regProtocol, Data: []byte{0, 0, 0, 0}},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

// parseForwardAccept reconstructs a ForwardAcceptSummary from one observed
// forward-accept rule (either guard direction). GuestCIDR and egress are present
// in both directions, so a single rule is sufficient to recover the summary;
// the group path validates that both guards exist.
func parseForwardAccept(guard endpointGuardKind, exprs []expr.Any, chain *nftables.Chain) (*firewall.ForwardAcceptSummary, error) {
	switch guard {
	case guardForwardEgress:
		return parseForwardEgress(exprs, chain)
	case guardForwardReturn:
		return parseForwardReturn(exprs, chain)
	default:
		return nil, invalidObservedState("unsupported forward-accept guard %q", guard)
	}
}

func parseForwardEgress(exprs []expr.Any, chain *nftables.Chain) (*firewall.ForwardAcceptSummary, error) {
	if len(exprs) != 6 {
		return nil, invalidObservedState("forward egress expression count is %d", len(exprs))
	}
	payload, ok := exprs[0].(*expr.Payload)
	if !ok || payload.OperationType != expr.PayloadLoad || payload.DestRegister != regMatch || payload.Base != expr.PayloadBaseNetworkHeader || payload.Offset != 12 || payload.Len != 4 {
		return nil, invalidObservedState("forward egress source payload expression is invalid")
	}
	bitwise, ok := exprs[1].(*expr.Bitwise)
	if !ok || bitwise.SourceRegister != regMatch || bitwise.DestRegister != regMask || bitwise.Len != 4 || len(bitwise.Mask) != 4 || !bytes.Equal(bitwise.Xor, []byte{0, 0, 0, 0}) {
		return nil, invalidObservedState("forward egress mask expression is invalid")
	}
	cmpCIDR, ok := exprs[2].(*expr.Cmp)
	if !ok || cmpCIDR.Op != expr.CmpOpEq || cmpCIDR.Register != regMask || len(cmpCIDR.Data) != 4 {
		return nil, invalidObservedState("forward egress CIDR comparison is invalid")
	}
	meta, ok := exprs[3].(*expr.Meta)
	if !ok || meta.Key != expr.MetaKeyOIFNAME || meta.Register != regMatch || meta.SourceRegister {
		return nil, invalidObservedState("forward egress output interface expression is invalid")
	}
	cmpIface, ok := exprs[4].(*expr.Cmp)
	if !ok || cmpIface.Op != expr.CmpOpEq || cmpIface.Register != regMatch {
		return nil, invalidObservedState("forward egress output interface comparison is invalid")
	}
	verdict, ok := exprs[5].(*expr.Verdict)
	if !ok || verdict.Kind != expr.VerdictAccept || verdict.Chain != "" {
		return nil, invalidObservedState("forward egress accept verdict is invalid")
	}
	return forwardSummaryFromMatch(cmpCIDR.Data, bitwise.Mask, cmpIface.Data, chain)
}

func parseForwardReturn(exprs []expr.Any, chain *nftables.Chain) (*firewall.ForwardAcceptSummary, error) {
	if len(exprs) != 9 {
		return nil, invalidObservedState("forward return expression count is %d", len(exprs))
	}
	payload, ok := exprs[0].(*expr.Payload)
	if !ok || payload.OperationType != expr.PayloadLoad || payload.DestRegister != regMatch || payload.Base != expr.PayloadBaseNetworkHeader || payload.Offset != 16 || payload.Len != 4 {
		return nil, invalidObservedState("forward return destination payload expression is invalid")
	}
	bitwise, ok := exprs[1].(*expr.Bitwise)
	if !ok || bitwise.SourceRegister != regMatch || bitwise.DestRegister != regMask || bitwise.Len != 4 || len(bitwise.Mask) != 4 || !bytes.Equal(bitwise.Xor, []byte{0, 0, 0, 0}) {
		return nil, invalidObservedState("forward return mask expression is invalid")
	}
	cmpCIDR, ok := exprs[2].(*expr.Cmp)
	if !ok || cmpCIDR.Op != expr.CmpOpEq || cmpCIDR.Register != regMask || len(cmpCIDR.Data) != 4 {
		return nil, invalidObservedState("forward return CIDR comparison is invalid")
	}
	meta, ok := exprs[3].(*expr.Meta)
	if !ok || meta.Key != expr.MetaKeyIIFNAME || meta.Register != regMatch || meta.SourceRegister {
		return nil, invalidObservedState("forward return input interface expression is invalid")
	}
	cmpIface, ok := exprs[4].(*expr.Cmp)
	if !ok || cmpIface.Op != expr.CmpOpEq || cmpIface.Register != regMatch {
		return nil, invalidObservedState("forward return input interface comparison is invalid")
	}
	ct, ok := exprs[5].(*expr.Ct)
	if !ok || ct.Key != expr.CtKeySTATE || ct.Register != regProtocol || ct.SourceRegister {
		return nil, invalidObservedState("forward return conntrack state expression is invalid")
	}
	ctMask := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	ctBitwise, ok := exprs[6].(*expr.Bitwise)
	if !ok || ctBitwise.SourceRegister != regProtocol || ctBitwise.DestRegister != regProtocol || ctBitwise.Len != 4 || !bytes.Equal(ctBitwise.Mask, ctMask) || !bytes.Equal(ctBitwise.Xor, []byte{0, 0, 0, 0}) {
		return nil, invalidObservedState("forward return conntrack mask expression is invalid")
	}
	ctCmp, ok := exprs[7].(*expr.Cmp)
	if !ok || ctCmp.Op != expr.CmpOpNeq || ctCmp.Register != regProtocol || !bytes.Equal(ctCmp.Data, []byte{0, 0, 0, 0}) {
		return nil, invalidObservedState("forward return conntrack comparison is invalid")
	}
	verdict, ok := exprs[8].(*expr.Verdict)
	if !ok || verdict.Kind != expr.VerdictAccept || verdict.Chain != "" {
		return nil, invalidObservedState("forward return accept verdict is invalid")
	}
	return forwardSummaryFromMatch(cmpCIDR.Data, bitwise.Mask, cmpIface.Data, chain)
}

func forwardSummaryFromMatch(cidrData []byte, mask []byte, ifaceData []byte, chain *nftables.Chain) (*firewall.ForwardAcceptSummary, error) {
	bits, ok := maskBits(mask)
	if !ok {
		return nil, invalidObservedState("forward-accept CIDR mask is not contiguous")
	}
	addr := netip.AddrFrom4(bytesTo4(cidrData))
	return &firewall.ForwardAcceptSummary{
		GuestCIDR:           netip.PrefixFrom(addr, bits).Masked(),
		EgressInterfaceName: firewall.InterfaceName(interfaceNameFromData(ifaceData)),
		Priority:            priorityFromChain(chain, firewall.PriorityNameForwardFilter),
	}, nil
}
