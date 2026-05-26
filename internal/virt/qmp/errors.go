package qmp

import (
	"errors"

	"github.com/suknna/govirta/internal/virt/qmp/internal/protocol"
)

type ResponseError = protocol.ResponseError

var (
	// ErrInvalidConfig marks QMP client configuration errors.
	ErrInvalidConfig = errors.New("invalid qmp config")
	// ErrNotConnected marks operations attempted before a QMP connection exists.
	ErrNotConnected = errors.New("qmp client is not connected")
	// ErrAlreadyConnected marks duplicate connect attempts for one client.
	ErrAlreadyConnected = errors.New("qmp client already connected")
	// ErrEventsAlreadyStarted marks duplicate event stream requests for one socket.
	ErrEventsAlreadyStarted = errors.New("qmp events already started")
)
