package display

import (
	"fmt"

	"github.com/suknna/govirta/pkg/virt/qemu/qopt"
)

// VNCUnix 描述 -vnc unix:<path> 的值。仅支持 unix socket，不支持 TCP
// 监听端口——避免无认证 VNC 网络端点（spec Q54-A 安全约束）。
type VNCUnix struct{ socketPath string }

// VNCUnixSocket 构造一个 unix-socket VNC 显示目标。
func VNCUnixSocket(socketPath string) VNCUnix { return VNCUnix{socketPath: socketPath} }

// Validate 校验 socket 路径不为空且不含 qemu option 注入字符。
func (v VNCUnix) Validate() error {
	return qopt.ValidateValue("vnc unix socket", v.socketPath)
}

// Arg 渲染 -vnc 的值，例如 unix:/var/lib/govirtlet/<uuid>/vnc.sock。
func (v VNCUnix) Arg() (string, error) {
	if err := v.Validate(); err != nil {
		return "", fmt.Errorf("vnc: %w", err)
	}
	return "unix:" + v.socketPath, nil
}
