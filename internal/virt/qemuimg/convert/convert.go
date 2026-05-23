package convert

import (
	"context"
	"strings"

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
	if strings.TrimSpace(b.source) == "" {
		return imgexec.InvalidRequest("source is required")
	}
	if strings.TrimSpace(b.target) == "" {
		return imgexec.InvalidRequest("target is required")
	}

	result, err := b.runner.Run(ctx, b.binary, []string{"convert", "-O", "qcow2", b.source, b.target})
	return imgexec.WrapError(result, err)
}
