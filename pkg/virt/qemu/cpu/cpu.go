package cpu

type Model string

const (
	ModelHost      Model = "host"
	ModelCortexA57 Model = "cortex-a57"
)

func (m Model) Valid() bool {
	switch m {
	case "", ModelHost, ModelCortexA57:
		return true
	default:
		return false
	}
}
