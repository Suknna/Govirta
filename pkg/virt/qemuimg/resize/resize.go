package resize

import (
	"context"
	"strconv"

	imgargv "github.com/suknna/govirta/pkg/virt/qemuimg/internal/argv"
	imgexec "github.com/suknna/govirta/pkg/virt/qemuimg/internal/exec"
)

// Builder assembles qemu-img resize calls for qcow2 images.
type Builder struct {
	binary string
	runner imgexec.Runner
	path   string
	size   int64
}

// New creates a resize builder using binary and runner.
func New(binary string, runner imgexec.Runner) *Builder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &Builder{binary: binary, runner: runner}
}

// Path sets the qcow2 image path to resize.
func (b *Builder) Path(path string) *Builder {
	b.path = path
	return b
}

// SizeBytes sets the target virtual size in bytes.
func (b *Builder) SizeBytes(size int64) *Builder {
	b.size = size
	return b
}

// Do validates the request and runs qemu-img resize through the runner.
func (b *Builder) Do(ctx context.Context) error {
	path, err := imgargv.PathOperand("path", b.path)
	if err != nil {
		return err
	}
	if b.size <= 0 {
		return imgexec.InvalidRequest("size must be positive")
	}

	result, err := b.runner.Run(ctx, b.binary, []string{"resize", "-f", "qcow2", path, strconv.FormatInt(b.size, 10)})
	return imgexec.WrapError(result, err)
}
