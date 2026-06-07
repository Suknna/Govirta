package device_test

import (
	"testing"

	"github.com/suknna/govirta/pkg/virt/qemu/device"
	"github.com/suknna/govirta/pkg/virt/qemu/netdev"
	"github.com/suknna/govirta/pkg/virt/qemu/qflag"
)

// TestVirtioNetPCIRomFileRendering pins the three RomFile states. The empty-set
// case is the regression that froze the e2e VM spawn: virtio-net-pci defaults to
// loading a PXE option ROM (efi-virtio.rom), which fails wherever the host QEMU
// install lacks that file. This project does not support PXE boot, so the VM
// controller sets RomFile to the empty string, which must render as a bare
// "romfile=" that disables the ROM. Unset must leave QEMU's default (no romfile
// token), and a non-empty value must render normally.
func TestVirtioNetPCIRomFileRendering(t *testing.T) {
	cases := []struct {
		name string
		dev  device.VirtioNetPCI
		want string
	}{
		{
			name: "unset leaves qemu default",
			dev: device.VirtioNetPCI{
				ID:     "nic0",
				Netdev: netdev.Ref("net0"),
			},
			want: "virtio-net-pci,netdev=net0,id=nic0",
		},
		{
			name: "empty disables option rom",
			dev: device.VirtioNetPCI{
				ID:      "nic0",
				Netdev:  netdev.Ref("net0"),
				RomFile: qflag.String(""),
			},
			want: "virtio-net-pci,netdev=net0,id=nic0,romfile=",
		},
		{
			name: "non-empty value renders normally",
			dev: device.VirtioNetPCI{
				ID:      "nic0",
				Netdev:  netdev.Ref("net0"),
				RomFile: qflag.String("custom.rom"),
			},
			want: "virtio-net-pci,netdev=net0,id=nic0,romfile=custom.rom",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.dev.Arg()
			if err != nil {
				t.Fatalf("Arg() error = %v, want nil", err)
			}
			if got != tc.want {
				t.Fatalf("Arg() = %q, want %q", got, tc.want)
			}
		})
	}
}
