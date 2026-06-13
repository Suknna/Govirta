// Tests for govirtad flag parsing. The MAC-prefix path has a dedicated
// regression case: an earlier version used net.ParseMAC, which rejects 3-byte
// OUI input outright, so --mac-prefix could never be satisfied and govirtad
// could not start at all. parseOUI fixes that; TestParseOUIThreeByte locks it.
package main

import (
	"testing"
	"time"
)

func TestParseConfigValid(t *testing.T) {
	args := []string{
		"--etcd-endpoint", "localhost:2379",
		"--node-name", "node-0",
		"--listen-addr", "127.0.0.1:8080",
		"--mac-prefix", "02:00:00",
		"--mac-suffix-start", "0",
		"--mac-suffix-end", "255",
		"--image-store-root", "/var/lib/govirta/images",
		"--image-store-public-url", "http://127.0.0.1:8080/",
		"--image-cache-root", "/var/lib/govirta/image-cache",
		"--image-controller-sync-period", "2s",
		"--phase-one-node-task-name", "phase-one-node-task",
		"--phase-one-node-task-node", "node-0",
		"--phase-one-cluster-task-name", "phase-one-cluster-task",
		"--phase-one-task-owner-name", "phase-one-owner",
		"--phase-one-task-owner-uid", "phase-one-owner-uid",
		"--phase-one-task-executor-id", "govirtad-test",
		"--phase-one-task-noop-marker", "phase-one",
	}
	cfg, err := parseConfig(args)
	if err != nil {
		t.Fatalf("parseConfig: unexpected error: %v", err)
	}
	if len(cfg.EtcdEndpoints) != 1 || cfg.EtcdEndpoints[0] != "localhost:2379" {
		t.Fatalf("EtcdEndpoints = %v, want [localhost:2379]", cfg.EtcdEndpoints)
	}
	if len(cfg.NodeNames) != 1 || cfg.NodeNames[0] != "node-0" {
		t.Fatalf("NodeNames = %v, want [node-0]", cfg.NodeNames)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("ListenAddr = %q, want 127.0.0.1:8080", cfg.ListenAddr)
	}
	if cfg.EtcdDialTimeout != 5*time.Second {
		t.Fatalf("EtcdDialTimeout = %v, want 5s (default)", cfg.EtcdDialTimeout)
	}
	wantPrefix := []byte{0x02, 0x00, 0x00}
	if len(cfg.MACPrefix) != 3 || cfg.MACPrefix[0] != wantPrefix[0] ||
		cfg.MACPrefix[1] != wantPrefix[1] || cfg.MACPrefix[2] != wantPrefix[2] {
		t.Fatalf("MACPrefix = %v, want %v", cfg.MACPrefix, wantPrefix)
	}
	if cfg.MACSuffixStart != 0 || cfg.MACSuffixEnd != 255 {
		t.Fatalf("MAC suffix range = [%d,%d], want [0,255]", cfg.MACSuffixStart, cfg.MACSuffixEnd)
	}
	if cfg.TaskManager.NodeTaskName != "phase-one-node-task" || cfg.TaskManager.NodeTaskNode != "node-0" || cfg.TaskManager.ClusterTaskName != "phase-one-cluster-task" || cfg.TaskManager.ExecutorID != "govirtad-test" || cfg.TaskManager.NoopMarker != "phase-one" {
		t.Fatalf("TaskManager = %+v, want explicit phase-one task config", cfg.TaskManager)
	}
	if cfg.ImageStoreRoot != "/var/lib/govirta/images" || cfg.ImageStorePublicURL != "http://127.0.0.1:8080" || cfg.ImageCacheRoot != "/var/lib/govirta/image-cache" || cfg.ImageControllerSyncPeriod != 2*time.Second {
		t.Fatalf("image config = (%q,%q,%q,%v), want explicit image roots/url/sync", cfg.ImageStoreRoot, cfg.ImageStorePublicURL, cfg.ImageCacheRoot, cfg.ImageControllerSyncPeriod)
	}
}

func TestParseConfigMissingRequired(t *testing.T) {
	base := func() []string {
		return []string{
			"--etcd-endpoint", "localhost:2379",
			"--node-name", "node-0",
			"--listen-addr", "127.0.0.1:8080",
			"--mac-prefix", "02:00:00",
			"--image-store-root", "/var/lib/govirta/images",
			"--image-store-public-url", "http://127.0.0.1:8080",
			"--image-cache-root", "/var/lib/govirta/image-cache",
			"--image-controller-sync-period", "2s",
			"--phase-one-node-task-name", "phase-one-node-task",
			"--phase-one-node-task-node", "node-0",
			"--phase-one-cluster-task-name", "phase-one-cluster-task",
			"--phase-one-task-owner-name", "phase-one-owner",
			"--phase-one-task-owner-uid", "phase-one-owner-uid",
			"--phase-one-task-executor-id", "govirtad-test",
			"--phase-one-task-noop-marker", "phase-one",
		}
	}
	tests := map[string]func() []string{
		"missing etcd-endpoint": func() []string {
			return append(base()[2:], []string{}...)
		},
		"missing node-name": func() []string {
			return []string{"--etcd-endpoint", "localhost:2379", "--listen-addr", "127.0.0.1:8080", "--mac-prefix", "02:00:00"}
		},
		"missing listen-addr": func() []string {
			return []string{"--etcd-endpoint", "localhost:2379", "--node-name", "node-0", "--mac-prefix", "02:00:00"}
		},
		"missing mac-prefix": func() []string {
			return []string{"--etcd-endpoint", "localhost:2379", "--node-name", "node-0", "--listen-addr", "127.0.0.1:8080"}
		},
		"missing phase-one task config": func() []string {
			return []string{"--etcd-endpoint", "localhost:2379", "--node-name", "node-0", "--listen-addr", "127.0.0.1:8080", "--mac-prefix", "02:00:00", "--image-store-root", "/var/lib/govirta/images", "--image-store-public-url", "http://127.0.0.1:8080", "--image-cache-root", "/var/lib/govirta/image-cache", "--image-controller-sync-period", "2s"}
		},
		"missing image config": func() []string {
			args := base()
			return append(args[:6], args[12:]...)
		},
		"missing image controller sync period": func() []string {
			args := base()
			return append(args[:14], args[16:]...)
		},
	}
	_ = base
	for name, mk := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig(mk()); err == nil {
				t.Fatalf("parseConfig(%v): expected error, got nil", mk())
			}
		})
	}
}

// TestParseOUIThreeByte is the regression guard for the net.ParseMAC bug: a
// 3-byte OUI must parse successfully into a 3-byte HardwareAddr.
func TestParseOUIThreeByte(t *testing.T) {
	hw, err := parseOUI("02:00:00")
	if err != nil {
		t.Fatalf("parseOUI(02:00:00): unexpected error: %v", err)
	}
	if len(hw) != 3 {
		t.Fatalf("parseOUI length = %d, want 3", len(hw))
	}
	if hw[0] != 0x02 || hw[1] != 0x00 || hw[2] != 0x00 {
		t.Fatalf("parseOUI = %v, want [02 00 00]", hw)
	}
}

func TestParseOUIRejectsBadShape(t *testing.T) {
	tests := []string{
		"02:00:00:00:00:00", // 6-byte EUI, not a 3-byte OUI
		"02:00",             // too few octets
		"0200:00",           // octet not two hex digits
		"02:00:zz",          // non-hex octet
		"",                  // empty
	}
	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			if _, err := parseOUI(in); err == nil {
				t.Fatalf("parseOUI(%q): expected error, got nil", in)
			}
		})
	}
}

// TestParseConfigRejectsSixByteMACPrefix proves the flag layer rejects a 6-byte
// MAC where a 3-byte OUI is required, with a clear flag-scoped error.
func TestParseConfigRejectsSixByteMACPrefix(t *testing.T) {
	args := []string{
		"--etcd-endpoint", "localhost:2379",
		"--node-name", "node-0",
		"--listen-addr", "127.0.0.1:8080",
		"--mac-prefix", "02:00:00:00:00:00",
		"--image-store-root", "/var/lib/govirta/images",
		"--image-store-public-url", "http://127.0.0.1:8080",
		"--image-cache-root", "/var/lib/govirta/image-cache",
		"--image-controller-sync-period", "2s",
		"--phase-one-node-task-name", "phase-one-node-task",
		"--phase-one-node-task-node", "node-0",
		"--phase-one-cluster-task-name", "phase-one-cluster-task",
		"--phase-one-task-owner-name", "phase-one-owner",
		"--phase-one-task-owner-uid", "phase-one-owner-uid",
		"--phase-one-task-executor-id", "govirtad-test",
		"--phase-one-task-noop-marker", "phase-one",
	}
	if _, err := parseConfig(args); err == nil {
		t.Fatal("parseConfig: expected error for 6-byte mac-prefix, got nil")
	}
}
