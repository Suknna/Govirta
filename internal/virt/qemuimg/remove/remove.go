package remove

import (
	"context"
	"os"
	"path/filepath"

	imgargv "github.com/suknna/govirta/internal/virt/qemuimg/internal/argv"
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
	path, err := imgargv.PathOperand("path", b.path)
	if err != nil {
		return err
	}
	if filepath.Ext(path) != ".qcow2" {
		return imgexec.InvalidRequest("path must be a .qcow2 file")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return os.Remove(path)
	}
	if info.IsDir() {
		return imgexec.InvalidRequest("path must be a .qcow2 file, not a directory")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return imgexec.InvalidRequest("path must be a regular .qcow2 file, not a symlink")
	}
	if !info.Mode().IsRegular() {
		return imgexec.InvalidRequest("path must be a regular .qcow2 file")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(path)
}
