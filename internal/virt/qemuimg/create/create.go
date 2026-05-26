package create

import (
	"context"
	"strconv"

	imgargv "github.com/suknna/govirta/internal/virt/qemuimg/internal/argv"
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
	target, err := imgargv.PathOperand("target", b.target)
	if err != nil {
		return err
	}
	base, err := imgargv.PathOperand("base", b.base)
	if err != nil {
		return err
	}
	if b.size <= 0 {
		return imgexec.InvalidRequest("size must be greater than zero")
	}

	args := []string{
		"create",
		"-f", "qcow2",
		"-F", "qcow2",
		"-b", base,
		target,
		strconv.FormatInt(b.size, 10),
	}
	result, err := b.runner.Run(ctx, b.binary, args)
	return imgexec.WrapError(result, err)
}
