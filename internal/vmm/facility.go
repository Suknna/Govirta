package vmm

import (
	"fmt"

	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/chardev"
	"github.com/suknna/govirta/pkg/virt/qemu/display"
	"github.com/suknna/govirta/pkg/virt/qemu/monitor"
	"github.com/suknna/govirta/pkg/virt/qemu/qflag"
	"github.com/suknna/govirta/pkg/virt/qemu/serial"
)

// qmpChardevID 是 QMP 控制 socket 的 chardev id（facility 内部约定）。
const qmpChardevID = "vmm-qmp"

// injectFacilityFlags 向「配置好但未 Build」的 builder 注入 vmm 的运行时设施
// flag，然后 Build + Argv，返回落盘用的 argv 快照（spec §6/§7）。
//
// 注入项：
//   - -pidfile <qemu.pid>：QEMU 自写 pid，供 ProcessAlive 探测
//   - QMP unix socket（-chardev socket,server=on,wait=off + -mon mode=control）：
//     server 模式 + wait=off 让 QEMU 不阻塞等待连接，支持重启后 reattach
//   - -serial file:<console.log>：串口控制台日志（spec §5，Q52「vnc日志」=console.log）
//   - -vnc unix:<vnc.sock>：被动持有的 VNC unix socket（Q54-A，不开 TCP）
//   - -daemonize：QEMU fork 后台脱离父进程（spec 硬约束 1）
//
// chardev/monitor/serial/vnc 的非法值（如路径含逗号）由各自 Validate() 在
// b.Build() 内捕获，统一以 ErrInvalidVM 经本函数的 b.Build() 错误返回，不在
// facility 层重复校验（积木式：校验归各 typed 子包，本层只做注入编排）。
func injectFacilityFlags(b *qemu.Builder, paths RuntimePaths) ([]string, error) {
	if b == nil {
		return nil, fmt.Errorf("%w: builder is required", ErrInvalidRequest)
	}

	b.PidFile(paths.PidFile).
		AddChardev(chardev.Socket{
			ID:     qmpChardevID,
			Path:   paths.QMPSocket,
			Server: qflag.On,
			Wait:   qflag.Off,
		}).
		Monitor(monitor.Monitor{
			Chardev: chardev.Ref(qmpChardevID),
			Mode:    monitor.ModeControl,
		}).
		Serial(serial.File(paths.ConsoleLog)).
		VNC(display.VNCUnixSocket(paths.VNCSocket)).
		Daemonize()

	vm, err := b.Build()
	if err != nil {
		return nil, fmt.Errorf("vmm: build qemu argv: %w", err)
	}
	return vm.Argv(), nil
}
