//go:build linux

package linux

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
)

func forwardAcceptTestSpec() firewall.ForwardAcceptSpec {
	return firewall.ForwardAcceptSpec{
		TableName:           "govirta-filter",
		ChainName:           "govirta-forward",
		RuleOwner:           "net-test",
		GuestCIDR:           netip.MustParsePrefix("192.168.100.0/24"),
		EgressInterfaceName: "eth0",
		Priority:            firewall.ExplicitPriority(0, firewall.PriorityNameForwardFilter),
	}
}

func TestForwardAcceptEnsureCreatesGroup(t *testing.T) {
	fh := &fakeHandle{}
	mgr := NewManagerWithHandle(fh)
	spec := forwardAcceptTestSpec()

	info, err := mgr.EnsureForwardAccept(context.Background(), spec)
	if err != nil {
		t.Fatalf("EnsureForwardAccept: %v", err)
	}
	if info.Summary.ForwardAccept == nil {
		t.Fatalf("expected forward-accept summary, got %+v", info.Summary)
	}
	if info.Summary.ForwardAccept.GuestCIDR != spec.GuestCIDR.Masked() {
		t.Fatalf("summary GuestCIDR = %s, want %s", info.Summary.ForwardAccept.GuestCIDR, spec.GuestCIDR.Masked())
	}
	if info.Summary.ForwardAccept.EgressInterfaceName != spec.EgressInterfaceName {
		t.Fatalf("summary egress = %q, want %q", info.Summary.ForwardAccept.EgressInterfaceName, spec.EgressInterfaceName)
	}
	if info.Ref.Purpose != firewall.RulePurposeForwardAccept {
		t.Fatalf("ref purpose = %q, want forward-accept", info.Ref.Purpose)
	}

	rules, err := mgr.ListRules(context.Background(), firewall.RuleFilter{
		Owner:   firewall.FilterOwner(spec.RuleOwner),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeForwardAccept),
		Family:  firewall.AnyFamily(),
		Table:   firewall.AnyTable(),
		Chain:   firewall.AnyChain(),
	})
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 logical forward-accept rule, got %d", len(rules))
	}
}

func TestForwardAcceptEnsureIsIdempotent(t *testing.T) {
	fh := &fakeHandle{}
	mgr := NewManagerWithHandle(fh)
	spec := forwardAcceptTestSpec()

	first, err := mgr.EnsureForwardAccept(context.Background(), spec)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	second, err := mgr.EnsureForwardAccept(context.Background(), spec)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if first.Ref.Handle != second.Ref.Handle {
		t.Fatalf("idempotent ensure changed handle: %d -> %d", first.Ref.Handle, second.Ref.Handle)
	}
}

func TestForwardAcceptEnsureConflictOnDifferentEgress(t *testing.T) {
	fh := &fakeHandle{}
	mgr := NewManagerWithHandle(fh)
	spec := forwardAcceptTestSpec()
	if _, err := mgr.EnsureForwardAccept(context.Background(), spec); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	conflicting := spec
	conflicting.EgressInterfaceName = "eth1"
	_, err := mgr.EnsureForwardAccept(context.Background(), conflicting)
	if !errors.Is(err, firewallerr.ErrConflict) {
		t.Fatalf("conflicting egress ensure = %v, want ErrConflict", err)
	}
}

func TestForwardAcceptDeleteRemovesGroup(t *testing.T) {
	fh := &fakeHandle{}
	mgr := NewManagerWithHandle(fh)
	spec := forwardAcceptTestSpec()
	info, err := mgr.EnsureForwardAccept(context.Background(), spec)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := mgr.DeleteForwardAccept(context.Background(), info.Ref); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rules, err := mgr.ListRules(context.Background(), firewall.RuleFilter{
		Owner:   firewall.FilterOwner(spec.RuleOwner),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeForwardAccept),
		Family:  firewall.AnyFamily(),
		Table:   firewall.AnyTable(),
		Chain:   firewall.AnyChain(),
	})
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules after delete, got %d", len(rules))
	}
}

func TestForwardAcceptValidationRejectsZeroCIDR(t *testing.T) {
	fh := &fakeHandle{}
	mgr := NewManagerWithHandle(fh)
	spec := forwardAcceptTestSpec()
	spec.GuestCIDR = netip.Prefix{}
	_, err := mgr.EnsureForwardAccept(context.Background(), spec)
	if !errors.Is(err, firewallerr.ErrInvalidRequest) {
		t.Fatalf("invalid CIDR ensure = %v, want ErrInvalidRequest", err)
	}
}
