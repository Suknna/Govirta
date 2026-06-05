//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/suknna/govirta/pkg/hostnet/link"
	"github.com/suknna/govirta/pkg/hostnet/link/linkerr"
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

func TestListResolvesMasterNamesFromSingleDump(t *testing.T) {
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 10}}
	tap0 := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0", Index: 11, MasterIndex: 10}, Mode: netlink.TUNTAP_MODE_TAP}
	tap1 := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap1", Index: 12, MasterIndex: 10}, Mode: netlink.TUNTAP_MODE_TAP}
	fake := newFakeHandle(bridge, tap0, tap1)
	manager := NewManagerWithHandle(fake)

	infos, err := manager.List(context.Background(), link.ListFilter{Kind: link.KindTap})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	for _, info := range infos {
		if info.MasterName != "br0" {
			t.Fatalf("tap %q master name = %q, want br0", info.Name, info.MasterName)
		}
	}
}

// TestListIssuesSingleLinkListRegardlessOfEnslavedCount pins the new contract
// after the O(n²) fix: List dumps links once and resolves every enslaved link's
// master name from that single slice, so the LinkList call count stays constant
// (1) no matter how many TAPs are enslaved. Before the fix, each enslaved link
// triggered a fresh full LinkList (N TAPs => N+1 dumps).
func TestListIssuesSingleLinkListRegardlessOfEnslavedCount(t *testing.T) {
	links := []netlink.Link{&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 10}}}
	for i := 0; i < 8; i++ {
		links = append(links, &netlink.Tuntap{
			LinkAttrs: netlink.LinkAttrs{Name: fmt.Sprintf("tap%d", i), Index: 11 + i, MasterIndex: 10},
			Mode:      netlink.TUNTAP_MODE_TAP,
		})
	}
	fake := newFakeHandle(links...)
	manager := NewManagerWithHandle(fake)

	if _, err := manager.List(context.Background(), link.ListFilter{Kind: link.KindAny}); err != nil {
		t.Fatalf("List failed: %v", err)
	}

	linkListCalls := 0
	for _, call := range fake.calls {
		if call == "LinkList" {
			linkListCalls++
		}
	}
	if linkListCalls != 1 {
		t.Fatalf("LinkList call count = %d, want 1 (O(n²) master resolution regression)", linkListCalls)
	}
}

func namesOf(infos []link.LinkInfo) []link.Name {
	names := make([]link.Name, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}

	return names
}
