//go:build linux

package linux

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/link/linkerr"
	"github.com/vishvananda/netlink"
)

func TestEnsureTapCreatesTapAttachedToBridge(t *testing.T) {
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 10}}
	fake := newFakeHandle(bridge)
	manager := NewManagerWithHandle(fake)
	spec := validTapSpec()

	info, err := manager.EnsureTap(context.Background(), spec)
	if err != nil {
		t.Fatalf("EnsureTap failed: %v", err)
	}

	if info.Name != spec.Name || info.Kind != link.KindTap || info.MTU != spec.MTU || info.AdminState != link.AdminStateUp || info.MasterName != spec.BridgeName {
		t.Fatalf("unexpected info: %+v", info)
	}
	if !reflect.DeepEqual(info.MAC, spec.MAC) {
		t.Fatalf("MAC = %s, want %s", info.MAC, spec.MAC)
	}
	tap, ok := fake.links[string(spec.Name)].(*netlink.Tuntap)
	if !ok {
		t.Fatalf("created link is %T, want *netlink.Tuntap", fake.links[string(spec.Name)])
	}
	if tap.Mode != netlink.TUNTAP_MODE_TAP || tap.Flags&netlink.TUNTAP_NO_PI == 0 || tap.Flags&netlink.TUNTAP_VNET_HDR == 0 || tap.Owner != spec.OwnerUID.Value || tap.Group != spec.OwnerGID.Value {
		t.Fatalf("unexpected tap: %+v", tap)
	}
	assertCallsContain(t, fake.calls, "LinkSetMaster:tap0:br0")
}

func TestEnsureTapIsIdempotent(t *testing.T) {
	spec := validTapSpec()
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 10}}
	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: "tap0", Index: 11, MasterIndex: 10},
		Mode:      netlink.TUNTAP_MODE_TAP,
		Flags:     netlink.TUNTAP_NO_PI | netlink.TUNTAP_VNET_HDR,
		Owner:     spec.OwnerUID.Value,
		Group:     spec.OwnerGID.Value,
	}
	fake := newFakeHandle(bridge, tap)
	manager := NewManagerWithHandle(fake)

	info, err := manager.EnsureTap(context.Background(), spec)
	if err != nil {
		t.Fatalf("EnsureTap failed: %v", err)
	}
	if info.Index != 11 || info.MasterName != "br0" {
		t.Fatalf("unexpected info: %+v", info)
	}
	for _, call := range fake.calls {
		if call == "LinkAdd:tap0" {
			t.Fatalf("existing tap should not be recreated: %v", fake.calls)
		}
	}
}

func TestEnsureTapRejectsExistingNonTap(t *testing.T) {
	fake := newFakeHandle(
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}},
		&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "tap0"}},
	)
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureTap(context.Background(), validTapSpec())
	if !errors.Is(err, linkerr.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestEnsureTapRejectsExistingTunMode(t *testing.T) {
	fake := newFakeHandle(
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}},
		&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0"}, Mode: netlink.TUNTAP_MODE_TUN},
	)
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureTap(context.Background(), validTapSpec())
	if !errors.Is(err, linkerr.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestEnsureTapRejectsExistingOwnerMismatch(t *testing.T) {
	spec := validTapSpec()
	fake := newFakeHandle(
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}},
		&netlink.Tuntap{
			LinkAttrs: netlink.LinkAttrs{Name: "tap0"},
			Mode:      netlink.TUNTAP_MODE_TAP,
			Flags:     netlink.TUNTAP_NO_PI | netlink.TUNTAP_VNET_HDR,
			Owner:     spec.OwnerUID.Value + 1,
			Group:     spec.OwnerGID.Value,
		},
	)
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureTap(context.Background(), spec)
	if !errors.Is(err, linkerr.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	assertCallsDoNotContain(t, fake.calls, "LinkAdd:tap0")
	assertCallsDoNotContainPrefix(t, fake.calls, "LinkSetHardwareAddr:tap0")
	assertCallsDoNotContainPrefix(t, fake.calls, "LinkSetMTU:tap0")
	assertCallsDoNotContain(t, fake.calls, "LinkSetMaster:tap0:br0")
	assertCallsDoNotContain(t, fake.calls, "LinkSetUp:tap0")
}

func TestEnsureTapRejectsExistingGroupMismatch(t *testing.T) {
	spec := validTapSpec()
	fake := newFakeHandle(
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}},
		&netlink.Tuntap{
			LinkAttrs: netlink.LinkAttrs{Name: "tap0"},
			Mode:      netlink.TUNTAP_MODE_TAP,
			Flags:     netlink.TUNTAP_NO_PI | netlink.TUNTAP_VNET_HDR,
			Owner:     spec.OwnerUID.Value,
			Group:     spec.OwnerGID.Value + 1,
		},
	)
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureTap(context.Background(), spec)
	if !errors.Is(err, linkerr.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	assertCallsDoNotContain(t, fake.calls, "LinkAdd:tap0")
	assertCallsDoNotContainPrefix(t, fake.calls, "LinkSetHardwareAddr:tap0")
	assertCallsDoNotContainPrefix(t, fake.calls, "LinkSetMTU:tap0")
	assertCallsDoNotContain(t, fake.calls, "LinkSetMaster:tap0:br0")
	assertCallsDoNotContain(t, fake.calls, "LinkSetUp:tap0")
}

func TestEnsureTapRejectsVNetHeaderConflict(t *testing.T) {
	spec := validTapSpec()
	fake := newFakeHandle(
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}},
		&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0"}, Mode: netlink.TUNTAP_MODE_TAP, Flags: netlink.TUNTAP_NO_PI},
	)
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureTap(context.Background(), spec)
	if !errors.Is(err, linkerr.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestEnsureTapReturnsUnsupportedWhenVNetHeaderCannotBeObserved(t *testing.T) {
	oldObserve := observeTapVNetHeader
	observeTapVNetHeader = func(netlink.Link) vnetHeaderObserved { return vnetHeaderObservedUnknown }
	t.Cleanup(func() { observeTapVNetHeader = oldObserve })
	// Owner/Group must match validTapSpec() (1000/1000) so the identity-conflict
	// checks in ensureTapLink pass and execution reaches the VNetHeader
	// observation path this test exercises.
	fake := newFakeHandle(
		&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}},
		&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0"}, Mode: netlink.TUNTAP_MODE_TAP, Owner: 1000, Group: 1000},
	)
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureTap(context.Background(), validTapSpec())
	if !errors.Is(err, linkerr.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestEnsureTapRollsBackCreatedTapOnMasterFailure(t *testing.T) {
	fake := newFakeHandle(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 10}})
	fake.injectFailure("LinkSetMaster", errors.New("master failed"))
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureTap(context.Background(), validTapSpec())
	if err == nil {
		t.Fatalf("expected error")
	}
	if _, ok := fake.links["tap0"]; ok {
		t.Fatalf("created tap was not rolled back")
	}
	assertCallsContain(t, fake.calls, "LinkDel:tap0")
}

func TestEnsureTapJoinsRollbackFailure(t *testing.T) {
	primary := errors.New("master failed")
	rollback := errors.New("delete failed")
	fake := newFakeHandle(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 10}})
	fake.injectFailure("LinkSetMaster", primary)
	fake.injectFailure("LinkDel", rollback)
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureTap(context.Background(), validTapSpec())
	if !errors.Is(err, primary) || !errors.Is(err, rollback) {
		t.Fatalf("expected joined primary and rollback errors, got %v", err)
	}
}

func TestEnsureTapPropagatesFinalLinkInfoMasterLookupError(t *testing.T) {
	fake := newFakeHandle(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 10}})
	fake.injectFailure("LinkList", netlink.ErrDumpInterrupted)
	manager := NewManagerWithHandle(fake)

	_, err := manager.EnsureTap(context.Background(), validTapSpec())
	if !errors.Is(err, linkerr.ErrIncompleteList) {
		t.Fatalf("expected ErrIncompleteList, got %v", err)
	}
	if _, ok := fake.links["tap0"]; !ok {
		t.Fatalf("final observation failure should not roll back configured tap")
	}
}
