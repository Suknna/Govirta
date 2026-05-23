package machine

import "strings"

type Type string

const (
	TypeQ35  Type = "q35"
	TypeVirt Type = "virt"
)

type Accel string

const AccelKVM Accel = "kvm"

type IRQChip string

const IRQChipSplit IRQChip = "split"

type Config struct {
	Type          Type
	Accel         Accel
	KernelIRQChip IRQChip
}

type Option func(*Config)

func WithAccel(v Accel) Option           { return func(c *Config) { c.Accel = v } }
func WithKernelIRQChip(v IRQChip) Option { return func(c *Config) { c.KernelIRQChip = v } }

func New(t Type, opts ...Option) Config {
	c := Config{Type: t}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

func (c Config) Arg() string {
	parts := []string{"type=" + string(c.Type)}
	if c.Accel != "" {
		parts = append(parts, "accel="+string(c.Accel))
	}
	if c.KernelIRQChip != "" {
		parts = append(parts, "kernel-irqchip="+string(c.KernelIRQChip))
	}
	return strings.Join(parts, ",")
}
