//go:build !linux

package controllers

import "fmt"

func openNoFollow(cacheRoot, path string) (cachedFile, error) {
	return nil, fmt.Errorf("safe cached image open is supported only on linux for %q under %q", path, cacheRoot)
}
