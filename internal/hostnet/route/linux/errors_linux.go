//go:build linux

package linux

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/suknna/govirta/internal/hostnet/route/routeerr"
	"github.com/vishvananda/netlink"
)

func translateError(operation string, err error) error {
	if err == nil {
		return nil
	}

	class := classifyError(err)
	if class == nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if errors.Is(err, class) {
		return fmt.Errorf("%s: %w", operation, err)
	}

	return fmt.Errorf("%s: %w: %w", operation, class, err)
}

func classifyError(err error) error {
	for _, sentinel := range []error{
		routeerr.ErrInvalidRequest,
		routeerr.ErrInvalidObservedState,
		routeerr.ErrNotReady,
		routeerr.ErrNotFound,
		routeerr.ErrAlreadyExists,
		routeerr.ErrConflict,
		routeerr.ErrPermission,
		routeerr.ErrIncompleteList,
		routeerr.ErrUnsupported,
	} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}

	var linkNotFound netlink.LinkNotFoundError
	if errors.As(err, &linkNotFound) {
		return routeerr.ErrNotFound
	}
	if errors.Is(err, netlink.ErrDumpInterrupted) {
		return routeerr.ErrIncompleteList
	}
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return routeerr.ErrPermission
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ESRCH) {
		return routeerr.ErrNotFound
	}
	if errors.Is(err, syscall.EEXIST) {
		return routeerr.ErrAlreadyExists
	}
	if errors.Is(err, syscall.EINVAL) {
		return routeerr.ErrInvalidRequest
	}

	return nil
}
