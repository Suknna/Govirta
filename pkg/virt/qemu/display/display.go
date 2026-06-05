package display

type Display string

const None Display = "none"

func (d Display) Valid() bool {
	switch d {
	case "", None:
		return true
	default:
		return false
	}
}
