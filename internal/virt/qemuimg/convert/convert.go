package convert

import (
	"context"

	imgargv "github.com/suknna/govirta/internal/virt/qemuimg/internal/argv"
	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

// Builder assembles qemu-img convert calls that produce qcow2 images.
type Builder struct {
	binary       string
	runner       imgexec.Runner
	source       string
	target       string
	sourceFormat string
}

// New creates a convert builder using binary and runner.
func New(binary string, runner imgexec.Runner) *Builder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &Builder{binary: binary, runner: runner}
}

// Source sets the source image path.
func (b *Builder) Source(path string) *Builder {
	b.source = path
	return b
}

// SourceFormat sets the explicit source image format.
//
// Empty format defaults to qcow2 for existing qcow2-to-qcow2 callers.
// Supported values are qcow2 and raw.
func (b *Builder) SourceFormat(format string) *Builder {
	b.sourceFormat = format
	return b
}

// Target sets the target qcow2 image path.
func (b *Builder) Target(path string) *Builder {
	b.target = path
	return b
}

// Do validates the request and runs qemu-img convert through the runner.
func (b *Builder) Do(ctx context.Context) error {
	source, err := imgargv.PathOperand("source", b.source)
	if err != nil {
		return err
	}
	target, err := imgargv.PathOperand("target", b.target)
	if err != nil {
		return err
	}
	format := b.sourceFormat
	if format == "" {
		format = "qcow2"
	}
	switch format {
	case "qcow2", "raw":
	default:
		return imgexec.InvalidRequest("source format must be qcow2 or raw")
	}

	result, err := b.runner.Run(ctx, b.binary, []string{"convert", "-f", format, "-O", "qcow2", source, target})
	return imgexec.WrapError(result, err)
}
