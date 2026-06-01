//go:build linux

package linux

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/suknna/govirta/internal/hostnet/link/linkerr"
	"github.com/vishvananda/netlink"
)

func translateError(op string, err error) error {
	if err == nil {
		return nil
	}
	class := classifyError(err)
	if class == nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if errors.Is(err, class) {
		return fmt.Errorf("%s: %w", op, err)
	}

	return fmt.Errorf("%s: %w: %w", op, class, err)
}

func classifyError(err error) error {
	for _, sentinel := range []error{
		linkerr.ErrInvalidRequest,
		linkerr.ErrNotFound,
		linkerr.ErrAlreadyExists,
		linkerr.ErrConflict,
		linkerr.ErrPermission,
		linkerr.ErrIncompleteList,
		linkerr.ErrUnsupported,
	} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}

	var notFound netlink.LinkNotFoundError
	if errors.As(err, &notFound) {
		return linkerr.ErrNotFound
	}
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return linkerr.ErrPermission
	}
	if errors.Is(err, syscall.EEXIST) {
		return linkerr.ErrAlreadyExists
	}
	if errors.Is(err, syscall.EINVAL) {
		return linkerr.ErrInvalidRequest
	}
	if errors.Is(err, netlink.ErrDumpInterrupted) {
		return linkerr.ErrIncompleteList
	}

	return nil
}
