package chardev

import "github.com/suknna/govirta/internal/virt/qemu/qflag"

type Ref string

type Socket struct {
	ID     string
	Path   string
	Server qflag.OnOff
	Wait   qflag.OnOff
}

func (c Socket) Arg() string {
	arg := "socket,id=" + c.ID + ",path=" + c.Path
	if c.Server != "" {
		arg += ",server=" + string(c.Server)
	}
	if c.Wait != "" {
		arg += ",wait=" + string(c.Wait)
	}
	return arg
}
