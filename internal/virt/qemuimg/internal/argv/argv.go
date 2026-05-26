package argv

import (
	"strings"

	imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

// PathOperand validates a qemu-img positional path operand before it is passed
// to qemu-img. Paths beginning with '-' are rejected so qemu-img cannot parse a
// caller-controlled path as another command-line option.
func PathOperand(name string, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", imgexec.InvalidRequest("%s is required", name)
	}
	if strings.HasPrefix(path, "-") {
		return "", imgexec.InvalidRequest("%s must not start with '-'", name)
	}
	return path, nil
}
