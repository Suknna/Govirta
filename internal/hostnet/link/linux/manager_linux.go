//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/suknna/govirta/internal/hostnet/link"
	"github.com/suknna/govirta/internal/hostnet/link/linkerr"
	"github.com/vishvananda/netlink"
)

type Manager struct {
	handle handle
}

var observeTapVNetHeader = observeVNetHeader

func NewManager() Manager {
	return NewManagerWithHandle(realHandle{})
}

func NewManagerWithHandle(h handle) Manager {
	if h == nil {
		h = realHandle{}
	}

	return Manager{handle: h}
}

func (m Manager) EnsureBridge(ctx context.Context, spec link.BridgeSpec) (link.LinkInfo, error) {
	if err := validateBridgeSpec(ctx, spec); err != nil {
		return link.LinkInfo{}, err
	}
	addr, err := netlink.ParseAddr(spec.GatewayCIDR)
	if err != nil {
		return link.LinkInfo{}, fmt.Errorf("parse bridge gateway CIDR %q: %w", spec.GatewayCIDR, linkerr.ErrInvalidRequest)
	}

	nlLink, created, err := m.ensureBridgeLink(ctx, spec)
	if err != nil {
		return link.LinkInfo{}, err
	}
	if err := m.configureCreatedLink(nlLink, created, func() error {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := m.handle.LinkSetHardwareAddr(nlLink, spec.MAC); err != nil {
			return translateError("set bridge MAC", err)
		}
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := m.handle.LinkSetMTU(nlLink, spec.MTU); err != nil {
			return translateError("set bridge MTU", err)
		}
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := m.handle.AddrReplace(nlLink, addr); err != nil {
			return translateError("replace bridge address", err)
		}
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := m.pruneStaleBridgeAddresses(nlLink, addr); err != nil {
			return err
		}
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := m.handle.LinkSetUp(nlLink); err != nil {
			return translateError("set bridge up", err)
		}

		return nil
	}); err != nil {
		return link.LinkInfo{}, err
	}

	return m.currentLinkInfo(ctx, string(spec.Name))
}

func (m Manager) EnsureTap(ctx context.Context, spec link.TapSpec) (link.LinkInfo, error) {
	if err := validateTapSpec(ctx, spec); err != nil {
		return link.LinkInfo{}, err
	}
	if err := checkContext(ctx); err != nil {
		return link.LinkInfo{}, err
	}
	bridgeLink, err := m.handle.LinkByName(string(spec.BridgeName))
	if err != nil {
		return link.LinkInfo{}, translateError("lookup tap bridge", err)
	}
	bridge, ok := bridgeLink.(*netlink.Bridge)
	if !ok {
		return link.LinkInfo{}, fmt.Errorf("tap bridge %q is %q: %w", spec.BridgeName, bridgeLink.Type(), linkerr.ErrConflict)
	}

	nlLink, created, err := m.ensureTapLink(ctx, spec)
	if err != nil {
		return link.LinkInfo{}, err
	}
	if err := m.configureCreatedLink(nlLink, created, func() error {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := m.handle.LinkSetHardwareAddr(nlLink, spec.MAC); err != nil {
			return translateError("set tap MAC", err)
		}
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := m.handle.LinkSetMTU(nlLink, spec.MTU); err != nil {
			return translateError("set tap MTU", err)
		}
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := m.handle.LinkSetMaster(nlLink, bridge); err != nil {
			return translateError("set tap master", err)
		}
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := m.handle.LinkSetUp(nlLink); err != nil {
			return translateError("set tap up", err)
		}

		return nil
	}); err != nil {
		return link.LinkInfo{}, err
	}

	return m.currentLinkInfo(ctx, string(spec.Name))
}

func (m Manager) Delete(ctx context.Context, name link.Name) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if err := validateName(name); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	nlLink, err := m.handle.LinkByName(string(name))
	if err != nil {
		translated := translateError("lookup link for delete", err)
		if errors.Is(translated, linkerr.ErrNotFound) {
			return nil
		}
		return translated
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := m.handle.LinkDel(nlLink); err != nil {
		return translateError("delete link", err)
	}

	return nil
}

func (m Manager) Exists(ctx context.Context, name link.Name) (bool, error) {
	if err := validateContext(ctx); err != nil {
		return false, err
	}
	if err := validateName(name); err != nil {
		return false, err
	}
	if err := checkContext(ctx); err != nil {
		return false, err
	}
	_, err := m.handle.LinkByName(string(name))
	if err != nil {
		translated := translateError("lookup link exists", err)
		if errors.Is(translated, linkerr.ErrNotFound) {
			return false, nil
		}
		return false, translated
	}

	return true, nil
}

func (m Manager) Get(ctx context.Context, name link.Name) (link.LinkInfo, error) {
	if err := validateContext(ctx); err != nil {
		return link.LinkInfo{}, err
	}
	if err := validateName(name); err != nil {
		return link.LinkInfo{}, err
	}

	return m.currentLinkInfo(ctx, string(name))
}

func (m Manager) List(ctx context.Context, filter link.ListFilter) ([]link.LinkInfo, error) {
	if err := validateListFilter(ctx, filter); err != nil {
		return nil, err
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	nlLinks, err := m.handle.LinkList()
	if err != nil {
		return nil, translateError("list links", err)
	}
	// Resolve master names from the already-fetched dump instead of issuing a
	// fresh full LinkList per enslaved link (the previous O(n²) behavior).
	resolveMaster := masterResolverFromLinks(nlLinks)
	infos := make([]link.LinkInfo, 0, len(nlLinks))
	for _, nlLink := range nlLinks {
		kind, err := kindOf(nlLink)
		if err != nil {
			if errors.Is(err, linkerr.ErrUnsupported) {
				continue
			}
			return nil, err
		}
		if filter.Kind != link.KindAny && filter.Kind != kind {
			continue
		}
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		info, err := linkInfoWith(m.handle, nlLink, resolveMaster)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	sortLinkInfosByName(infos)

	return infos, nil
}

func (m Manager) ensureBridgeLink(ctx context.Context, spec link.BridgeSpec) (netlink.Link, bool, error) {
	if err := checkContext(ctx); err != nil {
		return nil, false, err
	}
	nlLink, err := m.handle.LinkByName(string(spec.Name))
	if err == nil {
		if _, ok := nlLink.(*netlink.Bridge); !ok {
			return nil, false, fmt.Errorf("existing link %q is %q: %w", spec.Name, nlLink.Type(), linkerr.ErrConflict)
		}
		return nlLink, false, nil
	}
	translated := translateError("lookup bridge", err)
	if !errors.Is(translated, linkerr.ErrNotFound) {
		return nil, false, translated
	}

	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: string(spec.Name), MTU: spec.MTU, HardwareAddr: cloneHardwareAddr(spec.MAC)}}
	if err := checkContext(ctx); err != nil {
		return nil, false, err
	}
	if err := m.handle.LinkAdd(bridge); err != nil {
		return nil, false, translateError("add bridge", err)
	}

	return bridge, true, nil
}

func (m Manager) ensureTapLink(ctx context.Context, spec link.TapSpec) (netlink.Link, bool, error) {
	if err := checkContext(ctx); err != nil {
		return nil, false, err
	}
	nlLink, err := m.handle.LinkByName(string(spec.Name))
	if err == nil {
		tap, ok := nlLink.(*netlink.Tuntap)
		if !ok {
			return nil, false, fmt.Errorf("existing link %q is %q: %w", spec.Name, nlLink.Type(), linkerr.ErrConflict)
		}
		if tap.Mode != netlink.TUNTAP_MODE_TAP {
			return nil, false, fmt.Errorf("existing tuntap %q mode %v: %w", spec.Name, tap.Mode, linkerr.ErrConflict)
		}
		if tap.Owner != spec.OwnerUID.Value {
			return nil, false, fmt.Errorf("existing tap %q owner UID %d, want %d: %w", spec.Name, tap.Owner, spec.OwnerUID.Value, linkerr.ErrConflict)
		}
		if tap.Group != spec.OwnerGID.Value {
			return nil, false, fmt.Errorf("existing tap %q owner GID %d, want %d: %w", spec.Name, tap.Group, spec.OwnerGID.Value, linkerr.ErrConflict)
		}
		observed := observeTapVNetHeader(tap)
		if observed == vnetHeaderObservedUnknown {
			return nil, false, fmt.Errorf("observe tap %q VNetHeader: %w", spec.Name, linkerr.ErrUnsupported)
		}
		if observed != expectedVNetHeader(spec.VNetHeader) {
			return nil, false, fmt.Errorf("tap %q VNetHeader is %q, want %q: %w", spec.Name, observed, spec.VNetHeader, linkerr.ErrConflict)
		}
		return tap, false, nil
	}
	translated := translateError("lookup tap", err)
	if !errors.Is(translated, linkerr.ErrNotFound) {
		return nil, false, translated
	}

	flags := netlink.TUNTAP_NO_PI
	if spec.VNetHeader == link.VNetHeaderEnabled {
		flags |= netlink.TUNTAP_VNET_HDR
	}
	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: string(spec.Name), MTU: spec.MTU, HardwareAddr: cloneHardwareAddr(spec.MAC)},
		Mode:      netlink.TUNTAP_MODE_TAP,
		Flags:     flags,
		Owner:     spec.OwnerUID.Value,
		Group:     spec.OwnerGID.Value,
	}
	if err := checkContext(ctx); err != nil {
		return nil, false, err
	}
	if err := m.handle.LinkAdd(tap); err != nil {
		return nil, false, translateError("add tap", err)
	}

	return tap, true, nil
}

func (m Manager) configureCreatedLink(nlLink netlink.Link, created bool, configure func() error) error {
	if err := configure(); err != nil {
		if !created {
			return err
		}
		if rollbackErr := m.rollbackCreatedLink(nlLink); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}

	return nil
}

func (m Manager) rollbackCreatedLink(nlLink netlink.Link) error {
	if err := m.handle.LinkDel(nlLink); err != nil {
		return translateError("rollback created link", err)
	}

	return nil
}

// pruneStaleBridgeAddresses converges the bridge's observed IPv4 addresses onto
// the desired gateway. AddrReplace only adds/updates the desired prefix; without
// pruning, a changed GatewayCIDR would leave the previous gateway on the bridge,
// so observed Addresses would diverge from spec (contradicting observed-state-as
// -truth and "exactly match spec"). Only IPv4 unicast global-scope addresses are
// considered; IPv4 link-local (169.254.0.0/16) is left untouched, and IPv6 is
// out of scope for the bridge gateway. Delete failures are joined so no error is
// silently discarded.
func (m Manager) pruneStaleBridgeAddresses(nlLink netlink.Link, desired *netlink.Addr) error {
	current, err := m.handle.AddrList(nlLink, netlink.FAMILY_V4)
	if err != nil {
		return translateError("list bridge addresses for prune", err)
	}

	var pruneErrs []error
	for i := range current {
		existing := current[i]
		if existing.IPNet == nil {
			continue
		}
		if sameAddrPrefix(&existing, desired) {
			continue
		}
		if existing.IP.IsLinkLocalUnicast() {
			continue
		}
		stale := existing
		if err := m.handle.AddrDel(nlLink, &stale); err != nil {
			pruneErrs = append(pruneErrs, translateError("delete stale bridge address", err))
		}
	}

	return errors.Join(pruneErrs...)
}

// sameAddrPrefix reports whether two netlink addresses carry the same IP and
// mask, i.e. represent the same on-link prefix.
func sameAddrPrefix(left, right *netlink.Addr) bool {
	if left.IPNet == nil || right.IPNet == nil {
		return false
	}
	return left.IP.Equal(right.IP) && bytesEqual(left.Mask, right.Mask)
}

func bytesEqual(left, right net.IPMask) bool {
	return string(left) == string(right)
}

func (m Manager) currentLinkInfo(ctx context.Context, name string) (link.LinkInfo, error) {
	if err := checkContext(ctx); err != nil {
		return link.LinkInfo{}, err
	}
	nlLink, err := m.handle.LinkByName(name)
	if err != nil {
		return link.LinkInfo{}, translateError("lookup current link", err)
	}
	if err := checkContext(ctx); err != nil {
		return link.LinkInfo{}, err
	}

	return linkInfo(m.handle, nlLink)
}

func expectedVNetHeader(mode link.VNetHeaderMode) vnetHeaderObserved {
	if mode == link.VNetHeaderEnabled {
		return vnetHeaderObservedEnabled
	}

	return vnetHeaderObservedDisabled
}

func checkContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return nil
}
