package vmm

import (
	"errors"
	"strings"
	"testing"
)

func TestDeriveBuilderFillsMACAndDisks(t *testing.T) {
	spec := SpecSummary{
		Name:      "vm-derive",
		Arch:      "aarch64",
		VCPUs:     2,
		MemoryMiB: 512,
		CPUModel:  "host",
		Disks:     []DiskSpec{{Path: "/var/lib/govirta/d0.qcow2"}},
		NICs:      []NICSpec{{TapName: "gvtap0", MAC: "02:00:00:00:00:01"}},
	}
	b, err := deriveBuilder(spec, testNodeEnv)
	if err != nil {
		t.Fatalf("deriveBuilder: %v", err)
	}
	vm, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	argv := strings.Join(vm.Argv(), " ")
	if !strings.Contains(argv, "mac=02:00:00:00:00:01") {
		t.Fatalf("argv must carry real MAC, got: %s", argv)
	}
	if !strings.Contains(argv, "/var/lib/govirta/d0.qcow2") {
		t.Fatalf("argv must carry disk path, got: %s", argv)
	}
	if !strings.Contains(argv, "512") {
		t.Fatalf("argv must carry memory, got: %s", argv)
	}
}

func TestDeriveBuilderDeterministic(t *testing.T) {
	spec := SpecSummary{
		Name: "vm-det", Arch: "x86_64", VCPUs: 1, MemoryMiB: 256, CPUModel: "host",
		Disks: []DiskSpec{{Path: "/a.qcow2"}},
		NICs:  []NICSpec{{TapName: "gvtap0", MAC: "02:00:00:00:00:02"}},
	}
	b1, err := deriveBuilder(spec, testNodeEnv)
	if err != nil {
		t.Fatalf("deriveBuilder 1: %v", err)
	}
	b2, err := deriveBuilder(spec, testNodeEnv)
	if err != nil {
		t.Fatalf("deriveBuilder 2: %v", err)
	}
	vm1, _ := b1.Build()
	vm2, _ := b2.Build()
	a1, a2 := strings.Join(vm1.Argv(), " "), strings.Join(vm2.Argv(), " ")
	if a1 != a2 {
		t.Fatalf("derivation must be deterministic:\n%s\n%s", a1, a2)
	}
}

func TestDeriveBuilderRendersCDROMsFromSummary(t *testing.T) {
	bootIndex := 0
	spec := SpecSummary{
		Name:      "vm-cdrom",
		Arch:      "aarch64",
		VCPUs:     1,
		MemoryMiB: 256,
		CPUModel:  "host",
		Disks:     []DiskSpec{{Path: "/var/lib/govirta/root.qcow2"}},
		NICs:      []NICSpec{{TapName: "gvtap0", MAC: "02:00:00:00:00:03"}},
		CDROMs: []CDROM{
			{
				ImageName:     "installer",
				ImageUID:      "uid-installer",
				Version:       "v1",
				CachedPath:    "/var/lib/govirta/images/installer.iso",
				BootIndexMode: BootIndexModeUnset,
			},
			{
				ImageName:     "drivers",
				ImageUID:      "uid-drivers",
				Version:       "v2",
				CachedPath:    "/var/lib/govirta/images/drivers.iso",
				BootIndexMode: BootIndexModeIndex,
				BootIndex:     &bootIndex,
			},
		},
	}
	b, err := deriveBuilder(spec, testNodeEnv)
	if err != nil {
		t.Fatalf("deriveBuilder: %v", err)
	}
	vm, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	id0 := cdromID(0, spec.CDROMs[0])
	id1 := cdromID(1, spec.CDROMs[1])
	argv := vm.Argv()
	if id0 == id1 || !strings.HasPrefix(id0, "cdrom0-") || !strings.HasPrefix(id1, "cdrom1-") {
		t.Fatalf("cdrom IDs must be bounded and order-scoped, got %q and %q", id0, id1)
	}
	if !argvContainsValue(argv, "-blockdev", "driver=raw,node-name="+id0+",read-only=on,file.driver=file,file.filename=/var/lib/govirta/images/installer.iso,file.cache.direct=off,file.aio=threads") {
		t.Fatalf("argv missing first CDROM blockdev: %v", argv)
	}
	if !argvContainsValue(argv, "-device", "scsi-cd,drive="+id0+",bus="+id0+"-scsi.0,scsi-id=0,id="+id0+"-device") {
		t.Fatalf("argv must omit bootindex for nil BootIndex: %v", argv)
	}
	if !argvContainsValue(argv, "-blockdev", "driver=raw,node-name="+id1+",read-only=on,file.driver=file,file.filename=/var/lib/govirta/images/drivers.iso,file.cache.direct=off,file.aio=threads") {
		t.Fatalf("argv missing second CDROM blockdev: %v", argv)
	}
	if !argvContainsValue(argv, "-device", "scsi-cd,drive="+id1+",bus="+id1+"-scsi.0,scsi-id=0,bootindex=0,id="+id1+"-device") {
		t.Fatalf("argv must preserve explicit bootindex 0: %v", argv)
	}
	if !argvOrdered(argv, "driver=qcow2,node-name=disk0", "driver=raw,node-name="+id0, "tap,id=net0") {
		t.Fatalf("argv must render disks before CDROMs before NICs: %v", argv)
	}
}

func TestDeriveBuilderRejectsInvalidCDROMBootIndex(t *testing.T) {
	value := 1
	zero := 0
	negative := -1
	cases := []struct {
		name  string
		cdrom CDROM
	}{
		{
			name: "unknown_mode",
			cdrom: CDROM{
				ImageName:     "installer",
				ImageUID:      "uid-installer",
				Version:       "v1",
				CachedPath:    "/var/lib/govirta/images/installer.iso",
				BootIndexMode: BootIndexMode("legacy"),
			},
		},
		{
			name: "index_mode_without_value",
			cdrom: CDROM{
				ImageName:     "installer",
				ImageUID:      "uid-installer",
				Version:       "v1",
				CachedPath:    "/var/lib/govirta/images/installer.iso",
				BootIndexMode: BootIndexModeIndex,
			},
		},
		{
			name: "unset_mode_with_value",
			cdrom: CDROM{
				ImageName:     "installer",
				ImageUID:      "uid-installer",
				Version:       "v1",
				CachedPath:    "/var/lib/govirta/images/installer.iso",
				BootIndexMode: BootIndexModeUnset,
				BootIndex:     &value,
			},
		},
		{
			name: "index_mode_negative_value",
			cdrom: CDROM{
				ImageName:     "installer",
				ImageUID:      "uid-installer",
				Version:       "v1",
				CachedPath:    "/var/lib/govirta/images/installer.iso",
				BootIndexMode: BootIndexModeIndex,
				BootIndex:     &negative,
			},
		},
		{
			name: "index_mode_explicit_zero_is_valid",
			cdrom: CDROM{
				ImageName:     "installer",
				ImageUID:      "uid-installer",
				Version:       "v1",
				CachedPath:    "/var/lib/govirta/images/installer.iso",
				BootIndexMode: BootIndexModeIndex,
				BootIndex:     &zero,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := SpecSummary{
				Name:      "vm-cdrom-invalid",
				Arch:      "aarch64",
				VCPUs:     1,
				MemoryMiB: 256,
				CPUModel:  "host",
				Disks:     []DiskSpec{{Path: "/var/lib/govirta/root.qcow2"}},
				CDROMs:    []CDROM{tc.cdrom},
			}
			_, err := deriveBuilder(spec, testNodeEnv)
			if tc.name == "index_mode_explicit_zero_is_valid" {
				if err != nil {
					t.Fatalf("deriveBuilder() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("deriveBuilder() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func argvOrdered(argv []string, want ...string) bool {
	next := 0
	for _, arg := range argv {
		if strings.Contains(arg, want[next]) {
			next++
			if next == len(want) {
				return true
			}
		}
	}
	return false
}

// TestDeriveBuilderRendersNoNICWhenDiskOnly pins the fix for the QEMU default
// NIC defect: a disk-only VM (no NICs) must render an explicit "-nic none" so
// QEMU does not auto-add a default NIC that loads a missing PXE option ROM
// (efi-virtio.rom), which aborts spawn on hosts without that ROM. Explicit over
// implicit: production argv must never rely on QEMU network defaults.
func TestDeriveBuilderRendersNoNICWhenDiskOnly(t *testing.T) {
	spec := SpecSummary{
		Name:      "vm-disk-only",
		Arch:      "aarch64",
		VCPUs:     1,
		MemoryMiB: 256,
		CPUModel:  "host",
		Disks:     []DiskSpec{{Path: "/var/lib/govirta/root.qcow2"}},
		NICs:      nil,
	}
	b, err := deriveBuilder(spec, testNodeEnv)
	if err != nil {
		t.Fatalf("deriveBuilder: %v", err)
	}
	vm, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	argv := strings.Join(vm.Argv(), " ")
	if !strings.Contains(argv, "-nic none") {
		t.Fatalf("disk-only VM must render -nic none to suppress QEMU default NIC, got: %s", argv)
	}
}

// TestDeriveBuilderOmitsNoNICWhenNICsPresent proves the -nic none suppression is
// conditional: a VM with explicit NICs must NOT carry -nic none (its real NIC
// already governs networking, and the explicit virtio-net-pci disables its ROM).
func TestDeriveBuilderOmitsNoNICWhenNICsPresent(t *testing.T) {
	spec := SpecSummary{
		Name:      "vm-with-nic",
		Arch:      "aarch64",
		VCPUs:     1,
		MemoryMiB: 256,
		CPUModel:  "host",
		Disks:     []DiskSpec{{Path: "/var/lib/govirta/root.qcow2"}},
		NICs:      []NICSpec{{TapName: "gvtap0", MAC: "02:00:00:00:00:01"}},
	}
	b, err := deriveBuilder(spec, testNodeEnv)
	if err != nil {
		t.Fatalf("deriveBuilder: %v", err)
	}
	vm, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	argv := strings.Join(vm.Argv(), " ")
	if strings.Contains(argv, "-nic none") {
		t.Fatalf("VM with explicit NIC must not render -nic none, got: %s", argv)
	}
}

func TestMapArchRejectsUnknown(t *testing.T) {
	if _, _, err := mapArch("riscv64"); err == nil {
		t.Fatal("unknown arch must error")
	}
	for _, a := range []string{"x86_64", "aarch64"} {
		if _, _, err := mapArch(a); err != nil {
			t.Fatalf("arch %q must map: %v", a, err)
		}
	}
}
