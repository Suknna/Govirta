package serial

import (
	"fmt"
	"strings"

	"github.com/suknna/govirta/pkg/virt/qemu/qopt"
)

type Serial struct{ chardevID string }

func Chardev(id string) Serial { return Serial{chardevID: id} }

func (s Serial) Validate() error { return qopt.ValidateValue("chardev", s.chardevID) }

func (s Serial) Arg() (string, error) {
	if err := s.Validate(); err != nil {
		return "", fmt.Errorf("serial: %w", err)
	}
	return "chardev:" + strings.TrimPrefix(s.chardevID, "chardev:"), nil
}
