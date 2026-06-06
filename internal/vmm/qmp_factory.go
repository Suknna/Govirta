package vmm

import "github.com/suknna/govirta/pkg/virt/qmp"

// ProductionQMPFactory 是生产用的 QMPFactory：按 socket 路径构造真实
// qmp.SocketClient。注入此工厂使 vmm 在节点上连接真实 QEMU 的 QMP unix
// socket；单测改注入 fake，二者经同一 QMPFactory 边界整体替换（积木式铁律）。
func ProductionQMPFactory(socketPath string) (qmp.Client, error) {
	return qmp.NewSocketClient(qmp.Config{SocketPath: socketPath})
}
