// Command govirtad is the control-plane process. It reads every
// behavior-affecting input from flags (显式优于隐式: no hidden defaults baked
// into the controlplane package), constructs a controlplane.Config, and runs the
// assembled Service until the process context is cancelled.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/controlplane"
)

// stringSlice is a flag.Value collecting a repeatable string flag into a slice,
// so callers pass e.g. --etcd-endpoint a --etcd-endpoint b. We avoid a hidden
// comma-split default and make every value an explicit, ordered occurrence.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }

func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Str("process", "govirtad").Logger()
	ctx := logger.WithContext(context.Background())

	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("invalid configuration")
		os.Exit(2)
	}

	svc, err := controlplane.NewService(ctx, cfg)
	if err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("control plane assembly failed")
		os.Exit(1)
	}

	if err := svc.Run(ctx); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("control plane exited with error")
		os.Exit(1)
	}
}

// parseConfig builds a controlplane.Config from command-line flags. Every field
// is an explicit flag with no silent fallback beyond the documented flag default
// values, which are visible here at the composition root rather than buried in
// the controlplane package. The MAC prefix is parsed and validated to be exactly
// a 3-byte OUI here; deeper pool validation (locally-administered/unicast bits,
// suffix range) is left to mac.NewPool so the error surfaces with that package's
// context.
func parseConfig(args []string) (controlplane.Config, error) {
	fs := flag.NewFlagSet("govirtad", flag.ContinueOnError)

	var endpoints stringSlice
	var nodeNames stringSlice
	fs.Var(&endpoints, "etcd-endpoint", "etcd endpoint (repeatable); at least one required")
	fs.Var(&nodeNames, "node-name", "static candidate node name for scheduling (repeatable); at least one required")
	dialTimeout := fs.Duration("etcd-dial-timeout", 5*time.Second, "etcd initial dial timeout")
	listenAddr := fs.String("listen-addr", "", "TCP address the apiserver binds, e.g. 0.0.0.0:8080 (required)")
	macPrefix := fs.String("mac-prefix", "", "3-byte locally-administered unicast OUI for the MAC pool, e.g. 02:00:00 (required)")
	macStart := fs.Uint("mac-suffix-start", 0, "inclusive start of the MAC pool's 24-bit suffix interval")
	macEnd := fs.Uint("mac-suffix-end", 0, "inclusive end of the MAC pool's 24-bit suffix interval")

	if err := fs.Parse(args); err != nil {
		return controlplane.Config{}, fmt.Errorf("govirtad: parse flags: %w", err)
	}

	if len(endpoints) == 0 {
		return controlplane.Config{}, fmt.Errorf("govirtad: at least one --etcd-endpoint is required")
	}
	if len(nodeNames) == 0 {
		return controlplane.Config{}, fmt.Errorf("govirtad: at least one --node-name is required")
	}
	if *listenAddr == "" {
		return controlplane.Config{}, fmt.Errorf("govirtad: --listen-addr is required")
	}
	if *macPrefix == "" {
		return controlplane.Config{}, fmt.Errorf("govirtad: --mac-prefix is required")
	}

	hw, err := net.ParseMAC(*macPrefix)
	if err != nil {
		return controlplane.Config{}, fmt.Errorf("govirtad: parse --mac-prefix %q: %w", *macPrefix, err)
	}
	// net.ParseMAC accepts 6- or 8-byte forms too; the pool needs exactly a
	// 3-byte OUI, so reject other lengths here with a clear message rather than
	// letting mac.NewPool's length error surface without the flag name.
	if len(hw) != 3 {
		return controlplane.Config{}, fmt.Errorf("govirtad: --mac-prefix %q must be a 3-byte OUI, got %d bytes", *macPrefix, len(hw))
	}

	return controlplane.Config{
		EtcdEndpoints:   []string(endpoints),
		EtcdDialTimeout: *dialTimeout,
		ListenAddr:      *listenAddr,
		MACPrefix:       hw,
		MACSuffixStart:  uint32(*macStart),
		MACSuffixEnd:    uint32(*macEnd),
		NodeNames:       []string(nodeNames),
	}, nil
}
