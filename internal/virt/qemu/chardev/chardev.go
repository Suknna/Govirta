package chardev

import (
	"fmt"

	"github.com/suknna/govirta/internal/virt/qemu/qflag"
	"github.com/suknna/govirta/internal/virt/qemu/qopt"
)

type Ref string

type Socket struct {
	ID     string
	Path   string
	Server qflag.OnOff
	Wait   qflag.OnOff
}

func (c Socket) Validate() error {
	if err := qopt.ValidateValue("id", c.ID); err != nil {
		return err
	}
	if err := qopt.ValidateValue("path", c.Path); err != nil {
		return err
	}
	if err := qopt.ValidateEnum("server", string(c.Server), c.Server.Valid()); err != nil {
		return err
	}
	if err := qopt.ValidateEnum("wait", string(c.Wait), c.Wait.Valid()); err != nil {
		return err
	}
	return nil
}

func (c Socket) Arg() (string, error) {
	if err := c.Validate(); err != nil {
		return "", fmt.Errorf("socket chardev: %w", err)
	}
	return qopt.Render("socket",
		qopt.Required("id", c.ID),
		qopt.Required("path", c.Path),
		qopt.Optional("server", string(c.Server)),
		qopt.Optional("wait", string(c.Wait)),
	)
}
