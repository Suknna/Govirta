// Package networker defines stable error sentinels for the VM-facing network
// orchestration layer. Lower-layer primitive errors (linkerr, routeerr,
// firewallerr, dhcperr) are wrapped with %w and classified into these classes
// so callers can branch with errors.Is regardless of the failing primitive.
package networker

import "errors"

var (
	// ErrInvalidRequest marks caller input that cannot form a valid network operation.
	ErrInvalidRequest = errors.New("invalid network request")
	// ErrNotFound marks lookup or mutation requests for an unknown network or NIC.
	ErrNotFound = errors.New("network resource not found")
	// ErrAlreadyExists marks registration requests that would replace an existing definition.
	ErrAlreadyExists = errors.New("network resource already exists")
	// ErrConflict marks requests that violate current network or NIC definition state.
	ErrConflict = errors.New("network resource conflict")
	// ErrNotReady marks orchestration blocked by an unmet host prerequisite (e.g. IPv4 forwarding).
	ErrNotReady = errors.New("network prerequisite not ready")
)
