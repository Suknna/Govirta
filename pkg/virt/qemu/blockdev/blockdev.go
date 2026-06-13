package blockdev

import (
	"fmt"

	"github.com/suknna/govirta/pkg/virt/qemu/qflag"
	"github.com/suknna/govirta/pkg/virt/qemu/qopt"
)

type Ref string

type Blockdev interface{ Arg() (string, error) }

type AIO string

const AIOThreads AIO = "threads"

// Valid reports whether the AIO mode is unset or supported by Govirta.
func (a AIO) Valid() bool { return a == "" || a == AIOThreads }

type FileProtocol struct{ Filename string }
type Cache struct{ Direct qflag.OnOff }

type Qcow2 struct {
	NodeName string
	File     FileProtocol
	Cache    Cache
	AIO      AIO
}

type ISO struct {
	NodeName string
	File     FileProtocol
	Cache    Cache
	AIO      AIO
}

// Validate checks whether the block device can be safely rendered as a QEMU option.
func (d Qcow2) Validate() error {
	if err := qopt.ValidateValue("node-name", d.NodeName); err != nil {
		return err
	}
	if err := qopt.ValidateValue("file.filename", d.File.Filename); err != nil {
		return err
	}
	if err := qopt.ValidateEnum("file.cache.direct", string(d.Cache.Direct), d.Cache.Direct.Valid()); err != nil {
		return err
	}
	if err := qopt.ValidateEnum("file.aio", string(d.AIO), d.AIO.Valid()); err != nil {
		return err
	}
	return nil
}

func (d Qcow2) Arg() (string, error) {
	if err := d.Validate(); err != nil {
		return "", fmt.Errorf("qcow2 blockdev: %w", err)
	}
	return qopt.RenderPairs(
		qopt.Required("driver", "qcow2"),
		qopt.Required("node-name", d.NodeName),
		qopt.Required("file.driver", "file"),
		qopt.Required("file.filename", d.File.Filename),
		qopt.Optional("file.cache.direct", string(d.Cache.Direct)),
		qopt.Optional("file.aio", string(d.AIO)),
	)
}

// Validate checks whether the ISO media can be safely rendered as a read-only QEMU option.
func (d ISO) Validate() error {
	if err := qopt.ValidateValue("node-name", d.NodeName); err != nil {
		return err
	}
	if err := qopt.ValidateValue("file.filename", d.File.Filename); err != nil {
		return err
	}
	if err := qopt.ValidateEnum("file.cache.direct", string(d.Cache.Direct), d.Cache.Direct.Valid()); err != nil {
		return err
	}
	if err := qopt.ValidateEnum("file.aio", string(d.AIO), d.AIO.Valid()); err != nil {
		return err
	}
	return nil
}

func (d ISO) Arg() (string, error) {
	if err := d.Validate(); err != nil {
		return "", fmt.Errorf("iso blockdev: %w", err)
	}
	return qopt.RenderPairs(
		qopt.Required("driver", "raw"),
		qopt.Required("node-name", d.NodeName),
		qopt.Required("read-only", "on"),
		qopt.Required("file.driver", "file"),
		qopt.Required("file.filename", d.File.Filename),
		qopt.Optional("file.cache.direct", string(d.Cache.Direct)),
		qopt.Optional("file.aio", string(d.AIO)),
	)
}
