// Package routeerr defines stable host route error classes.
package routeerr

import "errors"

var (
	ErrInvalidRequest       = errors.New("invalid host route request")
	ErrInvalidObservedState = errors.New("invalid observed host route state")
	ErrNotReady             = errors.New("host route prerequisite not ready")
	ErrNotFound             = errors.New("host route not found")
	ErrAlreadyExists        = errors.New("host route already exists")
	ErrConflict             = errors.New("host route conflict")
	ErrPermission           = errors.New("host route permission denied")
	ErrIncompleteList       = errors.New("host route list incomplete")
	ErrUnsupported          = errors.New("host route operation unsupported")
)
