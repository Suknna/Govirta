//go:build !linux

// Package proc 的非 Linux 占位实现：所有方法返回 unsupported。
// 仅用于让 darwin 等非 Linux 平台编译通过（vmm service 是平台无关纯逻辑，
// 但 proc 包会被 service 引用）。真实进程控制只在 Linux + Lima acceptance 运行。
package proc

import (
	"context"
	"errors"
	"runtime"
)

// ErrUnsupportedPlatform 表示当前平台没有真实进程控制实现。
var ErrUnsupportedPlatform = errors.New("proc: process control unsupported on " + runtime.GOOS)

// LinuxController 在非 Linux 平台是一个永远返回 unsupported 的占位类型，
// 使 service 仍可编译、CLI 仍可构建，但真实进程操作必须在 Linux 上进行。
type LinuxController struct{}

// NewLinuxController 在非 Linux 平台返回占位实现。
func NewLinuxController() *LinuxController { return &LinuxController{} }

func (c *LinuxController) SpawnDaemonized(ctx context.Context, argv []string, runtimeDir string) error {
	return ErrUnsupportedPlatform
}

func (c *LinuxController) ProcessAlive(ctx context.Context, pidfilePath string) (bool, error) {
	return false, ErrUnsupportedPlatform
}

func (c *LinuxController) ForceKill(ctx context.Context, pidfilePath string) error {
	return ErrUnsupportedPlatform
}

func (c *LinuxController) WriteState(ctx context.Context, path string, data []byte) error {
	return ErrUnsupportedPlatform
}

func (c *LinuxController) ReadState(ctx context.Context, path string) ([]byte, error) {
	return nil, ErrUnsupportedPlatform
}

func (c *LinuxController) RemoveState(ctx context.Context, runtimeDir string) error {
	return ErrUnsupportedPlatform
}

func (c *LinuxController) ListStateDirs(ctx context.Context, runtimeRoot string) ([]string, error) {
	return nil, ErrUnsupportedPlatform
}
