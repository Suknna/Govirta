package remove

import (
	"context"
	"os"
	"strings"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

type Builder struct {
	binary string
	runner imgexec.Runner
	path   string
}

func New(binary string, runner imgexec.Runner) *Builder {
	return &Builder{binary: binary, runner: runner}
}

func (b *Builder) Path(path string) *Builder {
	b.path = path
	return b
}

func (b *Builder) Do(ctx context.Context) error {
	if strings.TrimSpace(b.path) == "" {
		return imgexec.InvalidRequest("path is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(b.path)
}
