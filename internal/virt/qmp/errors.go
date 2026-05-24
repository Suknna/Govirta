package qmp

import "errors"

var (
	// ErrInvalidConfig marks QMP client configuration errors.
	ErrInvalidConfig = errors.New("invalid qmp config")
	// ErrNotConnected marks operations attempted before a QMP connection exists.
	ErrNotConnected = errors.New("qmp client is not connected")
	// ErrEventsAlreadyStarted marks duplicate event stream requests for one socket.
	ErrEventsAlreadyStarted = errors.New("qmp events already started")
)
