package qopt

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Pair describes one QEMU key/value option. Optional pairs are omitted when
// their value is empty; required pairs reject empty values; present-empty-ok
// pairs always render even when the value is empty (a meaningful "key=").
type Pair struct {
	Key        string
	Value      string
	Optional   bool
	allowEmpty bool
}

// Required creates a required QEMU key/value option.
func Required(key string, value string) Pair {
	return Pair{Key: key, Value: value}
}

// Optional creates an optional QEMU key/value option omitted when value is empty.
func Optional(key string, value string) Pair {
	return Pair{Key: key, Value: value, Optional: true}
}

// PresentEmptyOK creates an option that always renders even when value is empty,
// producing a bare "key=". Some QEMU options carry a meaningful empty value
// (e.g. romfile= disables a device's option ROM), which Required rejects and
// Optional omits — this is the explicit third case for them.
func PresentEmptyOK(key string, value string) Pair {
	return Pair{Key: key, Value: value, allowEmpty: true}
}

// Render renders a driver-style QEMU option, such as tap,id=net0.
func Render(driver string, pairs ...Pair) (string, error) {
	if err := validateToken("driver", driver); err != nil {
		return "", err
	}
	parts := []string{driver}
	rendered, err := renderPairs(pairs...)
	if err != nil {
		return "", err
	}
	parts = append(parts, rendered...)
	return strings.Join(parts, ","), nil
}

// RenderPairs renders key-first QEMU options, such as driver=qcow2,node-name=root.
func RenderPairs(pairs ...Pair) (string, error) {
	parts, err := renderPairs(pairs...)
	if err != nil {
		return "", err
	}
	return strings.Join(parts, ","), nil
}

func renderPairs(pairs ...Pair) ([]string, error) {
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		if pair.Optional && pair.Value == "" {
			continue
		}
		if err := validateToken("key", pair.Key); err != nil {
			return nil, err
		}
		// A present-empty-ok pair with an empty value renders as a bare "key=":
		// ValidateValue rejects empty, so skip it for this explicit empty case
		// (a non-empty value still goes through full validation below).
		if pair.allowEmpty && pair.Value == "" {
			parts = append(parts, pair.Key+"=")
			continue
		}
		if err := ValidateValue(pair.Key, pair.Value); err != nil {
			return nil, err
		}
		parts = append(parts, pair.Key+"="+pair.Value)
	}
	return parts, nil
}

// ValidateValue rejects QEMU option values that could escape into adjacent
// comma-delimited options. QEMU has richer escaping rules, but rejecting unsafe
// delimiters keeps this first-stage builder deterministic and auditable.
func ValidateValue(name string, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid utf-8", name)
	}
	for _, r := range value {
		switch r {
		case ',', '\x00', '\n', '\r':
			return fmt.Errorf("%s contains invalid qemu option character %q", name, r)
		}
		if r < 0x20 {
			return fmt.Errorf("%s contains control character %q", name, r)
		}
	}
	return nil
}

// ValidateEnum validates a non-empty string-backed enum value.
func ValidateEnum(name string, value string, valid bool) error {
	if value == "" {
		return nil
	}
	if err := ValidateValue(name, value); err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("%s has unsupported value %q", name, value)
	}
	return nil
}

func validateToken(name string, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.ContainsAny(value, ",=\x00\n\r") {
		return fmt.Errorf("%s contains invalid qemu option token character", name)
	}
	return nil
}
