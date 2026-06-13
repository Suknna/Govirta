//go:build darwin

package controllers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	darwinSYSOpenat  = 463
	darwinODirectory = 0x00100000
)

func openNoFollow(cacheRoot, path string) (cachedFile, error) {
	rel, err := filepath.Rel(cacheRoot, path)
	if err != nil {
		return nil, fmt.Errorf("cached image path %q is not relative to cache root %q: %w", path, cacheRoot, err)
	}
	if rel == "." || rel == "" || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(os.PathSeparator) {
		return nil, fmt.Errorf("cached image path %q escapes cache root %q", path, cacheRoot)
	}

	parentRel := filepath.Dir(rel)
	leaf := filepath.Base(rel)
	if leaf == "." || leaf == ".." || leaf == string(os.PathSeparator) {
		return nil, fmt.Errorf("cached image path %q has invalid leaf", path)
	}

	parent, err := openDarwinCacheParentAt(cacheRoot, parentRel)
	if err != nil {
		return nil, err
	}
	defer parent.Close()

	fd, err := darwinOpenat(int(parent.Fd()), leaf, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openDarwinCacheParentAt(cacheRoot, parentRel string) (*os.File, error) {
	current, err := os.Open(cacheRoot)
	if err != nil {
		return nil, fmt.Errorf("open cache root %q: %w", cacheRoot, err)
	}

	if parentRel == "." {
		return current, nil
	}

	for _, segment := range splitPath(parentRel) {
		fd, err := darwinOpenat(int(current.Fd()), segment, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|darwinODirectory, 0)
		if err != nil {
			closeErr := current.Close()
			if closeErr != nil {
				return nil, fmt.Errorf("open cache parent segment %q: %w", segment, errors.Join(err, closeErr))
			}
			return nil, fmt.Errorf("open cache parent segment %q: %w", segment, err)
		}
		next := os.NewFile(uintptr(fd), segment)
		if err := current.Close(); err != nil {
			_ = next.Close()
			return nil, fmt.Errorf("close cache parent while opening %q: %w", segment, err)
		}
		current = next
	}

	return current, nil
}

func darwinOpenat(dirfd int, path string, flags int, perm uint32) (int, error) {
	pathPtr, err := syscall.BytePtrFromString(path)
	if err != nil {
		return -1, err
	}
	fd, _, errno := syscall.Syscall6(darwinSYSOpenat, uintptr(dirfd), uintptr(unsafe.Pointer(pathPtr)), uintptr(flags), uintptr(perm), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}
