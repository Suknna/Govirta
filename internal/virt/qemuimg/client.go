package qemuimg

import (
	"github.com/suknna/govirta/internal/virt/qemuimg/check"
	"github.com/suknna/govirta/internal/virt/qemuimg/convert"
	"github.com/suknna/govirta/internal/virt/qemuimg/create"
	"github.com/suknna/govirta/internal/virt/qemuimg/info"
	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
	"github.com/suknna/govirta/internal/virt/qemuimg/remove"
	"github.com/suknna/govirta/internal/virt/qemuimg/snapshot"
)

var ErrInvalidRequest = imgexec.ErrInvalidRequest

type Config struct {
	Binary string
	Runner imgexec.Runner
}

type Client interface {
	QCOW2() QCOW2Client
}

type ExecClient struct {
	binary string
	runner imgexec.Runner
}

type QCOW2Client struct {
	binary string
	runner imgexec.Runner
}

func NewClient(config Config) *ExecClient {
	binary := config.Binary
	if binary == "" {
		binary = "qemu-img"
	}
	runner := config.Runner
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &ExecClient{binary: binary, runner: runner}
}

func (c *ExecClient) QCOW2() QCOW2Client {
	return QCOW2Client{binary: c.binary, runner: c.runner}
}

func (c QCOW2Client) Binary() string {
	return c.binary
}

func (c QCOW2Client) Create() *create.Builder {
	return create.New(c.binary, c.runner)
}

func (c QCOW2Client) Info() *info.Builder {
	return info.New(c.binary, c.runner)
}

func (c QCOW2Client) Convert() *convert.Builder {
	return convert.New(c.binary, c.runner)
}

func (c QCOW2Client) Snapshot() *snapshot.Builder {
	return snapshot.New(c.binary, c.runner)
}

func (c QCOW2Client) Check() *check.Builder {
	return check.New(c.binary, c.runner)
}

func (c QCOW2Client) Remove() *remove.Builder {
	return remove.New(c.binary, c.runner)
}
