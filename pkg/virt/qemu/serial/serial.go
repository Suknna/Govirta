package serial

import (
	"fmt"
	"strings"

	"github.com/suknna/govirta/pkg/virt/qemu/qopt"
)

// kind 区分 -serial 的目标形态。state-machine 判别值用强类型常量
// （项目铁律：判别器不用裸 string）。
type kind string

const (
	kindChardev kind = "chardev"
	kindFile    kind = "file"
)

// Serial 描述一个 -serial 目标，可为 chardev:<id> 或 file:<path>。
type Serial struct {
	kind  kind
	value string
}

// Chardev 构造一个把串口接到已声明 chardev 的 -serial 目标（chardev:<id>）。
func Chardev(id string) Serial { return Serial{kind: kindChardev, value: id} }

// File 构造一个把串口输出写入文件的 -serial 目标（file:<path>）。
func File(path string) Serial { return Serial{kind: kindFile, value: path} }

func (s Serial) Validate() error {
	switch s.kind {
	case kindChardev:
		return qopt.ValidateValue("chardev", s.value)
	case kindFile:
		return qopt.ValidateValue("file", s.value)
	default:
		return fmt.Errorf("serial: unsupported kind %q", s.kind)
	}
}

func (s Serial) Arg() (string, error) {
	if err := s.Validate(); err != nil {
		return "", fmt.Errorf("serial: %w", err)
	}
	switch s.kind {
	case kindChardev:
		return "chardev:" + strings.TrimPrefix(s.value, "chardev:"), nil
	case kindFile:
		return "file:" + s.value, nil
	default:
		return "", fmt.Errorf("serial: unsupported kind %q", s.kind)
	}
}
