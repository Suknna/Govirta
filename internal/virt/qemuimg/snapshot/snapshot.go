package snapshot

import (
	"context"
	"strings"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

type Builder struct {
	binary string
	runner imgexec.Runner
	path   string
	name   string
}

func New(binary string, runner imgexec.Runner) *Builder {
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
	if strings.TrimSpace(b.path) == "" {
		return imgexec.InvalidRequest("path is required")
	}
	if strings.TrimSpace(b.name) == "" {
		return imgexec.InvalidRequest("name is required")
	}

	_, err := b.runner.Run(ctx, b.binary, []string{"snapshot", "-c", b.name, b.path})
	return err
}
