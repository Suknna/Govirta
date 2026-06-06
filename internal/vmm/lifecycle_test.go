package vmm

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/pkg/virt/qmp"
)

// startedVM 是一个 helper：Create 一台 VM 并返回其 runtime paths。
func createdVM(t *testing.T, svc *VMMService, uuid string) RuntimePaths {
	t.Helper()
	if _, err := svc.Create(context.Background(), newCreateRequest(uuid)); err != nil {
		t.Fatalf("Create(%q) error = %v", uuid, err)
	}
	return runtimePathsFor("/var/lib/govirtlet", uuid)
}

func TestStartSpawnsPersistedArgvAndSetsRunningIntent(t *testing.T) {
	fc := newFakeController()
	qc := &fakeQMPClient{queryStatus: statusRunning()}
	svc := newTestService(t, fc, qc)
	paths := createdVM(t, svc, "vm-1")

	// spawn 后标记进程存活，模拟真实 daemonize 成功。
	fc.spawnHook = func() { fc.aliveByPidfile[paths.PidFile] = true }

	vm, err := svc.Start(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if fc.spawnCalls != 1 {
		t.Fatalf("Start() spawn calls = %d, want 1", fc.spawnCalls)
	}
	// spawn 收到的 argv 必须等于 vm.json 里存好的 argv（持久化 argv 模型）。
	st, err := svc.loadState(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("loadState() error = %v", err)
	}
	if len(fc.spawnedArgv) != 1 || !sameArgv(fc.spawnedArgv[0], st.Argv) {
		t.Fatalf("Start() spawned argv = %v, want persisted argv %v", fc.spawnedArgv, st.Argv)
	}
	if st.Intended != IntendedRunning {
		t.Fatalf("Start() persisted intent = %q, want %q", st.Intended, IntendedRunning)
	}
	if vm.Phase != PhaseRunning {
		t.Fatalf("Start() phase = %q, want %q", vm.Phase, PhaseRunning)
	}
}

func TestStartIsIdempotentWhenProcessAlreadyAlive(t *testing.T) {
	fc := newFakeController()
	qc := &fakeQMPClient{}
	svc := newTestService(t, fc, qc)
	createdVM(t, svc, "vm-1")
	// 已有运行态的 VM（intent=Running 经先前 Start 持久化 + 进程活 + QMP running）：
	// 再次 Start 应幂等，不重复 spawn。
	markRunning(t, svc, fc, qc, "vm-1")

	vm, err := svc.Start(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if fc.spawnCalls != 0 {
		t.Fatalf("Start() spawn calls = %d on already-alive vm, want 0", fc.spawnCalls)
	}
	if vm.Phase != PhaseRunning {
		t.Fatalf("Start() phase = %q, want %q", vm.Phase, PhaseRunning)
	}
}

func TestStartSpawnFailureStillRecordsRunningIntent(t *testing.T) {
	fc := newFakeController()
	fc.spawnErr = errors.New("exec failed")
	svc := newTestService(t, fc, &fakeQMPClient{})
	createdVM(t, svc, "vm-1")

	_, err := svc.Start(context.Background(), "vm-1")
	if err == nil {
		t.Fatalf("Start() error = nil, want spawn error")
	}
	// 意图仍落 Running，下次探测会派生 Failed（live 赢）。
	st, lerr := svc.loadState(context.Background(), "vm-1")
	if lerr != nil {
		t.Fatalf("loadState() error = %v", lerr)
	}
	if st.Intended != IntendedRunning {
		t.Fatalf("Start() failed spawn intent = %q, want %q", st.Intended, IntendedRunning)
	}
}

func TestStopRequestsPowerdownAndSetsStoppedIntent(t *testing.T) {
	fc := newFakeController()
	qc := &fakeQMPClient{}
	svc := newTestService(t, fc, qc)
	createdVM(t, svc, "vm-1")

	if err := svc.Stop(context.Background(), "vm-1"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if qc.powerdownCalls != 1 {
		t.Fatalf("Stop() powerdown calls = %d, want 1", qc.powerdownCalls)
	}
	st, _ := svc.loadState(context.Background(), "vm-1")
	if st.Intended != IntendedStopped {
		t.Fatalf("Stop() intent = %q, want %q", st.Intended, IntendedStopped)
	}
}

func TestStopPropagatesQMPConnectError(t *testing.T) {
	fc := newFakeController()
	qc := &fakeQMPClient{connectErr: errors.New("no socket")}
	svc := newTestService(t, fc, qc)
	createdVM(t, svc, "vm-1")

	err := svc.Stop(context.Background(), "vm-1")
	if err == nil {
		t.Fatalf("Stop() error = nil, want connect error")
	}
	// 连接失败时不应改写意图。
	st, _ := svc.loadState(context.Background(), "vm-1")
	if st.Intended != IntendedDefined {
		t.Fatalf("Stop() intent after connect fail = %q, want unchanged %q", st.Intended, IntendedDefined)
	}
}

func TestKillUsesQMPQuitWhenReachable(t *testing.T) {
	fc := newFakeController()
	qc := &fakeQMPClient{}
	svc := newTestService(t, fc, qc)
	createdVM(t, svc, "vm-1")

	if err := svc.Kill(context.Background(), "vm-1"); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	if qc.quitCalls != 1 {
		t.Fatalf("Kill() quit calls = %d, want 1", qc.quitCalls)
	}
	if fc.killCalls != 0 {
		t.Fatalf("Kill() ForceKill calls = %d, want 0 when QMP quit succeeds", fc.killCalls)
	}
	st, _ := svc.loadState(context.Background(), "vm-1")
	if st.Intended != IntendedStopped {
		t.Fatalf("Kill() intent = %q, want %q", st.Intended, IntendedStopped)
	}
}

func TestKillFallsBackToSIGKILLWhenQMPUnreachable(t *testing.T) {
	fc := newFakeController()
	qc := &fakeQMPClient{connectErr: errors.New("no socket")}
	svc := newTestService(t, fc, qc)
	createdVM(t, svc, "vm-1")

	if err := svc.Kill(context.Background(), "vm-1"); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	if fc.killCalls != 1 {
		t.Fatalf("Kill() ForceKill calls = %d, want 1 SIGKILL fallback", fc.killCalls)
	}
	st, _ := svc.loadState(context.Background(), "vm-1")
	if st.Intended != IntendedStopped {
		t.Fatalf("Kill() intent = %q, want %q", st.Intended, IntendedStopped)
	}
}

func TestKillJoinsErrorsWhenQMPAndSIGKILLBothFail(t *testing.T) {
	fc := newFakeController()
	fc.forceKillErr = errors.New("sigkill failed")
	qc := &fakeQMPClient{connectErr: errors.New("no socket")}
	svc := newTestService(t, fc, qc)
	createdVM(t, svc, "vm-1")

	err := svc.Kill(context.Background(), "vm-1")
	if err == nil {
		t.Fatalf("Kill() error = nil, want joined error")
	}
	if !errStringContains(err, "no socket") || !errStringContains(err, "sigkill failed") {
		t.Fatalf("Kill() joined error = %v, want both qmp and sigkill causes", err)
	}
}

func statusRunning() qmp.Status { return qmp.Status{Running: true} }

// sameArgv 报告两个 argv 切片是否逐元素相等。
func sameArgv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func errStringContains(err error, sub string) bool {
	return err != nil && contains(err.Error(), sub)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
