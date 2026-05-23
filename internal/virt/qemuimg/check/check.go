package check

import (
	"context"
	"encoding/json"
	"strings"

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
	return &Builder{binary: binary, runner: runner}
}

func (b *Builder) Path(path string) *Builder {
	b.path = path
	return b
}

func (b *Builder) Do(ctx context.Context) (Result, error) {
	if strings.TrimSpace(b.path) == "" {
		return Result{}, imgexec.InvalidRequest("path is required")
	}

	runResult, err := b.runner.Run(ctx, b.binary, []string{"check", "--output=json", b.path})
	if err != nil {
		return Result{}, err
	}

	var result Result
	if err := json.Unmarshal([]byte(runResult.Stdout), &result); err != nil {
		return Result{}, err
	}
	result.RawOutput = runResult.Stdout
	return result, nil
}
