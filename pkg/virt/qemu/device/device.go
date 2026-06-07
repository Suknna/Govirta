package device

import (
	"fmt"
	"strconv"

	"github.com/suknna/govirta/pkg/virt/qemu/blockdev"
	"github.com/suknna/govirta/pkg/virt/qemu/netdev"
	"github.com/suknna/govirta/pkg/virt/qemu/qflag"
	"github.com/suknna/govirta/pkg/virt/qemu/qopt"
)

type MAC string

type VirtioBlkPCI struct {
	ID        string
	Drive     blockdev.Ref
	BootIndex qflag.OptionalInt
}

func (d VirtioBlkPCI) Validate() error {
	if err := qopt.ValidateValue("drive", string(d.Drive)); err != nil {
		return err
	}
	if d.ID != "" {
		if err := qopt.ValidateValue("id", d.ID); err != nil {
			return err
		}
	}
	return nil
}

func (d VirtioBlkPCI) Arg() (string, error) {
	if err := d.Validate(); err != nil {
		return "", fmt.Errorf("virtio-blk-pci device: %w", err)
	}
	bootIndex := ""
	if d.BootIndex.IsSet() {
		bootIndex = strconv.Itoa(d.BootIndex.Value())
	}
	return qopt.Render("virtio-blk-pci",
		qopt.Required("drive", string(d.Drive)),
		qopt.Optional("bootindex", bootIndex),
		qopt.Optional("id", d.ID),
	)
}

type VirtioNetPCI struct {
	ID     string
	Netdev netdev.Ref
	Mac    MAC
	// RomFile controls the device's option ROM. When set to the empty string it
	// renders as "romfile=", which disables loading a PXE/network-boot ROM. This
	// project does not support PXE boot, so the VM controller disables it: a
	// cold-boot guest boots from its disk and never needs the network-boot ROM,
	// and requiring the host QEMU install to ship efi-virtio.rom would otherwise
	// make spawn fail wherever that file is absent. Unset leaves QEMU's default.
	RomFile qflag.OptionalString
}

func (d VirtioNetPCI) Validate() error {
	if err := qopt.ValidateValue("netdev", string(d.Netdev)); err != nil {
		return err
	}
	if d.Mac != "" {
		if err := qopt.ValidateValue("mac", string(d.Mac)); err != nil {
			return err
		}
	}
	if d.ID != "" {
		if err := qopt.ValidateValue("id", d.ID); err != nil {
			return err
		}
	}
	return nil
}

func (d VirtioNetPCI) Arg() (string, error) {
	if err := d.Validate(); err != nil {
		return "", fmt.Errorf("virtio-net-pci device: %w", err)
	}
	pairs := []qopt.Pair{
		qopt.Required("netdev", string(d.Netdev)),
		qopt.Optional("mac", string(d.Mac)),
		qopt.Optional("id", d.ID),
	}
	if d.RomFile.IsSet() {
		pairs = append(pairs, qopt.PresentEmptyOK("romfile", d.RomFile.Value()))
	}
	return qopt.Render("virtio-net-pci", pairs...)
}
