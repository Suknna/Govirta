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

type CommandError struct {
	Result Result
	Err    error
}

func WrapError(result Result, err error) error {
	if err == nil {
		return nil
	}
	return &CommandError{Result: result, Err: err}
}

func (e *CommandError) Error() string {
	if e.Result.Stderr == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%v: %s", e.Err, e.Result.Stderr)
}

func (e *CommandError) Unwrap() error {
	return e.Err
}

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
