package vmm

import (
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
