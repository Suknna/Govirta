package device

import (
	"strconv"

	"github.com/suknna/govirta/internal/virt/qemu/blockdev"
	"github.com/suknna/govirta/internal/virt/qemu/netdev"
	"github.com/suknna/govirta/internal/virt/qemu/qflag"
)

type MAC string

type VirtioBlkPCI struct {
	ID        string
	Drive     blockdev.Ref
	BootIndex qflag.OptionalInt
}

func (d VirtioBlkPCI) Arg() string {
	arg := "virtio-blk-pci,drive=" + string(d.Drive)
	if d.BootIndex.IsSet() {
		arg += ",bootindex=" + strconv.Itoa(d.BootIndex.Value())
	}
	if d.ID != "" {
		arg += ",id=" + d.ID
	}
	return arg
}

type VirtioNetPCI struct {
	ID     string
	Netdev netdev.Ref
	Mac    MAC
}

func (d VirtioNetPCI) Arg() string {
	arg := "virtio-net-pci,netdev=" + string(d.Netdev)
	if d.Mac != "" {
		arg += ",mac=" + string(d.Mac)
	}
	if d.ID != "" {
		arg += ",id=" + d.ID
	}
	return arg
}
