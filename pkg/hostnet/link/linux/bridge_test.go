//go:build linux

package linux

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/suknna/govirta/pkg/hostnet/link"
	"github.com/suknna/govirta/pkg/hostnet/link/linkerr"
	"github.com/vishvananda/netlink"
)

func TestEnsureBridgeCreatesBridge(t *testing.T) {
	fake := newFakeHandle()
	manager := NewManagerWithHandle(fake)
	spec := validBridgeSpec()

	info, err := manager.EnsureBridge(context.Background(), spec)
	if err != nil {
		t.Fatalf("EnsureBridge failed: %v", err)
	}

	if info.Name != spec.Name || info.Kind != link.KindBridge || info.MTU != spec.MTU || info.AdminState != link.AdminStateUp {
		t.Fatalf("unexpected info: %+v", info)
	}
	if !reflect.DeepEqual(info.MAC, spec.MAC) {
		t.Fatalf("MAC = %s, want %s", info.MAC, spec.MAC)
	}
	if !reflect.DeepEqual(info.Addresses, []string{spec.GatewayCIDR}) {
		t.Fatalf("addresses = %v, want %v", info.Addresses, []string{spec.GatewayCIDR})
	}
	assertCalls(t, fake.calls, []string{
		"LinkByName:br0",
		"LinkAdd:br0",
		"LinkSetHardwareAddr:br0:02:00:00:00:00:01",
		"LinkSetMTU:br0:1500",
		"AddrReplace:br0:192.0.2.1/24",
		"AddrList:br0:2",
		"LinkSetUp:br0",
		"LinkByName:br0",
		"AddrList:br0:0",
	})
}

func TestEnsureBridgeIsIdempotent(t *testing.T) {
	spec := validBridgeSpec()
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: string(spec.Name), Index: 10}}
	fake := newFakeHandle(bridge)
	fake.addresses[string(spec.Name)] = []netlink.Addr{*mustParseAddr(t, spec.GatewayCIDR)}
	manager := NewManagerWithHandle(fake)

	info, err := manager.EnsureBridge(context.Background(), spec)
	if err != nil {
		t.Fatalf("EnsureBridge failed: %v", err)
	}

	if info.Index != 10 || info.Kind != link.KindBridge || info.AdminState != link.AdminStateUp {
		t.Fatalf("unexpected info: %+v", info)
	}
	for _, call := range fake.calls {
		if call == "LinkAdd:br0" {
			t.Fatalf("existing bridge should not be recreated: %v", fake.calls)
		}
	}
}

func TestEnsureBridgePrunesStaleGatewayAddress(t *testing.T) {
	// Reconcile convergence regression: AddrReplace only adds/updates the desired
	// gateway, so without pruning a changed GatewayCIDR would leave the previous
	// gateway on the bridge and observed Addresses would diverge from spec. The
	// fake models real netlink AddrReplace/AddrDel semantics (preserving other
	// addresses), so this test fails before the prune fix and passes after.
	specA := validBridgeSpec()
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: string(specA.Name), Index: 10}}
	fake := newFakeHandle(bridge)
	manager := NewManagerWithHandle(fake)

	if _, err := manager.EnsureBridge(context.Background(), specA); err != nil {
		t.Fatalf("EnsureBridge(gwA) failed: %v", err)
	}
	// A link-local address that the prune must leave untouched.
	fake.addresses[string(specA.Name)] = append(fake.addresses[string(specA.Name)], *mustParseAddr(t, "169.254.1.1/16"))

	specB := specA
	specB.GatewayCIDR = "192.0.2.129/25"
	fake.calls = nil
	info, err := manager.EnsureBridge(context.Background(), specB)
	if err != nil {
		t.Fatalf("EnsureBridge(gwB) failed: %v", err)
	}

	wantAddresses := []string{"169.254.1.1/16", "192.0.2.129/25"}
	if !reflect.DeepEqual(info.Addresses, wantAddresses) {
		t.Fatalf("addresses = %v, want %v (stale gateway pruned, link-local kept)", info.Addresses, wantAddresses)
	}
	assertCallsContain(t, fake.calls, "AddrDel:br0:192.0.2.1/24")
}

func TestEnsureBridgePruneFailureIsReturned(t *testing.T) {
	specA := validBridgeSpec()
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: string(specA.Name), Index: 10}}
	fake := newFakeHandle(bridge)
	manager := NewManagerWithHandle(fake)
	if _, err := manager.EnsureBridge(context.Background(), specA); err != nil {
		t.Fatalf("EnsureBridge(gwA) failed: %v", err)
	}

	prune := errors.New("address delete failed")
	fake.injectFailure("AddrDel", prune)
	specB := specA
	specB.GatewayCIDR = "192.0.2.129/25"

	if _, err := manager.EnsureBridge(context.Background(), specB); !errors.Is(err, prune) {
		t.Fatalf("EnsureBridge(gwB) error = %v, want prune failure", err)
	}
}

func TestEnsureBridgeRejectsExistingNonBridge(t *testing.T) {
	fake := newFakeHandle(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "br0"}})
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureBridge(context.Background(), validBridgeSpec())
	if !errors.Is(err, linkerr.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestEnsureBridgeRollsBackCreatedBridgeOnAddressFailure(t *testing.T) {
	fake := newFakeHandle()
	fake.injectFailure("AddrReplace", errors.New("address failed"))
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureBridge(context.Background(), validBridgeSpec())
	if err == nil {
		t.Fatalf("expected error")
	}
	if _, ok := fake.links["br0"]; ok {
		t.Fatalf("created bridge was not rolled back")
	}
	assertCallsContain(t, fake.calls, "LinkDel:br0")
}

func TestEnsureBridgeJoinsRollbackFailure(t *testing.T) {
	primary := errors.New("address failed")
	rollback := errors.New("delete failed")
	fake := newFakeHandle()
	fake.injectFailure("AddrReplace", primary)
	fake.injectFailure("LinkDel", rollback)
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureBridge(context.Background(), validBridgeSpec())
	if !errors.Is(err, primary) || !errors.Is(err, rollback) {
		t.Fatalf("expected joined primary and rollback errors, got %v", err)
	}
}

func TestEnsureBridgeRollsBackCreatedBridgeWhenConfigurationContextIsCanceled(t *testing.T) {
	fake := newFakeHandle()
	ctx, cancel := context.WithCancel(context.Background())
	fake.injectFailure("LinkSetHardwareAddr", cancelingError{cancel: cancel})
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureBridge(ctx, validBridgeSpec())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if _, ok := fake.links["br0"]; ok {
		t.Fatalf("created bridge was not rolled back after cancellation")
	}
	assertCallsContain(t, fake.calls, "LinkDel:br0")
}

func TestNewManagerWithHandleNilUsesRealHandle(t *testing.T) {
	manager := NewManagerWithHandle(nil)
	if manager.handle == nil {
		t.Fatalf("expected non-nil handle")
	}
	if _, ok := manager.handle.(realHandle); !ok {
		t.Fatalf("handle = %T, want realHandle", manager.handle)
	}
}

func TestPermissionErrorsTranslateToPermission(t *testing.T) {
	fake := newFakeHandle()
	fake.injectFailure("LinkAdd", syscall.EPERM)
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureBridge(context.Background(), validBridgeSpec())
	if !errors.Is(err, linkerr.ErrPermission) {
		t.Fatalf("expected ErrPermission, got %v", err)
	}
}

func TestCanceledContextForEveryManagerMethod(t *testing.T) {
	fake := newFakeHandle(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}})
	manager := NewManagerWithHandle(fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		call func() error
	}{
		{name: "EnsureBridge", call: func() error { _, err := manager.EnsureBridge(ctx, validBridgeSpec()); return err }},
		{name: "EnsureTap", call: func() error { _, err := manager.EnsureTap(ctx, validTapSpec()); return err }},
		{name: "Delete", call: func() error { return manager.Delete(ctx, "br0") }},
		{name: "Exists", call: func() error { _, err := manager.Exists(ctx, "br0"); return err }},
		{name: "Get", call: func() error { _, err := manager.Get(ctx, "br0"); return err }},
		{name: "List", call: func() error { _, err := manager.List(ctx, link.ListFilter{Kind: link.KindAny}); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled, got %v", err)
			}
		})
	}
}

func validBridgeSpec() link.BridgeSpec {
	return link.BridgeSpec{
		Name:        "br0",
		GatewayCIDR: "192.0.2.1/24",
		MTU:         1500,
		MAC:         net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01},
	}
}

func validTapSpec() link.TapSpec {
	return link.TapSpec{
		Name:       "tap0",
		BridgeName: "br0",
		OwnerUID:   link.ExplicitUID(1000),
		OwnerGID:   link.ExplicitGID(1000),
		MTU:        1500,
		MAC:        net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02},
		VNetHeader: link.VNetHeaderEnabled,
	}
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func assertCallsContain(t *testing.T, calls []string, want string) {
	t.Helper()
	for _, call := range calls {
		if call == want {
			return
		}
	}
	t.Fatalf("calls %v do not contain %q", calls, want)
}

func assertCallsDoNotContain(t *testing.T, calls []string, forbidden string) {
	t.Helper()
	for _, call := range calls {
		if call == forbidden {
			t.Fatalf("calls %v contain forbidden call %q", calls, forbidden)
		}
	}
}

func assertCallsDoNotContainPrefix(t *testing.T, calls []string, forbiddenPrefix string) {
	t.Helper()
	for _, call := range calls {
		if strings.HasPrefix(call, forbiddenPrefix) {
			t.Fatalf("calls %v contain forbidden prefix %q", calls, forbiddenPrefix)
		}
	}
}

type cancelingError struct {
	cancel context.CancelFunc
}

func (err cancelingError) Error() string {
	err.cancel()

	return context.Canceled.Error()
}

func (err cancelingError) Unwrap() error {
	err.cancel()

	return context.Canceled
}
