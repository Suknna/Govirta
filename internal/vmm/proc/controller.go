// Package proc 定义 vmm 的进程控制原语边界：spawn daemonized QEMU、
// 进程存活探测、SIGKILL 兜底、vm.json 原子读写、运行时目录扫描。
// 生产实现带真实 OS 副作用（controller_linux.go）；单测注入 fake，
// 使 vmm 状态机与编排逻辑无需真实 QEMU 即可验证。
package proc

import "context"

// ProcessController 抽象所有带真实 OS 副作用的进程/文件操作。
// QMP 交互不在此边界（pkg/virt/qmp 已是独立可替换边界，由 QMPFactory 注入）。
//
// 积木式可替换边界（项目铁律）：上层 vmm service 只依赖此接口，生产注入
// Linux 实现、单测注入 fake，二者可整体替换而不波及 service 编排逻辑。
type ProcessController interface {
	// SpawnDaemonized exec QEMU（argv 已含 -daemonize），QEMU fork 到后台后
	// 立即返回。QEMU 自己写 -pidfile；本方法不持有子进程、不 Wait、不依赖
	// 父子关系（spec 硬约束 1：编排器死后 guest 必须存活）。runtimeDir 作为
	// 子进程工作目录。
	SpawnDaemonized(ctx context.Context, argv []string, runtimeDir string) error

	// ProcessAlive 读 pidfile 解析 pid，再 signal 0 探测进程是否存活。
	// pidfile 不存在或进程不存在返回 (false, nil)；解析/权限错误返回 error。
	ProcessAlive(ctx context.Context, pidfilePath string) (bool, error)

	// ForceKill 读 pidfile 后向进程发 SIGKILL（QMP quit 不可达时的兜底）。
	// 进程已不存在视为幂等成功（返回 nil）。
	ForceKill(ctx context.Context, pidfilePath string) error

	// WriteState 原子写 vm.json（写临时文件 + rename）；目录不存在则创建。
	WriteState(ctx context.Context, path string, data []byte) error

	// ReadState 读 vm.json 原始字节；文件不存在返回 ErrStateNotFound。
	ReadState(ctx context.Context, path string) ([]byte, error)

	// RemoveState 删除整个运行时目录（Delete 用）。
	RemoveState(ctx context.Context, runtimeDir string) error

	// ListStateDirs 扫 runtimeRoot 列出直接子目录名（每个对应一个 uuid）。
	// runtimeRoot 不存在返回空切片 + nil（节点首次启动无任何 VM）。
	ListStateDirs(ctx context.Context, runtimeRoot string) ([]string, error)
}
