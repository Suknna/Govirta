package qemu

import "context"

// Driver defines the QEMU process management boundary.
type Driver interface {
	Name() string
	Start(ctx context.Context, cfg Config) (Process, error)
}

type RealDriver struct {
	builder Builder
	runner  ProcessRunner
}

func NewDriver(builder Builder, runner ProcessRunner) *RealDriver {
	return &RealDriver{builder: builder, runner: runner}
}

func NewDefaultDriver() *RealDriver {
	return NewDriver(NewBuilder(), NewOSProcessRunner())
}

func (d *RealDriver) Name() string {
	return "qemu"
}

func (d *RealDriver) Start(ctx context.Context, cfg Config) (Process, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	inv, err := d.builder.Build(cfg)
	if err != nil {
		return nil, err
	}
	return d.runner.Start(ctx, inv)
}

// NoopDriver is a non-operational QEMU driver for the initial skeleton.
type NoopDriver struct{}

// NewNoopDriver creates a QEMU driver that does not execute host commands.
func NewNoopDriver() *NoopDriver {
	return &NoopDriver{}
}

// Name returns the driver name.
func (d *NoopDriver) Name() string {
	return "qemu-noop"
}

// Start validates context cancellation and performs no host operation.
func (d *NoopDriver) Start(ctx context.Context, cfg Config) (Process, error) {
	_ = cfg
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return fakeNoopProcess{}, nil
	}
}

type fakeNoopProcess struct{}

func (fakeNoopProcess) PID() int                       { return 0 }
func (fakeNoopProcess) Wait() error                    { return nil }
func (fakeNoopProcess) Stop(ctx context.Context) error { return ctx.Err() }
