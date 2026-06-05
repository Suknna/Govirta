package vmm

import (
	"errors"
	"strings"
	"testing"

	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
	"github.com/suknna/govirta/pkg/virt/qemu/machine"
)

// argvFlagValue 返回 argv 中 flag 紧跟的值（找不到返回 ""）。
func argvFlagValue(argv []string, flag string) string {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

// TestInjectFacilityFlags 断言注入后渲染的 argv 含全部运行时设施 flag，
// 且路径值取自 vmm 私有运行时布局（spec §6/§7）。
func TestInjectFacilityFlags(t *testing.T) {
	paths := runtimePathsFor("/var/lib/govirtlet", "vm-1")
	b := qemu.NewVM(qemu.ArchX86_64).
		Machine(machine.ProfileX86_64Q35KVM).
		CPU(cpu.ModelHost).
		Memory(qemu.MiB(512))

	argv, err := injectFacilityFlags(b, paths)
	if err != nil {
		t.Fatalf("injectFacilityFlags() error = %v", err)
	}

	if got := argvFlagValue(argv, "-pidfile"); got != paths.PidFile {
		t.Errorf("-pidfile = %q, want %q", got, paths.PidFile)
	}
	if got := argvFlagValue(argv, "-serial"); got != "file:"+paths.ConsoleLog {
		t.Errorf("-serial = %q, want file:%s", got, paths.ConsoleLog)
	}
	if got := argvFlagValue(argv, "-vnc"); got != "unix:"+paths.VNCSocket {
		t.Errorf("-vnc = %q, want unix:%s", got, paths.VNCSocket)
	}
	if got := argvFlagValue(argv, "-mon"); got != "chardev="+qmpChardevID+",mode=control" {
		t.Errorf("-mon = %q, want chardev=%s,mode=control", got, qmpChardevID)
	}
	chardevArg := argvFlagValue(argv, "-chardev")
	if !strings.Contains(chardevArg, "path="+paths.QMPSocket) || !strings.Contains(chardevArg, "server=on") || !strings.Contains(chardevArg, "wait=off") {
		t.Errorf("-chardev = %q, want socket path=%s,server=on,wait=off", chardevArg, paths.QMPSocket)
	}
	foundDaemonize := false
	for _, a := range argv {
		if a == "-daemonize" {
			foundDaemonize = true
			break
		}
	}
	if !foundDaemonize {
		t.Errorf("argv missing -daemonize: %v", argv)
	}
}

// TestInjectFacilityFlagsNilBuilder 断言 nil builder 返回 ErrInvalidRequest。
func TestInjectFacilityFlagsNilBuilder(t *testing.T) {
	paths := runtimePathsFor("/var/lib/govirtlet", "vm-1")
	_, err := injectFacilityFlags(nil, paths)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("injectFacilityFlags(nil) error = %v, want ErrInvalidRequest", err)
	}
}

// TestInjectFacilityFlagsRejectsBadPath 断言非法路径（含逗号）经 builder
// 校验以 qemu.ErrInvalidVM 返回（facility 不重复校验，积木式）。
func TestInjectFacilityFlagsRejectsBadPath(t *testing.T) {
	paths := runtimePathsFor("/var/lib/govirtlet", "vm-1")
	paths.VNCSocket = "/run/a,b.sock" // 逗号会逃逸到相邻 qemu option
	b := qemu.NewVM(qemu.ArchX86_64).CPU(cpu.ModelHost)
	_, err := injectFacilityFlags(b, paths)
	if !errors.Is(err, qemu.ErrInvalidVM) {
		t.Fatalf("injectFacilityFlags() error = %v, want qemu.ErrInvalidVM", err)
	}
}
