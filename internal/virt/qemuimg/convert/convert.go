package convert

import (
	"context"

	imgargv "github.com/suknna/govirta/internal/virt/qemuimg/internal/argv"
	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

type Builder struct {
	binary string
	runner imgexec.Runner
	source string
	target string
}

func New(binary string, runner imgexec.Runner) *Builder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &Builder{binary: binary, runner: runner}
}

func (b *Builder) Source(path string) *Builder {
	b.source = path
	return b
}

func (b *Builder) Target(path string) *Builder {
	b.target = path
	return b
}

func (b *Builder) Do(ctx context.Context) error {
	source, err := imgargv.PathOperand("source", b.source)
	if err != nil {
		return err
	}
	target, err := imgargv.PathOperand("target", b.target)
	if err != nil {
		return err
	}

	result, err := b.runner.Run(ctx, b.binary, []string{"convert", "-f", "qcow2", "-O", "qcow2", source, target})
	return imgexec.WrapError(result, err)
}
