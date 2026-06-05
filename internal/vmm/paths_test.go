package vmm

import (
	"path/filepath"
	"testing"
)

// TestRuntimePathsForLayout 断言运行时路径布局正确、uuid 注入到目录层级。
func TestRuntimePathsForLayout(t *testing.T) {
	const root = "/var/lib/govirtlet"
	const uuid = "vm-abc-123"

	paths := runtimePathsFor(root, uuid)

	wantDir := filepath.Join(root, uuid)
	if paths.Dir != wantDir {
		t.Fatalf("Dir = %q, want %q", paths.Dir, wantDir)
	}

	checks := []struct {
		name string
		got  string
		want string
	}{
		{"StateFile", paths.StateFile, filepath.Join(wantDir, "vm.json")},
		{"PidFile", paths.PidFile, filepath.Join(wantDir, "qemu.pid")},
		{"QEMULog", paths.QEMULog, filepath.Join(wantDir, "qemu.log")},
		{"QMPSocket", paths.QMPSocket, filepath.Join(wantDir, "qmp.sock")},
		{"VNCSocket", paths.VNCSocket, filepath.Join(wantDir, "vnc.sock")},
		{"ConsoleLog", paths.ConsoleLog, filepath.Join(wantDir, "console.log")},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestIntendedPhaseValid 断言意图态合法性判定的边界。
func TestIntendedPhaseValid(t *testing.T) {
	valid := []IntendedPhase{IntendedDefined, IntendedRunning, IntendedStopped}
	for _, p := range valid {
		if !p.Valid() {
			t.Errorf("IntendedPhase(%q).Valid() = false, want true", p)
		}
	}
	invalid := []IntendedPhase{"", "running ", "RUNNING", "paused", "unknown"}
	for _, p := range invalid {
		if p.Valid() {
			t.Errorf("IntendedPhase(%q).Valid() = true, want false", p)
		}
	}
}
