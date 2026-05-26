package netdev

import (
	"fmt"

	"github.com/suknna/govirta/internal/virt/qemu/qflag"
	"github.com/suknna/govirta/internal/virt/qemu/qopt"
)

type Ref string
type Script string

const ScriptNo Script = "no"

// Valid reports whether the script setting is unset or supported by Govirta.
func (s Script) Valid() bool { return s == "" || s == ScriptNo }

type Tap struct {
	ID         string
	IfName     string
	Script     Script
	DownScript Script
	Vhost      qflag.OnOff
}

func (n Tap) Validate() error {
	if err := qopt.ValidateValue("id", n.ID); err != nil {
		return err
	}
	if err := qopt.ValidateValue("ifname", n.IfName); err != nil {
		return err
	}
	if err := qopt.ValidateEnum("script", string(n.Script), n.Script.Valid()); err != nil {
		return err
	}
	if err := qopt.ValidateEnum("downscript", string(n.DownScript), n.DownScript.Valid()); err != nil {
		return err
	}
	if err := qopt.ValidateEnum("vhost", string(n.Vhost), n.Vhost.Valid()); err != nil {
		return err
	}
	return nil
}

func (n Tap) Arg() (string, error) {
	if err := n.Validate(); err != nil {
		return "", fmt.Errorf("tap netdev: %w", err)
	}
	return qopt.Render("tap",
		qopt.Required("id", n.ID),
		qopt.Required("ifname", n.IfName),
		qopt.Optional("script", string(n.Script)),
		qopt.Optional("downscript", string(n.DownScript)),
		qopt.Optional("vhost", string(n.Vhost)),
	)
}
