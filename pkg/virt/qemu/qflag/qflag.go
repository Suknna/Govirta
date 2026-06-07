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

// OptionalString represents a string option that may be absent. It deliberately
// distinguishes "unset" from "set to the empty string": some QEMU options carry
// a meaningful empty value (e.g. romfile= disables a device's option ROM), so an
// empty value must still render as the bare "key=" when explicitly set.
type OptionalString struct {
	value string
	set   bool
}

// String returns a present optional string value. An empty v is a present empty
// value (renders as "key="), not an absence.
func String(v string) OptionalString {
	return OptionalString{value: v, set: true}
}

// IsSet reports whether the option should be rendered.
func (v OptionalString) IsSet() bool { return v.set }

// Value returns the string value.
func (v OptionalString) Value() string { return v.value }
