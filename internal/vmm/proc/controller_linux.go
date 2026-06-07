//go:build linux

// Package proc 的 Linux 真实实现：os/exec daemonize spawn、signal 0 存活探测、
// SIGKILL 兜底、vm.json 原子读写、运行时目录扫描。带真实 OS 副作用，仅在
// Lima acceptance 验证（单测用 fake，不碰真实 QEMU / 文件系统）。
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// LinuxController 是 ProcessController 的 Linux 真实实现。
type LinuxController struct{}

// NewLinuxController 构造 Linux 进程控制原语实现。
func NewLinuxController() *LinuxController { return &LinuxController{} }

// SpawnDaemonized exec QEMU（argv 已含 -daemonize），QEMU fork 到后台后立即
// 退出父进程，cmd.Run 随之返回。
//
// 解耦铁律（spec 硬约束 1）：不设 SysProcAttr.Pdeathsig、不设共享进程组、
// 不让 QEMU 依赖编排器持有的 stdio/QMP 存活。QEMU 原生 -daemonize 自行 fork
// 出真正的 guest 进程并 setsid 脱离，编排器崩溃/重启绝不波及运行中的 guest。
func (c *LinuxController) SpawnDaemonized(ctx context.Context, argv []string, runtimeDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(argv) == 0 {
		return errors.New("proc: spawn requires non-empty argv")
	}
	// 故意不使用 exec.CommandContext：ctx 取消不得 kill 已 daemonize 的 guest
	// （进程生命周期与编排器解耦）。ctx 只在进入 spawn 前做一次取消检查。
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = runtimeDir
	// daemonize 后父进程立即退出，guest 日志走 -D / -serial file:；父进程的
	// stdin/stdout 不被 guest 依赖，丢弃即可。stderr 例外：QEMU 在 fork 出
	// daemonized guest *之前* 的初始化错误（argv 解析、打开磁盘、bind QMP
	// socket 失败）写到 stderr 后带非零退出码退出。捕获它并入错误，否则
	// 调用方只看到无信息的 "exit status 1"（曾是诊断盲点）。这不违反解耦铁律：
	// 捕获的只是 fork 前父进程的同步输出，daemonize 后父进程已退出、guest 已
	// setsid 脱离并走自己的 -D/-serial 日志，buffer 不被运行中的 guest 依赖。
	cmd.Stdin = nil
	cmd.Stdout = nil
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if trimmed := strings.TrimSpace(stderr.String()); trimmed != "" {
			return fmt.Errorf("proc: spawn daemonized qemu: %w: %s", err, trimmed)
		}
		return fmt.Errorf("proc: spawn daemonized qemu: %w", err)
	}
	return nil
}

// ProcessAlive 读 pidfile 解析 pid，再 signal 0 探测存活。
// pidfile 不存在或进程不存在返回 (false, nil)；解析/权限错误返回 error。
func (c *LinuxController) ProcessAlive(ctx context.Context, pidfilePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	pid, ok, err := readPidfile(pidfilePath)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return processAlive(pid)
}

// ForceKill 读 pidfile 后向进程发 SIGKILL（QMP quit 不可达时的兜底）。
// pidfile 不存在或进程已退出视为幂等成功。
func (c *LinuxController) ForceKill(ctx context.Context, pidfilePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pid, ok, err := readPidfile(pidfilePath)
	if err != nil {
		return err
	}
	if !ok {
		return nil // pidfile 不存在：无可杀，幂等成功。
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // 进程已退出：幂等成功。
		}
		return fmt.Errorf("proc: sigkill pid %d: %w", pid, err)
	}
	return nil
}

// WriteState 原子写 vm.json（写临时文件 + rename）；目录不存在则创建。
func (c *LinuxController) WriteState(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("proc: mkdir runtime dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("proc: write temp state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// rename 失败：尽力清理临时文件，合并两个错误保全。
		rmErr := os.Remove(tmp)
		if rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return errors.Join(
				fmt.Errorf("proc: rename state: %w", err),
				fmt.Errorf("proc: cleanup temp state: %w", rmErr),
			)
		}
		return fmt.Errorf("proc: rename state: %w", err)
	}
	return nil
}

// ReadState 读 vm.json 原始字节；文件不存在返回 ErrStateNotFound。
func (c *LinuxController) ReadState(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrStateNotFound
		}
		return nil, fmt.Errorf("proc: read state: %w", err)
	}
	return data, nil
}

// RemoveState 删除整个运行时目录（Delete 用）。
func (c *LinuxController) RemoveState(ctx context.Context, runtimeDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.RemoveAll(runtimeDir); err != nil {
		return fmt.Errorf("proc: remove runtime dir: %w", err)
	}
	return nil
}

// ListStateDirs 扫 runtimeRoot 列出直接子目录名（每个对应一个 uuid）。
// runtimeRoot 不存在返回空切片 + nil（节点首次启动无任何 VM）。
func (c *LinuxController) ListStateDirs(ctx context.Context, runtimeRoot string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(runtimeRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("proc: read runtime root: %w", err)
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs, nil
}

// readPidfile 读 pidfile 并解析 pid。文件不存在返回 (0, false, nil)；
// 内容非法返回 error。
func readPidfile(path string) (int, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("proc: read pidfile: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0, false, nil
	}
	pid, err := strconv.Atoi(text)
	if err != nil {
		return 0, false, fmt.Errorf("proc: parse pidfile %q: %w", text, err)
	}
	if pid <= 0 {
		return 0, false, fmt.Errorf("proc: invalid pid %d in pidfile", pid)
	}
	return pid, true, nil
}

// processAlive 用 signal 0 探测进程是否存活。ESRCH → 不存在；EPERM → 存在
// 但无权发信号（仍视为存活）。
func processAlive(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return false, fmt.Errorf("proc: probe pid %d: %w", pid, err)
}
