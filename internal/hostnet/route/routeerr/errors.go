// Package routeerr defines stable host route error classes.
package routeerr

import "errors"

var (
	// ErrInvalidRequest reports malformed or incomplete route inputs.
	ErrInvalidRequest = errors.New("invalid host route request")
	// ErrInvalidObservedState reports host route state that cannot be mapped into the route contract.
	ErrInvalidObservedState = errors.New("invalid observed host route state")
	// ErrNotReady reports a host prerequisite that is not in the required state.
	ErrNotReady = errors.New("host route prerequisite not ready")
	// ErrNotFound reports that a requested host route was not found.
	ErrNotFound = errors.New("host route not found")
	// ErrAlreadyExists reports that a route already exists when creation required absence.
	ErrAlreadyExists = errors.New("host route already exists")
	// ErrConflict reports existing host route state that conflicts with the request.
	ErrConflict = errors.New("host route conflict")
	// ErrPermission reports insufficient privileges for host route operations.
	ErrPermission = errors.New("host route permission denied")
	// ErrIncompleteList reports partial route enumeration from the platform.
	ErrIncompleteList = errors.New("host route list incomplete")
	// ErrUnsupported reports a route operation or shape unsupported by this implementation.
	ErrUnsupported = errors.New("host route operation unsupported")
)
