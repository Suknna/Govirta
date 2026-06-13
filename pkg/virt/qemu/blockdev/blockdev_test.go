package blockdev_test

import (
	"strings"
	"testing"

	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/blockdev"
)

func TestISOArgRendersReadOnlyRawBlockdev(t *testing.T) {
	arg, err := blockdev.ISO{
		NodeName: "cdrom0",
		File:     blockdev.FileProtocol{Filename: "/var/lib/vm/install.iso"},
		Cache:    blockdev.Cache{Direct: qemu.Off},
		AIO:      blockdev.AIOThreads,
	}.Arg()
	if err != nil {
		t.Fatalf("Arg() error = %v", err)
	}

	want := "driver=raw,node-name=cdrom0,read-only=on,file.driver=file,file.filename=/var/lib/vm/install.iso,file.cache.direct=off,file.aio=threads"
	if arg != want {
		t.Fatalf("Arg() = %q, want %q", arg, want)
	}
}

func TestISOArgRejectsInvalidConfig(t *testing.T) {
	cases := []struct {
		name string
		iso  blockdev.ISO
	}{
		{name: "empty_node_name", iso: blockdev.ISO{File: blockdev.FileProtocol{Filename: "/var/lib/vm/install.iso"}}},
		{name: "empty_filename", iso: blockdev.ISO{NodeName: "cdrom0"}},
		{name: "unsafe_filename", iso: blockdev.ISO{NodeName: "cdrom0", File: blockdev.FileProtocol{Filename: "/var/lib/vm/install.iso,read-only=off"}}},
		{name: "invalid_aio", iso: blockdev.ISO{NodeName: "cdrom0", File: blockdev.FileProtocol{Filename: "/var/lib/vm/install.iso"}, AIO: blockdev.AIO("native")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.iso.Arg()
			if err == nil {
				t.Fatalf("Arg() error = nil, want error")
			}
			if !strings.Contains(err.Error(), "iso blockdev") {
				t.Fatalf("Arg() error = %q, want iso blockdev context", err.Error())
			}
		})
	}
}
