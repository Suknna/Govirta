package snapshot

import (
	"context"
	"strings"

	imgargv "github.com/suknna/govirta/pkg/virt/qemuimg/internal/argv"
	imgexec "github.com/suknna/govirta/pkg/virt/qemuimg/internal/exec"
)

type Builder struct {
	binary string
	runner imgexec.Runner
	path   string
	name   string
}

func New(binary string, runner imgexec.Runner) *Builder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &Builder{binary: binary, runner: runner}
}

func (b *Builder) Path(path string) *Builder {
	b.path = path
	return b
}

func (b *Builder) Name(name string) *Builder {
	b.name = name
	return b
}

func (b *Builder) Do(ctx context.Context) error {
	path, err := imgargv.PathOperand("path", b.path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(b.name) == "" {
		return imgexec.InvalidRequest("name is required")
	}

	result, err := b.runner.Run(ctx, b.binary, []string{"snapshot", "-c", b.name, path})
	return imgexec.WrapError(result, err)
}
