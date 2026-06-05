package vmm

import (
	"fmt"

	"github.com/suknna/govirta/internal/vmm/proc"
	"github.com/suknna/govirta/pkg/virt/qmp"
)

// QMPFactory 按 socket 路径构造一个 qmp.Client（按需、瞬时；不长期持有）。
// 注入此工厂使单测可用 fake qmp.Client，生产用真实 SocketClient——贴合
// 「QEMU 进程生命周期与编排器解耦」：qmp 连接是瞬时可重连的，不是 guest
// 存活的依赖（spec 硬约束 1）。
type QMPFactory func(socketPath string) (qmp.Client, error)

// VMMService 是节点本地 QEMU 进程生命周期领域服务（spec §3）。
//
// 它不长期持有 qmp.Client，也不缓存运行时态：运行时态永远从 live 探测
// （进程存活 + QMP query-status）派生（spec §4 上下一致）。持久化只承载
// 身份/spec/路径/意图（desired 维度），不承载运行态权威。
type VMMService struct {
	runtimeRoot string
	proc        proc.ProcessController
	qmpFactory  QMPFactory
}

// NewVMMService 构造服务。runtimeRoot 通常是 /var/lib/govirtlet。
// 三个依赖全部必填（显式铁律：不为调用方推断默认值）。
func NewVMMService(runtimeRoot string, pc proc.ProcessController, qf QMPFactory) (*VMMService, error) {
	if runtimeRoot == "" {
		return nil, fmt.Errorf("%w: runtime root is required", ErrInvalidRequest)
	}
	if pc == nil {
		return nil, fmt.Errorf("%w: process controller is required", ErrInvalidRequest)
	}
	if qf == nil {
		return nil, fmt.Errorf("%w: qmp factory is required", ErrInvalidRequest)
	}
	return &VMMService{runtimeRoot: runtimeRoot, proc: pc, qmpFactory: qf}, nil
}
