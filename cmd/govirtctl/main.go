// Command govirtctl is the Govirta control-plane CLI. It submits resource
// manifests to the master apiserver and reads objects back. All logic lives in
// internal/govirtctl; this entry only wires os.Args/stdio/exit code.
package main

import (
	"context"
	"os"

	"github.com/suknna/govirta/internal/govirtctl"
)

func main() {
	os.Exit(govirtctl.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
