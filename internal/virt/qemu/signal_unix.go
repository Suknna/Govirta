//go:build !windows

package qemu

import (
	"os"
	"syscall"
)

func execSignalTerm() os.Signal {
	return syscall.SIGTERM
}
