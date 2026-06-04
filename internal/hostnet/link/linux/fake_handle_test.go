//go:build linux

package linux

import (
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"syscall"
	"testing"

	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/link/linkerr"
	"github.com/vishvananda/netlink"
)

type fakeHandle struct {
	links     map[string]netlink.Link
	addresses map[string][]netlink.Addr
	calls     []string
	failures  map[string][]error
	nextIndex int
}

func newFakeHandle(links ...netlink.Link) *fakeHandle {
	fake := &fakeHandle{
		links:     make(map[string]netlink.Link),
		addresses: make(map[string][]netlink.Addr),
		failures:  make(map[string][]error),
		nextIndex: 1,
	}
	for _, nlLink := range links {
		attrs := nlLink.Attrs()
		if attrs.Index >= fake.nextIndex {
			fake.nextIndex = attrs.Index + 1
		}
		fake.links[nlLink.Attrs().Name] = nlLink
	}

	return fake
}

func (f *fakeHandle) injectFailure(op string, err error) {
	f.failures[op] = append(f.failures[op], err)
}

func (f *fakeHandle) LinkByName(name string) (netlink.Link, error) {
	f.record("LinkByName:" + name)
	if err := f.nextFailure("LinkByName"); err != nil {
		return nil, err
	}
	nlLink, ok := f.links[name]
	if !ok {
		return nil, fakeNotFound(name)
	}

	return nlLink, nil
}

func (f *fakeHandle) LinkAdd(nlLink netlink.Link) error {
	name := nlLink.Attrs().Name
	f.record("LinkAdd:" + name)
	if err := f.nextFailure("LinkAdd"); err != nil {
		return err
	}
	if _, ok := f.links[name]; ok {
		return fmt.Errorf("fake link %q: %w", name, linkerr.ErrAlreadyExists)
	}
	if nlLink.Attrs().Index == 0 {
		nlLink.Attrs().Index = f.nextIndex
		f.nextIndex++
	} else if nlLink.Attrs().Index >= f.nextIndex {
		f.nextIndex = nlLink.Attrs().Index + 1
	}
	f.links[name] = nlLink

	return nil
}

func (f *fakeHandle) LinkDel(nlLink netlink.Link) error {
	name := nlLink.Attrs().Name
	f.record("LinkDel:" + name)
	if err := f.nextFailure("LinkDel"); err != nil {
		return err
	}
	if _, ok := f.links[name]; !ok {
		return fakeNotFound(name)
	}
	delete(f.links, name)
	delete(f.addresses, name)

	return nil
}

func (f *fakeHandle) LinkSetUp(nlLink netlink.Link) error {
	name := nlLink.Attrs().Name
	f.record("LinkSetUp:" + name)
	if err := f.nextFailure("LinkSetUp"); err != nil {
		return err
	}
	if _, ok := f.links[name]; !ok {
		return fakeNotFound(name)
	}
	nlLink.Attrs().Flags |= net.FlagUp

	return nil
}

func (f *fakeHandle) LinkSetMTU(nlLink netlink.Link, mtu int) error {
	name := nlLink.Attrs().Name
	f.record(fmt.Sprintf("LinkSetMTU:%s:%d", name, mtu))
	if err := f.nextFailure("LinkSetMTU"); err != nil {
		return err
	}
	if _, ok := f.links[name]; !ok {
		return fakeNotFound(name)
	}
	nlLink.Attrs().MTU = mtu

	return nil
}

func (f *fakeHandle) LinkSetHardwareAddr(nlLink netlink.Link, hwaddr net.HardwareAddr) error {
	name := nlLink.Attrs().Name
	f.record("LinkSetHardwareAddr:" + name + ":" + hwaddr.String())
	if err := f.nextFailure("LinkSetHardwareAddr"); err != nil {
		return err
	}
	if _, ok := f.links[name]; !ok {
		return fakeNotFound(name)
	}
	nlLink.Attrs().HardwareAddr = cloneHardwareAddr(hwaddr)

	return nil
}

func (f *fakeHandle) LinkSetMaster(nlLink netlink.Link, master netlink.Link) error {
	name := nlLink.Attrs().Name
	masterName := master.Attrs().Name
	f.record("LinkSetMaster:" + name + ":" + masterName)
	if err := f.nextFailure("LinkSetMaster"); err != nil {
		return err
	}
	if _, ok := f.links[name]; !ok {
		return fakeNotFound(name)
	}
	if _, ok := f.links[masterName]; !ok {
		return fakeNotFound(masterName)
	}
	nlLink.Attrs().MasterIndex = master.Attrs().Index

	return nil
}

func (f *fakeHandle) AddrReplace(nlLink netlink.Link, addr *netlink.Addr) error {
	name := nlLink.Attrs().Name
	if addr == nil || addr.IPNet == nil {
		f.record("AddrReplace:" + name + ":<invalid>")
		return fmt.Errorf("fake link %q address is required: %w", name, linkerr.ErrInvalidRequest)
	}
	f.record("AddrReplace:" + name + ":" + addr.IPNet.String())
	if err := f.nextFailure("AddrReplace"); err != nil {
		return err
	}
	if _, ok := f.links[name]; !ok {
		return fakeNotFound(name)
	}
	// Mirror real netlink AddrReplace: add the prefix if absent, replace the
	// matching prefix if present, and leave every other address untouched. The
	// previous wipe-all behavior hid the stale-address divergence the prune fix
	// targets (a changed GatewayCIDR must not leave the old gateway behind).
	existing := f.addresses[name]
	for i := range existing {
		if existing[i].IPNet != nil && existing[i].IP.Equal(addr.IP) && string(existing[i].Mask) == string(addr.Mask) {
			existing[i] = *addr
			f.addresses[name] = existing
			return nil
		}
	}
	f.addresses[name] = append(existing, *addr)

	return nil
}

func (f *fakeHandle) AddrDel(nlLink netlink.Link, addr *netlink.Addr) error {
	name := nlLink.Attrs().Name
	if addr == nil || addr.IPNet == nil {
		f.record("AddrDel:" + name + ":<invalid>")
		return fmt.Errorf("fake link %q address is required: %w", name, linkerr.ErrInvalidRequest)
	}
	f.record("AddrDel:" + name + ":" + addr.IPNet.String())
	if err := f.nextFailure("AddrDel"); err != nil {
		return err
	}
	if _, ok := f.links[name]; !ok {
		return fakeNotFound(name)
	}
	existing := f.addresses[name]
	for i := range existing {
		if existing[i].IPNet != nil && existing[i].IP.Equal(addr.IP) && string(existing[i].Mask) == string(addr.Mask) {
			f.addresses[name] = append(existing[:i:i], existing[i+1:]...)
			return nil
		}
	}

	return fakeNotFound(name)
}

func (f *fakeHandle) LinkList() ([]netlink.Link, error) {
	f.record("LinkList")
	if err := f.nextFailure("LinkList"); err != nil {
		return nil, err
	}
	links := make([]netlink.Link, 0, len(f.links))
	for _, nlLink := range f.links {
		links = append(links, nlLink)
	}

	return links, nil
}

func (f *fakeHandle) AddrList(nlLink netlink.Link, family int) ([]netlink.Addr, error) {
	name := nlLink.Attrs().Name
	f.record(fmt.Sprintf("AddrList:%s:%d", name, family))
	if err := f.nextFailure("AddrList"); err != nil {
		return nil, err
	}
	if _, ok := f.links[name]; !ok {
		return nil, fakeNotFound(name)
	}
	addresses := f.addresses[name]
	copyAddresses := make([]netlink.Addr, len(addresses))
	copy(copyAddresses, addresses)

	return copyAddresses, nil
}

func (f *fakeHandle) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeHandle) nextFailure(op string) error {
	failures := f.failures[op]
	if len(failures) == 0 {
		return nil
	}
	f.failures[op] = failures[1:]

	return failures[0]
}

func fakeNotFound(name string) error {
	return fmt.Errorf("fake link %q: %w", name, linkerr.ErrNotFound)
}

func TestFakeHandleRecordsCallsAndMutatesLinks(t *testing.T) {
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 10}}
	tap := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0", Index: 11}, Mode: netlink.TUNTAP_MODE_TAP}
	fake := newFakeHandle(bridge, tap)
	mac := net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02}
	addr := mustParseAddr(t, "192.0.2.1/24")

	if err := fake.LinkSetUp(tap); err != nil {
		t.Fatalf("LinkSetUp failed: %v", err)
	}
	if err := fake.LinkSetMTU(tap, 1450); err != nil {
		t.Fatalf("LinkSetMTU failed: %v", err)
	}
	if err := fake.LinkSetHardwareAddr(tap, mac); err != nil {
		t.Fatalf("LinkSetHardwareAddr failed: %v", err)
	}
	if err := fake.LinkSetMaster(tap, bridge); err != nil {
		t.Fatalf("LinkSetMaster failed: %v", err)
	}
	if err := fake.AddrReplace(tap, addr); err != nil {
		t.Fatalf("AddrReplace failed: %v", err)
	}

	wantCalls := []string{
		"LinkSetUp:tap0",
		"LinkSetMTU:tap0:1450",
		"LinkSetHardwareAddr:tap0:02:00:00:00:00:02",
		"LinkSetMaster:tap0:br0",
		"AddrReplace:tap0:192.0.2.1/24",
	}
	if !reflect.DeepEqual(fake.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", fake.calls, wantCalls)
	}
	if tap.Attrs().Flags&net.FlagUp == 0 {
		t.Fatalf("expected tap to be up")
	}
	if tap.Attrs().MTU != 1450 {
		t.Fatalf("MTU = %d, want 1450", tap.Attrs().MTU)
	}
	if !reflect.DeepEqual(tap.Attrs().HardwareAddr, mac) {
		t.Fatalf("MAC = %v, want %v", tap.Attrs().HardwareAddr, mac)
	}
	if tap.Attrs().MasterIndex != bridge.Attrs().Index {
		t.Fatalf("MasterIndex = %d, want %d", tap.Attrs().MasterIndex, bridge.Attrs().Index)
	}
}

func TestFakeHandleMissingLinkWrapsNotFound(t *testing.T) {
	fake := newFakeHandle()
	_, err := fake.LinkByName("missing0")
	if !errors.Is(err, linkerr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestFakeHandleLinkAddAssignsPositiveIndex(t *testing.T) {
	existing := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 7}}
	fake := newFakeHandle(existing)
	first := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0"}, Mode: netlink.TUNTAP_MODE_TAP}
	second := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap1"}, Mode: netlink.TUNTAP_MODE_TAP}

	if err := fake.LinkAdd(first); err != nil {
		t.Fatalf("LinkAdd first failed: %v", err)
	}
	if err := fake.LinkAdd(second); err != nil {
		t.Fatalf("LinkAdd second failed: %v", err)
	}

	if first.Attrs().Index != 8 {
		t.Fatalf("first index = %d, want 8", first.Attrs().Index)
	}
	if second.Attrs().Index != 9 {
		t.Fatalf("second index = %d, want 9", second.Attrs().Index)
	}
}

func TestFakeHandleAddrReplaceRejectsInvalidAddress(t *testing.T) {
	tap := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0"}, Mode: netlink.TUNTAP_MODE_TAP}
	fake := newFakeHandle(tap)
	tests := []struct {
		name string
		addr *netlink.Addr
	}{
		{name: "nil addr", addr: nil},
		{name: "nil IPNet", addr: &netlink.Addr{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fake.AddrReplace(tap, tt.addr)
			if !errors.Is(err, linkerr.ErrInvalidRequest) {
				t.Fatalf("expected ErrInvalidRequest, got %v", err)
			}
		})
	}
}

func TestFakeHandleInjectedDumpInterrupted(t *testing.T) {
	fake := newFakeHandle(&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0"}})
	fake.injectFailure("LinkList", netlink.ErrDumpInterrupted)

	_, err := fake.LinkList()
	if !errors.Is(err, netlink.ErrDumpInterrupted) {
		t.Fatalf("expected ErrDumpInterrupted, got %v", err)
	}
}

func TestTranslateErrorMapsLinuxAndNetlinkClasses(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		want     error
		original error
	}{
		{name: "preserve sentinel", err: fmt.Errorf("wrapped: %w", linkerr.ErrConflict), want: linkerr.ErrConflict},
		{name: "permission os", err: osPermissionErr(), want: linkerr.ErrPermission, original: os.ErrPermission},
		{name: "permission eperm", err: syscall.EPERM, want: linkerr.ErrPermission, original: syscall.EPERM},
		{name: "permission eacces", err: syscall.EACCES, want: linkerr.ErrPermission, original: syscall.EACCES},
		{name: "exists", err: syscall.EEXIST, want: linkerr.ErrAlreadyExists, original: syscall.EEXIST},
		{name: "invalid", err: syscall.EINVAL, want: linkerr.ErrInvalidRequest, original: syscall.EINVAL},
		{name: "dump interrupted", err: netlink.ErrDumpInterrupted, want: linkerr.ErrIncompleteList, original: netlink.ErrDumpInterrupted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := translateError("test op", tt.err)
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
			if tt.original != nil && !errors.Is(err, tt.original) {
				t.Fatalf("expected original %v to be preserved, got %v", tt.original, err)
			}
		})
	}
}

func TestKindOfRecognizesBridgeAndTapOnly(t *testing.T) {
	tests := []struct {
		name string
		link netlink.Link
		want link.Kind
		err  error
	}{
		{name: "bridge", link: &netlink.Bridge{}, want: link.KindBridge},
		{name: "tap", link: &netlink.Tuntap{Mode: netlink.TUNTAP_MODE_TAP}, want: link.KindTap},
		{name: "tun unsupported", link: &netlink.Tuntap{Mode: netlink.TUNTAP_MODE_TUN}, err: linkerr.ErrUnsupported},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := kindOf(tt.link)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("expected %v, got %v", tt.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("kindOf failed: %v", err)
			}
			if got != tt.want {
				t.Fatalf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestObserveVNetHeader(t *testing.T) {
	tests := []struct {
		name string
		link netlink.Link
		want vnetHeaderObserved
	}{
		{name: "enabled", link: &netlink.Tuntap{Mode: netlink.TUNTAP_MODE_TAP, Flags: netlink.TUNTAP_VNET_HDR}, want: vnetHeaderObservedEnabled},
		{name: "disabled", link: &netlink.Tuntap{Mode: netlink.TUNTAP_MODE_TAP}, want: vnetHeaderObservedDisabled},
		{name: "tun unknown", link: &netlink.Tuntap{Mode: netlink.TUNTAP_MODE_TUN}, want: vnetHeaderObservedUnknown},
		{name: "bridge unknown", link: &netlink.Bridge{}, want: vnetHeaderObservedUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := observeVNetHeader(tt.link); got != tt.want {
				t.Fatalf("observed VNetHeader = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLinkInfoSortsAddressesAndResolvesMasterName(t *testing.T) {
	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "br0", Index: 10}}
	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{
			Name:         "tap0",
			Index:        11,
			MTU:          1450,
			HardwareAddr: net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x02},
			Flags:        net.FlagUp,
			MasterIndex:  10,
		},
		Mode: netlink.TUNTAP_MODE_TAP,
	}
	fake := newFakeHandle(bridge, tap)
	fake.addresses["tap0"] = []netlink.Addr{*mustParseAddr(t, "2001:db8::1/64"), *mustParseAddr(t, "192.0.2.1/24")}

	info, err := linkInfo(fake, tap)
	if err != nil {
		t.Fatalf("linkInfo failed: %v", err)
	}

	if info.Name != "tap0" || info.Kind != link.KindTap || info.MasterName != "br0" || info.AdminState != link.AdminStateUp {
		t.Fatalf("unexpected info: %+v", info)
	}
	wantAddresses := []string{"192.0.2.1/24", "2001:db8::1/64"}
	if !reflect.DeepEqual(info.Addresses, wantAddresses) {
		t.Fatalf("addresses = %v, want %v", info.Addresses, wantAddresses)
	}
}

func TestLinkInfoPropagatesMasterListDumpInterrupted(t *testing.T) {
	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: "tap0", MasterIndex: 10},
		Mode:      netlink.TUNTAP_MODE_TAP,
	}
	fake := newFakeHandle(tap)
	fake.injectFailure("LinkList", netlink.ErrDumpInterrupted)

	_, err := linkInfo(fake, tap)
	if !errors.Is(err, linkerr.ErrIncompleteList) {
		t.Fatalf("expected ErrIncompleteList, got %v", err)
	}
}

func TestSortLinkInfosByName(t *testing.T) {
	infos := []link.LinkInfo{{Name: "tap2"}, {Name: "br0"}, {Name: "tap1"}}
	sortLinkInfosByName(infos)

	want := []link.LinkInfo{{Name: "br0"}, {Name: "tap1"}, {Name: "tap2"}}
	if !reflect.DeepEqual(infos, want) {
		t.Fatalf("infos = %v, want %v", infos, want)
	}
}

func mustParseAddr(t *testing.T, cidr string) *netlink.Addr {
	t.Helper()
	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatalf("parse addr %q: %v", cidr, err)
	}

	return addr
}

func osPermissionErr() error {
	return fmt.Errorf("permission wrapper: %w", os.ErrPermission)
}
