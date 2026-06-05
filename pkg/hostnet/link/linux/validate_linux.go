//go:build linux

package linux

import (
	"context"
	"fmt"
	"net"

	"github.com/suknna/govirta/pkg/hostnet/link"
	"github.com/suknna/govirta/pkg/hostnet/link/linkerr"
)

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("host link context: %w", linkerr.ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	return nil
}

func validateBridgeSpec(ctx context.Context, spec link.BridgeSpec) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if err := validateName(spec.Name); err != nil {
		return err
	}
	if _, _, err := net.ParseCIDR(spec.GatewayCIDR); err != nil {
		return fmt.Errorf("bridge gateway CIDR %q: %w", spec.GatewayCIDR, linkerr.ErrInvalidRequest)
	}
	if err := validateMTU(spec.MTU); err != nil {
		return err
	}
	if err := validateMAC(spec.MAC); err != nil {
		return err
	}

	return nil
}

func validateTapSpec(ctx context.Context, spec link.TapSpec) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if err := validateName(spec.Name); err != nil {
		return err
	}
	if err := validateName(spec.BridgeName); err != nil {
		return err
	}
	if !spec.OwnerUID.Set {
		return fmt.Errorf("tap owner UID must be explicit: %w", linkerr.ErrInvalidRequest)
	}
	if !spec.OwnerGID.Set {
		return fmt.Errorf("tap owner GID must be explicit: %w", linkerr.ErrInvalidRequest)
	}
	if err := validateMTU(spec.MTU); err != nil {
		return err
	}
	if err := validateMAC(spec.MAC); err != nil {
		return err
	}
	if err := validateVNetHeader(spec.VNetHeader); err != nil {
		return err
	}

	return nil
}

func validateListFilter(ctx context.Context, filter link.ListFilter) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	switch filter.Kind {
	case link.KindAny, link.KindBridge, link.KindTap:
		return nil
	default:
		return fmt.Errorf("list filter kind %q: %w", filter.Kind, linkerr.ErrInvalidRequest)
	}
}

func validateName(name link.Name) error {
	if name == "" {
		return fmt.Errorf("host link name is required: %w", linkerr.ErrInvalidRequest)
	}
	if len(name) > link.MaxInterfaceNameLength {
		return fmt.Errorf("host link name %q exceeds %d bytes: %w", name, link.MaxInterfaceNameLength, linkerr.ErrInvalidRequest)
	}

	return nil
}

func validateMTU(mtu int) error {
	if mtu <= 0 {
		return fmt.Errorf("host link MTU must be positive: %w", linkerr.ErrInvalidRequest)
	}

	return nil
}

func validateMAC(mac net.HardwareAddr) error {
	if len(mac) != 6 {
		return fmt.Errorf("host link MAC must be 6 bytes: %w", linkerr.ErrInvalidRequest)
	}
	if mac[0]&1 != 0 {
		return fmt.Errorf("host link MAC must be unicast: %w", linkerr.ErrInvalidRequest)
	}
	if mac[0]&2 == 0 {
		return fmt.Errorf("host link MAC must be locally administered: %w", linkerr.ErrInvalidRequest)
	}

	return nil
}

func validateVNetHeader(mode link.VNetHeaderMode) error {
	switch mode {
	case link.VNetHeaderEnabled, link.VNetHeaderDisabled:
		return nil
	default:
		return fmt.Errorf("tap VNetHeader mode %q: %w", mode, linkerr.ErrInvalidRequest)
	}
}
