package blockdev

import "github.com/suknna/govirta/internal/virt/qemu/qflag"

type Ref string

type AIO string

const AIOThreads AIO = "threads"

type FileProtocol struct{ Filename string }
type Cache struct{ Direct qflag.OnOff }

type Qcow2 struct {
	NodeName string
	File     FileProtocol
	Cache    Cache
	AIO      AIO
}

func (d Qcow2) Arg() string {
	arg := "driver=qcow2,node-name=" + d.NodeName + ",file.driver=file,file.filename=" + d.File.Filename
	if d.Cache.Direct != "" {
		arg += ",cache.direct=" + string(d.Cache.Direct)
	}
	if d.AIO != "" {
		arg += ",aio=" + string(d.AIO)
	}
	return arg
}
