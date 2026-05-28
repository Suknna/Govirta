package qemuimg

import (
	"github.com/suknna/govirta/internal/virt/qemuimg/check"
	"github.com/suknna/govirta/internal/virt/qemuimg/convert"
	"github.com/suknna/govirta/internal/virt/qemuimg/create"
	"github.com/suknna/govirta/internal/virt/qemuimg/info"
	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
	"github.com/suknna/govirta/internal/virt/qemuimg/remove"
	"github.com/suknna/govirta/internal/virt/qemuimg/resize"
	"github.com/suknna/govirta/internal/virt/qemuimg/snapshot"
)

// ErrInvalidRequest 是 qemu-img 输入校验错误的根因 sentinel；调用方用
// errors.Is(err, ErrInvalidRequest) 区分「请求构造错误」与「外部进程错误」。
var ErrInvalidRequest = imgexec.ErrInvalidRequest

// CommandError 包装 qemu-img 进程级失败，保留 stdout/stderr 便于诊断。
// 调用方通过 errors.As(err, &*CommandError) 提取原始输出。
type CommandError = imgexec.CommandError

// DecodeError 包装 qemu-img JSON 输出解码失败，保留 stdout/stderr 便于诊断。
// 调用方通过 errors.As(err, &*DecodeError) 提取原始输出。
type DecodeError = imgexec.DecodeError

// RunResult 是注入式 qemu-img runner 返回的执行结果类型。
//
// 上层包通过该别名引用 runner 输出，不需要直接依赖 internal/exec。
type RunResult = imgexec.Result

// Runner 是 qemu-img 命令的执行边界。
//
// Config.Runner 接受任意实现，便于在测试或替代后端中注入伪造 runner，
// 而不暴露 internal/exec 的内部类型。
type Runner = imgexec.Runner

// Config 配置 qemu-img 客户端。
//
// Binary 为空时回退到 PATH 中的 "qemu-img"。Runner 为空时使用默认的
// OSRunner。注入自定义 Runner 时调用方必须保证其 Run 方法并发安全：
// QCOW2Client 是值类型，每次 Client.QCOW2() 调用返回的值会共享同一个
// Runner 接口实例，多个并发的 Do(ctx) 都会同时调用 Runner.Run。
type Config struct {
	Binary string
	Runner Runner
}

// Client 是 qemu-img 命令客户端的最小公共契约。
//
// 当前仅暴露 QCOW2() 入口；返回的 QCOW2Client 是值类型，可被安全复制
// 并并发使用——前提是底层 Runner.Run 方法本身并发安全。OSRunner 满足
// 该约束（os/exec.CommandContext 每次创建独立进程）；自定义 Runner
// 实现必须显式保证。
type Client interface {
	QCOW2() QCOW2Client
}

// ExecClient 是 Client 的默认实现，承载 binary + runner 注入。
//
// 并发安全性：在 binary 与 runner 设置完成后字段不再变更，多个 goroutine
// 可并发调用 QCOW2() 与后续子命令；并发安全最终取决于 runner 实现。
type ExecClient struct {
	binary string
	runner Runner
}

// QCOW2Client 是 qcow2 子命令的入口。
//
// 设计为值类型：可以自由复制，每个副本共享同一个 binary 字符串与
// runner 接口实例。子命令构造方法（Create/Info/Convert/Snapshot/Check/
// Remove）每次都返回新的 *Builder，调用方可以并发使用同一个 QCOW2Client
// 而不会污染彼此的 builder 状态——前提同样是 runner 并发安全。
type QCOW2Client struct {
	binary string
	runner imgexec.Runner
}

// NewClient 创建带默认值回填的 ExecClient。Binary 为空回退到 "qemu-img"；
// Runner 为空回退到 OSRunner。返回 *ExecClient 让调用方拿到稳定的指针
// 身份，便于在 logger / metrics 中做对象级别绑定。
func NewClient(config Config) *ExecClient {
	binary := config.Binary
	if binary == "" {
		binary = "qemu-img"
	}
	runner := config.Runner
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &ExecClient{binary: binary, runner: runner}
}

// QCOW2 返回 qcow2 子命令入口。每次调用返回新的值类型副本但共享同一
// runner，因此并发使用时仍受 runner 并发安全约束（见类型说明）。
func (c *ExecClient) QCOW2() QCOW2Client {
	return QCOW2Client{binary: c.binary, runner: c.runner}
}

// Binary returns the configured qemu-img binary path.
func (c QCOW2Client) Binary() string {
	return c.binary
}

// Create returns a builder for creating qcow2 images.
func (c QCOW2Client) Create() *create.Builder {
	return create.New(c.binary, c.runner)
}

// Info returns a builder for querying qcow2 image metadata.
func (c QCOW2Client) Info() *info.Builder {
	return info.New(c.binary, c.runner)
}

// Convert returns a builder for converting images into qcow2 output.
func (c QCOW2Client) Convert() *convert.Builder {
	return convert.New(c.binary, c.runner)
}

// Resize returns a builder for resizing qcow2 images.
func (c QCOW2Client) Resize() *resize.Builder {
	return resize.New(c.binary, c.runner)
}

// Snapshot returns a builder for creating qcow2 internal snapshots.
func (c QCOW2Client) Snapshot() *snapshot.Builder {
	return snapshot.New(c.binary, c.runner)
}

// Check returns a builder for checking qcow2 image consistency.
func (c QCOW2Client) Check() *check.Builder {
	return check.New(c.binary, c.runner)
}

// Remove returns a builder for deleting trusted Govirta-owned qcow2 files.
func (c QCOW2Client) Remove() *remove.Builder {
	return remove.New(c.binary, c.runner)
}
