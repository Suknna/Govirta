//go:build acceptance && linux

package acceptance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/vmm"
	vmmproc "github.com/suknna/govirta/internal/vmm/proc"
	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/blockdev"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
	"github.com/suknna/govirta/pkg/virt/qemu/device"
	"github.com/suknna/govirta/pkg/virt/qemu/display"
	"github.com/suknna/govirta/pkg/virt/qemu/machine"
)

// vmmBootBuilder 构造一台 cirros aarch64 VM 的「配置好但未 Build」builder。
// 用 direct-kernel boot（cirros aarch64 无 EFI bootloader 无法从磁盘独立启动），
// 不设 -daemonize/-pidfile/-qmp/-serial/-vnc——这些运行时设施 flag 由 vmm
// facility 注入。NoNIC 让 lifecycle 测试聚焦进程生命周期，不依赖 host 网络。
// 不设 NoShutdown：ACPI powerdown 必须让 QEMU 退出，graceful Stop 才能到 Stopped。
func vmmBootBuilder(env hostnetAcceptanceEnv, diskPath string) *qemu.Builder {
	return qemu.NewVM(qemu.ArchAArch64).
		Binary(env.QEMU).
		Machine(machine.ProfileAArch64VirtKVM).
		CPU(cpu.ModelHost).
		SMP(qemu.SMP{CPUs: 1, Cores: 1, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(256)).
		Kernel(env.Kernel).
		Initrd(env.Initramfs).
		Append("console=ttyAMA0 ds=none").
		AddBlockdev(blockdev.Qcow2{
			NodeName: "root",
			File:     blockdev.FileProtocol{Filename: diskPath},
			AIO:      blockdev.AIOThreads,
		}).
		AddDevice(device.VirtioBlkPCI{
			ID:        "rootdev",
			Drive:     blockdev.Ref("root"),
			BootIndex: qemu.Int(1),
		}).
		NoNIC().
		Display(display.None)
}

// TestVMMLifecycleEndToEnd 用真实 Linux ProcessController + 真实 QEMU daemonize
// 跑通 VM 进程生命周期全部能力：Create / Start / Status / Discover / 重启后
// Reattach / Delete-while-running 冲突 / 优雅 Stop / 再 Start / Kill / Delete。
func TestVMMLifecycleEndToEnd(t *testing.T) {
	env := requireHostnetAcceptanceEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	runtimeRoot := t.TempDir()
	diskPath := filepath.Join(t.TempDir(), "cirros-root.qcow2")
	if err := copyFile(env.Cirros, diskPath); err != nil {
		t.Fatalf("copy cirros image: %v", err)
	}

	svc, err := vmm.NewVMMService(runtimeRoot, vmmproc.NewLinuxController(), vmm.ProductionQMPFactory)
	if err != nil {
		t.Fatalf("NewVMMService() error = %v", err)
	}

	const uuid = "vm1"
	created, err := svc.Create(ctx, vmm.CreateRequest{
		UUID:    uuid,
		Builder: vmmBootBuilder(env, diskPath),
		Spec:    vmm.SpecSummary{Arch: "aarch64", VCPUs: 1, MemoryMiB: 256, DiskPaths: []string{diskPath}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Phase != vmm.PhaseDefined {
		t.Fatalf("Create() phase = %q, want %q", created.Phase, vmm.PhaseDefined)
	}
	// unix socket sun_path 限制 108 字节；提前校验 vmm 算出的 QMP socket 路径。
	if len(created.Paths.QMPSocket) >= 100 {
		t.Fatalf("QMP socket path too long (%d): %s", len(created.Paths.QMPSocket), created.Paths.QMPSocket)
	}
	if _, err := os.Stat(created.Paths.StateFile); err != nil {
		t.Fatalf("vm.json not persisted after Create: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		// best-effort：强杀残留进程并清运行时目录，错误仅记录不失败。
		if err := svc.Kill(cleanupCtx, uuid); err != nil && !errors.Is(err, vmm.ErrNotFound) {
			t.Logf("cleanup kill: %v", err)
		}
		if err := svc.Delete(cleanupCtx, uuid); err != nil && !errors.Is(err, vmm.ErrNotFound) {
			t.Logf("cleanup delete: %v", err)
		}
	})

	// Start：spawn daemonized QEMU，vmm 内部等 QMP 就绪。
	if _, err := svc.Start(ctx, uuid); err != nil {
		t.Fatalf("Start() error = %v\nconsole tail:\n%s", err, consoleTail(created.Paths.ConsoleLog))
	}
	running := waitForVMMPhase(t, ctx, svc, uuid, vmm.PhaseRunning)
	if running.Phase != vmm.PhaseRunning {
		t.Fatalf("after Start phase = %q, want Running", running.Phase)
	}
	// 等 guest 真正引导到 login（graceful powerdown 需要 guest ACPI 已就绪）。
	waitForConsoleMarker(t, ctx, created.Paths.ConsoleLog, "login:")

	// Discover 应发现这台 Running VM。
	vms, err := svc.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(vms) != 1 || vms[0].UUID != uuid || vms[0].Phase != vmm.PhaseRunning {
		t.Fatalf("Discover() = %+v, want one Running vm %q", vms, uuid)
	}

	// 模拟编排器重启：丢弃 svc，新建 svc2（同 runtimeRoot），Discover 仍 Running，
	// 证明发现/接管不依赖进程内存，只靠落盘 vm.json + live 探测。
	svc2, err := vmm.NewVMMService(runtimeRoot, vmmproc.NewLinuxController(), vmm.ProductionQMPFactory)
	if err != nil {
		t.Fatalf("NewVMMService() restart error = %v", err)
	}
	vms2, err := svc2.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover() after restart error = %v", err)
	}
	if len(vms2) != 1 || vms2[0].Phase != vmm.PhaseRunning {
		t.Fatalf("Discover() after restart = %+v, want one Running vm", vms2)
	}
	if _, err := svc2.Reattach(ctx, uuid); err != nil {
		t.Fatalf("Reattach() after restart error = %v", err)
	}

	// 运行中 Delete 必须冲突。
	if err := svc2.Delete(ctx, uuid); !errors.Is(err, vmm.ErrConflict) {
		t.Fatalf("Delete() running vm error = %v, want ErrConflict", err)
	}

	// 优雅 Stop：QMP system_powerdown → guest ACPI 关机 → QEMU 退出 → Stopped。
	if err := svc2.Stop(ctx, uuid); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	waitForVMMPhase(t, ctx, svc2, uuid, vmm.PhaseStopped)

	// 再 Start → Running，然后 Kill（QMP quit）→ Stopped。
	if _, err := svc2.Start(ctx, uuid); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	waitForVMMPhase(t, ctx, svc2, uuid, vmm.PhaseRunning)
	if err := svc2.Kill(ctx, uuid); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	waitForVMMPhase(t, ctx, svc2, uuid, vmm.PhaseStopped)

	// Delete 已停止的 VM：运行时目录消失。
	if err := svc2.Delete(ctx, uuid); err != nil {
		t.Fatalf("Delete() stopped vm error = %v", err)
	}
	if _, err := os.Stat(created.Paths.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime dir still present after Delete: stat err = %v", err)
	}
}

// TestVMMDiscoverNeverRestartsDeadVM 在真实内核上验证防脑裂结构护栏：
// intent=Running 但进程被带外 SIGKILL（模拟整机宕机后 VM 已迁走）时，
// Discover 必须派生 Failed（真实 pidfile + signal 0 探测），且绝不重新拉起。
func TestVMMDiscoverNeverRestartsDeadVM(t *testing.T) {
	env := requireHostnetAcceptanceEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	runtimeRoot := t.TempDir()
	diskPath := filepath.Join(t.TempDir(), "cirros-root.qcow2")
	if err := copyFile(env.Cirros, diskPath); err != nil {
		t.Fatalf("copy cirros image: %v", err)
	}

	svc, err := vmm.NewVMMService(runtimeRoot, vmmproc.NewLinuxController(), vmm.ProductionQMPFactory)
	if err != nil {
		t.Fatalf("NewVMMService() error = %v", err)
	}
	const uuid = "vm-dead"
	created, err := svc.Create(ctx, vmm.CreateRequest{
		UUID:    uuid,
		Builder: vmmBootBuilder(env, diskPath),
		Spec:    vmm.SpecSummary{Arch: "aarch64", VCPUs: 1, MemoryMiB: 256, DiskPaths: []string{diskPath}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := svc.Kill(cleanupCtx, uuid); err != nil && !errors.Is(err, vmm.ErrNotFound) {
			t.Logf("cleanup kill: %v", err)
		}
		if err := svc.Delete(cleanupCtx, uuid); err != nil && !errors.Is(err, vmm.ErrNotFound) {
			t.Logf("cleanup delete: %v", err)
		}
	})

	if _, err := svc.Start(ctx, uuid); err != nil {
		t.Fatalf("Start() error = %v\nconsole tail:\n%s", err, consoleTail(created.Paths.ConsoleLog))
	}
	waitForVMMPhase(t, ctx, svc, uuid, vmm.PhaseRunning)

	// 带外 SIGKILL：读 pidfile 拿真实 pid，直接 kill -9，绕过 vmm.Kill，
	// 使持久 intent 仍为 Running（模拟非受控的进程消失）。
	pid := readPidfileForTest(t, created.Paths.PidFile)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("out-of-band SIGKILL pid %d: %v", pid, err)
	}
	waitForProcessGone(t, ctx, pid)

	// 新建 service（模拟重启），Discover 必须把该 VM 判为 Failed 且不拉起。
	svc2, err := vmm.NewVMMService(runtimeRoot, vmmproc.NewLinuxController(), vmm.ProductionQMPFactory)
	if err != nil {
		t.Fatalf("NewVMMService() restart error = %v", err)
	}
	vms, err := svc2.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(vms) != 1 || vms[0].Phase != vmm.PhaseFailed {
		t.Fatalf("Discover() after out-of-band kill = %+v, want one Failed vm", vms)
	}
	// 护栏：Discover 之后该 pid 仍然是死的（没有任何重新拉起）。
	if processAliveForTest(pid) {
		t.Fatalf("pid %d alive after Discover; vmm must never auto-restart a dead vm", pid)
	}
	// Reattach 死进程必须拒绝（ErrNotReady），同样不拉起。
	if _, err := svc2.Reattach(ctx, uuid); !errors.Is(err, vmm.ErrNotReady) {
		t.Fatalf("Reattach() dead vm error = %v, want ErrNotReady", err)
	}
}

// waitForVMMPhase 轮询 Status 直到观测 Phase 达到 want（boot/shutdown 需要时间）。
func waitForVMMPhase(t *testing.T, ctx context.Context, svc *vmm.VMMService, uuid string, want vmm.Phase) vmm.VM {
	t.Helper()
	sub, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var last vmm.Phase
	for {
		if err := sub.Err(); err != nil {
			t.Fatalf("timed out waiting for phase %q (last %q): %v", want, last, err)
		}
		vm, err := svc.Status(sub, uuid)
		if err == nil {
			last = vm.Phase
			if vm.Phase == want {
				return vm
			}
		}
		select {
		case <-sub.Done():
			t.Fatalf("timed out waiting for phase %q (last %q): %v", want, last, sub.Err())
		case <-time.After(1 * time.Second):
		}
	}
}

// waitForConsoleMarker 轮询 console.log（vmm 注入的 -serial file:）直到出现 marker。
func waitForConsoleMarker(t *testing.T, ctx context.Context, path, marker string) {
	t.Helper()
	sub, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		if err := sub.Err(); err != nil {
			t.Fatalf("timed out waiting for console marker %q: %v\nconsole tail:\n%s", marker, err, consoleTail(path))
		}
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), marker) {
			return
		}
		select {
		case <-sub.Done():
			t.Fatalf("timed out waiting for console marker %q: %v\nconsole tail:\n%s", marker, sub.Err(), consoleTail(path))
		case <-time.After(1 * time.Second):
		}
	}
}

// waitForProcessGone 轮询直到 pid 不再存活（带外 SIGKILL 后等待内核回收）。
func waitForProcessGone(t *testing.T, ctx context.Context, pid int) {
	t.Helper()
	sub, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if !processAliveForTest(pid) {
			return
		}
		select {
		case <-sub.Done():
			t.Fatalf("pid %d still alive after SIGKILL: %v", pid, sub.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// readPidfileForTest 读 QEMU 自写的 pidfile 并解析 pid（带外杀进程用）。
func readPidfileForTest(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pidfile %q: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pidfile %q: %v", path, err)
	}
	return pid
}

// processAliveForTest 用 signal 0 探测进程存活（与生产 LinuxController 同语义）。
func processAliveForTest(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// consoleTail 返回 console.log 末尾片段用于失败诊断（读不到返回占位说明）。
func consoleTail(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "(console log unavailable: " + err.Error() + ")"
	}
	return tailString(string(data), 8192)
}
