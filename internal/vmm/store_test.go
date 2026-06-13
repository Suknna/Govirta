package vmm

import (
	"context"
	"errors"
	"testing"

	"github.com/suknna/govirta/pkg/virt/qmp"
)

// TestEncodeDecodeStateRoundTrip 断言 persistedState 编解码往返一致。
func TestEncodeDecodeStateRoundTrip(t *testing.T) {
	bootIndex := 0
	want := persistedState{
		UUID: "vm-1",
		Spec: SpecSummary{
			Name:      "vm-test",
			Arch:      "x86_64",
			VCPUs:     4,
			MemoryMiB: 2048,
			CPUModel:  "host",
			Disks:     []DiskSpec{{Path: "/d.qcow2"}},
			NICs:      []NICSpec{{TapName: "gvtap0", MAC: "02:00:00:00:00:01"}},
			CDROMs: []CDROM{{
				ImageName:     "installer",
				ImageUID:      "uid-installer",
				Version:       "v1",
				CachedPath:    "/var/lib/govirta/images/installer.iso",
				SHA256:        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				BootIndexMode: BootIndexModeIndex,
				BootIndex:     &bootIndex,
			}},
		},
		Paths:    runtimePathsFor("/var/lib/govirtlet", "vm-1"),
		Argv:     []string{"qemu-system-x86_64", "-daemonize"},
		Intended: IntendedRunning,
	}

	data, err := encodeState(want)
	if err != nil {
		t.Fatalf("encodeState() error = %v", err)
	}
	got, err := decodeState(data)
	if err != nil {
		t.Fatalf("decodeState() error = %v", err)
	}
	if got.UUID != want.UUID || got.Intended != want.Intended {
		t.Fatalf("decodeState() = %+v, want uuid/intent %q/%q", got, want.UUID, want.Intended)
	}
	if got.Spec.Arch != want.Spec.Arch || got.Spec.VCPUs != want.Spec.VCPUs {
		t.Fatalf("decodeState() spec = %+v, want %+v", got.Spec, want.Spec)
	}
	if got.Spec.Name != want.Spec.Name || got.Spec.CPUModel != want.Spec.CPUModel || got.Spec.MemoryMiB != want.Spec.MemoryMiB {
		t.Fatalf("decodeState() spec = %+v, want %+v", got.Spec, want.Spec)
	}
	if len(got.Spec.Disks) != len(want.Spec.Disks) || got.Spec.Disks[0].Path != want.Spec.Disks[0].Path {
		t.Fatalf("decodeState() disks = %+v, want %+v", got.Spec.Disks, want.Spec.Disks)
	}
	if len(got.Spec.NICs) != len(want.Spec.NICs) || got.Spec.NICs[0].TapName != want.Spec.NICs[0].TapName || got.Spec.NICs[0].MAC != want.Spec.NICs[0].MAC {
		t.Fatalf("decodeState() nics = %+v, want %+v", got.Spec.NICs, want.Spec.NICs)
	}
	if len(got.Spec.CDROMs) != len(want.Spec.CDROMs) || got.Spec.CDROMs[0].CachedPath != want.Spec.CDROMs[0].CachedPath || got.Spec.CDROMs[0].BootIndex == nil || *got.Spec.CDROMs[0].BootIndex != bootIndex {
		t.Fatalf("decodeState() cdroms = %+v, want %+v", got.Spec.CDROMs, want.Spec.CDROMs)
	}
	if len(got.Argv) != len(want.Argv) || got.Argv[0] != want.Argv[0] {
		t.Fatalf("decodeState() argv = %v, want %v", got.Argv, want.Argv)
	}
	if got.Paths.StateFile != want.Paths.StateFile {
		t.Fatalf("decodeState() state file = %q, want %q", got.Paths.StateFile, want.Paths.StateFile)
	}
}

// TestDecodeStateRejectsInvariants 断言缺 uuid / 非法 intent 被拒为 ErrInvalidRequest。
func TestDecodeStateRejectsInvariants(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{name: "missing_uuid", json: `{"intended_phase":"running"}`},
		{name: "invalid_intent", json: `{"uuid":"vm-1","intended_phase":"bogus"}`},
		{name: "empty_intent", json: `{"uuid":"vm-1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeState([]byte(tc.json))
			if err == nil {
				t.Fatalf("decodeState() error = nil, want error")
			}
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("decodeState() error = %v, want errors.Is(err, ErrInvalidRequest)", err)
			}
		})
	}
}

// TestDecodeStateRejectsMalformedJSON 断言坏 JSON 返回错误（非 panic）。
func TestDecodeStateRejectsMalformedJSON(t *testing.T) {
	_, err := decodeState([]byte("not json"))
	if err == nil {
		t.Fatalf("decodeState() error = nil, want error")
	}
}

// TestLoadStateNotFound 断言 vm.json 不存在映射为 ErrNotFound。
func TestLoadStateNotFound(t *testing.T) {
	fc := newFakeController()
	svc, err := NewVMMService("/var/lib/govirtlet", fc, stubQMPFactory(&fakeQMPClient{}), testNodeEnv)
	if err != nil {
		t.Fatalf("NewVMMService() error = %v", err)
	}
	_, err = svc.loadState(context.Background(), "missing-vm")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("loadState() error = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

// TestWriteThenLoadStateRoundTrip 断言 writeState 落盘后 loadState 能读回。
func TestWriteThenLoadStateRoundTrip(t *testing.T) {
	fc := newFakeController()
	svc, err := NewVMMService("/var/lib/govirtlet", fc, stubQMPFactory(&fakeQMPClient{}), testNodeEnv)
	if err != nil {
		t.Fatalf("NewVMMService() error = %v", err)
	}
	st := persistedState{
		UUID:     "vm-1",
		Paths:    runtimePathsFor("/var/lib/govirtlet", "vm-1"),
		Intended: IntendedDefined,
	}
	if err := svc.writeState(context.Background(), st); err != nil {
		t.Fatalf("writeState() error = %v", err)
	}
	got, err := svc.loadState(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("loadState() error = %v", err)
	}
	if got.UUID != "vm-1" || got.Intended != IntendedDefined {
		t.Fatalf("loadState() = %+v, want uuid vm-1 intent defined", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatalf("loadState() UpdatedAt is zero, want writeState to stamp it")
	}
}

// stubQMPFactory 返回一个固定 client 的 QMPFactory（store 测试不触发 QMP）。
func stubQMPFactory(c *fakeQMPClient) QMPFactory {
	return func(socketPath string) (qmp.Client, error) {
		return c, nil
	}
}
