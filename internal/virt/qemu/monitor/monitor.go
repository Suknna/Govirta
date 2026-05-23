package monitor

import "github.com/suknna/govirta/internal/virt/qemu/chardev"

type Mode string

const ModeControl Mode = "control"

type Monitor struct {
	Chardev chardev.Ref
	Mode    Mode
}

func (m Monitor) Arg() string { return "chardev=" + string(m.Chardev) + ",mode=" + string(m.Mode) }
