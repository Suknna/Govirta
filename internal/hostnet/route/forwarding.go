package route

type IPv4ForwardingState string

const (
	IPv4ForwardingEnabled  IPv4ForwardingState = "enabled"
	IPv4ForwardingDisabled IPv4ForwardingState = "disabled"
)

type IPv4ForwardingInfo struct {
	State IPv4ForwardingState
	Path  string
}
