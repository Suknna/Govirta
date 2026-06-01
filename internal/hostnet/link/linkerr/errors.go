// Package linkerr defines stable host link error classes.
package linkerr

import "errors"

var (
	ErrInvalidRequest = errors.New("invalid host link request")
	ErrNotFound       = errors.New("host link not found")
	ErrAlreadyExists  = errors.New("host link already exists")
	ErrConflict       = errors.New("host link conflict")
	ErrPermission     = errors.New("host link permission denied")
	ErrIncompleteList = errors.New("host link list incomplete")
	ErrUnsupported    = errors.New("host link operation unsupported")
)
