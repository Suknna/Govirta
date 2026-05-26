package check

import (
	"context"
	"encoding/json"

	imgargv "github.com/suknna/govirta/internal/virt/qemuimg/internal/argv"
	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

type Result struct {
	Filename       string `json:"filename"`
	Format         string `json:"format"`
	CheckErrors    int64  `json:"check-errors"`
	ImageEndOffset int64  `json:"image-end-offset"`
	Corruptions    int64  `json:"corruptions"`
	Leaks          int64  `json:"leaks"`
	RawOutput      string `json:"-"`
}

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

func (b *Builder) Do(ctx context.Context) (Result, error) {
	path, err := imgargv.PathOperand("path", b.path)
	if err != nil {
		return Result{}, err
	}

	runResult, err := b.runner.Run(ctx, b.binary, []string{"check", "--output=json", path})
	if err != nil {
		return Result{}, imgexec.WrapError(runResult, err)
	}

	var result Result
	if err := json.Unmarshal([]byte(runResult.Stdout), &result); err != nil {
		return Result{}, err
	}
	result.RawOutput = runResult.Stdout
	return result, nil
}
