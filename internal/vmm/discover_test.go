package vmm

import (
	"context"
	"errors"
	"testing"
)

// markRunning 把一台 VM 标记为真正运行态：持久 intent=Running（真实运行的 VM
// 经 Start 写入过）+ 进程活 + QMP running。仅标记 alive/QMP 而不持久 intent
// 会让 observedPhase(IntendedDefined, alive) 派生 Starting，不符合运行态语义。
func markRunning(t *testing.T, svc *VMMService, fc *fakeController, qc *fakeQMPClient, uuid string) {
	t.Helper()
	st, err := svc.loadState(context.Background(), uuid)
	if err != nil {
		t.Fatalf("loadState(%q) error = %v", uuid, err)
	}
	st.Intended = IntendedRunning
	if err := svc.writeState(context.Background(), st); err != nil {
		t.Fatalf("writeState(%q) error = %v", uuid, err)
	}
	fc.aliveByPidfile[st.Paths.PidFile] = true
	qc.queryStatus = statusRunning()
}

func TestStatusDerivesPhaseFromLiveProbe(t *testing.T) {
	cases := []struct {
		name      string
		intended  IntendedPhase
		alive     bool
		qmpRun    bool
		connectOK bool
		want      Phase
	}{
		{name: "defined_no_process", intended: IntendedDefined, alive: false, want: PhaseDefined},
		{name: "running_alive_qmp_running", intended: IntendedRunning, alive: true, qmpRun: true, connectOK: true, want: PhaseRunning},
		{name: "running_alive_qmp_unreachable", intended: IntendedRunning, alive: true, connectOK: false, want: PhaseStarting},
		{name: "running_process_dead", intended: IntendedRunning, alive: false, want: PhaseFailed},
		{name: "stopped_process_alive", intended: IntendedStopped, alive: true, qmpRun: true, connectOK: true, want: PhaseStopping},
		{name: "stopped_process_dead", intended: IntendedStopped, alive: false, want: PhaseStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeController()
			qc := &fakeQMPClient{}
			if tc.qmpRun {
				qc.queryStatus = statusRunning()
			}
			if !tc.connectOK {
				qc.connectErr = errors.New("no socket")
			}
			svc := newTestService(t, fc, qc)
			createdVM(t, svc, "vm-1")

			// 注入意图 + 存活状态。
			st, _ := svc.loadState(context.Background(), "vm-1")
			st.Intended = tc.intended
			if err := svc.writeState(context.Background(), st); err != nil {
				t.Fatalf("writeState() error = %v", err)
			}
			paths := runtimePathsFor("/var/lib/govirtlet", "vm-1")
			fc.aliveByPidfile[paths.PidFile] = tc.alive

			vm, err := svc.Status(context.Background(), "vm-1")
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if vm.Phase != tc.want {
				t.Fatalf("Status() phase = %q, want %q", vm.Phase, tc.want)
			}
		})
	}
}

func TestStatusMissingVMReturnsNotFound(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	if _, err := svc.Status(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Status() missing = %v, want ErrNotFound", err)
	}
}

func TestDiscoverReadsAllAndSortsByUUID(t *testing.T) {
	fc := newFakeController()
	qc := &fakeQMPClient{}
	svc := newTestService(t, fc, qc)
	// 乱序创建三台。
	for _, id := range []string{"vm-c", "vm-a", "vm-b"} {
		createdVM(t, svc, id)
	}
	fc.stateDirs = []string{"vm-c", "vm-a", "vm-b"}

	vms, err := svc.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(vms) != 3 {
		t.Fatalf("Discover() len = %d, want 3", len(vms))
	}
	if vms[0].UUID != "vm-a" || vms[1].UUID != "vm-b" || vms[2].UUID != "vm-c" {
		t.Fatalf("Discover() order = %q,%q,%q, want vm-a,vm-b,vm-c", vms[0].UUID, vms[1].UUID, vms[2].UUID)
	}
}

func TestDiscoverSkipsUndiscoverableDir(t *testing.T) {
	fc := newFakeController()
	qc := &fakeQMPClient{}
	svc := newTestService(t, fc, qc)
	createdVM(t, svc, "vm-good")
	// 一个有目录但无 vm.json 的坏 uuid。
	fc.stateDirs = []string{"vm-good", "vm-orphan-dir"}

	vms, err := svc.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(vms) != 1 || vms[0].UUID != "vm-good" {
		t.Fatalf("Discover() = %v, want only vm-good", vms)
	}
}

func TestDiscoverSurvivesRestartViaPersistedState(t *testing.T) {
	// 模拟编排器重启：用一个 service 创建 + 标记运行，丢弃它，新建另一个
	// service（同 runtimeRoot + 同 fake 磁盘），Discover 仍能发现 Running。
	fc := newFakeController()
	qc := &fakeQMPClient{}
	svc1 := newTestService(t, fc, qc)
	createdVM(t, svc1, "vm-1")
	markRunning(t, svc1, fc, qc, "vm-1")
	fc.stateDirs = []string{"vm-1"}

	// 「重启」：新建 service 实例，复用同一份 fake 磁盘状态。
	svc2 := newTestService(t, fc, qc)
	vms, err := svc2.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() after restart error = %v", err)
	}
	if len(vms) != 1 || vms[0].Phase != PhaseRunning {
		t.Fatalf("Discover() after restart = %v, want one Running vm", vms)
	}
}

func TestReattachAdoptsLiveProcess(t *testing.T) {
	fc := newFakeController()
	qc := &fakeQMPClient{}
	svc := newTestService(t, fc, qc)
	createdVM(t, svc, "vm-1")
	markRunning(t, svc, fc, qc, "vm-1")

	vm, err := svc.Reattach(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("Reattach() error = %v", err)
	}
	if vm.Phase != PhaseRunning {
		t.Fatalf("Reattach() phase = %q, want %q", vm.Phase, PhaseRunning)
	}
}

// TestReattachRefusesDeadProcessAndNeverSpawns 是防脑裂结构护栏的关键测试：
// 进程已死时 Reattach 必须返回 ErrNotReady 且绝不调 SpawnDaemonized
// （spec §8 问题2：重启绝不自动拉起已迁走的 VM）。
func TestReattachRefusesDeadProcessAndNeverSpawns(t *testing.T) {
	fc := newFakeController()
	qc := &fakeQMPClient{}
	svc := newTestService(t, fc, qc)
	createdVM(t, svc, "vm-1")
	// intent=running 但进程不存在（模拟整机宕机后 VM 已迁走）。
	st, _ := svc.loadState(context.Background(), "vm-1")
	st.Intended = IntendedRunning
	if err := svc.writeState(context.Background(), st); err != nil {
		t.Fatalf("writeState() error = %v", err)
	}

	_, err := svc.Reattach(context.Background(), "vm-1")
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("Reattach() dead process error = %v, want ErrNotReady", err)
	}
	if fc.spawnCalls != 0 {
		t.Fatalf("Reattach() spawn calls = %d, want 0 (must never auto-start)", fc.spawnCalls)
	}
}

func TestContextCancellationPropagates(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	createdVM(t, svc, "vm-1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := svc.Status(ctx, "vm-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Status() canceled ctx error = %v, want context.Canceled", err)
	}
}
