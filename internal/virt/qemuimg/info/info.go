package info

import (
	"context"
	"encoding/json"
	"strings"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

type Result struct {
	Filename              string `json:"filename"`
	Format                string `json:"format"`
	VirtualSize           int64  `json:"virtual-size"`
	ActualSize            int64  `json:"actual-size"`
	BackingFilename       string `json:"backing-filename"`
	BackingFilenameFormat string `json:"backing-filename-format"`
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
	if strings.TrimSpace(b.path) == "" {
		return Result{}, imgexec.InvalidRequest("path is required")
	}

	result, err := b.runner.Run(ctx, b.binary, []string{"info", "--output=json", b.path})
	if err != nil {
		return Result{}, imgexec.WrapError(result, err)
	}

	var info Result
	if err := json.Unmarshal([]byte(result.Stdout), &info); err != nil {
		return Result{}, err
	}
	return info, nil
}
