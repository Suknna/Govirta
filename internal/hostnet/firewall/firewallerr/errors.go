package firewallerr

import "errors"

var (
	// ErrInvalidRequest reports malformed or incomplete firewall inputs.
	ErrInvalidRequest = errors.New("invalid host firewall request")
	// ErrInvalidObservedState reports firewall state that cannot be translated into Govirta types.
	ErrInvalidObservedState = errors.New("invalid observed firewall state")
	// ErrNotFound reports a missing firewall table, chain, or rule.
	ErrNotFound = errors.New("host firewall rule not found")
	// ErrAlreadyExists reports an unexpected duplicate firewall object.
	ErrAlreadyExists = errors.New("host firewall rule already exists")
	// ErrConflict reports existing firewall state that conflicts with the requested rule.
	ErrConflict = errors.New("host firewall rule conflict")
	// ErrPermission reports insufficient privileges for firewall operations.
	ErrPermission = errors.New("host firewall permission denied")
	// ErrIncompleteList reports that firewall enumeration could not return a complete result.
	ErrIncompleteList = errors.New("incomplete host firewall rule list")
	// ErrUnsupported reports firewall operations outside the current implementation scope.
	ErrUnsupported = errors.New("unsupported host firewall operation")
)
