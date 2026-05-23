package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	os_exec "os/exec"
)

var ErrInvalidRequest = errors.New("invalid qemu-img request")

type Result struct {
	Stdout string
	Stderr string
}

type Runner interface {
	Run(ctx context.Context, binary string, args []string) (Result, error)
}

type OSRunner struct{}

func (r OSRunner) Run(ctx context.Context, binary string, args []string) (Result, error) {
	cmd := os_exec.CommandContext(ctx, binary, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return Result{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

func InvalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}
