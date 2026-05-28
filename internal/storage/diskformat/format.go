package diskformat

// Format identifies a storage image byte format accepted by storage APIs.
type Format string

const (
	// FormatQCOW2 identifies QEMU qcow2 image bytes.
	FormatQCOW2 Format = "qcow2"
	// FormatRaw identifies raw disk image bytes.
	FormatRaw Format = "raw"
)

// Valid reports whether f is one of the explicitly supported formats.
func (f Format) Valid() bool {
	switch f {
	case FormatQCOW2, FormatRaw:
		return true
	default:
		return false
	}
}
