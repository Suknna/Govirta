package coredhcp

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"

	"github.com/suknna/govirta/pkg/hostnet/dhcp/dhcperr"
)

func classifyStartError(err error) error {
	if err == nil {
		return nil
	}
	if isPermissionError(err) {
		return fmt.Errorf("%w: %w", dhcperr.ErrPermission, err)
	}
	if isConflictError(err) {
		return fmt.Errorf("%w: %w", dhcperr.ErrConflict, err)
	}
	return err
}

func isPermissionError(err error) bool {
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Err != nil && isPermissionError(opErr.Err)
}

func isConflictError(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.EADDRNOTAVAIL) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Err != nil && isConflictError(opErr.Err)
}
