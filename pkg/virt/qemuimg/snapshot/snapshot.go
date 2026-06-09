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

// DeleteBuilder builds `qemu-img snapshot -d <name> <path>` (delete an internal snapshot).
type DeleteBuilder struct {
	binary string
	runner imgexec.Runner
	path   string
	name   string
}

func NewDelete(binary string, runner imgexec.Runner) *DeleteBuilder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &DeleteBuilder{binary: binary, runner: runner}
}

func (b *DeleteBuilder) Path(path string) *DeleteBuilder {
	b.path = path
	return b
}

func (b *DeleteBuilder) Name(name string) *DeleteBuilder {
	b.name = name
	return b
}

func (b *DeleteBuilder) Do(ctx context.Context) error {
	path, err := imgargv.PathOperand("path", b.path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(b.name) == "" {
		return imgexec.InvalidRequest("name is required")
	}

	result, err := b.runner.Run(ctx, b.binary, []string{"snapshot", "-d", b.name, path})
	return imgexec.WrapError(result, err)
}

// RevertBuilder builds `qemu-img snapshot -a <name> <path>` (apply/revert to an
// internal snapshot). Execution-plane only; not wired to a declarative API in
// this knife (revert is a Job-backlog concern, memory 1042 / note #33).
type RevertBuilder struct {
	binary string
	runner imgexec.Runner
	path   string
	name   string
}

func NewRevert(binary string, runner imgexec.Runner) *RevertBuilder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &RevertBuilder{binary: binary, runner: runner}
}

func (b *RevertBuilder) Path(path string) *RevertBuilder {
	b.path = path
	return b
}

func (b *RevertBuilder) Name(name string) *RevertBuilder {
	b.name = name
	return b
}

func (b *RevertBuilder) Do(ctx context.Context) error {
	path, err := imgargv.PathOperand("path", b.path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(b.name) == "" {
		return imgexec.InvalidRequest("name is required")
	}

	result, err := b.runner.Run(ctx, b.binary, []string{"snapshot", "-a", b.name, path})
	return imgexec.WrapError(result, err)
}

// ListBuilder builds `qemu-img snapshot -l <path>` (list internal snapshots).
type ListBuilder struct {
	binary string
	runner imgexec.Runner
	path   string
}

func NewList(binary string, runner imgexec.Runner) *ListBuilder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &ListBuilder{binary: binary, runner: runner}
}

func (b *ListBuilder) Path(path string) *ListBuilder {
	b.path = path
	return b
}

// Do runs the list and returns the raw qemu-img output for the caller to parse.
func (b *ListBuilder) Do(ctx context.Context) (string, error) {
	path, err := imgargv.PathOperand("path", b.path)
	if err != nil {
		return "", err
	}

	result, err := b.runner.Run(ctx, b.binary, []string{"snapshot", "-l", path})
	if werr := imgexec.WrapError(result, err); werr != nil {
		return "", werr
	}
	return result.Stdout, nil
}
