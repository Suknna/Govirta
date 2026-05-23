package main

import (
	"strings"
	"testing"
)

func TestBuildDefaultArgvPrintsQEMUCommand(t *testing.T) {
	argv, err := buildDefaultArgv()
	if err != nil {
		t.Fatalf("buildDefaultArgv() error = %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"qemu-system-x86_64", "-name prod-vm", "-blockdev", "-netdev tap"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("command %q does not contain %q", joined, want)
		}
	}
}
