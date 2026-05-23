package qemu

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/suknna/govirta/internal/virt/qemu/blockdev"
	"github.com/suknna/govirta/internal/virt/qemu/chardev"
	"github.com/suknna/govirta/internal/virt/qemu/cpu"
	"github.com/suknna/govirta/internal/virt/qemu/device"
	"github.com/suknna/govirta/internal/virt/qemu/display"
	"github.com/suknna/govirta/internal/virt/qemu/machine"
	"github.com/suknna/govirta/internal/virt/qemu/monitor"
	"github.com/suknna/govirta/internal/virt/qemu/netdev"
	"github.com/suknna/govirta/internal/virt/qemu/qflag"
	"github.com/suknna/govirta/internal/virt/qemu/serial"
)

var ErrInvalidVM = errors.New("invalid qemu vm")

type Arch string

const (
	ArchX86_64  Arch = "x86_64"
	ArchAArch64 Arch = "aarch64"
)

type OnOff = qflag.OnOff
type OptionalInt = qflag.OptionalInt

const (
	On  = qflag.On
	Off = qflag.Off
)

func Int(v int) OptionalInt { return qflag.Int(v) }

type Memory struct{ MiB int }

func MiB(v int) Memory { return Memory{MiB: v} }

type SMP struct {
	CPUs    int
	Cores   int
	Threads int
	Sockets int
}

type Msg struct {
	Timestamp OnOff
	GuestName OnOff
}

type NameOption func(*nameConfig)

type nameConfig struct {
	value        string
	debugThreads OnOff
}

func NameDebugThreads(v OnOff) NameOption {
	return func(c *nameConfig) { c.debugThreads = v }
}

type argvEntry struct {
	flag  string
	value string
}

type Builder struct {
	binary     string
	name       *nameConfig
	machine    *machine.Config
	cpu        cpu.Model
	smp        *SMP
	memory     *Memory
	ordered    []argvEntry
	display    display.Display
	noReboot   bool
	noShutdown bool
	msg        *Msg
	pidFile    string
}

type VM struct{ builder Builder }

func NewVM(arch Arch) *Builder { return &Builder{binary: binaryForArch(arch)} }

func binaryForArch(arch Arch) string {
	switch arch {
	case ArchX86_64:
		return "qemu-system-x86_64"
	case ArchAArch64:
		return "qemu-system-aarch64"
	default:
		return ""
	}
}

func (b *Builder) Binary(path string) *Builder { b.binary = path; return b }

func (b *Builder) Name(name string, opts ...NameOption) *Builder {
	c := nameConfig{value: name}
	for _, opt := range opts {
		opt(&c)
	}
	b.name = &c
	return b
}

func (b *Builder) Machine(t machine.Type, opts ...machine.Option) *Builder {
	c := machine.New(t, opts...)
	b.machine = &c
	return b
}

func (b *Builder) CPU(model cpu.Model) *Builder { b.cpu = model; return b }

func (b *Builder) SMP(v SMP) *Builder { b.smp = &v; return b }

func (b *Builder) Memory(v Memory) *Builder { b.memory = &v; return b }

func (b *Builder) AddBlockdev(v blockdev.Qcow2) *Builder {
	b.ordered = append(b.ordered, argvEntry{flag: "-blockdev", value: v.Arg()})
	return b
}

func (b *Builder) AddDevice(v any) *Builder {
	switch d := v.(type) {
	case device.VirtioBlkPCI:
		b.ordered = append(b.ordered, argvEntry{flag: "-device", value: d.Arg()})
	case device.VirtioNetPCI:
		b.ordered = append(b.ordered, argvEntry{flag: "-device", value: d.Arg()})
	default:
		b.ordered = append(b.ordered, argvEntry{flag: "-device", value: fmt.Sprintf("unsupported-device:%T", v)})
	}
	return b
}

func (b *Builder) AddNetdev(v netdev.Tap) *Builder {
	b.ordered = append(b.ordered, argvEntry{flag: "-netdev", value: v.Arg()})
	return b
}

func (b *Builder) AddChardev(v chardev.Socket) *Builder {
	b.ordered = append(b.ordered, argvEntry{flag: "-chardev", value: v.Arg()})
	return b
}

func (b *Builder) Monitor(v monitor.Monitor) *Builder {
	b.ordered = append(b.ordered, argvEntry{flag: "-mon", value: v.Arg()})
	return b
}

func (b *Builder) Serial(v serial.Serial) *Builder {
	b.ordered = append(b.ordered, argvEntry{flag: "-serial", value: v.Arg()})
	return b
}

func (b *Builder) Display(v display.Display) *Builder { b.display = v; return b }
func (b *Builder) NoReboot() *Builder                 { b.noReboot = true; return b }
func (b *Builder) NoShutdown() *Builder               { b.noShutdown = true; return b }
func (b *Builder) Msg(v Msg) *Builder                 { b.msg = &v; return b }
func (b *Builder) PidFile(path string) *Builder       { b.pidFile = path; return b }

func (b *Builder) Build() (VM, error) {
	if b.binary == "" {
		return VM{}, fmt.Errorf("%w: qemu binary is required", ErrInvalidVM)
	}
	if b.smp != nil && (b.smp.CPUs <= 0 || b.smp.Cores <= 0 || b.smp.Threads <= 0 || b.smp.Sockets <= 0) {
		return VM{}, fmt.Errorf("%w: smp values must be positive", ErrInvalidVM)
	}
	if b.memory != nil && b.memory.MiB <= 0 {
		return VM{}, fmt.Errorf("%w: memory must be positive", ErrInvalidVM)
	}
	for _, entry := range b.ordered {
		if strings.Contains(entry.value, "unsupported-device:") {
			return VM{}, fmt.Errorf("%w: %s", ErrInvalidVM, entry.value)
		}
		if entry.value == "" {
			return VM{}, fmt.Errorf("%w: empty value for %s", ErrInvalidVM, entry.flag)
		}
	}
	return VM{builder: *b}, nil
}

func (v VM) Argv() ([]string, error) {
	b := v.builder
	argv := []string{b.binary}
	if b.name != nil {
		argv = append(argv, "-name", b.name.arg())
	}
	if b.machine != nil {
		argv = append(argv, "-machine", b.machine.Arg())
	}
	if b.cpu != "" {
		argv = append(argv, "-cpu", string(b.cpu))
	}
	if b.smp != nil {
		argv = append(argv, "-smp", b.smp.arg())
	}
	if b.memory != nil {
		argv = append(argv, "-m", "size="+strconv.Itoa(b.memory.MiB))
	}
	for _, entry := range b.ordered {
		argv = append(argv, entry.flag, entry.value)
	}
	if b.display != "" {
		argv = append(argv, "-display", string(b.display))
	}
	if b.noReboot {
		argv = append(argv, "-no-reboot")
	}
	if b.noShutdown {
		argv = append(argv, "-no-shutdown")
	}
	if b.msg != nil {
		argv = append(argv, "-msg", b.msg.arg())
	}
	if b.pidFile != "" {
		argv = append(argv, "-pidfile", b.pidFile)
	}
	return argv, nil
}

func (c nameConfig) arg() string {
	arg := c.value
	if c.debugThreads != "" {
		arg += ",debug-threads=" + string(c.debugThreads)
	}
	return arg
}

func (s SMP) arg() string {
	return "cpus=" + strconv.Itoa(s.CPUs) +
		",cores=" + strconv.Itoa(s.Cores) +
		",threads=" + strconv.Itoa(s.Threads) +
		",sockets=" + strconv.Itoa(s.Sockets)
}

func (m Msg) arg() string {
	parts := make([]string, 0, 2)
	if m.Timestamp != "" {
		parts = append(parts, "timestamp="+string(m.Timestamp))
	}
	if m.GuestName != "" {
		parts = append(parts, "guest-name="+string(m.GuestName))
	}
	return strings.Join(parts, ",")
}
