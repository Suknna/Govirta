package vmm

import (
	"time"

	"github.com/suknna/govirta/pkg/virt/qemu"
)

// Phase 是对外观测的 VM 运行态（由 IntendedPhase + live 探测派生，spec §4）。
// 强类型常量（项目铁律：state-machine 值不能用裸 string）。
type Phase string

const (
	// PhaseDefined 已建未启：intent=defined 且无进程。
	PhaseDefined Phase = "defined"
	// PhaseStarting 进程活、QMP 未就绪/未 running。
	PhaseStarting Phase = "starting"
	// PhaseRunning 进程活、QMP query-status=running。
	PhaseRunning Phase = "running"
	// PhaseStopping intent=stopped（已发 powerdown）但进程仍活。
	PhaseStopping Phase = "stopping"
	// PhaseStopped 进程死、intent=stopped。
	PhaseStopped Phase = "stopped"
	// PhaseFailed 进程死、intent=running（异常退出 / spawn 失败）。
	PhaseFailed Phase = "failed"
)

// IntendedPhase 是持久化到 vm.json 的意图态（desired 维度）。
// 仅用于在 live 信号相同的态之间消歧（spec §4 冲突仲裁）；冲突时 live 赢。
type IntendedPhase string

const (
	// IntendedDefined Create 后、尚未 Start 的初始意图。
	IntendedDefined IntendedPhase = "defined"
	// IntendedRunning Start 写入：调用方意图让 VM 运行。
	IntendedRunning IntendedPhase = "running"
	// IntendedStopped Stop/Kill 写入：调用方意图让 VM 停止。
	IntendedStopped IntendedPhase = "stopped"
)

// Valid 报告 IntendedPhase 是否为已知值。
func (p IntendedPhase) Valid() bool {
	switch p {
	case IntendedDefined, IntendedRunning, IntendedStopped:
		return true
	default:
		return false
	}
}

// SpecSummary 是落盘的不可变 VM spec 摘要（上报用，非运行态权威，spec §5）。
type SpecSummary struct {
	Arch      string   `json:"arch"`
	VCPUs     int      `json:"vcpus"`
	MemoryMiB int      `json:"memory_mib"`
	DiskPaths []string `json:"disk_paths"`
	TapNames  []string `json:"tap_names"`
}

// RuntimePaths 是运行时目录内各文件的绝对路径集（vmm 私有布局产物，spec §5）。
type RuntimePaths struct {
	Dir        string `json:"dir"`
	StateFile  string `json:"state_file"`
	PidFile    string `json:"pid_file"`
	QEMULog    string `json:"qemu_log"`
	QMPSocket  string `json:"qmp_socket"`
	VNCSocket  string `json:"vnc_socket"`
	ConsoleLog string `json:"console_log"`
}

// persistedState 是 vm.json 的磁盘表示。argv 落盘是「持久化 argv 模型」的核心：
// Start 直接 exec 它、Discover 重启后据它重建，无需重持 builder。
type persistedState struct {
	UUID      string        `json:"uuid"`
	Spec      SpecSummary   `json:"spec"`
	Paths     RuntimePaths  `json:"paths"`
	Argv      []string      `json:"argv"`
	Intended  IntendedPhase `json:"intended_phase"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// VM 是对外返回的 VM 视图：身份 + spec + 路径 + 持久意图 + live 观测 Phase。
type VM struct {
	UUID     string
	Spec     SpecSummary
	Paths    RuntimePaths
	Intended IntendedPhase
	Phase    Phase // live 派生，不落盘
}

// CreateRequest 是 Create 的输入。Builder 为「配置好但未 Build」的 builder
// （Q6-A + Q70）：上层设 cpu/内存/machine/磁盘/tap，vmm 注入设施 flag 后 Build。
type CreateRequest struct {
	UUID    string        // 调用方显式提供，vmm 不生成
	Builder *qemu.Builder // 配置好但未 Build；vmm 收尾注入设施 flag + Build
	Spec    SpecSummary   // 上报用只读摘要
}
