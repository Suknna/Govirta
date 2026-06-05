//go:build linux

package linux

import (
	"errors"
	"fmt"
	"os"

	"github.com/suknna/govirta/pkg/hostnet/firewall/firewallerr"
	"golang.org/x/sys/unix"
)

func translateError(operation string, err error) error {
	if err == nil {
		return nil
	}
	class := classifyError(err)
	if class == err {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w: %w", operation, class, err)
}

func classifyError(err error) error {
	switch {
	case errors.Is(err, firewallerr.ErrInvalidRequest),
		errors.Is(err, firewallerr.ErrInvalidObservedState),
		errors.Is(err, firewallerr.ErrNotFound),
		errors.Is(err, firewallerr.ErrAlreadyExists),
		errors.Is(err, firewallerr.ErrConflict),
		errors.Is(err, firewallerr.ErrPermission),
		errors.Is(err, firewallerr.ErrIncompleteList),
		errors.Is(err, firewallerr.ErrUnsupported):
		return err
	case errors.Is(err, os.ErrPermission), errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
		return firewallerr.ErrPermission
	case errors.Is(err, os.ErrNotExist), errors.Is(err, unix.ENOENT):
		return firewallerr.ErrNotFound
	case errors.Is(err, os.ErrExist), errors.Is(err, unix.EEXIST):
		return firewallerr.ErrAlreadyExists
	default:
		return err
	}
}
