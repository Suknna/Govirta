package netdev

import "github.com/suknna/govirta/internal/virt/qemu/qflag"

type Ref string
type Script string

const ScriptNo Script = "no"

type Tap struct {
	ID         string
	IfName     string
	Script     Script
	DownScript Script
	Vhost      qflag.OnOff
}

func (n Tap) Arg() string {
	arg := "tap,id=" + n.ID + ",ifname=" + n.IfName
	if n.Script != "" {
		arg += ",script=" + string(n.Script)
	}
	if n.DownScript != "" {
		arg += ",downscript=" + string(n.DownScript)
	}
	if n.Vhost != "" {
		arg += ",vhost=" + string(n.Vhost)
	}
	return arg
}
