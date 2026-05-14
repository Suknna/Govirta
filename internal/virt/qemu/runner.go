package qemu

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
)

type ProcessRunner interface {
	Start(ctx context.Context, inv Invocation) (Process, error)
}

type Process interface {
	PID() int
	Wait() error
	Stop(ctx context.Context) error
}

type OSProcessRunner struct{}

func NewOSProcessRunner() OSProcessRunner {
	return OSProcessRunner{}
}

func (r OSProcessRunner) Start(ctx context.Context, inv Invocation) (Process, error) {
	cmd := exec.CommandContext(ctx, inv.Binary, inv.Args...)
	var output *os.File
	if inv.StdoutPath != "" {
		file, err := os.OpenFile(inv.StdoutPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		output = file
		cmd.Stdout = file
		cmd.Stderr = file
	}
	if err := cmd.Start(); err != nil {
		if output != nil {
			_ = output.Close()
		}
		return nil, err
	}
	return &osProcess{cmd: cmd, output: output, waitDone: make(chan struct{})}, nil
}

type osProcess struct {
	cmd       *exec.Cmd
	output    *os.File
	closeOnce sync.Once
	waitOnce  sync.Once
	waitDone  chan struct{}
	waitErr   error
}

func (p *osProcess) PID() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *osProcess) Wait() error {
	return p.wait()
}

func (p *osProcess) Stop(ctx context.Context) error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Signal(execSignalTerm()); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- p.wait() }()
	select {
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		<-done
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (p *osProcess) wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		p.closeOutput()
		close(p.waitDone)
	})
	<-p.waitDone
	return p.waitErr
}

func (p *osProcess) closeOutput() {
	p.closeOnce.Do(func() {
		if p.output != nil {
			_ = p.output.Close()
		}
	})
}
