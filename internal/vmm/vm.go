package vmm

import (
	"time"
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

// SpecSummary 是落盘的 VM 配置权威描述（spec §5）。argv 由它确定性派生，
// 它是 json→qemu flag 单向映射的唯一来源（无第二份 argv 配置）。
type SpecSummary struct {
	Name      string     `json:"name"`
	Arch      string     `json:"arch"`
	VCPUs     int        `json:"vcpus"`
	MemoryMiB int        `json:"memory_mib"`
	CPUModel  string     `json:"cpu_model"`
	Disks     []DiskSpec `json:"disks"`
	NICs      []NICSpec  `json:"nics"`
	CDROMs    []CDROM    `json:"cdroms,omitempty"`
}

// DiskSpec 是一块已解析的物理盘配置。Path 由控制器从 Volume.status.VolumePath
// 解析；NodeName/Frontend 是 vmm 派生时的内部约定常量，不属于配置描述。
type DiskSpec struct {
	Path string `json:"path"`
}

// NICSpec 是一张已解析的物理网卡配置。TapName 来自 NIC.status.TapName，
// MAC 是控制面分配的 NIC.spec.MAC，原样贯穿到 qemu argv（memory 698）。
type NICSpec struct {
	TapName string `json:"tap_name"`
	MAC     string `json:"mac"`
}

// BootIndexMode names how a CD-ROM participates in QEMU boot ordering.
type BootIndexMode string

const (
	// BootIndexModeUnset means no boot index is assigned to the CD-ROM.
	BootIndexModeUnset BootIndexMode = "unset"
	// BootIndexModeIndex means BootIndex carries an explicit QEMU boot index.
	BootIndexModeIndex BootIndexMode = "index"
)

// CDROM is a resolved node-local ISO cache attachment for later argv derivation.
type CDROM struct {
	ImageName     string        `json:"image_name"`
	ImageUID      string        `json:"image_uid"`
	Version       string        `json:"version"`
	CachedPath    string        `json:"cached_path"`
	SHA256        string        `json:"sha256"`
	BootIndexMode BootIndexMode `json:"boot_index_mode"`
	BootIndex     *int          `json:"boot_index,omitempty"`
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

// CreateRequest 是 Create 的输入。Spec 是唯一配置权威；vmm 据它确定性派生 argv，
// 不再接收外部 Builder（杜绝 argv↔Spec 漂移）。
type CreateRequest struct {
	UUID string      // 调用方显式提供，vmm 不生成
	Spec SpecSummary // 唯一配置权威
}
