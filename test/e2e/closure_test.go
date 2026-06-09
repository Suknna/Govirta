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
)

// Object names the spine is built from. They MUST match metadata.name in the
// manifests under test/e2e/manifests; the teardown drives govirtctl with these
// exact names, so a drift here would delete the wrong object (or nothing).
const (
	vmName    = "vm-e2e"       // 07-vm.json
	nicName   = "nic-e2e"      // 06-nic.json
	netName   = "net-e2e"      // 05-network.json
	volName   = "vol-e2e-root" // 04-volume.json
	imageName = "image-cirros" // 03-image.json
	poolBlock = "pool-block"   // 01-storagepool-block.json
	poolFile  = "pool-file"    // 02-storagepool-file.json
	snapName  = "snap-e2e"     // 08-snapshot.json
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

// TestDistributedSpineClosure drives the full lifecycle against the real
// three-node topology: apply the spine in dependency order, wait for the VM to
// reach Running (forward closure), then tear the spine down in reverse
// dependency order and prove every object truly disappears (reverse closure).
//
// The reverse segment exercises the deletion lifecycle end to end: the
// apiserver's reference-protection (409 while still referenced), the
// finalizer two-phase (delete stamps deletionTimestamp, the node tears down the
// live resource, the finalizer is dropped, the apiserver truly removes the
// object → 404). Host-side proof that no live kernel/QEMU resources leak is the
// job of scripts/e2e.sh verify_no_orphans, which inspects the guest directly.
func TestDistributedSpineClosure(t *testing.T) {
	if os.Getenv(e2eEnabledEnv) != "1" {
		t.Skipf("set %s=1 (via scripts/e2e.sh) to run the e2e closure test", e2eEnabledEnv)
	}
	server := requireEnv(t, e2eServerEnv)
	ctl := requireEnv(t, e2eCtlEnv)
	manifests := requireEnv(t, e2eManifestEnv)

	// The forward apply + wait-Running can take minutes; the reverse teardown
	// adds a VM stop+delete plus six more delete→404 polls. Give the whole
	// lifecycle ample headroom so a slow-but-correct teardown is not failed.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	// Forward segment: apply dependencies first, define the VM while powered Off,
	// then drive declared power intent through On, Shutdown, and Off updates.
	applySpineDependencies(ctx, t, ctl, server, manifests)
	waitObjectPhase(ctx, t, ctl, server, "NIC", nicName, "ready", 3*time.Minute)

	tmpDir := t.TempDir()
	applyVMVariant(ctx, t, ctl, server, manifests, tmpDir, vmName, "vm-e2e-001", "Off")
	waitVMOffConverged(ctx, t, ctl, server, 3*time.Minute)

	applyVMVariant(ctx, t, ctl, server, manifests, tmpDir, vmName, "vm-e2e-001", "On")
	waitVMOnRunning(ctx, t, ctl, server)

	applyVMVariant(ctx, t, ctl, server, manifests, tmpDir, vmName, "vm-e2e-001", "Shutdown")
	waitVMShutdownRequestedOrOff(ctx, t, ctl, server, 2*time.Minute)

	applyVMVariant(ctx, t, ctl, server, manifests, tmpDir, vmName, "vm-e2e-001", "Off")
	waitVMOffConverged(ctx, t, ctl, server, 3*time.Minute)

	// Cold-snapshot segment: the VM has just converged to Off/stopped, which is
	// the cold precondition for taking and deleting an integral snapshot. Run the
	// full snapshot closure here, before teardownSpine deletes the VM, because the
	// reverse-reference edge (VM ← Snapshot.vmRef) pins the VM while the snapshot
	// is alive.
	snapshotColdCycle(ctx, t, ctl, server, manifests)

	expectShutdownCreateRejected(ctx, t, ctl, server, manifests, tmpDir)

	// Reverse segment: tear the spine down and prove the deletion lifecycle.
	teardownSpine(ctx, t, ctl, server)
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

func applyVMVariant(ctx context.Context, t *testing.T, ctl, server, manifests, tmpDir, name, uid, powerState string) {
	t.Helper()
	path := writeVMManifestVariant(t, filepath.Join(manifests, vmManifestName), tmpDir, name, uid, powerState)
	out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", path)
	if err != nil {
		t.Fatalf("apply VM %s powerState %s failed: %v\noutput:\n%s", name, powerState, err, out)
	}
	t.Logf("applied VM %s powerState %s: %s", name, powerState, strings.TrimSpace(out))
}

func expectShutdownCreateRejected(ctx context.Context, t *testing.T, ctl, server, manifests, tmpDir string) {
	t.Helper()
	path := writeVMManifestVariant(t, filepath.Join(manifests, vmManifestName), tmpDir, "vm-shutdown-create-rejected", "vm-shutdown-create-rejected-001", "Shutdown")
	out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", path)
	if err == nil {
		t.Fatalf("create VM with powerState Shutdown must be rejected, but it was accepted:\n%s", out)
	}
	if !strings.Contains(out, "Shutdown is only valid for VM updates") && !strings.Contains(out, "powerState Shutdown") {
		t.Fatalf("create VM Shutdown rejection should include admission error, got:\n%s", out)
	}
	t.Logf("create VM with powerState Shutdown correctly rejected: %s", strings.TrimSpace(out))
}

// snapshotColdCycle drives the full cold-snapshot closure against a VM that has
// already converged to Off/stopped: apply the snapshot and wait for it to reach
// ready (the node ran qemu-img against the now-cold disks), prove the
// reverse-reference edge (deleting the VM is refused with a 409 while the
// snapshot pins it via spec.vmRef), then delete the snapshot and poll it to 404.
// Running build+delete here keeps the scenario self-contained so the later
// teardownSpine, which deletes the VM first, is not blocked by a live snapshot.
func snapshotColdCycle(ctx context.Context, t *testing.T, ctl, server, manifests string) {
	t.Helper()

	path := filepath.Join(manifests, snapshotManifestName)
	out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", path)
	if err != nil {
		t.Fatalf("apply %s failed: %v\noutput:\n%s", snapshotManifestName, err, out)
	}
	t.Logf("applied %s: %s", snapshotManifestName, strings.TrimSpace(out))

	waitObjectPhase(ctx, t, ctl, server, "Snapshot", snapName, "ready", 2*time.Minute)

	// The snapshot pins the VM through spec.vmRef, so the VM must not be
	// deletable while the snapshot is alive — proving the reverse-reference edge
	// (VM ← Snapshot.vmRef) is enforced at admission.
	expectReferencedRejection(ctx, t, ctl, server, "VM", vmName)

	deleteAndWaitGone(ctx, t, ctl, server, "Snapshot", snapName, 2*time.Minute)
}

func writeVMManifestVariant(t *testing.T, basePath, tmpDir, name, uid, powerState string) string {
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

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode VM manifest variant %s/%s: %v", name, powerState, err)
	}
	path := filepath.Join(tmpDir, fmt.Sprintf("%s-%s.json", name, powerState))
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
		if err == nil && strings.Contains(out, "phase: "+want) {
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

func waitVMShutdownRequestedOrOff(ctx context.Context, t *testing.T, ctl, server string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context ended before VM reached shutdown request or Off/None: %v\nlast get:\n%s", err, last)
		}
		out, err := runCtl(ctx, ctl, "get", "--server", server, "VM", vmName)
		last = out
		status := decodeVMStatus(t, out)
		if err == nil {
			shutdownRequested := status.ObservedPowerState == "On" && status.PowerTransition == "ShutdownRequested"
			alreadyOff := status.ObservedPowerState == "Off" && status.PowerTransition == "None"
			if shutdownRequested || alreadyOff {
				t.Logf("VM %s reached shutdown convergence checkpoint:\n%s", vmName, out)
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("VM %s did not reach ShutdownRequested or Off/None before deadline\nlast get:\n%s", vmName, last)
}

type vmStatusSnapshot struct {
	Phase              string `json:"phase"`
	ObservedPowerState string `json:"observedPowerState"`
	PowerTransition    string `json:"powerTransition"`
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

// teardownSpine deletes the spine in reverse dependency order and proves the
// deletion lifecycle: referenced objects are refused (409), then each object is
// deleted and polled to 404. The order is the reverse of applyOrder because each
// object can only be deleted once the object referencing it is gone — VM first
// (nothing references it), then its NIC and Volume, then the Network and Image
// they reference, then the pools.
func teardownSpine(ctx context.Context, t *testing.T, ctl, server string) {
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
