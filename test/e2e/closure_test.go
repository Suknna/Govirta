//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	e2eEnabledEnv  = "GOVIRTA_E2E"
	e2eServerEnv   = "GOVIRTA_E2E_SERVER"
	e2eCtlEnv      = "GOVIRTA_E2E_GOVIRTCTL"
	e2eManifestEnv = "GOVIRTA_E2E_MANIFESTS"
	e2eNodeEnv     = "GOVIRTA_E2E_NODE"
)

// Object names the spine is built from. They MUST match metadata.name in the
// manifests under test/e2e/manifests; the teardown drives govirtctl with these
// exact names, so a drift here would delete the wrong object (or nothing).
const (
	vmName    = "vm-e2e"       // 07-vm.json metadata.name
	nicName   = "nic-e2e"      // 06-nic.json
	netName   = "net-e2e"      // 05-network.json
	volName   = "vol-e2e-root" // 04-volume.json
	imageName = "image-cirros" // 03-image.json
	poolBlock = "pool-block"   // 01-storagepool-block.json
	poolFile  = "pool-file"    // 02-storagepool-file.json
	snapName  = "snap-e2e"     // 08-snapshot.json metadata.name
)

// Non-name identifiers the guest-side live assertions key off. Unlike the object
// names above (which drive govirtctl), these are the manifest fields that decide
// the guest's on-disk/kernel layout — so they MUST match the manifests exactly,
// and are the single source of truth shared by the apply calls and the Guest
// probes (a drift here would assert against the wrong qcow2/bridge and mask leaks).
const (
	vmUID        = "vm-e2e-001"   // 07-vm.json metadata.uid (qcow2 dir + runtime dir + identity derivations)
	snapUID      = "snap-e2e-001" // 08-snapshot.json metadata.uid (internal qcow2 snapshot tag)
	orphanBridge = "govirta0"     // 05-network.json spec.bridgeName (non-derived, asserted by name)
	diskIndex    = 0              // 04-volume.json spec.diskIndex (qcow2 file suffix)
	nicIndex     = 0              // 06-nic.json single nicRef, fixed index 0 (TAP + anti-spoof chain suffix)
)

// 冷扩容容量契约（刀 5）。volBaseCapacityBytes 必须等于 04-volume.json 的
// spec.capacityBytes（1 GiB），是 live qcow2 virtual-size 的初始权威；扩容目标取
// 2× = 2 GiB，远低于 01-storagepool-block.json 的 pool 容量 10 GiB（预分配记账
// 不会触 ErrPoolCapacityExceeded）。缩容用 volShrinkCapacityBytes（< 基准）触发
// admission 「只增不减」拒绝（ReasonConflict → HTTP 409）。
const (
	volBaseCapacityBytes   int64 = 1073741824 // 1 GiB，与 04-volume.json spec.capacityBytes 一致
	volGrownCapacityBytes  int64 = 2147483648 // 2 GiB = 旧值 2×（冷扩容目标）
	volShrinkCapacityBytes int64 = 536870912  // 512 MiB < 基准，负向缩容用例
)

// 刀 6 冷配置改契约。vmBaseMemoryMiB 必须等于 07-vm.json 的 spec.memoryMiB（256），
// 是 live `-m size=<MiB>` argv 断言的初始权威；场景 1 把内存翻倍到 vmGrownMemoryMiB
// （= 2×），场景 3 在它之上再翻倍到 vmRejectMemoryMiB（被 On 态门禁拒绝，不会落地）。
// 三者由 assertVMBaseMemoryContract 在执行前结构性钉死，防 manifest 与常量漂移。
const (
	vmBaseMemoryMiB   = 256  // 与 07-vm.json spec.memoryMiB 一致
	vmGrownMemoryMiB  = 512  // 256×2，场景 1 冷态翻倍目标
	vmRejectMemoryMiB = 1024 // 512×2，场景 3 On 态改内存（期望被拒，不落地）
)

// 场景 2 加的第二块 data 盘。name/uid 与现有 vol-e2e-root 不同以避免冲突；
// diskIndexData 必须与 09-volume-data.json 的 spec.diskIndex 一致（qcow2 文件后缀）。
const (
	volDataName         = "vol-e2e-data" // 09-volume-data.json metadata.name
	volDataManifestName = "09-volume-data.json"
	diskIndexData       = 1 // 09-volume-data.json spec.diskIndex（≠ root 盘的 0）
)

// applyOrder is the dependency order the controllers gate on: pools first, then
// image (needs file pool), volume (needs block pool + image), network, and NIC
// (needs network). The VM is applied separately so the test can drive explicit
// powerState variants across one committed topology.
var dependencyApplyOrder = []string{
	"01-storagepool-block.json",
	"02-storagepool-file.json",
	"03-image.json",
	"04-volume.json",
	"05-network.json",
	"06-nic.json",
}

const vmManifestName = "07-vm.json"

const snapshotManifestName = "08-snapshot.json"

// volumeManifestName 是 04-volume.json 的基线 Volume manifest；冷扩容/缩容用例在
// 它之上改 spec.capacityBytes 渲染到 tmpDir，基线文件本身保持不变（与
// writeVMManifestVariant 同款：测试侧渲染变体，不污染受测 manifest）。
const volumeManifestName = "04-volume.json"

// TestDistributedSpineClosure drives the full lifecycle against the real
// three-node topology: apply the spine in dependency order, wait for the VM to
// reach Running (forward closure), then tear the spine down in reverse
// dependency order and prove every object truly disappears (reverse closure).
//
// The reverse segment exercises the deletion lifecycle end to end: the
// apiserver's reference-protection (409 while still referenced), the
// finalizer two-phase (delete stamps deletionTimestamp, the node tears down the
// live resource, the finalizer is dropped, the apiserver truly removes the
// object → 404). Guest-side proof that no live kernel/QEMU resources leak is the
// job of this test's own assertNoOrphans, called after teardown to inspect the
// guest directly (via the Guest handle) and confirm no live residue remains.
func TestDistributedSpineClosure(t *testing.T) {
	if os.Getenv(e2eEnabledEnv) != "1" {
		t.Skipf("set %s=1 (via scripts/e2e.sh) to run the e2e closure test", e2eEnabledEnv)
	}
	server := requireEnv(t, e2eServerEnv)
	ctl := requireEnv(t, e2eCtlEnv)
	manifests := requireEnv(t, e2eManifestEnv)
	nodeName := os.Getenv(e2eNodeEnv)
	if nodeName == "" {
		nodeName = "node0"
	}

	// The forward apply + wait-Running can take minutes; the reverse teardown
	// adds a VM stop+delete plus six more delete→404 polls. Give the whole
	// lifecycle ample headroom so a slow-but-correct teardown is not failed.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	// Guest handle for guest-side live verification (上下一致: assert lower-layer
	// reality, not just the master's API projection).
	g := newGuest(t)

	// Phase-one Task proof: scripts/e2e.sh starts govirtad with explicit
	// phase-one Task config and govirtlet with the same nodeName. These checks prove
	// the real etcd + govirtad + govirtlet path completes both ClusterTask and
	// NodeTask before the legacy business controllers drive the VM spine.
	waitTaskSucceeded(ctx, t, ctl, server, "phase-one-cluster-task", 2*time.Minute)
	waitTaskSucceeded(ctx, t, ctl, server, "phase-one-node-task-"+nodeName, 2*time.Minute)

	// Forward segment: apply dependencies first, define the VM while powered Off,
	// then drive declared power intent through the two-dimensional power model:
	// On, Off+Acpi (graceful ACPI), On again, Off+Force (forced power-off).
	applySpineDependencies(ctx, t, ctl, server, manifests)
	waitObjectPhase(ctx, t, ctl, server, "NIC", nicName, "ready", 3*time.Minute)

	tmpDir := t.TempDir()
	// Scenario 1: create powerState=Off + powerOffMode=Acpi → defined, Off/None.
	applyVMVariant(ctx, t, ctl, server, manifests, tmpDir, vmName, vmUID, "Off", "Acpi")
	waitVMOffConverged(ctx, t, ctl, server, 3*time.Minute)
	replaceCycle(ctx, t, ctl, server, tmpDir)

	// Scenario 2: update powerState=On (powerOffMode empty) → running, On/None.
	applyVMVariant(ctx, t, ctl, server, manifests, tmpDir, vmName, vmUID, "On", "")
	waitVMOnRunning(ctx, t, ctl, server)

	// MAC 透传 live 铁证：正在运行的 QEMU 进程 argv 必须携带控制面分配的 NIC.spec.MAC，
	// 证明 MAC 真正贯穿到 qemu argv（整顿前 device.VirtioNetPCI.Mac 从不设置）。MAC 由
	// apiserver admission 分配（06-nic.json 不含 mac），故动态从 master 读分配值，不硬编码
	// （上下一致 + memory 698）。断言落在 QEMU argv 层而非 CirrOS guest 内网卡：limactl
	// shell 进的是 Lima VM，QEMU 在其中运行（argv 可达），但 QEMU 再 spawn 的 CirrOS
	// 嵌套 guest 网卡不在 Lima VM（读 /sys/class/net 只会读到 Lima VM 自己的网卡）。
	assignedMAC := readNICMAC(ctx, t, ctl, server, nicName)
	g.AssertRunningQEMUArgvHasMAC(ctx, vmUID, assignedMAC)

	// Scenario 3: update powerState=Off + powerOffMode=Acpi → graceful ACPI
	// shutdown. CirrOS 0.6.2 supports ACPI shutdown (≥0.3.1), so this verifies
	// true convergence to Off (not just that Stop was issued).
	applyVMVariant(ctx, t, ctl, server, manifests, tmpDir, vmName, vmUID, "Off", "Acpi")
	waitVMOffConverged(ctx, t, ctl, server, 3*time.Minute)

	// Scenario 4: update powerState=On → running again.
	applyVMVariant(ctx, t, ctl, server, manifests, tmpDir, vmName, vmUID, "On", "")
	waitVMOnRunning(ctx, t, ctl, server)

	// Scenario 5: update powerState=Off + powerOffMode=Force → forced power-off
	// (vmm.Kill). Does not depend on guest cooperation; the QEMU process must be
	// gone. Verify both the master status (Off/None) and the live absence of any
	// QEMU process keyed by the VM uid (上下一致).
	applyVMVariant(ctx, t, ctl, server, manifests, tmpDir, vmName, vmUID, "Off", "Force")
	waitVMOffConverged(ctx, t, ctl, server, 3*time.Minute)
	g.AssertNoQEMUProcess(ctx, vmUID)

	// Cold-snapshot segment: the VM has just converged to Off/stopped, which is
	// the cold precondition for taking and deleting an integral snapshot. Run the
	// full snapshot closure here, before teardownSpine deletes the VM, because the
	// reverse-reference edge (VM ← Snapshot.vmRef) pins the VM while the snapshot
	// is alive.
	snapshotColdCycle(ctx, t, ctl, server, manifests, g)

	// Cold-resize segment: still in the same Off/cold window (resize 的前提是 VM
	// stopped/defined)。放在 snapshotColdCycle 之后是有意为之——彼时内部快照已被
	// deleteAndVerify 删净，qcow2 不带内部快照，扩容路径不与快照交互。
	coldResizeVolume(ctx, t, ctl, server, manifests, tmpDir, g)

	// Cold-config-change segment（刀 6）：在 snapshotColdCycle + coldResizeVolume
	// 之后——彼时 VM 仍 Off/cold。三场景会把 VM On 起来验证 Redefine 重派生的 argv，
	// 故必须排在依赖「VM 持续冷态」的前两段之后；场景结束把第二块盘的引用与资源都清掉，
	// 让随后的 teardownSpine（只删固定 vol-e2e-root）不留 orphan。
	coldConfigChange(ctx, t, ctl, server, manifests, tmpDir, g)

	expectPowerAdmissionRejections(ctx, t, ctl, server, manifests, tmpDir)

	// Reverse segment: tear the spine down and prove the deletion lifecycle.
	teardownSpine(ctx, t, ctl, server, g)
}

// applySpineDependencies applies the non-VM spine manifests in dependency order,
// failing the test on the first apply that the master rejects.
func applySpineDependencies(ctx context.Context, t *testing.T, ctl, server, manifests string) {
	t.Helper()
	for _, name := range dependencyApplyOrder {
		path := filepath.Join(manifests, name)
		out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", path)
		if err != nil {
			t.Fatalf("apply %s failed: %v\noutput:\n%s", name, err, out)
		}
		t.Logf("applied %s: %s", name, strings.TrimSpace(out))
	}
}

func applyVMVariant(ctx context.Context, t *testing.T, ctl, server, manifests, tmpDir, name, uid, powerState, powerOffMode string) {
	t.Helper()
	path := writeVMManifestVariant(t, filepath.Join(manifests, vmManifestName), tmpDir, name, uid, powerState, powerOffMode)
	out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", path)
	if err != nil {
		t.Fatalf("apply VM %s powerState %s/%s failed: %v\noutput:\n%s", name, powerState, powerOffMode, err, out)
	}
	t.Logf("applied VM %s powerState %s/%s: %s", name, powerState, powerOffMode, strings.TrimSpace(out))
}

// expectPowerAdmissionRejections proves the apiserver admission rejects the
// three illegal create-time power specs from design §7 scenario 6:
//   - powerState=Shutdown (the removed value is no longer a valid powerState)
//   - powerState=On with a non-empty powerOffMode (mode must be empty when On)
//   - powerState=Off with an empty powerOffMode (mode is required when Off)
//
// Each must be rejected by govirtctl apply (non-nil error); the value
// dimensions are exercised through writeVMManifestVariant so the rejection
// comes from the real admission chain, not a client-side check.
func expectPowerAdmissionRejections(ctx context.Context, t *testing.T, ctl, server, manifests, tmpDir string) {
	t.Helper()
	base := filepath.Join(manifests, vmManifestName)
	cases := []struct {
		name         string
		powerState   string
		powerOffMode string
	}{
		{"shutdown-create-rejected", "Shutdown", ""},
		{"on-with-mode-rejected", "On", "Acpi"},
		{"off-missing-mode-rejected", "Off", ""},
	}
	for _, tc := range cases {
		objName := "vm-" + tc.name
		path := writeVMManifestVariant(t, base, tmpDir, objName, objName+"-001", tc.powerState, tc.powerOffMode)
		out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", path)
		if err == nil {
			t.Fatalf("create VM (powerState=%q powerOffMode=%q) must be rejected, but it was accepted:\n%s", tc.powerState, tc.powerOffMode, out)
		}
		t.Logf("create VM (powerState=%q powerOffMode=%q) correctly rejected: %s", tc.powerState, tc.powerOffMode, strings.TrimSpace(out))
	}
}

func snapshotColdCycle(ctx context.Context, t *testing.T, ctl, server, manifests string, g *Guest) {
	t.Helper()
	// qcow2 root = block StoragePool spec.storageRoot (01-storagepool-block.json).
	qcow := guestQcowPath(guestBlockStorageRoot, poolBlock, vmUID, vmName, diskIndex)

	applyAndVerify(ctx, t, ctl, server, manifests, resourceLifecycle{
		manifest: snapshotManifestName, kind: "Snapshot", name: snapName,
		waitPhase: "ready", waitFor: 2 * time.Minute,
		afterReady: func(ctx context.Context) {
			// items 2/5: post-create the node really ran qemu-img snapshot -c.
			g.AssertQcowHasSnapshot(ctx, qcow, snapUID)
		},
	})

	// reverse-reference edge: deleting the VM is refused while the snapshot pins it.
	expectReferencedRejection(ctx, t, ctl, server, "VM", vmName)

	deleteAndVerify(ctx, t, ctl, server, resourceLifecycle{
		kind: "Snapshot", name: snapName, waitFor: 2 * time.Minute,
		afterGone: func(ctx context.Context) {
			// items 2/5: post-delete the node really ran qemu-img snapshot -d.
			g.AssertQcowNoSnapshot(ctx, qcow, snapUID)
		},
	})
}

// coldResizeVolume 验证刀 5 冷扩容端到端：VM 已 Off（冷窗口）时 apply 改大的
// Volume capacityBytes，断言卷仍 Ready 且 guest 内 qcow2 的真实 virtual-size 收敛
// 到新目标；再 apply 缩小值断言被 apiserver admission 拒绝（只增不减契约）。
func coldResizeVolume(ctx context.Context, t *testing.T, ctl, server, manifests, tmpDir string, g *Guest) {
	t.Helper()
	// qcow2 root = block StoragePool spec.storageRoot（与 snapshotColdCycle 同源）。
	qcow := guestQcowPath(guestBlockStorageRoot, poolBlock, vmUID, vmName, diskIndex)

	// 漂移守卫：扩容目标是「基准 2×」，而基准的唯一权威是 04-volume.json 的
	// spec.capacityBytes。若有人改了 manifest 却没同步常量，下面的 live virtual-size
	// 断言会去追一个错误的目标值（且失败信息会误导）。这里读基准 manifest 钉死两件事：
	// 基准 == volBaseCapacityBytes，目标 == 基准 2×。任一不符立即 Fatalf，把"测试常量
	// 与 manifest 漂移"这种隐性 bug 在执行扩容前结构性挡掉。
	assertVolumeCapacityContract(t, filepath.Join(manifests, volumeManifestName))

	// 正向：apply 旧值 2× 的 Volume manifest。扩容是 A2 语义——phase 始终保持 Ready，
	// 容量不进 status（决策 3），所以不能靠 phase 变化判定收敛。先确认 apply 被接受、
	// 卷仍 Ready，再轮询 guest qcow2 的 live virtual-size 直到等于新目标（控制器的
	// qemu-img resize 是异步收敛的，紧跟 apply 直接断言会与控制器竞争）。
	grownPath := writeVolumeManifestVariant(t, filepath.Join(manifests, volumeManifestName), tmpDir, volGrownCapacityBytes)
	out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", grownPath)
	if err != nil {
		t.Fatalf("apply grown Volume capacityBytes=%d failed: %v\noutput:\n%s", volGrownCapacityBytes, err, out)
	}
	t.Logf("applied grown Volume capacityBytes=%d: %s", volGrownCapacityBytes, strings.TrimSpace(out))

	// A2：phase 不变，卷仍 Ready（扩容失败也只是保持 Ready + 结构化日志 + 重试）。
	waitObjectPhase(ctx, t, ctl, server, "Volume", volName, "ready", 2*time.Minute)

	// live 铁证：guest 内读 qcow2 真实 virtual-size 收敛到新目标，证明 resize 真落到
	// 磁盘（不只信 master 的 Ready 投影 — 上下一致铁律）。先轮询到收敛，再用语义化的
	// AssertQcowVirtualSize 钉死最终值（最后一次断言把"恰等于"写进失败信息）。
	waitQcowVirtualSize(ctx, t, g, qcow, volGrownCapacityBytes, 2*time.Minute)
	g.AssertQcowVirtualSize(ctx, qcow, volGrownCapacityBytes)

	// 负向：apply 缩小的 capacityBytes 必须被 apiserver admission 拒绝。admission
	// fields.go 对 Volume update 的 capacityBytes 减少返回 ReasonConflict（→ HTTP 409），
	// govirtctl 把非 2xx 映射成非零退出并在输出里带 admission 错误文本。
	shrinkPath := writeVolumeManifestVariant(t, filepath.Join(manifests, volumeManifestName), tmpDir, volShrinkCapacityBytes)
	out, err = runCtl(ctx, ctl, "apply", "--server", server, "-f", shrinkPath)
	if err == nil {
		t.Fatalf("apply shrunk Volume capacityBytes=%d must be rejected (只增不减契约), but it was accepted:\n%s", volShrinkCapacityBytes, out)
	}
	if !strings.Contains(out, "capacityBytes cannot decrease") {
		t.Fatalf("shrink rejection must carry the admission 只增不减 reason (%q), got:\n%s", "capacityBytes cannot decrease", out)
	}
	t.Logf("shrink Volume capacityBytes=%d correctly rejected (409): %s", volShrinkCapacityBytes, strings.TrimSpace(out))
}

// coldConfigChange 验证刀 6 冷配置改端到端（三场景），前提是进入时 VM 处于 Off/cold
// （前序 coldResizeVolume 收敛到 Off）。每个场景把 VM On 起来后用 guest-side argv 实况
// 断言新配置真正贯穿到 QEMU 启动参数（不只信 master 投影 — 上下一致铁律）：
//
//	场景 1 改标量：冷态把 memoryMiB 翻倍 → On → argv 含 `-m size=<新值>`。
//	场景 2 增硬件：新建第二块 data Volume，冷态 volumeRefs 追加它 → On → argv 含两块
//	         virtio-blk 盘；随后用「删硬件 = 先从 refs 移除再删独立资源」逆操作收尾，
//	         让 teardownSpine（只删固定 vol-e2e-root）不留 orphan（选项 A）。
//	场景 3 admission 拒绝：On 态改 memoryMiB → 被门禁 1 拒（错误信息含 powerState=Off）。
func coldConfigChange(ctx context.Context, t *testing.T, ctl, server, manifests, tmpDir string, g *Guest) {
	t.Helper()
	base := filepath.Join(manifests, vmManifestName)

	// 漂移守卫：场景常量必须与基线 manifest 对齐，否则 live argv 断言会去追错误的
	// 内存目标且失败信息误导（与 assertVolumeCapacityContract 同款，执行前结构性挡掉）。
	assertVMBaseMemoryContract(t, base)

	rootOnly := []string{volName}
	rootAndData := []string{volName, volDataName}

	// --- 场景 1：改标量（内存翻倍）---
	// VM 此刻 Off。冷态 apply memoryMiB 翻倍（volumeRefs 维持单盘），等仍收敛到 Off。
	p := writeVMManifestColdVariant(t, base, tmpDir, vmColdVariant{
		memoryMiB: vmGrownMemoryMiB, volumeRefs: rootOnly, powerState: "Off", powerOffMode: "Acpi",
	})
	applyVMManifest(ctx, t, ctl, server, p, "scenario1 cold memory grow Off")
	waitVMOffConverged(ctx, t, ctl, server, 3*time.Minute)

	// On 起来：内存不变（512==512，非 cold 变更，纯电源变更，门禁放行），等 Running。
	p = writeVMManifestColdVariant(t, base, tmpDir, vmColdVariant{
		memoryMiB: vmGrownMemoryMiB, volumeRefs: rootOnly, powerState: "On",
	})
	applyVMManifest(ctx, t, ctl, server, p, "scenario1 power On")
	waitVMOnRunning(ctx, t, ctl, server)

	// live 铁证：正在运行的 QEMU argv 携带新内存 token `-m size=512`（Redefine 重派生）。
	g.AssertRunningQEMUArgvHasMemory(ctx, vmUID, vmGrownMemoryMiB)

	// --- 场景 2：增硬件（加第二块盘）---
	// 先建第二块 data Volume，等 Ready（它 vmRef 指向本 VM uid；VM 尚未引用它，
	// 刀 1 反向引用守卫放行其创建）。
	applyAndVerify(ctx, t, ctl, server, manifests, resourceLifecycle{
		manifest: volDataManifestName, kind: "Volume", name: volDataName,
		waitPhase: "ready", waitFor: 2 * time.Minute,
	})

	// VM 当前 On → 先回冷态（仅电源变更）再加盘（cold 变更须 Off）。
	p = writeVMManifestColdVariant(t, base, tmpDir, vmColdVariant{
		memoryMiB: vmGrownMemoryMiB, volumeRefs: rootOnly, powerState: "Off", powerOffMode: "Acpi",
	})
	applyVMManifest(ctx, t, ctl, server, p, "scenario2 power Off before add disk")
	waitVMOffConverged(ctx, t, ctl, server, 3*time.Minute)

	// 冷态 volumeRefs 追加第二块盘，等收敛（仍 Off）。
	p = writeVMManifestColdVariant(t, base, tmpDir, vmColdVariant{
		memoryMiB: vmGrownMemoryMiB, volumeRefs: rootAndData, powerState: "Off", powerOffMode: "Acpi",
	})
	applyVMManifest(ctx, t, ctl, server, p, "scenario2 cold add second disk Off")
	waitVMOffConverged(ctx, t, ctl, server, 3*time.Minute)

	// On 起来（纯电源变更），等 Running。
	p = writeVMManifestColdVariant(t, base, tmpDir, vmColdVariant{
		memoryMiB: vmGrownMemoryMiB, volumeRefs: rootAndData, powerState: "On",
	})
	applyVMManifest(ctx, t, ctl, server, p, "scenario2 power On with two disks")
	waitVMOnRunning(ctx, t, ctl, server)

	// live 铁证：QEMU argv 恰含两块 virtio-blk 盘（Redefine 重派生加盘）。
	g.AssertRunningQEMUArgvDiskCount(ctx, vmUID, 2)

	// 选项 A 收尾「删硬件 = 先从 refs 移除再删独立资源」：冷态把 volumeRefs 移除第二块盘，
	// 等收敛，再删第二块 Volume 到 404（VM 已不引用 → 刀 1 反向引用守卫放行删除），并断言
	// 其 guest qcow2 真消失。这让随后只删 vol-e2e-root 的 teardownSpine 不留 orphan。
	p = writeVMManifestColdVariant(t, base, tmpDir, vmColdVariant{
		memoryMiB: vmGrownMemoryMiB, volumeRefs: rootOnly, powerState: "Off", powerOffMode: "Acpi",
	})
	applyVMManifest(ctx, t, ctl, server, p, "scenario2 cold remove second disk Off")
	waitVMOffConverged(ctx, t, ctl, server, 3*time.Minute)

	dataQcow := guestQcowPath(guestBlockStorageRoot, poolBlock, vmUID, vmName, diskIndexData)
	deleteAndVerify(ctx, t, ctl, server, resourceLifecycle{
		kind: "Volume", name: volDataName, waitFor: 2 * time.Minute,
		afterGone: func(ctx context.Context) {
			g.AssertNoQcow2(ctx, dataQcow)
		},
	})

	// --- 场景 3：admission 拒绝 ---
	// VM 当前 Off（场景 2 收尾把它停下并移除了第二块盘）。On 态改 memoryMiB 必须被门禁 1
	// 拒绝：cold-mutable 变更要求 powerState=Off。govirtctl 把非 2xx 映射成非零退出，
	// 错误文本带 admission 的 powerState=Off 语义。
	p = writeVMManifestColdVariant(t, base, tmpDir, vmColdVariant{
		memoryMiB: vmRejectMemoryMiB, volumeRefs: rootOnly, powerState: "On",
	})
	out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", p)
	if err == nil {
		t.Fatalf("apply cold memory change (memoryMiB=%d) with powerState=On must be rejected (门禁 1), but it was accepted:\n%s", vmRejectMemoryMiB, out)
	}
	if !strings.Contains(out, "powerState=Off") {
		t.Fatalf("cold config change rejection must carry the admission powerState=Off requirement (%q), got:\n%s", "powerState=Off", out)
	}
	t.Logf("cold memory change (memoryMiB=%d) with powerState=On correctly rejected (4xx): %s", vmRejectMemoryMiB, strings.TrimSpace(out))
}

// applyVMManifest applies a pre-rendered VM manifest file, failing on rejection.
// 与 applyVMVariant 同款，但接受已渲染的路径（冷配置改用 writeVMManifestColdVariant
// 渲染富变体，故不复用按 powerState/powerOffMode 渲染的 applyVMVariant）。
func applyVMManifest(ctx context.Context, t *testing.T, ctl, server, path, label string) {
	t.Helper()
	out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", path)
	if err != nil {
		t.Fatalf("apply VM (%s) failed: %v\noutput:\n%s", label, err, out)
	}
	t.Logf("applied VM (%s): %s", label, strings.TrimSpace(out))
}

// assertVMBaseMemoryContract 读基线 VM manifest，钉死冷配置改场景的内存不变式：
// 基线 spec.memoryMiB == vmBaseMemoryMiB，且场景目标按 2× 链对齐
// （grown == base×2，reject == grown×2）。manifest 是基线内存的唯一权威，常量是
// live argv 断言追的目标；二者漂移会让断言去追错误值并给出误导性失败信息。
func assertVMBaseMemoryContract(t *testing.T, basePath string) {
	t.Helper()
	body, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("read VM manifest %q: %v", basePath, err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode VM manifest %q: %v", basePath, err)
	}
	spec := objectMap(t, manifest, "spec")
	// JSON number 解码进 map[string]any 是 float64；内存 MiB 远在 2^53 内，精确。
	raw, ok := spec["memoryMiB"].(float64)
	if !ok {
		t.Fatalf("VM manifest %q spec.memoryMiB must be a JSON number, got %T", basePath, spec["memoryMiB"])
	}
	base := int(raw)
	if base != vmBaseMemoryMiB {
		t.Fatalf("VM manifest %q spec.memoryMiB=%d drifted from test constant vmBaseMemoryMiB=%d", basePath, base, vmBaseMemoryMiB)
	}
	if vmGrownMemoryMiB != base*2 {
		t.Fatalf("cold-config grow target vmGrownMemoryMiB=%d must be 2× the base %d", vmGrownMemoryMiB, base)
	}
	if vmRejectMemoryMiB != vmGrownMemoryMiB*2 {
		t.Fatalf("cold-config reject target vmRejectMemoryMiB=%d must be 2× the grown %d", vmRejectMemoryMiB, vmGrownMemoryMiB)
	}
}

// vmColdVariant 是 writeVMManifestColdVariant 的覆写集：冷配置改场景需要在基线 VM
// manifest 之上同时设置 memoryMiB / volumeRefs / 电源二维，故用结构体承载所有可变字段
// （现有 writeVMManifestVariant 只改 powerState/powerOffMode，覆盖不到 cold-mutable
// 字段）。name/uid 固定用 vmName/vmUID（同一 VM 的连续 update，不另起对象）。
type vmColdVariant struct {
	memoryMiB    int
	volumeRefs   []string
	powerState   string
	powerOffMode string
}

// writeVMManifestColdVariant 读基线 VM manifest，覆写 memoryMiB / volumeRefs /
// powerState / powerOffMode，渲染到 tmpDir。复用 writeVMManifestVariant 的
// 「读基线→改字段→写临时文件」模式，不重抄 manifest（基线是 07-vm.json 的唯一权威）。
// powerOffMode 空串删除该字段（powerState=On 的二维模型要求），非空则设置（Off 必填）。
func writeVMManifestColdVariant(t *testing.T, basePath, tmpDir string, v vmColdVariant) string {
	t.Helper()
	body, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("read VM manifest %q: %v", basePath, err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode VM manifest %q: %v", basePath, err)
	}
	metadata := objectMap(t, manifest, "metadata")
	metadata["name"] = vmName
	metadata["uid"] = vmUID
	spec := objectMap(t, manifest, "spec")
	spec["memoryMiB"] = v.memoryMiB
	// volumeRefs 写为 []string；MarshalIndent 按 Go 类型重新编码为 JSON 字符串数组。
	spec["volumeRefs"] = v.volumeRefs
	spec["powerState"] = v.powerState
	if v.powerOffMode == "" {
		delete(spec, "powerOffMode")
	} else {
		spec["powerOffMode"] = v.powerOffMode
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode VM manifest cold variant mem=%d disks=%d %s: %v", v.memoryMiB, len(v.volumeRefs), v.powerState, err)
	}
	path := filepath.Join(tmpDir, fmt.Sprintf("%s-cold-mem%d-disks%d-%s.json", vmName, v.memoryMiB, len(v.volumeRefs), v.powerState))
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write VM manifest cold variant %q: %v", path, err)
	}
	return path
}

// waitQcowVirtualSize 轮询 guest qcow2 的 live virtual-size 直到等于 want 或超时。
// 扩容是异步收敛的且 phase 不变（A2），没有 master 侧信号可等，所以以 guest 实况为准
// 轮询（与 waitObjectPhase/waitGone 同款 deadline 循环）。连接层错误（limactl 失败 /
// ctx 取消）立即 Fatalf，绝不当成"未收敛"重试，避免把探针失效误读为容量不对。
func waitQcowVirtualSize(ctx context.Context, t *testing.T, g *Guest, qcowPath string, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last int64
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context ended before qcow %q virtual size reached %d: %v (last: %d)", qcowPath, want, err, last)
		}
		got, err := g.QcowVirtualSize(ctx, qcowPath)
		if err != nil {
			t.Fatalf("read qcow virtual size %q: %v", qcowPath, err)
		}
		last = got
		if got == want {
			t.Logf("qcow %q virtual size converged to %d", qcowPath, want)
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("qcow %q virtual size did not reach %d before deadline (last: %d)", qcowPath, want, last)
}

// assertVolumeCapacityContract 读基准 Volume manifest，钉死冷扩容用例的两条不变式：
// 基准 spec.capacityBytes == volBaseCapacityBytes，且扩容目标 volGrownCapacityBytes
// 恰为基准 2×。manifest 是基准容量的唯一权威，常量是 live virtual-size 断言追的目标；
// 二者漂移会让断言去追错误值并给出误导性失败信息，所以执行扩容前结构性挡掉。
func assertVolumeCapacityContract(t *testing.T, basePath string) {
	t.Helper()
	body, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("read Volume manifest %q: %v", basePath, err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode Volume manifest %q: %v", basePath, err)
	}
	spec := objectMap(t, manifest, "spec")
	// JSON number 解码进 map[string]any 是 float64；容量字节数在 2^53 内，float64
	// 表示精确，转回 int64 不丢精度。
	raw, ok := spec["capacityBytes"].(float64)
	if !ok {
		t.Fatalf("Volume manifest %q spec.capacityBytes must be a JSON number, got %T", basePath, spec["capacityBytes"])
	}
	base := int64(raw)
	if base != volBaseCapacityBytes {
		t.Fatalf("Volume manifest %q spec.capacityBytes=%d drifted from test constant volBaseCapacityBytes=%d", basePath, base, volBaseCapacityBytes)
	}
	if volGrownCapacityBytes != base*2 {
		t.Fatalf("cold-resize target volGrownCapacityBytes=%d must be 2× the base %d", volGrownCapacityBytes, base)
	}
}

// writeVolumeManifestVariant 读取基准 Volume manifest，仅改写 spec.capacityBytes，
// 渲染到 tmpDir。复用 writeVMManifestVariant 的「读基准→改字段→写临时文件」模式，
// 不重抄 manifest 内容（基准是 04-volume.json 的唯一权威）。
func writeVolumeManifestVariant(t *testing.T, basePath, tmpDir string, capacityBytes int64) string {
	t.Helper()
	body, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("read Volume manifest %q: %v", basePath, err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode Volume manifest %q: %v", basePath, err)
	}
	spec := objectMap(t, manifest, "spec")
	// JSON number 落进 map[string]any 是 float64，但这里直接覆盖成目标字节数；
	// MarshalIndent 会按 Go 类型重新编码，int64 写出为无小数点的整数（容量字节数
	// 在 2^53 内，float64/int64 表示均精确，不丢精度）。
	spec["capacityBytes"] = capacityBytes

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode Volume manifest variant capacityBytes=%d: %v", capacityBytes, err)
	}
	path := filepath.Join(tmpDir, fmt.Sprintf("%s-cap-%d.json", volName, capacityBytes))
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write Volume manifest variant %q: %v", path, err)
	}
	return path
}

// writeVMManifestVariant renders a VM manifest with the given powerState and
// powerOffMode. An empty powerOffMode removes the field entirely (required for
// powerState=On, where the two-dimensional power model forbids a non-empty
// powerOffMode); a non-empty value sets it (required for powerState=Off).
func writeVMManifestVariant(t *testing.T, basePath, tmpDir, name, uid, powerState, powerOffMode string) string {
	t.Helper()
	body, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("read VM manifest %q: %v", basePath, err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode VM manifest %q: %v", basePath, err)
	}
	metadata := objectMap(t, manifest, "metadata")
	metadata["name"] = name
	metadata["uid"] = uid
	spec := objectMap(t, manifest, "spec")
	spec["powerState"] = powerState
	if powerOffMode == "" {
		delete(spec, "powerOffMode")
	} else {
		spec["powerOffMode"] = powerOffMode
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode VM manifest variant %s/%s: %v", name, powerState, err)
	}
	path := filepath.Join(tmpDir, fmt.Sprintf("%s-%s-%s.json", name, powerState, powerOffMode))
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write VM manifest variant %q: %v", path, err)
	}
	return path
}

func objectMap(t *testing.T, obj map[string]any, key string) map[string]any {
	t.Helper()
	child, ok := obj[key].(map[string]any)
	if !ok {
		t.Fatalf("VM manifest field %q must be a JSON object", key)
	}
	return child
}

func waitObjectPhase(ctx context.Context, t *testing.T, ctl, server, kind, name, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context ended before %s/%s reached phase %s: %v\nlast get:\n%s", kind, name, want, err, last)
		}
		out, err := runCtl(ctx, ctl, "get", "--server", server, kind, name)
		last = out
		if err == nil && decodeObjectPhase(t, out) == want {
			t.Logf("%s/%s reached phase %s:\n%s", kind, name, want, out)
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("%s/%s did not reach phase %s before deadline\nlast get:\n%s", kind, name, want, last)
}

func waitVMOnRunning(ctx context.Context, t *testing.T, ctl, server string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context ended before VM reached Running/On: %v\nlast get:\n%s", err, last)
		}
		out, err := runCtl(ctx, ctl, "get", "--server", server, "VM", vmName)
		last = out
		status := decodeVMStatus(t, out)
		if err == nil && status.Phase == "running" && status.ObservedPowerState == "On" && status.PowerTransition == "None" {
			t.Logf("VM %s reached Running/On/None:\n%s", vmName, out)
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("VM %s did not reach Running/On/None before deadline\nlast get:\n%s", vmName, last)
}

func waitVMOffConverged(ctx context.Context, t *testing.T, ctl, server string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context ended before VM reached Off/None: %v\nlast get:\n%s", err, last)
		}
		out, err := runCtl(ctx, ctl, "get", "--server", server, "VM", vmName)
		last = out
		status := decodeVMStatus(t, out)
		if err == nil && status.ObservedPowerState == "Off" && status.PowerTransition == "None" {
			t.Logf("VM %s reached Off/None:\n%s", vmName, out)
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("VM %s did not reach Off/None before deadline\nlast get:\n%s", vmName, last)
}

type vmStatusSnapshot struct {
	Phase              string `json:"phase"`
	ObservedPowerState string `json:"observedPowerState"`
	PowerTransition    string `json:"powerTransition"`
}

// readNICMAC reads the apiserver-assigned MAC from the NIC object's spec. The
// MAC is NOT hardcoded in 06-nic.json — the control plane allocates it at
// admission time, so the e2e must read the assigned value back rather than
// assume a fixed one.
func readNICMAC(ctx context.Context, t *testing.T, ctl, server, name string) string {
	t.Helper()
	out, err := runCtl(ctx, ctl, "get", "--server", server, "NIC", name)
	if err != nil {
		t.Fatalf("get NIC %q for MAC: %v\noutput:\n%s", name, err, out)
	}
	var obj struct {
		Spec struct {
			MAC string `json:"mac"`
		} `json:"spec"`
	}
	if derr := json.NewDecoder(strings.NewReader(out)).Decode(&obj); derr != nil {
		t.Fatalf("decode NIC %q spec.mac: %v\noutput:\n%s", name, derr, out)
	}
	if obj.Spec.MAC == "" {
		t.Fatalf("NIC %q spec.mac is empty (control plane must have allocated it)\noutput:\n%s", name, out)
	}
	return obj.Spec.MAC
}

func decodeVMStatus(t *testing.T, out string) vmStatusSnapshot {
	t.Helper()
	var obj struct {
		Status vmStatusSnapshot `json:"status"`
	}
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&obj); err != nil {
		return vmStatusSnapshot{}
	}
	return obj.Status
}

func decodeObjectPhase(t *testing.T, out string) string {
	t.Helper()
	var obj struct {
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&obj); err != nil {
		return ""
	}
	return obj.Status.Phase
}

func waitTaskSucceeded(ctx context.Context, t *testing.T, ctl, server, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context ended before Task/%s reached Succeeded: %v\nlast get:\n%s", name, err, last)
		}
		out, err := runCtl(ctx, ctl, "get", "--server", server, "Task", name)
		last = out
		if err == nil && decodeObjectPhase(t, out) == "Succeeded" {
			t.Logf("Task/%s reached Succeeded:\n%s", name, out)
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("Task/%s did not reach Succeeded before deadline\nlast get:\n%s", name, last)
}

// teardownSpine deletes the spine in reverse dependency order and proves the
// deletion lifecycle: referenced objects are refused (409), then each object is
// deleted and polled to 404. The order is the reverse of applyOrder because each
// object can only be deleted once the object referencing it is gone — VM first
// (nothing references it), then its NIC and Volume, then the Network and Image
// they reference, then the pools.
func teardownSpine(ctx context.Context, t *testing.T, ctl, server string, g *Guest) {
	t.Helper()

	// 1. Reference protection: while the VM is alive it pins its Volume, and the
	// Volume pins its block pool, so both deletions must be refused with a 409
	// that names the referencing object. This is the key safety semantic of the
	// deletion chain — proving it before tearing anything down.
	expectReferencedRejection(ctx, t, ctl, server, "Volume", volName)
	expectReferencedRejection(ctx, t, ctl, server, "StoragePool", poolBlock)

	// 2. Reverse-order delete with finalizer two-phase verification. The VM goes
	// first (Running→Stop→Stopped→Delete is the slowest step), then the leaf
	// references it pinned, then the resources those reference, then the pools.
	deleteVMAndWaitGone(ctx, t, ctl, server)
	deleteAndWaitGone(ctx, t, ctl, server, "NIC", nicName, 2*time.Minute)
	deleteAndWaitGone(ctx, t, ctl, server, "Volume", volName, 2*time.Minute)
	deleteAndWaitGone(ctx, t, ctl, server, "Network", netName, 2*time.Minute)
	deleteAndWaitGone(ctx, t, ctl, server, "Image", imageName, 2*time.Minute)
	deleteAndWaitGone(ctx, t, ctl, server, "StoragePool", poolBlock, 2*time.Minute)
	deleteAndWaitGone(ctx, t, ctl, server, "StoragePool", poolFile, 2*time.Minute)

	// 3. The apiserver reports every object as 404, but a 404 only proves the API
	// projection is gone — not that the live kernel/QEMU/disk resources the node
	// owned were torn down. Cross the layer boundary and prove the guest itself
	// has no orphaned VM/TAP/bridge/nftables/qcow2 残留 (上下一致铁律).
	assertNoOrphans(ctx, t, g)
}

// 迁移自 e2e.sh verify_no_orphans：teardown 后证明 guest 内无 live 残留
// （上下一致：API 404 不等于 live 资源真没了）。
func assertNoOrphans(ctx context.Context, t *testing.T, g *Guest) {
	t.Helper()
	g.AssertNoQEMUProcess(ctx, vmUID)
	g.AssertNoRuntimeDir(ctx, vmUID)
	g.AssertNoLink(ctx, guestTAPName(vmUID, nicIndex))
	g.AssertNoNftablesChain(ctx, guestAntiSpoofChain(vmUID, nicIndex))
	g.AssertNoLink(ctx, orphanBridge)
	g.AssertNoNftablesChain(ctx, guestMasqueradeChain(netName))
	g.AssertNoNftablesChain(ctx, guestForwardChain(netName))
	g.AssertNoQcow2(ctx, guestQcowPath(guestBlockStorageRoot, poolBlock, vmUID, vmName, diskIndex))
	t.Logf("host-side orphan check passed: no live VM/TAP/bridge/nftables/qcow2 resources remain")
}

// expectReferencedRejection asserts that deleting kind/name is refused because it
// is still referenced: govirtctl must exit non-zero (the client maps the
// apiserver 409 to an error) and the diagnostic must carry the apiserver's
// "still referenced by" protection text.
func expectReferencedRejection(ctx context.Context, t *testing.T, ctl, server, kind, name string) {
	t.Helper()
	out, err := runCtl(ctx, ctl, "delete", "--server", server, kind, name)
	if err == nil {
		t.Fatalf("delete %s/%s must be rejected while still referenced, but it was accepted:\n%s", kind, name, out)
	}
	if !strings.Contains(out, "still referenced by") {
		t.Fatalf("delete %s/%s rejection must name the referencing object (%q), got:\n%s", kind, name, "still referenced by", out)
	}
	t.Logf("%s/%s correctly refused while referenced (409): %s", kind, name, strings.TrimSpace(out))
}

// deleteVMAndWaitGone deletes the VM and proves the finalizer two-phase: the
// delete is accepted (202 → "deleting"), the object may linger in the deleting
// state (deletionTimestamp stamped, finalizer not yet dropped) while the node
// stops and deletes the live VM, and it finally disappears (404). The mid-state
// window can be short, so a 404 on the immediate read is also valid (fast
// teardown); what is asserted is that IF the object is still readable, it carries
// deletionTimestamp — never a fully-live object after an accepted delete.
func deleteVMAndWaitGone(ctx context.Context, t *testing.T, ctl, server string) {
	t.Helper()
	out, err := runCtl(ctx, ctl, "delete", "--server", server, "VM", vmName)
	if err != nil {
		t.Fatalf("delete VM %s failed: %v\noutput:\n%s", vmName, err, out)
	}
	if !strings.Contains(out, "deleting") {
		t.Fatalf("delete VM %s should report acceptance (%q), got:\n%s", vmName, "deleting", out)
	}
	t.Logf("delete VM %s accepted: %s", vmName, strings.TrimSpace(out))

	if body, gerr := runCtl(ctx, ctl, "get", "--server", server, "VM", vmName); gerr == nil {
		if !strings.Contains(body, "deletionTimestamp") {
			t.Fatalf("VM %s lingering after delete must carry deletionTimestamp (finalizer two-phase), got:\n%s", vmName, body)
		}
		t.Logf("VM %s is in the deleting state (deletionTimestamp stamped, finalizer pending)", vmName)
	} else if strings.Contains(body, "not found") {
		t.Logf("VM %s already fully removed before the mid-state read (fast teardown)", vmName)
	} else {
		t.Fatalf("VM %s get after delete failed but was not a 404 (%q) — a transient error must not be mistaken for fast teardown: %v\noutput:\n%s", vmName, "not found", gerr, body)
	}

	// The VM controller stops a Running VM before deleting it
	// (Running→Stop→Stopped→Delete), then drops the finalizer so the apiserver
	// truly removes the object. Give that real teardown ample time.
	waitGone(ctx, t, ctl, server, "VM", vmName, 4*time.Minute)
}

// deleteAndWaitGone deletes kind/name, asserts the master accepted it (202 →
// "deleting"), then polls until the object is gone (404). It is used for every
// non-VM object, whose teardown is a live-resource delete plus finalizer drop.
func deleteAndWaitGone(ctx context.Context, t *testing.T, ctl, server, kind, name string, timeout time.Duration) {
	t.Helper()
	out, err := runCtl(ctx, ctl, "delete", "--server", server, kind, name)
	if err != nil {
		t.Fatalf("delete %s/%s failed: %v\noutput:\n%s", kind, name, err, out)
	}
	if !strings.Contains(out, "deleting") {
		t.Fatalf("delete %s/%s should report acceptance (%q), got:\n%s", kind, name, "deleting", out)
	}
	t.Logf("delete %s/%s accepted: %s", kind, name, strings.TrimSpace(out))
	waitGone(ctx, t, ctl, server, kind, name, timeout)
}

// waitGone polls get kind/name until the master returns not-found (404), failing
// the test if it is still present after timeout. A 404 is recognised by govirtctl
// exiting non-zero AND the diagnostic carrying "not found"; a transient error
// without that text is not treated as gone, so a flaky connection cannot be
// mistaken for a successful delete.
func waitGone(ctx context.Context, t *testing.T, ctl, server, kind, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context ended before %s/%s disappeared: %v\nlast get:\n%s", kind, name, err, last)
		}
		out, err := runCtl(ctx, ctl, "get", "--server", server, kind, name)
		last = out
		if err != nil && strings.Contains(out, "not found") {
			t.Logf("%s/%s is gone (404)", kind, name)
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("%s/%s still present after %s\nlast get:\n%s", kind, name, timeout, last)
}

func runCtl(ctx context.Context, ctl string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, ctl, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is required when %s=1", name, e2eEnabledEnv)
	}
	return v
}
