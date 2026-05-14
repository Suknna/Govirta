//go:build windows

package qemu

import "os"

func execSignalTerm() os.Signal {
	return os.Kill
}
