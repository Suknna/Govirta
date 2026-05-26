// Package qflag defines primitive QEMU flag value types shared by qemu packages.
package qflag

// OnOff represents QEMU's common on/off option values.
type OnOff string

const (
	// On renders as "on".
	On OnOff = "on"
	// Off renders as "off".
	Off OnOff = "off"
)

// Valid reports whether the value is unset or one of QEMU's supported on/off values.
func (v OnOff) Valid() bool { return v == "" || v == On || v == Off }

// OptionalInt represents an integer option that may be absent.
type OptionalInt struct {
	value int
	set   bool
}

// Int returns a present optional integer value.
func Int(v int) OptionalInt {
	return OptionalInt{value: v, set: true}
}

// IsSet reports whether the option should be rendered.
func (v OptionalInt) IsSet() bool { return v.set }

// Value returns the integer value.
func (v OptionalInt) Value() int { return v.value }
