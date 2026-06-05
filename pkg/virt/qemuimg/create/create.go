package create

import (
	"context"
	"strconv"
	"strings"

	imgargv "github.com/suknna/govirta/pkg/virt/qemuimg/internal/argv"
	imgexec "github.com/suknna/govirta/pkg/virt/qemuimg/internal/exec"
)

// Builder 组装 qemu-img create 调用。base 是可选项：
//   - 设置 base：生成带 backing file 的 overlay qcow2（CirrOS overlay 流程）。
//   - 不设置 base：生成全新的空 qcow2（新 VM 数据盘 / 根盘流程）。
type Builder struct {
	binary string
	runner imgexec.Runner
	target string
	base   string
	size   int64
}

func New(binary string, runner imgexec.Runner) *Builder {
	if runner == nil {
		runner = imgexec.OSRunner{}
	}
	return &Builder{binary: binary, runner: runner}
}

func (b *Builder) Target(path string) *Builder {
	b.target = path
	return b
}

// FromBase 设置可选的 backing file 路径。空字符串表示不使用 backing file，
// argv 会省略 `-b` 与 `-F`。仅 whitespace 视同空。
func (b *Builder) FromBase(path string) *Builder {
	b.base = path
	return b
}

func (b *Builder) SizeBytes(size int64) *Builder {
	b.size = size
	return b
}

func (b *Builder) Do(ctx context.Context) error {
	target, err := imgargv.PathOperand("target", b.target)
	if err != nil {
		return err
	}
	if b.size <= 0 {
		return imgexec.InvalidRequest("size must be greater than zero")
	}

	args := []string{"create", "-f", "qcow2"}
	if strings.TrimSpace(b.base) != "" {
		// base 显式设置时仍走 PathOperand 校验，拒绝以 `-` 开头的路径，
		// 防止 qemu-img 把 caller-controlled 路径当成另一个 CLI 选项解析。
		base, err := imgargv.PathOperand("base", b.base)
		if err != nil {
			return err
		}
		args = append(args, "-F", "qcow2", "-b", base)
	}
	args = append(args, target, strconv.FormatInt(b.size, 10))

	result, err := b.runner.Run(ctx, b.binary, args)
	return imgexec.WrapError(result, err)
}
