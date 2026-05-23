package check

import imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"

type Builder struct {
	binary string
	runner imgexec.Runner
}

func New(binary string, runner imgexec.Runner) *Builder {
	return &Builder{binary: binary, runner: runner}
}
