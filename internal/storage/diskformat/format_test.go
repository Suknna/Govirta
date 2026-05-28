package diskformat

import "testing"

func TestFormatValidAcceptsExplicitFormats(t *testing.T) {
	for _, f := range []Format{FormatQCOW2, FormatRaw} {
		if !f.Valid() {
			t.Fatalf("%q Valid() = false, want true", f)
		}
	}
}

func TestFormatValidRejectsImplicitOrUnknownFormats(t *testing.T) {
	for _, f := range []Format{"", "vmdk", "QCOW2"} {
		if f.Valid() {
			t.Fatalf("%q Valid() = true, want false", f)
		}
	}
}
