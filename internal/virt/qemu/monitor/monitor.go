package monitor

import (
	"fmt"

	"github.com/suknna/govirta/internal/virt/qemu/chardev"
	"github.com/suknna/govirta/internal/virt/qemu/qopt"
)

type Mode string

const ModeControl Mode = "control"

// Valid reports whether the monitor mode is supported by Govirta.
func (m Mode) Valid() bool { return m == ModeControl }

type Monitor struct {
	Chardev chardev.Ref
	Mode    Mode
}

func (m Monitor) Validate() error {
	if err := qopt.ValidateValue("chardev", string(m.Chardev)); err != nil {
		return err
	}
	if err := qopt.ValidateEnum("mode", string(m.Mode), m.Mode.Valid()); err != nil {
		return err
	}
	return nil
}

func (m Monitor) Arg() (string, error) {
	if err := m.Validate(); err != nil {
		return "", fmt.Errorf("monitor: %w", err)
	}
	return qopt.RenderPairs(qopt.Required("chardev", string(m.Chardev)), qopt.Required("mode", string(m.Mode)))
}
