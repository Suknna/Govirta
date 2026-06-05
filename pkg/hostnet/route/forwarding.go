package route

// IPv4ForwardingState reports whether host IPv4 forwarding is enabled.
type IPv4ForwardingState string

const (
	// IPv4ForwardingEnabled reports that host IPv4 forwarding is enabled.
	IPv4ForwardingEnabled IPv4ForwardingState = "enabled"
	// IPv4ForwardingDisabled reports that host IPv4 forwarding is disabled.
	IPv4ForwardingDisabled IPv4ForwardingState = "disabled"
)

// IPv4ForwardingInfo reports observed host IPv4 forwarding state.
//
// Path identifies the host source that was read, such as the Linux procfs sysctl
// file. Managers report this state but must not mutate forwarding configuration.
type IPv4ForwardingInfo struct {
	State IPv4ForwardingState
	Path  string
}
