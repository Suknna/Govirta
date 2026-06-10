package vmm

import (
	"context"
	"errors"
	"testing"
)

// newTestService 构造一个注入 fake 依赖的 VMMService，供生命周期单测使用。
func newTestService(t *testing.T, fc *fakeController, qc *fakeQMPClient) *VMMService {
	t.Helper()
	svc, err := NewVMMService("/var/lib/govirtlet", fc, stubQMPFactory(qc))
	if err != nil {
		t.Fatalf("NewVMMService() error = %v", err)
	}
	return svc
}

// newCreateRequest 构造一个完整的 CreateRequest：Spec 是唯一配置权威，
// vmm 据它确定性派生 argv（facility 注入后能成功渲染）。
func newCreateRequest(uuid string) CreateRequest {
	return CreateRequest{
		UUID: uuid,
		Spec: SpecSummary{
			Name: "vm-test", Arch: "aarch64", VCPUs: 1, MemoryMiB: 256, CPUModel: "host",
			Disks: []DiskSpec{{Path: "/d.qcow2"}},
			NICs:  []NICSpec{{TapName: "gvtap0", MAC: "02:00:00:00:00:01"}},
		},
	}
}

func TestNewVMMServiceRejectsMissingDependencies(t *testing.T) {
	fc := newFakeController()
	cases := []struct {
		name        string
		runtimeRoot string
		pc          *fakeController
		qf          QMPFactory
	}{
		{name: "empty_runtime_root", runtimeRoot: "", pc: fc, qf: stubQMPFactory(&fakeQMPClient{})},
		{name: "nil_controller", runtimeRoot: "/var/lib/govirtlet", pc: nil, qf: stubQMPFactory(&fakeQMPClient{})},
		{name: "nil_qmp_factory", runtimeRoot: "/var/lib/govirtlet", pc: fc, qf: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pc *fakeController
			if tc.pc != nil {
				pc = tc.pc
			}
			var svc *VMMService
			var err error
			if pc == nil {
				svc, err = NewVMMService(tc.runtimeRoot, nil, tc.qf)
			} else {
				svc, err = NewVMMService(tc.runtimeRoot, pc, tc.qf)
			}
			if err == nil {
				t.Fatalf("NewVMMService() error = nil, want error")
			}
			if svc != nil {
				t.Fatalf("NewVMMService() svc = %v, want nil", svc)
			}
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("NewVMMService() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestCreatePersistsArgvAndDefinedIntent(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})

	vm, err := svc.Create(context.Background(), newCreateRequest("vm-1"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if vm.Phase != PhaseDefined {
		t.Fatalf("Create() phase = %q, want %q", vm.Phase, PhaseDefined)
	}
	if vm.Intended != IntendedDefined {
		t.Fatalf("Create() intended = %q, want %q", vm.Intended, IntendedDefined)
	}

	// 断言 argv 已落盘且含设施 flag。
	st, err := svc.loadState(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("loadState() error = %v", err)
	}
	if st.Intended != IntendedDefined {
		t.Fatalf("persisted intent = %q, want %q", st.Intended, IntendedDefined)
	}
	if !argvContains(st.Argv, "-daemonize") {
		t.Fatalf("persisted argv missing -daemonize: %v", st.Argv)
	}
	if !argvContainsValue(st.Argv, "-pidfile", st.Paths.PidFile) {
		t.Fatalf("persisted argv missing -pidfile %q: %v", st.Paths.PidFile, st.Argv)
	}
}

func TestCreatePersistsArgvMatchingSpecDerivation(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	req := newCreateRequest("vm-derive")
	if _, err := svc.Create(context.Background(), req); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 独立派生一份 argv，证明落盘 argv == Spec 的确定性派生（无漂移）。
	b, err := deriveBuilder(req.Spec)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	paths := runtimePathsFor(svc.runtimeRoot, req.UUID)
	want, err := injectFacilityFlags(b, paths)
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	st, err := svc.loadState(context.Background(), req.UUID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !sameArgv(st.Argv, want) {
		t.Fatalf("persisted argv drifted from Spec derivation:\nstored=%v\nwant =%v", st.Argv, want)
	}
}

func TestCreateRejectsDuplicateUUID(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	if _, err := svc.Create(context.Background(), newCreateRequest("vm-1")); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	_, err := svc.Create(context.Background(), newCreateRequest("vm-1"))
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v, want ErrAlreadyExists", err)
	}
}

func TestCreateRejectsInvalidRequest(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	cases := []struct {
		name string
		req  CreateRequest
	}{
		{name: "empty_uuid", req: CreateRequest{Spec: SpecSummary{Arch: "aarch64", VCPUs: 1, MemoryMiB: 256}}},
		{name: "unknown_arch", req: CreateRequest{UUID: "vm-1", Spec: SpecSummary{Arch: "sparc", VCPUs: 1, MemoryMiB: 256}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), tc.req)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Create() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestDeleteRejectsRunningVM(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	if _, err := svc.Create(context.Background(), newCreateRequest("vm-1")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// 标记进程存活。
	paths := runtimePathsFor("/var/lib/govirtlet", "vm-1")
	fc.aliveByPidfile[paths.PidFile] = true

	err := svc.Delete(context.Background(), "vm-1")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Delete() running vm error = %v, want ErrConflict", err)
	}
	if fc.removeCalls != 0 {
		t.Fatalf("Delete() called RemoveState %d times on running vm, want 0", fc.removeCalls)
	}
}

func TestDeleteRemovesStoppedVM(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	if _, err := svc.Create(context.Background(), newCreateRequest("vm-1")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := svc.Delete(context.Background(), "vm-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if fc.removeCalls != 1 {
		t.Fatalf("Delete() RemoveState calls = %d, want 1", fc.removeCalls)
	}
	if _, err := svc.loadState(context.Background(), "vm-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("loadState() after delete = %v, want ErrNotFound", err)
	}
}

func TestDeleteMissingVMReturnsNotFound(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	if err := svc.Delete(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete() missing vm = %v, want ErrNotFound", err)
	}
}

// argvContains 报告 argv 是否含某个 flag。
func argvContains(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

// argvContainsValue 报告 argv 是否含相邻的 flag value 对。
func argvContainsValue(argv []string, flag, value string) bool {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}
