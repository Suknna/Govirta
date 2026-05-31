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
	"github.com/suknna/govirta/internal/virt/qemu/device"
	"github.com/suknna/govirta/internal/virt/qemu/display"
	"github.com/suknna/govirta/internal/virt/qemu/firmware"
	"github.com/suknna/govirta/internal/virt/qemu/machine"
	"github.com/suknna/govirta/internal/virt/qemu/monitor"
	"github.com/suknna/govirta/internal/virt/qemu/netdev"
	"github.com/suknna/govirta/internal/virt/qemu/qflag"
	"github.com/suknna/govirta/internal/virt/qemu/qopt"
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
// intentionally sealed; callers should create generic arguments with Arg, Flag,
// or TypedArg. Build validates all generic arguments with an allowlist and an
// arity policy before exposing argv.
type Argument interface {
	appendArgv([]string) []string
}

type argumentShape string

const (
	argumentShapeFlag       argumentShape = "flag"
	argumentShapeValue      argumentShape = "value"
	argumentShapeTypedValue argumentShape = "typed-value"
	argumentShapeUnknown    argumentShape = "unknown"
)

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

type typedArgument struct {
	flag     string
	render   func() (string, error)
	value    string
	err      error
	internal bool
}

func TypedArg(flag string, render func() (string, error)) Argument {
	return &typedArgument{flag: flag, render: render}
}

func typedArg(flag string, render func() (string, error)) Argument {
	return &typedArgument{flag: flag, render: render, internal: true}
}

func (a *typedArgument) appendArgv(argv []string) []string { return append(argv, a.flag, a.value) }

func (a *typedArgument) valid() bool {
	a.prepare()
	return a.err == nil && a.flag != "" && a.value != ""
}

func (a *typedArgument) name() string { return a.flag }

func (a *typedArgument) argumentError() error {
	a.prepare()
	return a.err
}

func (a *typedArgument) prepare() {
	if a == nil || a.err != nil || a.value != "" {
		return
	}
	if a.render == nil {
		a.err = errors.New("renderer is required")
		return
	}
	a.value, a.err = a.render()
}

// Device renders a value accepted by QEMU's -device flag.
type Device interface{ Arg() (string, error) }

type Builder struct {
	binary string
	// archErr 记录 NewVM 收到未知 Arch 时的错误，使 Build 能给出
	// 「unsupported arch X」而不是模糊的「qemu binary is required」。
	// 显式调用 Binary(path) 提供非空路径会清除此错误，因为显式 binary
	// 优先于基于 arch 的自动选择。
	archErr    error
	name       *nameConfig
	machine    *machine.Config
	cpu        cpu.Model
	smp        *SMP
	memory     *Memory
	ordered    []Argument
	display    display.Display
	noNIC      bool
	network    bool
	noReboot   bool
	noShutdown bool
	msg        *Msg
	pidFile    string
	err        error
}

type VM struct{ builder Builder }

// NewVM 根据 arch 选择默认 QEMU binary。未知 arch 会被记录到内部 archErr，
// Build 时返回「unsupported arch X」错误；调用方若要使用未在内置列表中
// 的 arch（例如远程主机的 /usr/libexec/qemu-kvm），应紧接着调用 Binary
// 显式指定二进制路径以覆盖错误。
func NewVM(arch Arch) *Builder {
	bin := binaryForArch(arch)
	b := &Builder{binary: bin}
	if bin == "" {
		b.archErr = fmt.Errorf("unsupported arch %q, expected %q or %q", string(arch), ArchX86_64, ArchAArch64)
	}
	return b
}

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

// Binary 显式指定 QEMU 可执行文件路径。提供非空 path 会清除 NewVM 阶段
// 因未知 Arch 记录的 archErr，因为显式 binary 优先级高于 arch 自动选择。
func (b *Builder) Binary(path string) *Builder {
	b.binary = path
	if path != "" {
		b.archErr = nil
	}
	return b
}

func (b *Builder) Name(name string, opts ...NameOption) *Builder {
	c := nameConfig{value: name}
	for _, opt := range opts {
		if opt == nil {
			b.err = errors.Join(b.err, errors.New("nil name option"))
			continue
		}
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
	b.ordered = append(b.ordered, typedArg("-blockdev", v.Arg))
	return b
}

func (b *Builder) AddDevice(v Device) *Builder {
	if isNilDevice(v) {
		b.ordered = append(b.ordered, typedArg("-device", func() (string, error) { return "", errors.New("device is required") }))
		return b
	}
	switch v.(type) {
	case device.VirtioNetPCI, *device.VirtioNetPCI:
		b.network = true
	}
	b.ordered = append(b.ordered, typedArg("-device", v.Arg))
	return b
}

func (b *Builder) AddNetdev(v netdev.Tap) *Builder {
	b.network = true
	b.ordered = append(b.ordered, typedArg("-netdev", v.Arg))
	return b
}

func (b *Builder) AddChardev(v chardev.Socket) *Builder {
	b.ordered = append(b.ordered, typedArg("-chardev", v.Arg))
	return b
}

func (b *Builder) BIOS(v firmware.BIOS) *Builder {
	b.ordered = append(b.ordered, typedArg("-bios", v.Arg))
	return b
}

func (b *Builder) Monitor(v monitor.Monitor) *Builder {
	b.ordered = append(b.ordered, typedArg("-mon", v.Arg))
	return b
}

func (b *Builder) Serial(v serial.Serial) *Builder {
	b.ordered = append(b.ordered, typedArg("-serial", v.Arg))
	return b
}

// AddArgument appends an allowlisted generic QEMU argument without adding a dedicated builder method.
func (b *Builder) AddArgument(v Argument) *Builder {
	b.ordered = append(b.ordered, v)
	return b
}

func (b *Builder) Display(v display.Display) *Builder { b.display = v; return b }

// NoNIC renders an explicit -nic none so callers do not rely on QEMU network defaults.
func (b *Builder) NoNIC() *Builder              { b.noNIC = true; return b }
func (b *Builder) NoReboot() *Builder           { b.noReboot = true; return b }
func (b *Builder) NoShutdown() *Builder         { b.noShutdown = true; return b }
func (b *Builder) Msg(v Msg) *Builder           { b.msg = &v; return b }
func (b *Builder) PidFile(path string) *Builder { b.pidFile = path; return b }

func (b *Builder) Build() (VM, error) {
	if b.err != nil {
		return VM{}, fmt.Errorf("%w: %v", ErrInvalidVM, b.err)
	}
	if b.binary == "" {
		// archErr 优先于通用「binary 缺失」，让调用方一眼看出是 arch 拼写错误。
		if b.archErr != nil {
			return VM{}, fmt.Errorf("%w: %v", ErrInvalidVM, b.archErr)
		}
		return VM{}, fmt.Errorf("%w: qemu binary is required", ErrInvalidVM)
	}
	if b.name != nil {
		if err := b.name.validate(); err != nil {
			return VM{}, fmt.Errorf("%w: invalid name: %v", ErrInvalidVM, err)
		}
	}
	if b.smp != nil && (b.smp.CPUs <= 0 || b.smp.Cores <= 0 || b.smp.Threads <= 0 || b.smp.Sockets <= 0) {
		return VM{}, fmt.Errorf("%w: smp values must be positive", ErrInvalidVM)
	}
	if !b.cpu.Valid() {
		return VM{}, fmt.Errorf("%w: unsupported cpu model %q", ErrInvalidVM, b.cpu)
	}
	if b.memory != nil && b.memory.MiB <= 0 {
		return VM{}, fmt.Errorf("%w: memory must be positive", ErrInvalidVM)
	}
	if !b.display.Valid() {
		return VM{}, fmt.Errorf("%w: unsupported display %q", ErrInvalidVM, b.display)
	}
	if b.msg != nil {
		if err := b.msg.validate(); err != nil {
			return VM{}, fmt.Errorf("%w: invalid msg: %v", ErrInvalidVM, err)
		}
	}
	if b.machine != nil && !b.machine.Profile.IsSupported() {
		return VM{}, fmt.Errorf("%w: unsupported machine profile", ErrInvalidVM)
	}
	if b.noNIC && b.network {
		return VM{}, fmt.Errorf("%w: NoNIC cannot be combined with explicit network devices", ErrInvalidVM)
	}
	for _, entry := range b.ordered {
		if !validArgument(entry) {
			if err := argumentError(entry); err != nil {
				return VM{}, fmt.Errorf("%w: invalid qemu argument: %v", ErrInvalidVM, err)
			}
			return VM{}, fmt.Errorf("%w: invalid qemu argument", ErrInvalidVM)
		}
		if err := validateGenericArgumentPolicy(entry); err != nil {
			return VM{}, fmt.Errorf("%w: %v", ErrInvalidVM, err)
		}
	}
	built := *b
	built.ordered = append([]Argument(nil), b.ordered...)
	return VM{builder: built}, nil
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
	if b.noNIC {
		argv = append(argv, "-nic", "none")
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

func argumentError(v Argument) error {
	type errorer interface{ argumentError() error }
	if checked, ok := v.(errorer); ok {
		return checked.argumentError()
	}
	return nil
}

func validateGenericArgumentPolicy(v Argument) error {
	if isInternalTypedArgument(v) {
		return nil
	}
	flag, shape, ok := genericArgumentShape(v)
	if !ok {
		return nil
	}
	if isRejectedTypedArgument(flag) {
		return fmt.Errorf("%s must use a typed builder", flag)
	}
	if err := validateAllowedGenericArgumentShape(flag, shape); err != nil {
		return err
	}
	return nil
}

func validateAllowedGenericArgumentShape(flag string, shape argumentShape) error {
	switch flag {
	case "-enable-kvm":
		if shape == argumentShapeFlag {
			return nil
		}
		return fmt.Errorf("%s must be added with Flag", flag)
	case "-rtc":
		if shape == argumentShapeValue || shape == argumentShapeTypedValue {
			return nil
		}
		return fmt.Errorf("%s must be added with a value", flag)
	}
	return fmt.Errorf("generic qemu argument %s is not allowlisted", flag)
}

func isInternalTypedArgument(v Argument) bool {
	a, ok := v.(*typedArgument)
	return ok && a != nil && a.internal
}

func genericArgumentShape(v Argument) (string, argumentShape, bool) {
	switch a := v.(type) {
	case argvArg:
		return a.name(), argumentShapeValue, true
	case argvFlag:
		return a.name(), argumentShapeFlag, true
	case *typedArgument:
		if a == nil {
			return "", argumentShapeUnknown, false
		}
		return a.name(), argumentShapeTypedValue, true
	}

	type named interface{ name() string }
	if checked, ok := v.(named); ok {
		return checked.name(), argumentShapeUnknown, true
	}
	return "", argumentShapeUnknown, false
}

func isRejectedTypedArgument(flag string) bool {
	switch flag {
	case "-machine", "-M", "-name", "-cpu", "-smp", "-m", "-bios", "-blockdev", "-device", "-netdev", "-chardev", "-mon", "-serial", "-display", "-msg", "-pidfile", "-no-reboot", "-no-shutdown":
		return true
	default:
		return false
	}
}

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

func (c nameConfig) validate() error {
	if err := qopt.ValidateValue("name", c.value); err != nil {
		return err
	}
	return qopt.ValidateEnum("debug-threads", string(c.debugThreads), c.debugThreads.Valid())
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

func (m Msg) validate() error {
	if err := qopt.ValidateEnum("timestamp", string(m.Timestamp), m.Timestamp.Valid()); err != nil {
		return err
	}
	return qopt.ValidateEnum("guest-name", string(m.GuestName), m.GuestName.Valid())
}
