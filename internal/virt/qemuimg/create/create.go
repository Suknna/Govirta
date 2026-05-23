package create

import (
	"context"
	"strconv"
	"strings"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

type Builder struct {
	binary string
	runner imgexec.Runner
	target string
	base   string
	size   int64
}

func New(binary string, runner imgexec.Runner) *Builder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &Builder{binary: binary, runner: runner}
}

func (b *Builder) Target(path string) *Builder {
	b.target = path
	return b
}

func (b *Builder) FromBase(path string) *Builder {
	b.base = path
	return b
}

func (b *Builder) SizeBytes(size int64) *Builder {
	b.size = size
	return b
}

func (b *Builder) Do(ctx context.Context) error {
	if strings.TrimSpace(b.target) == "" {
		return imgexec.InvalidRequest("target is required")
	}
	if strings.TrimSpace(b.base) == "" {
		return imgexec.InvalidRequest("base is required")
	}
	if b.size <= 0 {
		return imgexec.InvalidRequest("size must be greater than zero")
	}

	args := []string{
		"create",
		"-f", "qcow2",
		"-F", "qcow2",
		"-b", b.base,
		b.target,
		strconv.FormatInt(b.size, 10),
	}
	result, err := b.runner.Run(ctx, b.binary, args)
	return imgexec.WrapError(result, err)
}
