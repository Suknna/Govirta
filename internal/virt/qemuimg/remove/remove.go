package remove

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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

// Do removes a Govirta-owned qcow2 image path from a trusted storage
// directory. Callers must pass a path resolved by the Govirta trusted storage
// layer, or otherwise ensure the parent directory cannot be written by
// untrusted users. The Lstat checks below are guardrails against accidental
// directory, symlink, or non-regular-file deletion; they do not prove the inode
// is unchanged when the parent directory is hostile and can race pathname
// replacement between Lstat and Remove.
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
		// 仅 NotExist 走 Remove 让调用方拿到稳定的 fs.ErrNotExist；
		// 其它 Lstat 错误（permission denied、I/O error、stale NFS 等）
		// 必须显式透出，避免被 os.Remove 的二次错误掩盖真实根因。
		if errors.Is(err, fs.ErrNotExist) {
			return os.Remove(path)
		}
		return fmt.Errorf("stat %s: %w", path, err)
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
