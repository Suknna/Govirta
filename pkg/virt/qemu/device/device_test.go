package device_test

import (
	"strings"
	"testing"

	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/blockdev"
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

func TestSCSICDArgRendersExplicitSCSIID(t *testing.T) {
	arg, err := device.SCSICD{
		ID:        "cdrom0-device",
		Drive:     blockdev.Ref("cdrom0"),
		Bus:       "cdrom0-scsi.0",
		SCSIID:    device.NewSCSIID(0),
		BootIndex: qemu.Int(0),
	}.Arg()
	if err != nil {
		t.Fatalf("Arg() error = %v", err)
	}

	want := "scsi-cd,drive=cdrom0,bus=cdrom0-scsi.0,scsi-id=0,bootindex=0,id=cdrom0-device"
	if arg != want {
		t.Fatalf("Arg() = %q, want %q", arg, want)
	}
}

func TestSCSICDArgRejectsInvalidConfig(t *testing.T) {
	cases := []struct {
		name string
		dev  device.SCSICD
	}{
		{name: "empty_drive", dev: device.SCSICD{Bus: "scsi0.0", SCSIID: device.NewSCSIID(0)}},
		{name: "empty_bus", dev: device.SCSICD{Drive: blockdev.Ref("cdrom0"), SCSIID: device.NewSCSIID(0)}},
		{name: "missing_scsi_id", dev: device.SCSICD{Drive: blockdev.Ref("cdrom0"), Bus: "scsi0.0"}},
		{name: "negative_scsi_id", dev: device.SCSICD{Drive: blockdev.Ref("cdrom0"), Bus: "scsi0.0", SCSIID: device.NewSCSIID(-1)}},
		{name: "unsafe_id", dev: device.SCSICD{ID: "cdrom0,bad", Drive: blockdev.Ref("cdrom0"), Bus: "scsi0.0", SCSIID: device.NewSCSIID(0)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.dev.Arg()
			if err == nil {
				t.Fatalf("Arg() error = nil, want error")
			}
			if !strings.Contains(err.Error(), "scsi-cd device") {
				t.Fatalf("Arg() error = %q, want scsi-cd device context", err.Error())
			}
		})
	}
}
