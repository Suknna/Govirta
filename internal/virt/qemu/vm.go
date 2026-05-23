package qemu

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/suknna/govirta/internal/virt/qemu/blockdev"
	"github.com/suknna/govirta/internal/virt/qemu/chardev"
	"github.com/suknna/govirta/internal/virt/qemu/cpu"
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

// Argument appends one QEMU command-line argument form to argv. The interface is
// intentionally sealed; callers should create generic arguments with Arg or Flag.
type Argument interface {
	appendArgv([]string) []string
}

type argvFlag struct{ flag string }

// Flag creates a QEMU flag that has no following value, such as -enable-kvm.
func Flag(flag string) Argument { return argvFlag{flag: flag} }

func (f argvFlag) appendArgv(argv []string) []string { return append(argv, f.flag) }

func (f argvFlag) valid() bool { return f.flag != "" }

func (f argvFlag) name() string { return f.flag }

type argvArg struct {
	flag  string
	value string
}

// Arg creates a QEMU flag/value pair, such as -rtc base=utc.
func Arg(flag string, value string) Argument { return argvArg{flag: flag, value: value} }

func (a argvArg) appendArgv(argv []string) []string { return append(argv, a.flag, a.value) }

func (a argvArg) valid() bool { return a.flag != "" && a.value != "" }

func (a argvArg) name() string { return a.flag }

// Device renders a value accepted by QEMU's -device flag.
type Device interface{ Arg() string }

type Builder struct {
	binary     string
	name       *nameConfig
	machine    *machine.Config
	cpu        cpu.Model
	smp        *SMP
	memory     *Memory
	ordered    []Argument
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

func (b *Builder) Machine(profile machine.Profile) *Builder {
	c := machine.New(profile)
	b.machine = &c
	return b
}

func (b *Builder) CPU(model cpu.Model) *Builder { b.cpu = model; return b }

func (b *Builder) SMP(v SMP) *Builder { b.smp = &v; return b }

func (b *Builder) Memory(v Memory) *Builder { b.memory = &v; return b }

func (b *Builder) AddBlockdev(v blockdev.Qcow2) *Builder {
	b.ordered = append(b.ordered, Arg("-blockdev", v.Arg()))
	return b
}

func (b *Builder) AddDevice(v Device) *Builder {
	if isNilDevice(v) {
		b.ordered = append(b.ordered, Arg("-device", ""))
		return b
	}
	b.ordered = append(b.ordered, Arg("-device", v.Arg()))
	return b
}

func (b *Builder) AddNetdev(v netdev.Tap) *Builder {
	b.ordered = append(b.ordered, Arg("-netdev", v.Arg()))
	return b
}

func (b *Builder) AddChardev(v chardev.Socket) *Builder {
	b.ordered = append(b.ordered, Arg("-chardev", v.Arg()))
	return b
}

func (b *Builder) Monitor(v monitor.Monitor) *Builder {
	b.ordered = append(b.ordered, Arg("-mon", v.Arg()))
	return b
}

func (b *Builder) Serial(v serial.Serial) *Builder {
	b.ordered = append(b.ordered, Arg("-serial", v.Arg()))
	return b
}

// AddArgument appends a generic QEMU argument without adding a dedicated builder method.
func (b *Builder) AddArgument(v Argument) *Builder {
	b.ordered = append(b.ordered, v)
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
	if b.machine != nil && !b.machine.Profile.IsSupported() {
		return VM{}, fmt.Errorf("%w: unsupported machine profile", ErrInvalidVM)
	}
	for _, entry := range b.ordered {
		if !validArgument(entry) {
			return VM{}, fmt.Errorf("%w: invalid qemu argument", ErrInvalidVM)
		}
		if isMachineArgument(argumentName(entry)) {
			return VM{}, fmt.Errorf("%w: -machine must use a predefined profile", ErrInvalidVM)
		}
	}
	return VM{builder: *b}, nil
}

func (v VM) Argv() []string {
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
		argv = entry.appendArgv(argv)
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
	return argv
}

func validArgument(v Argument) bool {
	type validator interface{ valid() bool }
	if checked, ok := v.(validator); ok {
		return checked.valid()
	}
	return v != nil
}

func argumentName(v Argument) string {
	type named interface{ name() string }
	if checked, ok := v.(named); ok {
		return checked.name()
	}
	return ""
}

func isMachineArgument(flag string) bool { return flag == "-machine" || flag == "-M" }

func isNilDevice(v Device) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
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
