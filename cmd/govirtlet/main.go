// Command govirtlet is the compute-node agent process. It reads every
// behavior-affecting input from flags (显式优于隐式: no hidden defaults baked
// into the node package), constructs a node.Config, and runs the assembled
// controller-manager Agent until the process context is cancelled.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/node"
	"github.com/suknna/govirta/pkg/hostnet/link"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
)

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("process", "govirtlet").Logger()
	ctx := logger.WithContext(context.Background())

	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("invalid configuration")
		os.Exit(2)
	}

	agent, err := node.NewAgent(cfg)
	if err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("node agent assembly failed")
		os.Exit(1)
	}

	if err := agent.Run(ctx); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("node agent exited with error")
		os.Exit(1)
	}
}

// parseConfig builds a node.Config from command-line flags. Every field is an
// explicit flag with no silent fallback beyond the documented flag defaults,
// which are visible here at the composition root rather than buried in the node
// package. The TAP owner UID/GID default to the process's own uid/gid only as a
// flag default surfaced here; callers can override explicitly.
func parseConfig(args []string) (node.Config, error) {
	fs := flag.NewFlagSet("govirtlet", flag.ContinueOnError)

	masterURL := fs.String("master-url", "", "master apiserver root, e.g. http://10.0.0.1:8080 (required)")
	nodeName := fs.String("node-name", "", "this node's identity; the master streams only objects bound to it (required)")
	runtimeRoot := fs.String("runtime-root", "", "directory for per-VM runtime state (vm.json, pidfile, QMP socket) (required)")
	imageCacheRoot := fs.String("image-cache-root", "", "directory for node-local image cache bytes; defaults to sibling <runtime-root-parent>/image-cache")
	ownerUID := fs.Int("owner-uid", -1, "OS uid that owns guest TAP devices (the user QEMU runs as) (required)")
	ownerGID := fs.Int("owner-gid", -1, "OS gid that owns guest TAP devices (required)")
	guestCPU := fs.String("guest-cpu", "host", "QEMU CPU model the node runs guests with")
	qemuBinary := fs.String("qemu-binary", "", "absolute path to this node's QEMU executable, e.g. /usr/libexec/qemu-kvm (required)")
	firmware := fs.String("firmware", "", "guest firmware image path (aarch64 virt disk-boot needs edk2); empty renders no -bios (x86_64 q35 ships SeaBIOS)")

	if err := fs.Parse(args); err != nil {
		return node.Config{}, fmt.Errorf("govirtlet: parse flags: %w", err)
	}

	if *masterURL == "" {
		return node.Config{}, fmt.Errorf("govirtlet: --master-url is required")
	}
	if *nodeName == "" {
		return node.Config{}, fmt.Errorf("govirtlet: --node-name is required")
	}
	if *runtimeRoot == "" {
		return node.Config{}, fmt.Errorf("govirtlet: --runtime-root is required")
	}
	if *ownerUID < 0 {
		return node.Config{}, fmt.Errorf("govirtlet: --owner-uid is required and must be non-negative")
	}
	if *ownerGID < 0 {
		return node.Config{}, fmt.Errorf("govirtlet: --owner-gid is required and must be non-negative")
	}
	if *qemuBinary == "" {
		return node.Config{}, fmt.Errorf("govirtlet: --qemu-binary is required")
	}

	cacheRoot := *imageCacheRoot
	if cacheRoot == "" {
		cacheRoot = filepath.Join(filepath.Dir(*runtimeRoot), "image-cache")
	}

	return node.Config{
		MasterURL:      *masterURL,
		NodeName:       *nodeName,
		RuntimeRoot:    *runtimeRoot,
		ImageCacheRoot: cacheRoot,
		OwnerUID:       link.ExplicitUID(uint32(*ownerUID)),
		OwnerGID:       link.ExplicitGID(uint32(*ownerGID)),
		GuestCPU:       cpu.Model(*guestCPU),
		QEMUBinary:     *qemuBinary,
		Firmware:       *firmware,
	}, nil
}
