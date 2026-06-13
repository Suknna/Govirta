//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd

package controllers

import "fmt"

func openNoFollow(cacheRoot, path string) (cachedFile, error) {
	return nil, fmt.Errorf("safe cached image open is unsupported on this platform for %q under %q", path, cacheRoot)
}
