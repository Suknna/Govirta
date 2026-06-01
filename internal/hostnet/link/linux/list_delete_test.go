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

func TestDeleteMissingLinkIsIdempotent(t *testing.T) {
	fake := newFakeHandle()
	manager := NewManagerWithHandle(fake)

	if err := manager.Delete(context.Background(), "missing0"); err != nil {
		t.Fatalf("Delete missing failed: %v", err)
	}
}

func TestExistsMissingLinkReturnsFalse(t *testing.T) {
	fake := newFakeHandle()
	manager := NewManagerWithHandle(fake)

	exists, err := manager.Exists(context.Background(), "missing0")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if exists {
		t.Fatalf("exists = true, want false")
	}
}

func TestGetMissingLinkReturnsNotFound(t *testing.T) {
	fake := newFakeHandle()
	manager := NewManagerWithHandle(fake)

	_, err := manager.Get(context.Background(), "missing0")
	if !errors.Is(err, linkerr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListRequiresExplicitKind(t *testing.T) {
	fake := newFakeHandle()
	manager := NewManagerWithHandle(fake)

	_, err := manager.List(context.Background(), link.ListFilter{})
	if !errors.Is(err, linkerr.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestListFiltersAndSortsBridgeAndTap(t *testing.T) {
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 10}}
	tap2 := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap2", Index: 12, MasterIndex: 10}, Mode: netlink.TUNTAP_MODE_TAP}
	tap1 := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap1", Index: 11, MasterIndex: 10}, Mode: netlink.TUNTAP_MODE_TAP}
	dummy := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "dummy0", Index: 13}}
	fake := newFakeHandle(tap2, dummy, bridge, tap1)
	manager := NewManagerWithHandle(fake)

	bridges, err := manager.List(context.Background(), link.ListFilter{Kind: link.KindBridge})
	if err != nil {
		t.Fatalf("List bridges failed: %v", err)
	}
	if got, want := namesOf(bridges), []link.Name{"br0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bridge names = %v, want %v", got, want)
	}
	taps, err := manager.List(context.Background(), link.ListFilter{Kind: link.KindTap})
	if err != nil {
		t.Fatalf("List taps failed: %v", err)
	}
	if got, want := namesOf(taps), []link.Name{"tap1", "tap2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tap names = %v, want %v", got, want)
	}
	all, err := manager.List(context.Background(), link.ListFilter{Kind: link.KindAny})
	if err != nil {
		t.Fatalf("List all failed: %v", err)
	}
	if got, want := namesOf(all), []link.Name{"br0", "tap1", "tap2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all names = %v, want %v", got, want)
	}
}

func TestListReturnsIncompleteListOnDumpInterrupted(t *testing.T) {
	fake := newFakeHandle(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}})
	fake.injectFailure("LinkList", netlink.ErrDumpInterrupted)
	manager := NewManagerWithHandle(fake)

	_, err := manager.List(context.Background(), link.ListFilter{Kind: link.KindAny})
	if !errors.Is(err, linkerr.ErrIncompleteList) {
		t.Fatalf("expected ErrIncompleteList, got %v", err)
	}
}

func TestGetPropagatesMasterLookupDumpInterrupted(t *testing.T) {
	tap := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0", MasterIndex: 10}, Mode: netlink.TUNTAP_MODE_TAP}
	fake := newFakeHandle(tap)
	fake.injectFailure("LinkList", netlink.ErrDumpInterrupted)
	manager := NewManagerWithHandle(fake)

	_, err := manager.Get(context.Background(), "tap0")
	if !errors.Is(err, linkerr.ErrIncompleteList) {
		t.Fatalf("expected ErrIncompleteList, got %v", err)
	}
}

func TestListDoesNotSilentlyDropMasterNameOnLinkListError(t *testing.T) {
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 10}}
	tap := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0", MasterIndex: 10}, Mode: netlink.TUNTAP_MODE_TAP}
	fake := newFakeHandle(bridge, tap)
	fake.injectFailure("LinkList", nil)
	fake.injectFailure("LinkList", netlink.ErrDumpInterrupted)
	manager := NewManagerWithHandle(fake)

	_, err := manager.List(context.Background(), link.ListFilter{Kind: link.KindTap})
	if !errors.Is(err, linkerr.ErrIncompleteList) {
		t.Fatalf("expected ErrIncompleteList instead of partial info without master, got %v", err)
	}
}

func namesOf(infos []link.LinkInfo) []link.Name {
	names := make([]link.Name, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}

	return names
}
