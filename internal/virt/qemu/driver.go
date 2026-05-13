package qemu

import "context"

// Driver defines the QEMU process management boundary.
type Driver interface {
	Name() string
	Start(ctx context.Context, vmName string) error
	Stop(ctx context.Context, vmName string) error
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
func (d *NoopDriver) Start(ctx context.Context, vmName string) error {
	_ = vmName

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// Stop validates context cancellation and performs no host operation.
func (d *NoopDriver) Stop(ctx context.Context, vmName string) error {
	_ = vmName

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
