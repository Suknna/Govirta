package vmm

import (
	"context"
	"errors"
	"testing"
)

// redefineSpec 据基础 spec 派生一份改了内存的新 spec（标量变更 → argv -m 变）。
func redefineSpec() SpecSummary {
	return SpecSummary{
		Name: "vm-test", Arch: "aarch64", VCPUs: 2, MemoryMiB: 512, CPUModel: "host",
		Disks: []DiskSpec{{Path: "/d.qcow2"}},
		NICs:  []NICSpec{{TapName: "gvtap0", MAC: "02:00:00:00:00:01"}},
	}
}

func TestRedefineOverwritesSpecAndRederivesArgv(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	req := newCreateRequest("vm-1")
	if _, err := svc.Create(context.Background(), req); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// 落盘后的原始 argv（用于证明 Redefine 后 argv 变了）。
	before, err := svc.loadState(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("loadState() before = %v", err)
	}

	newSpec := redefineSpec()
	vm, err := svc.Redefine(context.Background(), "vm-1", newSpec)
	if err != nil {
		t.Fatalf("Redefine() error = %v", err)
	}
	// 返回视图反映新 spec 且为冷态 Defined（进程未活、intent 仍 Defined）。
	if vm.Spec.MemoryMiB != newSpec.MemoryMiB {
		t.Fatalf("Redefine() returned Spec.MemoryMiB = %d, want %d", vm.Spec.MemoryMiB, newSpec.MemoryMiB)
	}
	if vm.Phase != PhaseDefined {
		t.Fatalf("Redefine() phase = %q, want %q", vm.Phase, PhaseDefined)
	}

	after, err := svc.loadState(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("loadState() after = %v", err)
	}
	// Spec 已覆写为新值。
	if after.Spec.MemoryMiB != newSpec.MemoryMiB || after.Spec.VCPUs != newSpec.VCPUs {
		t.Fatalf("persisted Spec = %+v, want MemoryMiB=%d VCPUs=%d", after.Spec, newSpec.MemoryMiB, newSpec.VCPUs)
	}
	// argv 已重派生：含新 -m 值，且与改之前不同。
	if !argvContainsValue(after.Argv, "-m", "size=512") {
		t.Fatalf("persisted argv missing -m size=512: %v", after.Argv)
	}
	if sameArgv(before.Argv, after.Argv) {
		t.Fatalf("Redefine() did not change argv: %v", after.Argv)
	}

	// 落盘 argv 必须 == 新 Spec 的确定性派生（无漂移）。
	b, err := deriveBuilder(newSpec, testNodeEnv)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	want, err := injectFacilityFlags(b, after.Paths)
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if !sameArgv(after.Argv, want) {
		t.Fatalf("persisted argv drifted from new Spec derivation:\nstored=%v\nwant =%v", after.Argv, want)
	}
}

func TestRedefinePreservesIntentAndIdentity(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	req := newCreateRequest("vm-1")
	if _, err := svc.Create(context.Background(), req); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	before, err := svc.loadState(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("loadState() before = %v", err)
	}

	if _, err := svc.Redefine(context.Background(), "vm-1", redefineSpec()); err != nil {
		t.Fatalf("Redefine() error = %v", err)
	}

	after, err := svc.loadState(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("loadState() after = %v", err)
	}
	if after.Intended != IntendedDefined {
		t.Fatalf("Redefine() changed Intended = %q, want %q", after.Intended, IntendedDefined)
	}
	if after.UUID != before.UUID {
		t.Fatalf("Redefine() changed UUID = %q, want %q", after.UUID, before.UUID)
	}
	if after.Paths != before.Paths {
		t.Fatalf("Redefine() changed Paths = %+v, want %+v", after.Paths, before.Paths)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("Redefine() changed CreatedAt = %v, want %v", after.CreatedAt, before.CreatedAt)
	}
	// 纯磁盘操作：不 spawn 进程、不 force-kill。
	if fc.spawnCalls != 0 {
		t.Fatalf("Redefine() spawned %d processes, want 0", fc.spawnCalls)
	}
	if fc.killCalls != 0 {
		t.Fatalf("Redefine() killed %d processes, want 0", fc.killCalls)
	}
}

func TestRedefineIsIdempotent(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	if _, err := svc.Create(context.Background(), newCreateRequest("vm-1")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	spec := redefineSpec()
	if _, err := svc.Redefine(context.Background(), "vm-1", spec); err != nil {
		t.Fatalf("first Redefine() error = %v", err)
	}
	first, err := svc.loadState(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("loadState() first = %v", err)
	}
	if _, err := svc.Redefine(context.Background(), "vm-1", spec); err != nil {
		t.Fatalf("second Redefine() error = %v", err)
	}
	second, err := svc.loadState(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("loadState() second = %v", err)
	}
	// 同一 spec 重复 Redefine 产生同样的 argv（确定性派生）。
	if !sameArgv(first.Argv, second.Argv) {
		t.Fatalf("Redefine() not idempotent:\nfirst =%v\nsecond=%v", first.Argv, second.Argv)
	}
}

func TestRedefineUpdatesCDROMArgv(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	req := newCreateRequest("vm-1")
	req.Spec.CDROMs = []CDROM{{
		ImageName:     "installer",
		ImageUID:      "uid-installer",
		Version:       "v1",
		CachedPath:    "/var/lib/govirta/images/installer-v1.iso",
		BootIndexMode: BootIndexModeUnset,
	}}
	if _, err := svc.Create(context.Background(), req); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	before, err := svc.loadState(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("loadState() before = %v", err)
	}
	bootIndex := 0
	newSpec := redefineSpec()
	newSpec.CDROMs = []CDROM{{
		ImageName:     "installer",
		ImageUID:      "uid-installer",
		Version:       "v2",
		CachedPath:    "/var/lib/govirta/images/installer-v2.iso",
		BootIndexMode: BootIndexModeIndex,
		BootIndex:     &bootIndex,
	}}

	if _, err := svc.Redefine(context.Background(), "vm-1", newSpec); err != nil {
		t.Fatalf("Redefine() error = %v", err)
	}
	after, err := svc.loadState(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("loadState() after = %v", err)
	}
	if sameArgv(before.Argv, after.Argv) {
		t.Fatalf("Redefine() did not update argv for changed CDROM: %v", after.Argv)
	}
	id := cdromID(0, newSpec.CDROMs[0])
	if !argvContainsValue(after.Argv, "-blockdev", "driver=raw,node-name="+id+",read-only=on,file.driver=file,file.filename=/var/lib/govirta/images/installer-v2.iso,file.cache.direct=off,file.aio=threads") {
		t.Fatalf("persisted argv missing updated CDROM path: %v", after.Argv)
	}
	if !argvContainsValue(after.Argv, "-device", "scsi-cd,drive="+id+",bus="+id+"-scsi.0,scsi-id=0,bootindex=0,id="+id+"-device") {
		t.Fatalf("persisted argv missing updated CDROM bootindex: %v", after.Argv)
	}
}

func TestRedefineMissingVMReturnsNotFound(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	_, err := svc.Redefine(context.Background(), "never-created", redefineSpec())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Redefine() missing vm = %v, want ErrNotFound", err)
	}
}

func TestRedefineRejectsUnsupportedArch(t *testing.T) {
	fc := newFakeController()
	svc := newTestService(t, fc, &fakeQMPClient{})
	if _, err := svc.Create(context.Background(), newCreateRequest("vm-1")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	bad := redefineSpec()
	bad.Arch = "sparc" // deriveBuilder/mapArch 只支持 x86_64/aarch64
	_, err := svc.Redefine(context.Background(), "vm-1", bad)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Redefine() unsupported arch = %v, want ErrInvalidRequest", err)
	}
}
