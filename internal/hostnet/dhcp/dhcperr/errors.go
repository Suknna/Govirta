package dhcperr

import "errors"

var (
	ErrInvalidRequest       = errors.New("invalid DHCP request")
	ErrNotFound             = errors.New("DHCP resource not found")
	ErrAlreadyExists        = errors.New("DHCP resource already exists")
	ErrAlreadyRunning       = errors.New("DHCP server already running")
	ErrNotRunning           = errors.New("DHCP server not running")
	ErrConflict             = errors.New("DHCP resource conflict")
	ErrPermission           = errors.New("DHCP permission denied")
	ErrUnsupported          = errors.New("DHCP operation unsupported")
	ErrInvalidObservedState = errors.New("invalid observed DHCP state")
)
