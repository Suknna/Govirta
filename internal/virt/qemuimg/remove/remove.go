package remove

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

type Builder struct {
	binary string
	runner imgexec.Runner
	path   string
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

func (b *Builder) Do(ctx context.Context) error {
	if strings.TrimSpace(b.path) == "" {
		return imgexec.InvalidRequest("path is required")
	}
	if filepath.Ext(b.path) != ".qcow2" {
		return imgexec.InvalidRequest("path must be a .qcow2 file")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(b.path)
	if err != nil {
		return os.Remove(b.path)
	}
	if info.IsDir() {
		return imgexec.InvalidRequest("path must be a .qcow2 file, not a directory")
	}
	return os.Remove(b.path)
}
