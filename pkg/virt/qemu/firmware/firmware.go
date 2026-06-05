package firmware

import (
	"fmt"
	"strings"
)

type BIOS struct {
	Path string
}

func (b BIOS) Arg() (string, error) {
	if b.Path == "" {
		return "", fmt.Errorf("bios path is required")
	}
	if strings.HasPrefix(b.Path, "-") {
		return "", fmt.Errorf("bios path must not start with '-'")
	}
	return b.Path, nil
}
